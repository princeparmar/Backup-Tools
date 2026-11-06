package restore

import (
	"context"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
)

type restoreErrorKind string

const (
	restoreKindStorxMissing       restoreErrorKind = "storx_missing"
	restoreKindStorxUplink        restoreErrorKind = "storx_uplink"
	restoreKindStorxRefreshFailed restoreErrorKind = "storx_refresh_failed"
	restoreKindStorxRefreshLimit  restoreErrorKind = "storx_refresh_limit"
	restoreKindGoogleAuth           restoreErrorKind = "google_auth"
	restoreKindGoogleInsufficient   restoreErrorKind = "google_insufficient_scope"
	restoreKindGoogleDelegation     restoreErrorKind = "google_delegation"
	restoreKindMicrosoftAuth        restoreErrorKind = "microsoft_auth"
	restoreKindMicrosoftInsufficient restoreErrorKind = "microsoft_insufficient_scope"
	restoreKindNetwork              restoreErrorKind = "network"
	restoreKindGeneric              restoreErrorKind = "generic"
)

type restoreErrorOutcome struct {
	Kind           restoreErrorKind
	JobMessage     string
	TaskMessage    string
	ClearGoogle    bool
	ClearMicrosoft bool
}

func classifyRestoreError(method, loginID string, processErr error) restoreErrorOutcome {
	if processErr == nil {
		return restoreErrorOutcome{
			Kind:        restoreKindGeneric,
			JobMessage:  restoreJobGeneric,
			TaskMessage: restoreJobGeneric,
		}
	}
	errMsg := processErr.Error()
	errLower := strings.ToLower(errMsg)

	switch {
	case storxrefresh.IsRefreshLimitError(processErr):
		return restoreErrorOutcome{
			Kind:        restoreKindStorxRefreshLimit,
			JobMessage:  restoreJobStorxRefreshLimit,
			TaskMessage: restoreTaskStorxRefreshLimit,
		}

	case quota.IsBandwidthQuota(processErr):
		return restoreErrorOutcome{
			Kind:        restoreKindGeneric,
			JobMessage:  processErr.Error(),
			TaskMessage: processErr.Error(),
		}

	case storxrefresh.IsRefreshFailedError(processErr):
		return restoreErrorOutcome{
			Kind:        restoreKindStorxRefreshFailed,
			JobMessage:  restoreJobStorxSatelliteRefresh,
			TaskMessage: restoreTaskStorxSatelliteRefresh,
		}

	case strings.Contains(errMsg, "storx access grant not found") ||
		strings.Contains(errMsg, "storx_token is required") ||
		strings.Contains(errMsg, "google refresh token missing") ||
		strings.Contains(errMsg, "refresh token not found"):
		if strings.Contains(errMsg, "refresh token") {
			if IsMicrosoftRestoreMethod(method) {
				return restoreErrorOutcome{
					Kind:           restoreKindMicrosoftAuth,
					JobMessage:     restoreJobMicrosoftAuth,
					TaskMessage:    restoreTaskMicrosoftAuth,
					ClearMicrosoft: isMicrosoftRefreshRevokedText(errMsg),
				}
			}
			return restoreErrorOutcome{
				Kind:        restoreKindGoogleAuth,
				JobMessage:  restoreJobGoogleAuth,
				TaskMessage: restoreTaskGoogleAuth,
				ClearGoogle: isGoogleRefreshRevokedText(errMsg),
			}
		}
		return restoreErrorOutcome{
			Kind:        restoreKindStorxMissing,
			JobMessage:  restoreJobStorxMissing,
			TaskMessage: restoreTaskStorxMissing,
		}

	case IsMicrosoftRestoreMethod(method) && isMicrosoftRefreshRevokedText(errMsg):
		return restoreErrorOutcome{
			Kind:           restoreKindMicrosoftAuth,
			JobMessage:     restoreJobMicrosoftAuth,
			TaskMessage:    restoreTaskMicrosoftAuth,
			ClearMicrosoft: true,
		}

	case IsMicrosoftRestoreMethod(method) &&
		(strings.Contains(errLower, "insufficient privileges") ||
			strings.Contains(errLower, "accessdenied") ||
			strings.Contains(errLower, "authorization_requestdenied") ||
			strings.Contains(errLower, "missing restore write") ||
			strings.Contains(errLower, "consent_required")):
		return restoreErrorOutcome{
			Kind:        restoreKindMicrosoftInsufficient,
			JobMessage:  restoreJobMicrosoftInsufficientScope,
			TaskMessage: restoreTaskMicrosoftInsufficientScope,
		}

	case repo.IsGoogleMediaOrGmailMethod(method) && method == "gmail" && isConsumerGmailLogin(loginID) &&
		(isGmailOAuthOrDelegationFailureRestore(errMsg) || googleAuthImmediateFailure(method, errMsg)):
		return restoreErrorOutcome{
			Kind:        restoreKindGoogleAuth,
			JobMessage:  restoreJobGoogleAuth,
			TaskMessage: restoreTaskGoogleAuth,
			ClearGoogle: true,
		}

	case repo.IsGoogleMediaOrGmailMethod(method) && isGmailOAuthOrDelegationFailureRestore(errMsg):
		return restoreErrorOutcome{
			Kind:        restoreKindGoogleDelegation,
			JobMessage:  restoreJobDelegation,
			TaskMessage: restoreTaskDelegation,
		}

	case repo.IsGoogleMediaOrGmailMethod(method) &&
		(strings.Contains(errLower, "access_token_scope_insufficient") ||
			strings.Contains(errLower, "insufficient authentication scopes")):
		return restoreErrorOutcome{
			Kind:        restoreKindGoogleInsufficient,
			JobMessage:  restoreJobGoogleInsufficientScope,
			TaskMessage: restoreTaskGoogleInsufficientScope,
			ClearGoogle: true,
		}

	case googleAuthImmediateFailure(method, errMsg):
		return restoreErrorOutcome{
			Kind:        restoreKindGoogleAuth,
			JobMessage:  restoreJobGoogleAuth,
			TaskMessage: restoreTaskGoogleAuth,
			ClearGoogle: true,
		}

	case strings.Contains(errMsg, "googleapi: Error 401") ||
		strings.Contains(errMsg, "oauth credential not found") ||
		strings.Contains(errMsg, "google access token missing") ||
		strings.Contains(errMsg, "google token refresh"):
		return restoreErrorOutcome{
			Kind:        restoreKindGoogleAuth,
			JobMessage:  restoreJobGoogleAuth,
			TaskMessage: restoreTaskGoogleAuth,
			ClearGoogle: true,
		}

	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		return restoreErrorOutcome{
			Kind:        restoreKindStorxUplink,
			JobMessage:  restoreJobStorxUplink,
			TaskMessage: restoreTaskStorxUplink,
		}

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		return restoreErrorOutcome{
			Kind:        restoreKindNetwork,
			JobMessage:  restoreJobNetwork,
			TaskMessage: restoreTaskNetwork,
		}

	default:
		return restoreErrorOutcome{
			Kind:        restoreKindGeneric,
			JobMessage:  restoreJobGeneric,
			TaskMessage: errMsg,
		}
	}
}

