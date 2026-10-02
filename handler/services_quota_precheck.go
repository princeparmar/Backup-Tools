package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// servicesQuotaPrecheckRequest is the body for onboarding / add-account service selection.
// Satellite injects refresh_token + google_email from connected credentials (same as job create).
type servicesQuotaPrecheckRequest struct {
	ProjectID    string   `json:"project_id"`
	Services     []string `json:"services"`
	Emails       []string `json:"emails,omitempty"`
	Email        string   `json:"email,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	GoogleEmail  string   `json:"google_email,omitempty"`
	AccountType  string   `json:"account_type,omitempty"`
}

// HandleAutomaticSyncServicesQuotaPrecheck estimates Google sizes for selected services
// and compares to satellite usage-limits. Used before job create (onboarding / new account).
// Does not write Redis. Distinct from buckets/check-upload (soft % threshold popup).
func HandleAutomaticSyncServicesQuotaPrecheck(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return sendJSONError(c, http.StatusUnauthorized, "Invalid Request", err)
	}

	var body servicesQuotaPrecheckRequest
	if err := c.Bind(&body); err != nil {
		return sendJSONError(c, http.StatusBadRequest, "Invalid request body", err)
	}

	services := normalizeServiceList(body.Services)
	if len(services) == 0 {
		return sendJSONError(c, http.StatusBadRequest, "services is required", nil)
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	emails := make([]string, 0, len(body.Emails))
	for _, e := range body.Emails {
		e = strings.TrimSpace(e)
		if e != "" {
			emails = append(emails, e)
		}
	}
	if len(emails) == 0 && strings.TrimSpace(body.Email) != "" {
		emails = []string{strings.TrimSpace(body.Email)}
	}

	projectID := strings.TrimSpace(body.ProjectID)
	creds, _ := database.CredentialRepo.ListByUserID(userID, "")
	if len(emails) == 0 && strings.TrimSpace(body.GoogleEmail) != "" {
		emails = []string{strings.TrimSpace(body.GoogleEmail)}
	}
	if len(emails) == 0 && len(creds) > 0 {
		emails = []string{creds[0].Email}
	}
	if projectID == "" {
		for _, c := range creds {
			if pid := strings.TrimSpace(c.StorjProjectID); pid != "" {
				projectID = pid
				break
			}
		}
	}
	if projectID == "" {
		return sendJSONError(c, http.StatusBadRequest, "project_id is required", nil)
	}

	usage, usageErr := quota.FetchUsageLimitsForRestore(ctx, userID, projectID)
	if usageErr != nil {
		return sendJSONError(c, http.StatusBadGateway, "Failed to fetch usage-limits", usageErr)
	}

	credByEmail := map[string]*repo.GoogleBackupCredentialDB{}
	for i := range creds {
		c := &creds[i]
		credByEmail[strings.ToLower(strings.TrimSpace(c.Email))] = c
	}

	auth := resolvePrecheckAuth(body, credByEmail)
	if auth.refreshToken != "" && auth.accessToken == "" {
		if at, aerr := google.AuthTokenUsingRefreshToken(auth.refreshToken); aerr == nil {
			auth.accessToken = at
		}
	}

	// Finish before satellite backupToolsRequest 30s client timeout.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	perService := map[string]int64{}
	perMailbox := map[string]map[string]int64{}
	ensureMailbox := func(email string) map[string]int64 {
		key := strings.TrimSpace(email)
		if key == "" {
			key = "account"
		}
		if perMailbox[key] == nil {
			perMailbox[key] = map[string]int64{}
		}
		return perMailbox[key]
	}
	var totalEstimate int64
	estimateErrors := map[string]string{}

	mailboxCount := int64(len(emails))
	if mailboxCount < 1 {
		mailboxCount = 1
		emails = []string{auth.holderEmail}
		if emails[0] == "" {
			emails = []string{""}
		}
	}

	type workItem struct {
		mailbox string
		svc     string
	}
	var work []workItem
	for _, svc := range services {
		switch svc {
		case "photos":
			est := fallbackEstimate(svc)
			perService[svc] = est * mailboxCount
			totalEstimate += est * mailboxCount
			for _, mailbox := range emails {
				mb := ensureMailbox(mailbox)
				mb[svc] = est
				mb["total"] += est
			}
		case "contacts", "calendar":
			// Tiny vs Drive/Gmail — use fixed buffers (no per-mailbox API) so admin Workspace stays fast.
			est := fallbackEstimate(svc)
			perService[svc] = est * mailboxCount
			totalEstimate += est * mailboxCount
			for _, mailbox := range emails {
				mb := ensureMailbox(mailbox)
				mb[svc] = est
				mb["total"] += est
			}
		case "drive", "gmail":
			for _, mailbox := range emails {
				work = append(work, workItem{mailbox: mailbox, svc: svc})
			}
		default:
			est := fallbackEstimate(svc)
			perService[svc] = est
			totalEstimate += est
			share := est / mailboxCount
			if share < 1 {
				share = est
			}
			for _, mailbox := range emails {
				mb := ensureMailbox(mailbox)
				mb[svc] = share
				mb["total"] += share
			}
		}
	}

	// Parallel Drive/Gmail estimates (admin Workspace × many mailboxes).
	if len(work) > 0 {
		type workResult struct {
			mailbox string
			svc     string
			bytes   int64
			err     error
		}
		results := make(chan workResult, len(work))
		sem := make(chan struct{}, 4) // bound Google API concurrency
		var wg sync.WaitGroup
		for _, w := range work {
			wg.Add(1)
			go func(w workItem) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				est, eerr := estimateGoogleServiceForPrecheck(ctx, w.mailbox, w.svc, auth)
				results <- workResult{mailbox: w.mailbox, svc: w.svc, bytes: est, err: eerr}
			}(w)
		}
		go func() {
			wg.Wait()
			close(results)
		}()
		for r := range results {
			est := r.bytes
			if r.err != nil {
				estimateErrors[r.svc+"/"+r.mailbox] = r.err.Error()
				est = fallbackEstimate(r.svc)
			}
			mb := ensureMailbox(r.mailbox)
			mb[r.svc] = est
			mb["total"] += est
			perService[r.svc] += est
			totalEstimate += est
		}
	}

	required := quota.RequiredBytes(totalEstimate)
	remaining := usage.RemainingStorage()
	allowed := true
	if usage.StorageLimit > 0 {
		allowed = required <= remaining
	}

	failureCode := ""
	message := ""
	if !allowed {
		failureCode = quota.FailureCodeStorageQuota
		message = "Selected Google services need more storage than your CyberLS plan has available. You can still continue and create the job, or upgrade your plan."
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"allowed":         allowed,
		"failure_code":    failureCode,
		"quota_kind":      "storage",
		"estimate_bytes":  totalEstimate,
		"required_bytes":  required,
		"remaining_bytes": remaining,
		"storage_used":    usage.StorageUsed,
		"storage_limit":   usage.StorageLimit,
		"safety_factor":   quota.SafetyFactor,
		"per_service":     perService,
		"per_mailbox":     perMailbox,
		"estimate_errors": estimateErrors,
		"message":         message,
		"mailbox_count":   mailboxCount,
		"oauth_holder":    auth.holderEmail,
	})
}

func normalizeServiceList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "google_drive":
			s = "drive"
		case "google_calendar":
			s = "calendar"
		case "google_contacts":
			s = "contacts"
		case "google_photos":
			s = "photos"
		}
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func fallbackEstimate(svc string) int64 {
	switch svc {
	case "drive":
		return 5 * 1024 * 1024 * 1024
	case "gmail":
		return 2 * 1024 * 1024 * 1024
	case "contacts":
		return google.ContactsMinEstimateBytes * 5
	case "calendar":
		return google.CalendarMinEstimateBytes
	case "photos":
		return 50 * 1024 * 1024
	default:
		return google.ContactsMinEstimateBytes
	}
}

type precheckAuth struct {
	refreshToken string
	accessToken  string
	holderEmail  string
}

func resolvePrecheckAuth(body servicesQuotaPrecheckRequest, credByEmail map[string]*repo.GoogleBackupCredentialDB) precheckAuth {
	auth := precheckAuth{
		refreshToken: strings.TrimSpace(body.RefreshToken),
		holderEmail:  strings.TrimSpace(body.GoogleEmail),
	}
	if auth.refreshToken != "" && auth.holderEmail != "" {
		return auth
	}
	if auth.holderEmail != "" {
		if c := credByEmail[strings.ToLower(auth.holderEmail)]; c != nil && strings.TrimSpace(c.RefreshToken) != "" {
			auth.refreshToken = strings.TrimSpace(c.RefreshToken)
			if auth.holderEmail == "" {
				auth.holderEmail = strings.TrimSpace(c.Email)
			}
			return auth
		}
	}
	for _, c := range credByEmail {
		if c == nil || strings.TrimSpace(c.RefreshToken) == "" {
			continue
		}
		if auth.refreshToken == "" {
			auth.refreshToken = strings.TrimSpace(c.RefreshToken)
		}
		if auth.holderEmail == "" {
			auth.holderEmail = strings.TrimSpace(c.Email)
		}
		break
	}
	return auth
}

func estimateGoogleServiceForPrecheck(ctx context.Context, mailbox, svc string, auth precheckAuth) (int64, error) {
	mailbox = strings.TrimSpace(mailbox)
	holder := strings.TrimSpace(auth.holderEmail)
	needsDWD := google.MediaBackupNeedsDelegation(mailbox, holder)

	switch svc {
	case "drive":
		callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if needsDWD {
			if !google.WorkspaceServiceAccountConfigured() {
				return 0, fmt.Errorf("domain-wide delegation not configured for mailbox estimate")
			}
			svcDrive, err := google.GetDriveServiceForBackupDWD(callCtx, mailbox)
			if err != nil {
				return 0, err
			}
			return google.EstimateDriveBytes(callCtx, svcDrive)
		}
		accessToken := auth.accessToken
		if accessToken == "" {
			if auth.refreshToken == "" {
				return 0, fmt.Errorf("google refresh token not found for estimate")
			}
			at, err := google.AuthTokenUsingRefreshToken(auth.refreshToken)
			if err != nil {
				return 0, err
			}
			accessToken = at
		}
		b, err := os.ReadFile("credentials.json")
		if err != nil {
			return 0, err
		}
		config, err := oauth2google.ConfigFromJSON(b, drive.DriveReadonlyScope)
		if err != nil {
			return 0, err
		}
		client := config.Client(callCtx, &oauth2.Token{AccessToken: accessToken})
		svcDrive, err := drive.NewService(callCtx, option.WithHTTPClient(client))
		if err != nil {
			return 0, err
		}
		return google.EstimateDriveBytes(callCtx, svcDrive)

	case "gmail":
		callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		accessToken := auth.accessToken
		if accessToken == "" && auth.refreshToken != "" {
			at, err := google.AuthTokenUsingRefreshToken(auth.refreshToken)
			if err != nil {
				return 0, err
			}
			accessToken = at
		}
		if accessToken == "" && !needsDWD {
			return 0, fmt.Errorf("google refresh token not found for estimate")
		}
		apiUser := mailbox
		if apiUser == "" {
			apiUser = holder
		}
		sess, err := google.NewWorkspaceGmailSession(callCtx, accessToken, holder, apiUser)
		if err != nil {
			return 0, err
		}
		if sess == nil || sess.Client == nil || sess.Client.Service == nil {
			return 0, fmt.Errorf("gmail service unavailable")
		}
		// Quick list-only estimate — full sampling is too slow for admin × N mailboxes.
		return google.EstimateGmailBytesQuick(callCtx, sess.Client.Service, sess.APIUser)

	default:
		return fallbackEstimate(svc), nil
	}
}
