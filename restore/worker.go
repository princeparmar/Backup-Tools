package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	gormdb "gorm.io/gorm"
)

var ErrJobCancelled = errors.New("restore job cancelled")

// ProcessRestoreJobs runs batches for retry, running, and queued jobs until none are claimable.
func ProcessRestoreJobs(ctx context.Context, store *db.PostgresDb) error {
	stale := time.Now().Add(-10 * time.Minute)
	_ = store.RestoreJobRepo.MissedHeartbeatForJobs(stale)

	for {
		if err := processRetryTask(ctx, store); err != nil && !errors.Is(err, gormdb.ErrRecordNotFound) {
			logger.Warn(ctx, "restore retry task", logger.ErrorField(err))
		} else if err == nil {
			continue
		}

		job, err := store.RestoreJobRepo.ClaimNextRunningJob()
		if err == nil {
			if err := processJobBatches(ctx, store, job, nil); err != nil && !errors.Is(err, ErrJobCancelled) {
				return err
			}
			continue
		}
		if !errors.Is(err, gormdb.ErrRecordNotFound) {
			return err
		}

		job, err = store.RestoreJobRepo.ClaimNextQueuedJob()
		if err != nil {
			if errors.Is(err, gormdb.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := processJobBatches(ctx, store, job, nil); err != nil && !errors.Is(err, ErrJobCancelled) {
			return err
		}
	}
}

func processRetryTask(ctx context.Context, store *db.PostgresDb) error {
	task, err := store.RestoreTaskRepo.ClaimNextRetryTask()
	if err != nil {
		return err
	}
	job, err := store.RestoreJobRepo.GetByID(task.RestoreJobID)
	if err != nil {
		return err
	}
	if job.Status == repo.RestoreJobStatusCancelled {
		return nil
	}
	return processJobBatches(ctx, store, job, task)
}

func processJobBatches(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB, retryTask *repo.RestoreTaskListingDB) error {
	if job.Status == repo.RestoreJobStatusCancelled {
		return nil
	}

	logger.Info(ctx, "Processing restore job batch",
		logger.Int("job_id", int(job.ID)),
		logger.String("method", job.Method),
		logger.String("login_id", job.LoginID),
		logger.Int("cursor_id", int(job.CursorID)))

	if retryTask == nil && job.CursorID == 0 && job.ProcessedCount == 0 {
		notifyRestoreStarted(ctx, job)
	}

	proc, err := ProcessorForMethod(job.Method)
	if err != nil {
		return failJob(ctx, store, job, err)
	}

	deps, err := buildRestoreDeps(ctx, store, job)
	if err != nil {
		return failJob(ctx, store, job, err)
	}

	if err := proc.Setup(ctx, deps); err != nil {
		return failJob(ctx, store, job, err)
	}
	defer proc.Cleanup(ctx, deps)

	cfg := deps.Config
	prefix := strings.TrimSuffix(job.LoginID, "/") + "/"

	if job.TotalCount == 0 && job.CursorID == 0 {
		total, err := store.SyncedObjectRepo.CountSyncedObjectsForRestore(
			job.UserID, cfg.Bucket, cfg.Source, cfg.ObjectType, prefix)
		if err == nil {
			_ = store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{"total_count": total})
			job.TotalCount = uint(total)
		}
	}

	var rows []repo.SyncedObject
	if retryTask != nil && retryTask.CursorEndID > retryTask.CursorStartID {
		rows, err = store.SyncedObjectRepo.GetSyncedObjectsInIDRange(
			job.UserID, cfg.Bucket, cfg.Source, cfg.ObjectType, retryTask.CursorStartID, retryTask.CursorEndID)
	} else {
		rows, err = store.SyncedObjectRepo.GetSyncedObjectKeysPage(
			job.UserID, cfg.Bucket, cfg.Source, cfg.ObjectType, prefix, job.CursorID, cfg.BatchSize)
	}
	if err != nil {
		return failJob(ctx, store, job, err)
	}

	if len(rows) == 0 {
		return tryFinalizeJob(ctx, store, job)
	}

	var batchTask *repo.RestoreTaskListingDB
	if retryTask != nil {
		batchTask = retryTask
	} else {
		batchIdx, _ := store.RestoreJobRepo.CountBatchesForJob(job.ID)
		now := time.Now()
		batchTask = &repo.RestoreTaskListingDB{
			RestoreJobID:  job.ID,
			Status:        repo.RestoreTaskStatusRunning,
			BatchIndex:    batchIdx,
			CursorStartID: job.CursorID,
			StartedAt:     &now,
		}
		if err := store.RestoreTaskRepo.Create(batchTask); err != nil {
			return err
		}
	}

	if err := checkJobContinuable(store, job.ID); err != nil {
		return err
	}

	expectedCursor := job.CursorID
	batchResult := RunBatch(ctx, deps, proc, rows)

	if shouldOAuth401Retry(deps, batchResult) {
		logger.Warn(ctx, "Restore batch hit OAuth 401, refreshing Google token",
			logger.Int("job_id", int(job.ID)),
			logger.Int("task_id", int(batchTask.ID)))
		if err := deps.RefreshGoogleAccessToken(ctx); err != nil {
			logger.Warn(ctx, "Restore OAuth token refresh failed",
				logger.Int("job_id", int(job.ID)),
				logger.ErrorField(err))
			return failJob(ctx, store, job, err)
		}
		if err := proc.Setup(ctx, deps); err != nil {
			logger.Warn(ctx, "Restore processor setup failed after token refresh",
				logger.Int("job_id", int(job.ID)),
				logger.ErrorField(err))
			return failJob(ctx, store, job, err)
		}
		batchResult = RunBatch(ctx, deps, proc, rows)
	}

	newCursor := expectedCursor
	if batchResult.LastObjectID > 0 {
		newCursor = batchResult.LastObjectID
	}

	advanced, err := store.RestoreJobRepo.AdvanceRestoreJobCursor(
		job.ID, expectedCursor, newCursor, batchResult.Processed, batchResult.Failed)
	if err != nil {
		return err
	}
	if !advanced && retryTask == nil {
		logger.Warn(ctx, "restore cursor CAS lost — another worker may have advanced",
			logger.Int("job_id", int(job.ID)))
		return nil
	}

	_ = store.RestoreJobRepo.UpdateHeartBeat(job.ID)

	if len(rows) > 0 {
		batchTask.CursorEndID = newCursor
	}
	batchTask.ProcessedCount = batchResult.Processed
	batchTask.FailedCount = batchResult.Failed

	finishBatch(ctx, store, job, batchTask, batchResult)

	recordBatchItems(job.Method, batchResult.Processed, batchResult.Failed)
	maybeNotifyProgress(ctx, job)

	job.CursorID = newCursor
	job.ProcessedCount += batchResult.Processed
	job.FailedCount += batchResult.Failed

	if !shouldFetchAnotherBatch(len(rows), cfg.BatchSize) {
		return tryFinalizeJob(ctx, store, job)
	}
	return nil
}

func shouldOAuth401Retry(deps *RestoreDeps, result BatchResult) bool {
	if deps.AuthMode == repo.RestoreAuthModeDWD || strings.TrimSpace(deps.RefreshToken) == "" {
		return false
	}
	if result.Processed > 0 || result.Failed == 0 {
		return false
	}
	for _, fk := range result.FailedKeys {
		if fk.ErrorCode == "401" || strings.Contains(strings.ToLower(fk.Reason), "invalid_token") {
			return true
		}
	}
	return false
}

func checkJobContinuable(store *db.PostgresDb, jobID uint) error {
	job, err := store.RestoreJobRepo.GetByID(jobID)
	if err != nil {
		return err
	}
	if job.Status == repo.RestoreJobStatusCancelled {
		return ErrJobCancelled
	}
	return nil
}

func resolveJobTerminalStatus(failedCount uint) (status, message string) {
	if failedCount > 0 {
		return repo.RestoreJobStatusPartialCompleted, "restore completed with failures"
	}
	return repo.RestoreJobStatusCompleted, "restore completed"
}

func shouldFetchAnotherBatch(rowCount, batchSize int) bool {
	return rowCount >= batchSize
}

func IsActiveRestoreConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already in progress")
}