func googleAuthImmediateFailure(method, errMsg string) bool {
	if !repo.IsGoogleMediaOrGmailMethod(method) {
		return false
	}
	return isGoogleRefreshRevokedText(errMsg)
}

func isConsumerGmailLogin(loginID string) bool {
	e := strings.ToLower(strings.TrimSpace(loginID))
	if e == "" || !strings.Contains(e, "@") {
		return false
	}
	return strings.HasSuffix(e, "@gmail.com") || strings.HasSuffix(e, "@googlemail.com")
}

func isGmailOAuthOrDelegationFailureRestore(errMsg string) bool {
	e := strings.ToLower(errMsg)
	return strings.Contains(e, "unauthorized_client") ||
		(strings.Contains(e, "oauth2:") && strings.Contains(e, "cannot fetch token")) ||
		strings.Contains(e, "delegated gmail access failed") ||
		strings.Contains(e, "domain-wide delegation") ||
		strings.Contains(e, "reconnecting the same google account")
}

func linkedCronJob(store *db.PostgresDb, restoreJob *repo.RestoreJobListingDB) *repo.CronJobListingDB {
	if store == nil || restoreJob == nil || restoreJob.CronJobID == 0 {
		return nil
	}
	job, err := store.CronJobRepo.GetCronJobByID(restoreJob.CronJobID)
	if err != nil {
		return nil
	}
	return job
}

