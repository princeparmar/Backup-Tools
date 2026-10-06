package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
	"google.golang.org/api/drive/v3"
)

// servicesQuotaPrecheckRequest is the body for onboarding / add-account service selection.
// Satellite injects refresh_token + google_email (Google) or microsoft_email (Microsoft) from
// connected credentials (same as job create).
type servicesQuotaPrecheckRequest struct {
	ProjectID      string   `json:"project_id"`
	Services       []string `json:"services"`
	Emails         []string `json:"emails,omitempty"`
	Email          string   `json:"email,omitempty"`
	RefreshToken   string   `json:"refresh_token,omitempty"`
	GoogleEmail    string   `json:"google_email,omitempty"`
	MicrosoftEmail string   `json:"microsoft_email,omitempty"`
	AccountType    string   `json:"account_type,omitempty"`
}

func (b servicesQuotaPrecheckRequest) isMicrosoft() bool {
	return strings.TrimSpace(b.MicrosoftEmail) != ""
}

// precheckTally accumulates per-service and per-mailbox estimates.
type precheckTally struct {
	perService map[string]int64
	perMailbox map[string]map[string]int64
	total      int64
	errors     map[string]string
}

func newPrecheckTally() *precheckTally {
	return &precheckTally{
		perService: map[string]int64{},
		perMailbox: map[string]map[string]int64{},
		errors:     map[string]string{},
	}
}

func (t *precheckTally) mailbox(email string) map[string]int64 {
	key := strings.TrimSpace(email)
	if key == "" {
		key = "account"
	}
	if t.perMailbox[key] == nil {
		t.perMailbox[key] = map[string]int64{}
	}
	return t.perMailbox[key]
}

// add records an estimate that belongs to one mailbox.
func (t *precheckTally) add(mailbox, svc string, bytes int64) {
	mb := t.mailbox(mailbox)
	mb[svc] = bytes
	mb["total"] += bytes
	t.perService[svc] += bytes
	t.total += bytes
}

// addEach records the same fixed estimate for every mailbox.
func (t *precheckTally) addEach(mailboxes []string, svc string, bytes int64) {
	for _, mailbox := range mailboxes {
		t.add(mailbox, svc, bytes)
	}
}

// addShared records one account-wide estimate, split across mailboxes for display.
func (t *precheckTally) addShared(mailboxes []string, svc string, bytes int64) {
	t.perService[svc] += bytes
	t.total += bytes
	share := bytes / int64(len(mailboxes))
	if share < 1 {
		share = bytes
	}
	for _, mailbox := range mailboxes {
		mb := t.mailbox(mailbox)
		mb[svc] = share
		mb["total"] += share
	}
}

// precheckWorkItem is one per-mailbox estimate that calls a provider API.
type precheckWorkItem struct {
	mailbox string
	svc     string
}

