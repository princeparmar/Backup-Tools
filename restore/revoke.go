package restore

import (
	"context"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const backupJobGoogleAuthDeactivate = "Google permission revoked. Re-authenticate to grant access again."
const backupJobMicrosoftAuthDeactivate = "Microsoft permission revoked. Re-authenticate to grant access again."

func isGoogleRefreshRevoked(err error) bool {
	if err == nil {
		return false
	}
	return isGoogleRefreshRevokedText(err.Error())
}

func isGoogleRefreshRevokedText(errMsg string) bool {
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "" {
		return false
	}
	if strings.Contains(e, "error making http request") ||
		strings.Contains(e, "error sending http request") ||
		strings.Contains(e, "timeout") ||
		strings.Contains(e, "connection refused") ||
		strings.Contains(e, "connection reset") ||
		strings.Contains(e, "temporary failure") ||
		strings.Contains(e, "network is unreachable") {
		return false
	}
	return strings.Contains(e, "invalid_grant") ||
		strings.Contains(e, "expired or revoked") ||
		strings.Contains(e, "token has been expired") ||
		strings.Contains(e, "token has been revoked")
}

// IsMicrosoftRefreshRevoked reports whether a Microsoft token mint/refresh failed because
// the refresh token was revoked or expired (same signals as Google invalid_grant).
func IsMicrosoftRefreshRevoked(err error) bool {
	if err == nil {
		return false
	}
	return isMicrosoftRefreshRevokedText(err.Error())
}

func isMicrosoftRefreshRevokedText(errMsg string) bool {
	e := strings.ToLower(strings.TrimSpace(errMsg))
	if e == "" {
		return false
	}
	if strings.Contains(e, "error making http request") ||
		strings.Contains(e, "error sending http request") ||
		strings.Contains(e, "timeout") ||
		strings.Contains(e, "connection refused") ||
		strings.Contains(e, "connection reset") ||
		strings.Contains(e, "temporary failure") ||
		strings.Contains(e, "network is unreachable") {
		return false
	}
	return strings.Contains(e, "invalid_grant") ||
		strings.Contains(e, "aadsts70008") || // refresh token expired
		strings.Contains(e, "aadsts50173") || // revoked
		strings.Contains(e, "aadsts70000") || // invalid grant
		strings.Contains(e, "expired or revoked") ||
		strings.Contains(e, "token has been expired") ||
		strings.Contains(e, "token has been revoked") ||
		strings.Contains(e, "refresh token is expired") ||
		strings.Contains(e, "refresh token was issued to a different client")
}

func shouldRecordGoogleRestoreRevoke(out *ReadinessResult) bool {
	if out == nil || out.Ready {
		return false
	}
	if strings.TrimSpace(out.Reason) != ReadinessReasonTokenRefreshFailed {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(out.Message), restoreReadinessRefreshMissing) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(out.AuthMode), RestoreAuthModeDWD) {
		return false
	}
	method, ok := APIServiceToMethod[APIService(strings.TrimSpace(out.Service))]
	return ok && !IsMicrosoftRestoreMethod(method)
}

func shouldRecordMicrosoftRestoreRevoke(out *ReadinessResult) bool {
	if out == nil || out.Ready {
		return false
	}
	if strings.TrimSpace(out.Reason) != ReadinessReasonTokenRefreshFailed {
		return false
	}
	method, ok := APIServiceToMethod[APIService(strings.TrimSpace(out.Service))]
	return ok && IsMicrosoftRestoreMethod(method)
}

