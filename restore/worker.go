package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	gormdb "gorm.io/gorm"
)

var ErrJobCancelled = errors.New("restore job cancelled")

// ProcessRestoreJobs runs one batch for each claimed job or retry task.
func ProcessRestoreJobs(ctx context.Context, store *db.PostgresDb) error {
	stale := time.Now().Add(-10 * time.Minute)
	_ = store.RestoreTaskRepo.MissedHeartbeatForTasks(stale)
	_ = store.RestoreJobRepo.MissedHeartbeatForJobs(stale)

	if err := processRetryTask(ctx, store); err != nil && !errors.Is(err, gormdb.ErrRecordNotFound) {
		logger.Warn(ctx, "restore retry task", logger.ErrorField(err))
	}

	job, err := store.RestoreJobRepo.ClaimNextQueuedJob()
	if err != nil {
		if errors.Is(err, gormdb.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	return processJobBatches(ctx, store, job, nil)
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

	if retryTask == nil && job.CursorID == 0 && job.ProcessedCount == 0 {
		notifyRestoreStarted(ctx, job)
	}

	proc, err := ProcessorForMethod(job.Method)
	if err != nil {
		return failJob(ctx, store, job, err)
	}

	deps, err := buildRestoreDeps(store, job)
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
		batchTask = &repo.RestoreTaskListingDB{
			RestoreJobID:  job.ID,
			Status:        repo.RestoreTaskStatusRunning,
			BatchIndex:    job.TasksTotal,
			CursorStartID: job.CursorID,
			ItemCount:     uint(len(rows)),
		}
		now := time.Now()
		batchTask.StartTime = &now
		batchTask.LastHeartBeat = &now
		if err := store.RestoreTaskRepo.Create(batchTask); err != nil {
			return err
		}
		_ = store.RestoreJobRepo.IncrementTasksTotal(job.ID, 1)
	}

	if err := checkJobContinuable(store, job.ID); err != nil {
		return err
	}

	expectedCursor := job.CursorID
	batchResult := RunBatch(ctx, deps, proc, rows)

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

	endID := newCursor
	if len(rows) > 0 {
		batchTask.CursorEndID = endID
	}
	batchTask.ProcessedCount = batchResult.Processed
	batchTask.FailedCount = batchResult.Failed
	batchTask.ItemCount = uint(len(rows))

	finishBatch(store, job, batchTask, batchResult)

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

// resolveJobTerminalStatus picks completed vs partial_completed from aggregate failures.
func resolveJobTerminalStatus(failedCount uint) (status, message string) {
	if failedCount > 0 {
		return repo.RestoreJobStatusPartialCompleted, "restore completed with failures"
	}
	return repo.RestoreJobStatusCompleted, "restore completed"
}

// shouldFetchAnotherBatch reports whether the cursor may have more rows after this page.
func shouldFetchAnotherBatch(rowCount, batchSize int) bool {
	return rowCount >= batchSize
}

// IsActiveRestoreConflict reports duplicate active job errors from CreateRestoreJob.
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

	_ = store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{
		"status":  status,
		"message": msg,
	})

	recordJobTerminal(job.Method, status)
	notifyJobTerminal(ctx, job, status)
	return nil
}

func failJob(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB, err error) error {
	_ = store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{
		"status":  repo.RestoreJobStatusFailed,
		"message": err.Error(),
	})
	recordJobTerminal(job.Method, repo.RestoreJobStatusFailed)
	notifyJobTerminal(ctx, job, repo.RestoreJobStatusFailed)
	return err
}

