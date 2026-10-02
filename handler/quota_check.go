package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

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
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// HandleAutomaticSyncQuotaCheck estimates backup size and compares against satellite usage-limits.
// Used by CyberLS before backup-now (popup). Does not start a job and does not write Redis.
func HandleAutomaticSyncQuotaCheck(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return sendJSONError(c, http.StatusBadRequest, "Invalid Job ID", err)
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return sendJSONError(c, http.StatusUnauthorized, "Invalid Request", err)
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	job, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return sendJSONError(c, http.StatusNotFound, "Job not found", err)
	}

	estimate, estErr := estimateJobBackupBytes(ctx, database, job)
	if estErr != nil {
		estimate = 0
	}

	usage, usageErr := quota.FetchUsageLimits(ctx, database, job)
	if usageErr != nil {
		return sendJSONError(c, http.StatusBadGateway, "Failed to fetch usage-limits", usageErr)
	}

	required := quota.RequiredBytes(estimate)
	remaining := usage.RemainingStorage()
	allowed := true
	if usage.StorageLimit > 0 {
		if estimate > 0 {
			allowed = required <= remaining
		} else {
			allowed = remaining > 0
		}
	}

	failureCode := ""
	if !allowed {
		failureCode = quota.FailureCodeStorageQuota
	}

	estErrMsg := ""
	if estErr != nil {
		estErrMsg = estErr.Error()
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"allowed":         allowed,
		"failure_code":    failureCode,
		"quota_kind":      "storage",
		"estimate_bytes":  estimate,
		"required_bytes":  required,
		"remaining_bytes": remaining,
		"storage_used":    usage.StorageUsed,
		"storage_limit":   usage.StorageLimit,
		"bandwidth_used":  usage.BandwidthUsed,
		"bandwidth_limit": usage.BandwidthLimit,
		"method":          job.Method,
		"safety_factor":   quota.SafetyFactor,
		"estimate_error":  estErrMsg,
	})
}

func estimateJobBackupBytes(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB) (int64, error) {
	if job == nil {
		return 0, fmt.Errorf("nil job")
	}
	switch job.Method {
	case "google_drive":
		svc, err := driveServiceForQuotaEstimate(ctx, store, job)
		if err != nil {
			return 0, err
		}
		return google.EstimateDriveBytes(ctx, svc)
	case "gmail":
		svc, apiUser, err := gmailServiceForQuotaEstimate(ctx, store, job)
		if err != nil {
			return 0, err
		}
		return google.EstimateGmailBytes(ctx, svc, apiUser)
	case "google_contacts":
		return google.EstimateContactsBytes(ctx, nil)
	case "google_calendar":
		return google.EstimateCalendarBytes(ctx, nil)
	case "google_photos":
		return 50 * 1024 * 1024, nil
	default:
		return google.ContactsMinEstimateBytes, nil
	}
}

func driveServiceForQuotaEstimate(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB) (*drive.Service, error) {
	mailbox := strings.TrimSpace(job.Name)
	oauthHolder := store.CronJobRepo.ResolvedOAuthHolderEmail(job)
	rt := store.CronJobRepo.ResolvedRefreshToken(job)
	if rt == "" {
		rt = store.CronJobRepo.GmailResolvedRefreshToken(job)
	}
	if rt == "" && mailbox != "" && !strings.EqualFold(mailbox, oauthHolder) {
		svc, err := google.GetDriveServiceForBackupDWD(ctx, mailbox)
		if err == nil {
			return svc, nil
		}
	}
	if rt == "" {
		return nil, fmt.Errorf("no google credentials for drive estimate")
	}
	accessToken, err := google.AuthTokenUsingRefreshToken(rt)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		return nil, err
	}
	client := config.Client(ctx, &oauth2.Token{AccessToken: accessToken})
	return drive.NewService(ctx, option.WithHTTPClient(client))
}

func gmailServiceForQuotaEstimate(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB) (*gmail.Service, string, error) {
	refreshToken := store.CronJobRepo.GmailResolvedRefreshToken(job)
	mailbox := job.Name
	if job.InputData != nil && job.InputData.Json() != nil {
		if email, ok := (*job.InputData.Json())["email"].(string); ok && email != "" {
			mailbox = email
		}
	}
	oauthHolder := store.CronJobRepo.ResolvedOAuthHolderEmail(job)
	delegationOnly := refreshToken == "" && google.GmailJobUsesDelegationWithoutOAuth(mailbox, oauthHolder)
	accessToken := ""
	if !delegationOnly {
		if refreshToken == "" {
			return nil, "", fmt.Errorf("refresh token required for gmail estimate")
		}
		at, err := google.AuthTokenUsingRefreshToken(refreshToken)
		if err != nil {
			return nil, "", err
		}
		accessToken = at
	}
	sess, err := google.NewWorkspaceGmailSession(ctx, accessToken, oauthHolder, mailbox)
	if err != nil {
		return nil, "", err
	}
	if sess == nil || sess.Client == nil || sess.Client.Service == nil {
		return nil, "", fmt.Errorf("gmail service unavailable")
	}
	return sess.Client.Service, sess.APIUser, nil
}