func applyGoogleRestoreRevoked(ctx context.Context, store *db.PostgresDb, userID string, out *ReadinessResult) {
	if store == nil || !shouldRecordGoogleRestoreRevoke(out) {
		return
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	out.Message = restoreLogGoogleRevoked

	if out.Migration {
		if out.CredentialID > 0 {
			if err := store.CronJobRepo.DeactivateAllJobsForCredential(out.CredentialID, backupJobGoogleAuthDeactivate, true); err != nil {
				logger.Warn(ctx, "Restore revoke failed to deactivate destination backup",
					logger.Int("credential_id", int(out.CredentialID)),
					logger.ErrorField(err))
			}
		}
	} else if out.CronJobID > 0 {
		cronJob, err := store.CronJobRepo.GetCronJobByID(out.CronJobID)
		if err != nil {
			logger.Warn(ctx, "Restore revoke skipped backup deactivate — cron job missing",
				logger.Int("cron_job_id", int(out.CronJobID)),
				logger.ErrorField(err))
		} else if err := store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(cronJob, backupJobGoogleAuthDeactivate); err != nil {
			logger.Warn(ctx, "Restore revoke failed to deactivate backup jobs",
				logger.Int("cron_job_id", int(out.CronJobID)),
				logger.ErrorField(err))
		}
	}

	if err := recordFailedRestoreRevokeJob(store, userID, out, restoreLogGoogleRevoked); err != nil {
		logger.Warn(ctx, "Restore revoke log was not written",
			logger.String("login_id", out.LoginID),
			logger.String("service", out.Service),
			logger.ErrorField(err))
	}
}

// ApplyMicrosoftRestoreRevoked deactivates linked Microsoft backups and writes a failed
// restore history row when prepare hits a revoked/expired refresh token (mirrors Google).
func ApplyMicrosoftRestoreRevoked(ctx context.Context, store *db.PostgresDb, userID string, out *ReadinessResult) {
	if store == nil || !shouldRecordMicrosoftRestoreRevoke(out) {
		return
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	out.Message = restoreLogMicrosoftRevoked

	if out.Migration {
		if out.CredentialID > 0 {
			if err := store.CronJobRepo.DeactivateAllJobsForCredential(out.CredentialID, backupJobMicrosoftAuthDeactivate, true); err != nil {
				logger.Warn(ctx, "Restore revoke failed to deactivate destination Microsoft backup",
					logger.Int("credential_id", int(out.CredentialID)),
					logger.ErrorField(err))
			}
		}
	} else if out.CronJobID > 0 {
		cronJob, err := store.CronJobRepo.GetCronJobByID(out.CronJobID)
		if err != nil {
			logger.Warn(ctx, "Restore revoke skipped Microsoft backup deactivate — cron job missing",
				logger.Int("cron_job_id", int(out.CronJobID)),
				logger.ErrorField(err))
		} else if err := store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(cronJob, backupJobMicrosoftAuthDeactivate); err != nil {
			logger.Warn(ctx, "Restore revoke failed to deactivate Microsoft backup jobs",
				logger.Int("cron_job_id", int(out.CronJobID)),
				logger.ErrorField(err))
		}
	}

	if err := recordFailedRestoreRevokeJob(store, userID, out, restoreLogMicrosoftRevoked); err != nil {
		logger.Warn(ctx, "Restore Microsoft revoke log was not written",
			logger.String("login_id", out.LoginID),
			logger.String("service", out.Service),
			logger.ErrorField(err))
	}
}

func recordFailedRestoreRevokeJob(store *db.PostgresDb, userID string, out *ReadinessResult, message string) error {
	method, ok := APIServiceToMethod[APIService(strings.TrimSpace(out.Service))]
	if !ok {
		return nil
	}
	loginID := strings.TrimSpace(out.LoginID)
	active, err := store.RestoreJobRepo.HasActiveJob(userID, method, loginID, strings.TrimSpace(out.ProjectID), strings.TrimSpace(out.TargetEmail))
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	return store.RestoreJobRepo.Create(&repo.RestoreJobListingDB{
		UserID:         userID,
		StorjProjectID: strings.TrimSpace(out.ProjectID),
		LoginID:        loginID,
		TargetEmail:    strings.TrimSpace(out.TargetEmail),
		Method:         method,
		AccountType:    strings.TrimSpace(out.AccountType),
		CredentialID:   out.CredentialID,
		CronJobID:      out.CronJobID,
		Status:         repo.RestoreJobStatusFailed,
		Message:        message,
		MessageStatus:  repo.JobMessageStatusError,
		TotalCount:     out.BackupItemCount,
	})
}