func finishBatch(store *db.PostgresDb, job *repo.RestoreJobListingDB, batchTask *repo.RestoreTaskListingDB, batchResult BatchResult) {
	if batchResult.Failed > 0 && batchResult.Processed == 0 && batchTask.RetryCount < repo.RestoreMaxRetryCount {
		scheduleTaskRetry(store, batchTask, "batch_failed")
		_ = store.RestoreJobRepo.IncrementTasksFailed(job.ID)
		return
	}
	markTaskSuccess(store, batchTask)
	_ = store.RestoreJobRepo.IncrementTasksCompleted(job.ID)
	if batchResult.Failed > 0 {
		writeDLQ(store, job, batchTask, batchResult.FailedKeys)
	}
}

func markTaskSuccess(store *db.PostgresDb, task *repo.RestoreTaskListingDB) {
	now := time.Now()
	exec := uint64(0)
	if task.StartTime != nil {
		exec = uint64(now.Sub(*task.StartTime).Seconds())
	}
	_ = store.RestoreTaskRepo.Update(task.ID, map[string]interface{}{
		"status":     repo.RestoreTaskStatusSuccess,
		"execution":  exec,
		"message":    "batch completed",
	})
}

func scheduleTaskRetry(store *db.PostgresDb, task *repo.RestoreTaskListingDB, msg string) {
	next := NextRetryTime(task.RetryCount)
	_ = store.RestoreTaskRepo.Update(task.ID, map[string]interface{}{
		"status":          repo.RestoreTaskStatusRetrying,
		"retry_count":     task.RetryCount + 1,
		"retry_after":     next,
		"next_attempt_at": next,
		"message":         msg,
	})
}

func writeDLQ(store *db.PostgresDb, job *repo.RestoreJobListingDB, task *repo.RestoreTaskListingDB, keys []FailedKey) {
	for _, fk := range keys {
		_ = store.RestoreJobRepo.CreateDeadItem(&repo.RestoreDeadItemDB{
			RestoreJobID:  job.ID,
			RestoreTaskID: task.ID,
			ObjectKey:     fk.ObjectKey,
			Service:       job.Service,
			Reason:        fk.Reason,
			LastErrorCode: fk.ErrorCode,
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

func notifyJobTerminal(ctx context.Context, job *repo.RestoreJobListingDB, status string) {
	priority := "normal"
	event := "restore_completed"
	title := "Restore completed"
	if status == repo.RestoreJobStatusPartialCompleted {
		event = "restore_partial_completed"
		title = "Restore partially completed"
	} else if status == repo.RestoreJobStatusFailed {
		event = "restore_failed"
		title = "Restore failed"
		priority = "high"
	} else if status == repo.RestoreJobStatusCancelled {
		event = "restore_cancelled"
		title = "Restore cancelled"
	}
	data := map[string]interface{}{
		"event": event, "job_id": job.ID, "method": job.Method, "login_id": job.LoginID,
		"processed": job.ProcessedCount, "failed": job.FailedCount, "total": job.TotalCount,
	}
	satellite.SendNotificationAsync(ctx, job.UserID, title,
		fmt.Sprintf("Restore for %s finished (%s)", job.LoginID, status),
		&priority, data, nil)
}

// CreateRestoreJob inserts a queued restore job from API input.
func CreateRestoreJob(store *db.PostgresDb, userID, method, service, loginID, storxToken, googleAccessToken string) (*repo.RestoreJobListingDB, error) {
	active, err := store.RestoreJobRepo.HasActiveJob(userID, method, loginID)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, fmt.Errorf("a restore is already in progress for this account and service")
	}
	input := map[string]interface{}{
		"google_access_token": googleAccessToken,
		"email":               loginID,
	}
	job := &repo.RestoreJobListingDB{
		UserID:        userID,
		LoginID:       loginID,
		Method:        method,
		Service:       service,
		Status:        repo.RestoreJobStatusQueued,
		StorxToken:    storxToken,
		InputData:     database.NewDbJsonFromValue(input),
		MessageStatus: repo.JobMessageStatusInfo,
		Message:       "restore queued",
	}
	if err := store.RestoreJobRepo.Create(job); err != nil {
		return nil, err
	}
	return job, nil
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