func tryFinalizeJob(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB) error {
	pending, err := store.RestoreJobRepo.CountPendingRetryTasks(job.ID)
	if err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}

	status, msg := resolveJobTerminalStatus(job.FailedCount)

	logger.Info(ctx, "Restore job finished",
		logger.Int("job_id", int(job.ID)),
		logger.String("method", job.Method),
		logger.String("login_id", job.LoginID),
		logger.String("status", status),
		logger.Int("processed", int(job.ProcessedCount)),
		logger.Int("failed", int(job.FailedCount)),
		logger.Int("total", int(job.TotalCount)))

	_ = store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{
		"status":  status,
		"message": msg,
	})

	recordJobTerminal(job.Method, status)
	notifyJobTerminal(ctx, job, status, msg)
	return nil
}

func failJob(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB, err error) error {
	outcome := handleRestoreFailure(ctx, store, job, err)

	_ = store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{
		"status":  repo.RestoreJobStatusFailed,
		"message": outcome.JobMessage,
	})
	recordJobTerminal(job.Method, repo.RestoreJobStatusFailed)
	notifyJobTerminal(ctx, job, repo.RestoreJobStatusFailed, outcome.JobMessage)
	return err
}

func finishBatch(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB, batchTask *repo.RestoreTaskListingDB, batchResult BatchResult) {
	if batchResult.Failed > 0 && batchResult.Processed == 0 && batchTask.RetryCount < repo.RestoreMaxRetryCount {
		logger.Warn(ctx, "Restore batch failed, scheduling retry",
			logger.Int("job_id", int(job.ID)),
			logger.Int("task_id", int(batchTask.ID)),
			logger.Int("retry_count", int(batchTask.RetryCount)),
			logger.Int("failed", int(batchResult.Failed)))
		scheduleTaskRetry(ctx, store, batchTask, "batch_failed")
		return
	}
	markTaskSuccess(store, batchTask)
	if batchResult.Failed > 0 {
		logger.Warn(ctx, "Restore batch completed with item failures",
			logger.Int("job_id", int(job.ID)),
			logger.Int("task_id", int(batchTask.ID)),
			logger.Int("processed", int(batchResult.Processed)),
			logger.Int("failed", int(batchResult.Failed)))
		writeDLQ(ctx, store, job, batchResult.FailedKeys)
	}
}

