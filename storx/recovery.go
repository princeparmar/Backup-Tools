package storx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	DefaultMaxRefreshFailures        = 3
	DefaultMaxUplinkRecoveriesPerRun = 3
	RefreshLimitJobMessage           = "StorX access could not be restored after 3 attempts. Automatic backup has been paused. Please contact support."
	TerminalRefreshJobMessage        = "StorX token refresh failed due to a server configuration error. Automatic backup has been paused. Please contact support."
)

// ErrRefreshFailed is returned when Satellite refresh fails but the linked job is still active.
var ErrRefreshFailed = errors.New("storx satellite token refresh failed")

// ErrRefreshLimitExceeded is returned when refresh failures reached the limit and the job was deactivated.
var ErrRefreshLimitExceeded = errors.New("storx refresh limit exceeded")

// Recovery applies shared StorX refresh policy for cron and restore-all.
type Recovery struct {
	Store *db.PostgresDb
	Job   *repo.CronJobListingDB
	Max   int
}

// NewRecovery creates a recovery helper for a linked backup cron job.
func NewRecovery(store *db.PostgresDb, job *repo.CronJobListingDB) *Recovery {
	return &Recovery{
		Store: store,
		Job:   job,
		Max:   maxRefreshFailures(),
	}
}

func maxRefreshFailures() int {
	raw := strings.TrimSpace(utils.GetEnvWithKey("STORX_REFRESH_MAX_ATTEMPTS"))
	if raw == "" {
		return DefaultMaxRefreshFailures
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return DefaultMaxRefreshFailures
	}
	return n
}

// MaxUplinkRecoveriesPerRun returns the in-memory uplink recovery cap per autosync task or restore batch.
func MaxUplinkRecoveriesPerRun() int {
	return DefaultMaxUplinkRecoveriesPerRun
}

// IsRefreshLimitError reports whether err ended in job deactivation after repeated refresh failures.
func IsRefreshLimitError(err error) bool {
	return errors.Is(err, ErrRefreshLimitExceeded)
}

// IsRefreshFailedError reports whether err is a Satellite refresh failure with the job still active.
func IsRefreshFailedError(err error) bool {
	return errors.Is(err, ErrRefreshFailed)
}

// IsTerminalRefreshError reports Satellite refresh failures that will not succeed on retry
// (e.g. missing user in service context). These stop the job immediately.
func IsTerminalRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "user is not in context")
}

// OnStorxError refreshes storx after an uplink/grant error and returns whether to retry the operation.
func (r *Recovery) OnStorxError(ctx context.Context, uplinkErr error) (newGrant string, continueOK bool, err error) {
	if r == nil || r.Store == nil || r.Job == nil {
		return "", false, uplinkErr
	}
	if !IsUplinkError(uplinkErr) {
		return "", false, uplinkErr
	}

	grant, refreshErr := RefreshAndSave(ctx, r.Store, r.Job)
	if refreshErr != nil {
		if IsTerminalRefreshError(refreshErr) {
			if termErr := r.deactivateJob(ctx, TerminalRefreshJobMessage); termErr != nil {
				return "", false, termErr
			}
			return "", false, fmt.Errorf("%w: %v", ErrRefreshLimitExceeded, refreshErr)
		}
		if recErr := r.recordRefreshFailure(ctx); recErr != nil {
			return "", false, recErr
		}
		if r.Job.StorxRefreshFailures >= uint(r.Max) {
			if termErr := r.deactivateJob(ctx, RefreshLimitJobMessage); termErr != nil {
				return "", false, termErr
			}
			return "", false, fmt.Errorf("%w: %v", ErrRefreshLimitExceeded, refreshErr)
		}
		return "", false, fmt.Errorf("%w: %v", ErrRefreshFailed, refreshErr)
	}

	if err := r.resetRefreshFailures(ctx); err != nil {
		return "", false, err
	}
	return grant, true, nil
}

func (r *Recovery) recordRefreshFailure(ctx context.Context) error {
	_ = ctx
	r.Job.StorxRefreshFailures++
	return r.Store.CronJobRepo.UpdateCronJobByID(r.Job.ID, map[string]interface{}{
		"storx_refresh_failures": r.Job.StorxRefreshFailures,
	})
}

func (r *Recovery) resetRefreshFailures(ctx context.Context) error {
	_ = ctx
	if r.Job.StorxRefreshFailures == 0 {
		return nil
	}
	r.Job.StorxRefreshFailures = 0
	return r.Store.CronJobRepo.UpdateCronJobByID(r.Job.ID, map[string]interface{}{
		"storx_refresh_failures": uint(0),
	})
}

func (r *Recovery) deactivateJob(ctx context.Context, msg string) error {
	_ = ctx
	if err := ClearGrant(r.Store, r.Job); err != nil {
		return err
	}
	if cid := repo.JobCredentialID(r.Job); cid > 0 {
		if err := r.Store.CronJobRepo.DeactivateAllJobsForCredential(cid, msg, false); err != nil {
			return err
		}
	}
	r.Job.Active = false
	r.Job.AutoDeactivated = true
	r.Job.Message = msg
	r.Job.MessageStatus = repo.JobMessageStatusError
	if r.Job.Status != repo.JobStatusSuccess {
		r.Job.Status = repo.JobStatusCancelled
	}
	patch := map[string]interface{}{
		"active":                 false,
		"auto_deactivated":       true,
		"message":                msg,
		"message_status":         repo.JobMessageStatusError,
		"storx_refresh_failures": r.Job.StorxRefreshFailures,
	}
	if r.Job.Status == repo.JobStatusCancelled {
		patch["status"] = repo.JobStatusCancelled
	}
	return r.Store.CronJobRepo.UpdateCronJobByID(r.Job.ID, patch)
}
