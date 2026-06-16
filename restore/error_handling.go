package restore

import (
	"context"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
)

type restoreErrorKind string

const (
	restoreKindStorxMissing        restoreErrorKind = "storx_missing"
	restoreKindStorxUplink         restoreErrorKind = "storx_uplink"
	restoreKindStorxRefreshFailed  restoreErrorKind = "storx_refresh_failed"
	restoreKindStorxRefreshLimit   restoreErrorKind = "storx_refresh_limit"
	restoreKindGoogleAuth         restoreErrorKind = "google_auth"
	restoreKindGoogleInsufficient restoreErrorKind = "google_insufficient_scope"
	restoreKindGoogleDelegation   restoreErrorKind = "google_delegation"
	restoreKindNetwork            restoreErrorKind = "network"
	restoreKindGeneric            restoreErrorKind = "generic"
)

type restoreErrorOutcome struct {
	Kind        restoreErrorKind
	JobMessage  string
	TaskMessage string
	ClearGoogle bool
}

func classifyRestoreError(method string, processErr error) restoreErrorOutcome {
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
			return restoreErrorOutcome{
				Kind:        restoreKindGoogleAuth,
				JobMessage:  restoreJobGoogleAuth,
				TaskMessage: restoreTaskGoogleAuth,
				ClearGoogle: true,
			}
		}
		return restoreErrorOutcome{
			Kind:        restoreKindStorxMissing,
			JobMessage:  restoreJobStorxMissing,
			TaskMessage: restoreTaskStorxMissing,
		}

	case repo.IsGoogleMediaOrGmailMethod(method) &&
		(strings.Contains(errLower, "unauthorized_client") ||
			strings.Contains(errLower, "oauth2:") && strings.Contains(errLower, "cannot fetch token")):
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
	e := strings.ToLower(errMsg)
	return strings.Contains(e, "invalid_grant") ||
		strings.Contains(errMsg, "error while generating auth token") ||
		strings.Contains(e, "error parsing response json")
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
	cronJob := linkedCronJob(store, restoreJob)
	if cronJob == nil {
		logger.Warn(ctx, "Restore Google token cleanup skipped — no linked cron job",
			logger.Int("restore_job_id", int(restoreJob.ID)),
			logger.String("error_kind", string(outcome.Kind)))
		return
	}
	if err := store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(cronJob, outcome.JobMessage); err != nil {
		logger.Warn(ctx, "Restore failed to clear Google tokens on linked backup jobs",
			logger.Int("restore_job_id", int(restoreJob.ID)),
			logger.Int("cron_job_id", int(cronJob.ID)),
			logger.String("error_kind", string(outcome.Kind)),
			logger.ErrorField(err))
		return
	}
	repo.StripGmailRefreshTokenFromCronJobInputData(cronJob)
	logger.Warn(ctx, "Restore cleared Google refresh tokens on linked backup jobs",
		logger.Int("restore_job_id", int(restoreJob.ID)),
		logger.Int("cron_job_id", int(cronJob.ID)),
		logger.String("error_kind", string(outcome.Kind)))
}

func handleRestoreFailure(ctx context.Context, store *db.PostgresDb, restoreJob *repo.RestoreJobListingDB, processErr error) restoreErrorOutcome {
	outcome := classifyRestoreError(restoreJob.Method, processErr)

	logger.Warn(ctx, "Restore failure classified",
		logger.Int("restore_job_id", int(restoreJob.ID)),
		logger.String("method", restoreJob.Method),
		logger.String("login_id", restoreJob.LoginID),
		logger.String("error_kind", string(outcome.Kind)),
		logger.Bool("clear_google", outcome.ClearGoogle),
		logger.ErrorField(processErr))

	applyRestoreGoogleTokenCleanup(ctx, store, restoreJob, outcome)
	return outcome
}