func markTaskSuccess(store *db.PostgresDb, task *repo.RestoreTaskListingDB) {
	now := time.Now()
	_ = store.RestoreTaskRepo.Update(task.ID, map[string]interface{}{
		"status":      repo.RestoreTaskStatusSuccess,
		"finished_at": now,
	})
}

func scheduleTaskRetry(ctx context.Context, store *db.PostgresDb, task *repo.RestoreTaskListingDB, code string) {
	next := NextRetryTime(task.RetryCount)
	retryMsg := restoreBatchRetryMessage(task.RetryCount + 1)
	logger.Info(ctx, "Restore task retry scheduled",
		logger.Int("task_id", int(task.ID)),
		logger.Int("job_id", int(task.RestoreJobID)),
		logger.Int("attempt", int(task.RetryCount+1)),
		logger.String("error_code", code),
		logger.String("message", retryMsg))

	_ = store.RestoreTaskRepo.Update(task.ID, map[string]interface{}{
		"status":          repo.RestoreTaskStatusRetrying,
		"retry_count":     task.RetryCount + 1,
		"next_attempt_at": next,
		"error_code":      code,
	})
}

func writeDLQ(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB, keys []FailedKey) {
	if len(keys) == 0 {
		return
	}
	logger.Warn(ctx, "Restore dead-letter items recorded",
		logger.Int("job_id", int(job.ID)),
		logger.Int("count", len(keys)))

	for _, fk := range keys {
		reason := fk.Reason
		if len(reason) > 500 {
			reason = reason[:500]
		}
		_ = store.RestoreJobRepo.CreateDeadItem(&repo.RestoreDeadItemDB{
			RestoreJobID: job.ID,
			ObjectKey:    fk.ObjectKey,
			Reason:       reason,
			ErrorCode:    fk.ErrorCode,
		})
	}
}