// applyRestoreGoogleTokenCleanup deactivates linked backup jobs when Google auth cannot be recovered.
func applyRestoreGoogleTokenCleanup(ctx context.Context, store *db.PostgresDb, restoreJob *repo.RestoreJobListingDB, outcome restoreErrorOutcome) {
	if !outcome.ClearGoogle {
		return
	}
	target := strings.TrimSpace(restoreJob.TargetEmail)
	if target != "" && !strings.EqualFold(target, strings.TrimSpace(restoreJob.LoginID)) {
		if restoreJob.CredentialID == 0 {
			return
		}
		if err := store.CronJobRepo.DeactivateAllJobsForCredential(restoreJob.CredentialID, backupJobGoogleAuthDeactivate, true); err != nil {
			logger.Warn(ctx, "Restore failed to clear destination Google credential",
				logger.Int("restore_job_id", int(restoreJob.ID)),
				logger.Int("credential_id", int(restoreJob.CredentialID)),
				logger.ErrorField(err))
		}
		return
	}
	cronJob := linkedCronJob(store, restoreJob)
	if cronJob == nil {
		return
	}
	if err := store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(cronJob, backupJobGoogleAuthDeactivate); err != nil {
		logger.Warn(ctx, "Restore failed to clear Google tokens on linked backup jobs",
			logger.Int("restore_job_id", int(restoreJob.ID)),
			logger.Int("cron_job_id", int(cronJob.ID)),
			logger.ErrorField(err))
		return
	}
	repo.StripGmailRefreshTokenFromCronJobInputData(cronJob)
}

// applyRestoreMicrosoftTokenCleanup deactivates linked backup jobs when Microsoft auth cannot be recovered.
func applyRestoreMicrosoftTokenCleanup(ctx context.Context, store *db.PostgresDb, restoreJob *repo.RestoreJobListingDB, outcome restoreErrorOutcome) {
	if !outcome.ClearMicrosoft {
		return
	}
	target := strings.TrimSpace(restoreJob.TargetEmail)
	if target != "" && !strings.EqualFold(target, strings.TrimSpace(restoreJob.LoginID)) {
		if restoreJob.CredentialID == 0 {
			return
		}
		if err := store.CronJobRepo.DeactivateAllJobsForCredential(restoreJob.CredentialID, backupJobMicrosoftAuthDeactivate, true); err != nil {
			logger.Warn(ctx, "Restore failed to clear destination Microsoft credential",
				logger.Int("restore_job_id", int(restoreJob.ID)),
				logger.Int("credential_id", int(restoreJob.CredentialID)),
				logger.ErrorField(err))
		}
		return
	}
	cronJob := linkedCronJob(store, restoreJob)
	if cronJob == nil {
		return
	}
	if err := store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(cronJob, backupJobMicrosoftAuthDeactivate); err != nil {
		logger.Warn(ctx, "Restore failed to clear Microsoft tokens on linked backup jobs",
			logger.Int("restore_job_id", int(restoreJob.ID)),
			logger.Int("cron_job_id", int(cronJob.ID)),
			logger.ErrorField(err))
	}
}

func handleRestoreFailure(ctx context.Context, store *db.PostgresDb, restoreJob *repo.RestoreJobListingDB, processErr error) restoreErrorOutcome {
	outcome := classifyRestoreError(restoreJob.Method, restoreJob.LoginID, processErr)

	logger.Warn(ctx, "Restore failure classified",
		logger.Int("restore_job_id", int(restoreJob.ID)),
		logger.String("method", restoreJob.Method),
		logger.String("login_id", restoreJob.LoginID),
		logger.String("error_kind", string(outcome.Kind)),
		logger.Bool("clear_google", outcome.ClearGoogle),
		logger.Bool("clear_microsoft", outcome.ClearMicrosoft),
		logger.ErrorField(processErr))

	applyRestoreGoogleTokenCleanup(ctx, store, restoreJob, outcome)
	applyRestoreMicrosoftTokenCleanup(ctx, store, restoreJob, outcome)
	return outcome
}