// runPrecheckWork runs estimate for each item with bounded concurrency and records results,
// substituting fallback(svc) when an estimate fails.
func runPrecheckWork(ctx context.Context, tally *precheckTally, work []precheckWorkItem,
	estimate func(ctx context.Context, mailbox, svc string) (int64, error), fallback func(svc string) int64) {
	if len(work) == 0 {
		return
	}
	type workResult struct {
		precheckWorkItem
		bytes int64
		err   error
	}
	results := make(chan workResult, len(work))
	sem := make(chan struct{}, 4) // bound provider API concurrency
	var wg sync.WaitGroup
	for _, w := range work {
		wg.Add(1)
		go func(w precheckWorkItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			est, eerr := estimate(ctx, w.mailbox, w.svc)
			results <- workResult{precheckWorkItem: w, bytes: est, err: eerr}
		}(w)
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	for r := range results {
		est := r.bytes
		if r.err != nil {
			tally.errors[r.svc+"/"+r.mailbox] = r.err.Error()
			est = fallback(r.svc)
		}
		tally.add(r.mailbox, r.svc, est)
	}
}

// HandleAutomaticSyncServicesQuotaPrecheck estimates Google or Microsoft sizes for selected
// services and compares to satellite usage-limits. Used before job create (onboarding / new account).
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
	if len(emails) == 0 && body.isMicrosoft() {
		emails = []string{strings.TrimSpace(body.MicrosoftEmail)}
	}
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

	// Finish before satellite backupToolsRequest 30s client timeout.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	credByEmail := map[string]*repo.GoogleBackupCredentialDB{}
	for i := range creds {
		c := &creds[i]
		credByEmail[strings.ToLower(strings.TrimSpace(c.Email))] = c
	}

	var tally *precheckTally
	var holderEmail, providerLabel string
	if body.isMicrosoft() {
		holderEmail = strings.TrimSpace(body.MicrosoftEmail)
		tenantID := ""
		if c := credByEmail[strings.ToLower(holderEmail)]; c != nil {
			tenantID = c.TenantID
		}
		providerLabel = "Microsoft 365"
		tally = estimateMicrosoftServicesForPrecheck(ctx, resolveMicrosoftPrecheckAuth(ctx, body, tenantID), services, emails)
	} else {
		auth := resolvePrecheckAuth(body, credByEmail)
		if auth.refreshToken == "" {
			auth.tokenErr = fmt.Errorf("google refresh token not found for estimate")
		} else {
			auth.accessToken, auth.tokenErr = google.AuthTokenUsingRefreshToken(auth.refreshToken)
		}
		holderEmail = auth.holderEmail
		if len(emails) == 0 {
			emails = []string{holderEmail}
		}
		providerLabel = "Google"
		tally = estimateGoogleServicesForPrecheck(ctx, auth, services, emails)
	}
	mailboxCount := int64(len(emails))

	required := quota.RequiredBytes(tally.total)
	remaining := usage.RemainingStorage()
	allowed := true
	if usage.StorageLimit > 0 {
		allowed = required <= remaining
	}

	failureCode := ""
	message := ""
	if !allowed {
		failureCode = quota.FailureCodeStorageQuota
		message = "Selected " + providerLabel + " services need more storage than your CyberLS plan has available. You can still continue and create the job, or upgrade your plan."
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"allowed":         allowed,
		"failure_code":    failureCode,
		"quota_kind":      "storage",
		"estimate_bytes":  tally.total,
		"required_bytes":  required,
		"remaining_bytes": remaining,
		"storage_used":    usage.StorageUsed,
		"storage_limit":   usage.StorageLimit,
		"safety_factor":   quota.SafetyFactor,
		"per_service":     tally.perService,
		"per_mailbox":     tally.perMailbox,
		"estimate_errors": tally.errors,
		"message":         message,
		"mailbox_count":   mailboxCount,
		"oauth_holder":    holderEmail,
	})
}

func estimateGoogleServicesForPrecheck(ctx context.Context, auth precheckAuth, services, emails []string) *precheckTally {
	tally := newPrecheckTally()
	var work []precheckWorkItem
	for _, svc := range services {
		switch svc {
		case "photos", "contacts", "calendar":
			// Tiny vs Drive/Gmail — use fixed buffers (no per-mailbox API) so admin Workspace stays fast.
			tally.addEach(emails, svc, fallbackEstimate(svc))
		case "drive", "gmail":
			for _, mailbox := range emails {
				work = append(work, precheckWorkItem{mailbox: mailbox, svc: svc})
			}
		default:
			tally.addShared(emails, svc, fallbackEstimate(svc))
		}
	}
	// Parallel Drive/Gmail estimates (admin Workspace × many mailboxes).
	runPrecheckWork(ctx, tally, work, func(ctx context.Context, mailbox, svc string) (int64, error) {
		return estimateGoogleServiceForPrecheck(ctx, mailbox, svc, auth)
	}, fallbackEstimate)
	return tally
}

// microsoftPrecheckAuth holds the Graph token used for size estimates. application is true when
// an app-only tenant token is used (organization backup), which can read any user's mailbox/drive.
type microsoftPrecheckAuth struct {
	accessToken string
	application bool
	holderEmail string
	err         error
}