func notifyRestoreStarted(ctx context.Context, job *repo.RestoreJobListingDB) {
	priority := "normal"
	data := map[string]interface{}{
		"event":    "restore_started",
		"job_id":   job.ID,
		"method":   job.Method,
		"login_id": job.LoginID,
	}
	satellite.SendNotificationAsync(ctx, job.UserID, "Restore started",
		fmt.Sprintf("Restore all started for %s", job.LoginID),
		&priority, data, nil)
}

func maybeNotifyProgress(ctx context.Context, job *repo.RestoreJobListingDB) {
	if job.TotalCount == 0 {
		return
	}
	pct := float64(job.ProcessedCount+job.FailedCount) / float64(job.TotalCount)
	if pct < 0.05 && job.ProcessedCount < 500 {
		return
	}
	priority := "normal"
	data := map[string]interface{}{
		"event":     "restore_progress",
		"job_id":    job.ID,
		"method":    job.Method,
		"login_id":  job.LoginID,
		"processed": job.ProcessedCount,
		"failed":    job.FailedCount,
		"total":     job.TotalCount,
	}
	satellite.SendNotificationAsync(ctx, job.UserID, "Restore in progress",
		fmt.Sprintf("Restore %s: %d/%d processed", job.LoginID, job.ProcessedCount, job.TotalCount),
		&priority, data, nil)
}

func notifyJobTerminal(ctx context.Context, job *repo.RestoreJobListingDB, status, detail string) {
	priority := "normal"
	event := "restore_completed"
	title := "Restore completed"
	body := fmt.Sprintf("Restore for %s finished (%s)", job.LoginID, status)
	if status == repo.RestoreJobStatusPartialCompleted {
		event = "restore_partial_completed"
		title = "Restore partially completed"
	} else if status == repo.RestoreJobStatusFailed {
		event = "restore_failed"
		title = restoreNotifyFailedTitle
		priority = "high"
		if detail != "" {
			body = detail
		}
	} else if status == repo.RestoreJobStatusCancelled {
		event = "restore_cancelled"
		title = "Restore cancelled"
	}
	data := map[string]interface{}{
		"event": event, "job_id": job.ID, "method": job.Method, "login_id": job.LoginID,
		"processed": job.ProcessedCount, "failed": job.FailedCount, "total": job.TotalCount,
	}
	satellite.SendNotificationAsync(ctx, job.UserID, title, body, &priority, data, nil)
}

// CancelRestoreJob cancels a running restore job.
func CancelRestoreJob(store *db.PostgresDb, userID string, jobID uint) error {
	job, err := store.RestoreJobRepo.GetByIDForUser(userID, jobID)
	if err != nil {
		return err
	}
	if job.Status == repo.RestoreJobStatusCompleted || job.Status == repo.RestoreJobStatusFailed {
		return fmt.Errorf("job already finished")
	}
	now := time.Now()
	_ = store.RestoreTaskRepo.CancelPendingForJob(job.ID)
	return store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{
		"status":       repo.RestoreJobStatusCancelled,
		"cancelled_at": now,
		"message":      "cancelled by user",
	})
}
