package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	accessVerifyStaleRunning = 30 * time.Minute
	accessVerifyGraceDays    = 14
)

// runMicrosoftAccessCheckForTask resolves the tenant app-only token and probes one draft job.
// Used by ProcessTask for System: Access check tasks (same sequential worker as backups).
func (a *AutosyncManager) runMicrosoftAccessCheckForTask(ctx context.Context, task *repo.TaskListingDB, job *repo.CronJobListingDB) (string, error) {
	if a == nil || a.store == nil || job == nil {
		return outlook.AccessProbeError, fmt.Errorf("access check: missing store or job")
	}
	if !outlook.IsAccessVerifyUserMethod(job.Method) {
		return outlook.AccessProbeAccessible, nil
	}
	if !repo.IsAccessVerifyOwned(job) {
		// Already promoted / not a draft — treat as passed so backup can run.
		return outlook.AccessProbeAccessible, nil
	}
	credID := repo.JobCredentialID(job)
	tenantID := strings.ToLower(strings.TrimSpace(job.TenantID))
	if credID == 0 || tenantID == "" {
		return outlook.AccessProbeError, fmt.Errorf("access check: job missing credential or tenant")
	}
	batchID := uint(0)
	if m := repo.JobAccessVerify(job); m != nil {
		batchID = m.BatchID
	}
	tc, err := mstenant.Resolve(ctx, a.store, mstenant.Request{
		UserID:             job.UserID,
		CredentialID:       credID,
		TenantID:           tenantID,
		RequireApplication: true,
	})
	if err != nil {
		return outlook.AccessProbeError, fmt.Errorf("access check: resolve token: %w", err)
	}
	token := strings.TrimSpace(tc.Token)
	if token == "" {
		return outlook.AccessProbeError, fmt.Errorf("access check: empty app-only token")
	}
	_ = task
	status := handler.ApplyMicrosoftAccessVerifyProbe(ctx, a.store, token, job, batchID)
	return status, nil
}

// processMicrosoftAccessVerifyClaims claims and processes access-verify batches one at a time,
// matching backup ProcessTask: pick next work unit, finish it, then pick the next.
func (a *AutosyncManager) processMicrosoftAccessVerifyClaims(ctx context.Context) {
	if a == nil || a.store == nil || a.store.MicrosoftAccessVerifyRepo == nil {
		return
	}
	if _, err := a.store.MicrosoftAccessVerifyRepo.RequeueRunningStale(accessVerifyStaleRunning); err != nil {
		logger.Warn(ctx, "access-verify requeue stale", logger.ErrorField(err))
	}
	for {
		batch, err := a.store.MicrosoftAccessVerifyRepo.ClaimNextQueued()
		if err != nil {
			logger.Warn(ctx, "access-verify claim", logger.ErrorField(err))
			return
		}
		if batch == nil {
			return
		}
		if err := a.runMicrosoftAccessVerifyBatch(ctx, batch); err != nil {
			logger.Warn(ctx, "access-verify batch failed",
				logger.Int("batch_id", int(batch.ID)),
				logger.ErrorField(err))
			_ = a.store.MicrosoftAccessVerifyRepo.MarkFailed(batch.ID, err.Error())
		}
	}
}

func (a *AutosyncManager) runMicrosoftAccessVerifyBatch(ctx context.Context, batch *repo.MicrosoftAccessVerifyBatchDB) error {
	jobs, err := a.store.CronJobRepo.ListMicrosoftJobsByIDs(batch.UserID, batch.JobIDList())
	if err != nil {
		return err
	}
	tc, err := mstenant.Resolve(ctx, a.store, mstenant.Request{
		UserID:             batch.UserID,
		CredentialID:       batch.CredentialID,
		TenantID:           batch.TenantID,
		RequireApplication: true,
	})
	if err != nil {
		return fmt.Errorf("resolve tenant token: %w", err)
	}
	token := strings.TrimSpace(tc.Token)
	if token == "" {
		return fmt.Errorf("empty app-only token")
	}

	promoted, unavailable, errors, stillPend := 0, 0, 0, 0
	changed := false
	// Same as backup ProcessTask: one job probe at a time (no parallel workers).
	for i := range jobs {
		j := &jobs[i]
		if !outlook.IsAccessVerifyUserMethod(j.Method) {
			continue
		}
		m := repo.JobAccessVerify(j)
		if m == nil {
			if j.Active {
				continue
			}
		} else if m.State != repo.AccessVerifyStatePending && m.State != repo.AccessVerifyStateUnavailable {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res := handler.ApplyMicrosoftAccessVerifyProbe(ctx, a.store, token, j, batch.ID)
		switch res {
		case outlook.AccessProbeAccessible:
			promoted++
			changed = true
		case outlook.AccessProbeNoAccess:
			unavailable++
			changed = true
		case outlook.AccessProbeError:
			errors++
			stillPend++
		}
	}

	summary := map[string]interface{}{
		"promoted":    promoted,
		"unavailable": unavailable,
		"probe_error": errors,
		"pending":     stillPend,
	}
	notified := false
	if changed {
		notified = handler.NotifyMicrosoftAccessVerifyOwner(ctx, batch.UserID, batch.TenantID, promoted, unavailable)
	}
	return a.store.MicrosoftAccessVerifyRepo.MarkCompleted(batch.ID, summary, notified)
}

// microsoftAccessVerifyGraceDelete deletes jobs unavailable for 14+ days (no re-probe).
func (a *AutosyncManager) microsoftAccessVerifyGraceDelete(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	rows, err := a.store.CronJobRepo.ListMicrosoftAccessVerifyUnavailableJobs("", "")
	if err != nil {
		logger.Warn(ctx, "access-verify grace list", logger.ErrorField(err))
		return
	}
	cutoff := time.Now().UTC().Add(-accessVerifyGraceDays * 24 * time.Hour)
	deleted := 0
	for i := range rows {
		job := &rows[i]
		m := repo.JobAccessVerify(job)
		if m == nil || m.State != repo.AccessVerifyStateUnavailable {
			continue
		}
		since := strings.TrimSpace(m.UnavailableSince)
		if since == "" {
			since = strings.TrimSpace(m.CheckedAt)
		}
		ts, err := time.Parse(time.RFC3339, since)
		if err != nil || ts.IsZero() || ts.After(cutoff) {
			continue
		}
		if err := a.store.CronJobRepo.DeleteCronJobAndTasksByID(job.ID); err != nil {
			logger.Warn(ctx, "access-verify grace delete",
				logger.Int("job_id", int(job.ID)),
				logger.ErrorField(err))
			continue
		}
		_ = a.store.MicrosoftResourceRepo.RemoveUnavailableService(job.TenantID, job.ResourceType, job.ResourceID, job.Method)
		deleted++
	}
	if deleted > 0 {
		logger.Info(ctx, "access-verify grace deleted unavailable jobs", logger.Int("count", deleted))
	}
}