// resolveMicrosoftPrecheckAuth prefers the app-only tenant token for admin_workspace accounts
// (tenantID from the stored credential, else from the delegated token) and otherwise uses the
// delegated token.
func resolveMicrosoftPrecheckAuth(ctx context.Context, body servicesQuotaPrecheckRequest, tenantID string) microsoftPrecheckAuth {
	auth := microsoftPrecheckAuth{holderEmail: strings.TrimSpace(body.MicrosoftEmail)}
	orgBackup := outlook.NormalizeAccountType(body.AccountType) == outlook.AccountTypeAdminWorkspace
	appOnly := func(tid string) bool {
		if strings.TrimSpace(tid) == "" {
			return false
		}
		appToken, _, err := outlook.AppOnlyToken(ctx, tid)
		if err != nil || strings.TrimSpace(appToken) == "" {
			return false
		}
		auth.accessToken, auth.application = appToken, true
		return true
	}
	if orgBackup && appOnly(tenantID) {
		return auth
	}

	refresh := strings.TrimSpace(body.RefreshToken)
	if refresh == "" {
		auth.err = fmt.Errorf("microsoft refresh token not found for estimate")
		return auth
	}
	delegated, err := outlook.AuthTokenUsingRefreshToken(refresh)
	if err != nil {
		auth.err = err
		return auth
	}
	if orgBackup && strings.TrimSpace(tenantID) == "" {
		if tid, terr := outlook.TenantIDFromAccessToken(delegated); terr == nil && appOnly(tid) {
			return auth
		}
	}
	auth.accessToken = delegated
	return auth
}

func estimateMicrosoftServicesForPrecheck(ctx context.Context, auth microsoftPrecheckAuth, services, emails []string) *precheckTally {
	tally := newPrecheckTally()
	var work []precheckWorkItem
	for _, svc := range services {
		switch svc {
		case "outlook", "onedrive":
			for _, mailbox := range emails {
				work = append(work, precheckWorkItem{mailbox: mailbox, svc: svc})
			}
		case "calendar", "contacts":
			tally.addEach(emails, svc, microsoftFallbackEstimate(svc))
		default:
			// SharePoint / Teams / Groups are tenant resources, not per mailbox; the wizard does not
			// send the selected sites/teams/groups here, so use a fixed account-wide buffer.
			tally.addShared(emails, svc, microsoftFallbackEstimate(svc))
		}
	}
	runPrecheckWork(ctx, tally, work, func(ctx context.Context, mailbox, svc string) (int64, error) {
		return estimateMicrosoftServiceForPrecheck(ctx, mailbox, svc, auth)
	}, microsoftFallbackEstimate)
	return tally
}

func estimateMicrosoftServiceForPrecheck(ctx context.Context, mailbox, svc string, auth microsoftPrecheckAuth) (int64, error) {
	if auth.err != nil {
		return 0, auth.err
	}
	userBaseURL, err := outlook.UserBaseURL(mailbox, auth.holderEmail, "", auth.application)
	if err != nil {
		return 0, err
	}
	if !auth.application && !strings.HasSuffix(userBaseURL, "/me") {
		return 0, fmt.Errorf("delegated token can only read the signed-in user's own %s", svc)
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	switch svc {
	case "outlook":
		return outlook.EstimateMailboxBytes(callCtx, auth.accessToken, userBaseURL)
	case "onedrive":
		return outlook.EstimateOneDriveBytes(callCtx, auth.accessToken, userBaseURL)
	default:
		return microsoftFallbackEstimate(svc), nil
	}
}

func microsoftFallbackEstimate(svc string) int64 {
	switch svc {
	case "outlook":
		return outlook.MailboxFallbackEstimateBytes
	case "onedrive":
		return outlook.OneDriveFallbackEstimateBytes
	case "sharepoint":
		return 5 * 1024 * 1024 * 1024
	case "teams", "groups":
		return 1 * 1024 * 1024 * 1024
	default:
		return fallbackEstimate(svc)
	}
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
	tokenErr     error
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

	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	switch svc {
	case "drive":
		var svcDrive *drive.Service
		var err error
		switch {
		case needsDWD:
			if !google.WorkspaceServiceAccountConfigured() {
				return 0, fmt.Errorf("domain-wide delegation not configured for mailbox estimate")
			}
			svcDrive, err = google.GetDriveServiceForBackupDWD(callCtx, mailbox)
		case auth.tokenErr != nil:
			return 0, auth.tokenErr
		default:
			svcDrive, err = google.DriveReadonlyServiceUsingToken(callCtx, auth.accessToken)
		}
		if err != nil {
			return 0, err
		}
		return google.EstimateDriveBytes(callCtx, svcDrive)

	case "gmail":
		if auth.tokenErr != nil && !needsDWD {
			return 0, auth.tokenErr
		}
		apiUser := mailbox
		if apiUser == "" {
			apiUser = holder
		}
		sess, err := google.NewWorkspaceGmailSession(callCtx, auth.accessToken, holder, apiUser)
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
