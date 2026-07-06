package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	tasks "github.com/StorX2-0/Backup-Tools/tasks"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type ProcessorInput struct {
	InputData     *database.DbJson[map[string]interface{}]
	Task          *repo.TaskListingDB
	Job           *repo.CronJobListingDB
	HeartBeatFunc func() error
	Database      *db.PostgresDb
	StorxRecovery *handler.StorxRecovery
}

type Processor interface {
	Run(ProcessorInput) error
}

var processorMap = map[string]Processor{
	"gmail":           NewGmailProcessor(),
	"outlook":         NewOutlookProcessor(),
	"psql_database":   NewPsqlDatabaseProcessor(),
	"google_drive":    NewGoogleDriveProcessor(),
	"google_photos":   NewGooglePhotosProcessor(),
	"google_contacts": NewGoogleContactsProcessor(),
	"google_calendar": NewGoogleCalendarProcessor(),
}

type AutosyncManager struct {
	store *db.PostgresDb
}

func NewAutosyncManager(store *db.PostgresDb) *AutosyncManager {
	return &AutosyncManager{store: store}
}

// createCronContext creates a context with trace ID for cron jobs
func createCronContext(operation string) context.Context {
	traceID := uuid.New().String()
	ctx := logger.WithTraceID(context.Background(), traceID)
	logger.Info(ctx, "Cron job started", logger.String("operation", operation))
	return ctx
}

func (a *AutosyncManager) Start() {
	// Skip a new process_tasks tick while the previous tick is still running a backup.
	// One autosync run at a time; queued tasks (pushed) wait until the loop finishes.
	c := cron.New(cron.WithChain(cron.DelayIfStillRunning(cron.DefaultLogger)))

	// Create tasks for pending jobs
	c.AddFunc("@every 1m", func() {
		ctx := createCronContext("create_tasks")
		logger.Info(ctx, "Creating tasks for all pending jobs")
		err := a.CreateTaskForAllPendingJobs(ctx)
		if err != nil {
			logger.Error(ctx, "Failed to create tasks for pending jobs", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully created tasks for pending jobs")
		}
	})

	// Process tasks
	c.AddFunc("@every 1m", func() {
		ctx := createCronContext("process_tasks")
		logger.Info(ctx, "Processing tasks")
		err := a.ProcessTask(ctx)
		if err != nil {
			logger.Error(ctx, "Failed to process tasks", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully processed tasks")
		}
	})

	// Check for missed heartbeats
	c.AddFunc("@every 1m", func() {
		ctx := createCronContext("missed_heartbeat_check")
		logger.Info(ctx, "Checking for missed heartbeats")

		err := a.store.TaskRepo.MissedHeartbeatForTask()
		if err != nil {
			logger.Error(ctx, "Failed to check for missed heartbeats", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully checked for missed heartbeats")
		}

	})

	// Check for missed heartbeats for scheduled tasks
	c.AddFunc("@every 1m", func() {
		ctx := createCronContext("missed_scheduled_task_heartbeat_check")
		logger.Info(ctx, "Checking for missed scheduled task heartbeats")

		err := a.store.ScheduledTasksRepo.MissedHeartbeatForScheduledTask()
		if err != nil {
			logger.Error(ctx, "Failed to check for missed scheduled task heartbeats", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully checked for missed scheduled task heartbeats")
		}
	})

	// Process scheduled tasks
	c.AddFunc("@every 30s", func() {
		ctx := createCronContext("process_scheduled_tasks")
		logger.Info(ctx, "Processing scheduled tasks")
		scheduledTaskManager := tasks.NewScheduledTaskManager(a.store)
		err := scheduledTaskManager.ProcessScheduledTasks(ctx)
		if err != nil {
			logger.Error(ctx, "Failed to process scheduled tasks", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully processed scheduled tasks")
		}
	})

	// c.AddFunc("@every 1m", func() {
	// 	fmt.Println("Refreshing google auth token")
	// 	err := a.RefreshGoogleAuthToken()
	// 	if err != nil {
	// 		fmt.Println("Failed to refresh google auth token", err)
	// 		return
	// 	}

	// 	fmt.Println("Google auth token refreshed")
	// })

	c.Start()
	logger.Info(context.Background(), "Cron scheduler started successfully")
}

func (a *AutosyncManager) CreateTaskForAllPendingJobs(ctx context.Context) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	jobIDs, err := a.store.CronJobRepo.GetJobsToProcess()
	if err != nil {
		return fmt.Errorf("failed to get jobs to process: %w", err)
	}

	if len(jobIDs) == 0 {
		logger.Info(ctx, "No jobs to process")
		return nil
	}

	successCount := 0
	errorCount := 0

	for _, jobID := range jobIDs {
		logger.Info(ctx, "Creating task for job",
			logger.Int("job_id", int(jobID.ID)),
			logger.String("job_name", jobID.Name),
		)

		_, err := a.store.TaskRepo.CreateTaskForCronJob(jobID.ID)
		if err != nil {
			// Log error but continue with other jobs
			logger.Error(ctx, "Failed to create task for job",
				logger.Int("job_id", int(jobID.ID)),
				logger.ErrorField(err),
			)
			errorCount++
			continue
		}

		logger.Info(ctx, "Successfully created task for job",
			logger.Int("job_id", int(jobID.ID)),
		)
		successCount++
	}

	// Record overall execution metrics

	logger.Info(ctx, "Task creation completed",
		logger.Int("successful", successCount),
		logger.Int("failed", errorCount),
	)

	return nil
}

func (a *AutosyncManager) ProcessTask(ctx context.Context) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	processedCount := 0
	errorCount := 0

	for {
		task, err := a.store.TaskRepo.GetPushedTask()
		if err != nil {
			if strings.Contains(err.Error(), "record not found") {
				logger.Info(ctx, "No tasks to process")
				break
			}
			return fmt.Errorf("failed to get pushed task: %w", err)
		}

		logger.Info(ctx, "Processing task",
			logger.Int("task_id", int(task.ID)),
			logger.Int("job_id", int(task.CronJobID)),
		)

		job, err := a.store.CronJobRepo.GetCronJobByID(task.CronJobID)
		if err != nil {
			logger.Error(ctx, "Failed to get cron job for task",
				logger.Int("task_id", int(task.ID)),
				logger.Int("job_id", int(task.CronJobID)),
				logger.ErrorField(err),
			)
			errorCount++
			// Update task status with error and continue to next task
			if updateErr := a.UpdateTaskStatus(task, job, err); updateErr != nil {
				logger.Error(ctx, "Failed to update task status after job fetch error",
					logger.Int("task_id", int(task.ID)),
					logger.ErrorField(updateErr),
				)
			}
			continue
		}

		// Send notification for cron task started running
		priority := "normal"
		data := map[string]interface{}{
			"event":   "cron_started_running",
			"level":   2,
			"task_id": task.ID,
			"job_id":  job.ID,
			"method":  job.Method,
			"name":    job.Name,
		}
		satellite.SendNotificationAsync(ctx, job.UserID, "Automatic Backup Started", fmt.Sprintf("Automatic backup for %s has started running", job.Name), &priority, data, nil)

		// Process the task
		processErr := a.processTask(ctx, task, job)

		// Update task status
		if updateErr := a.UpdateTaskStatus(task, job, processErr); updateErr != nil {
			logger.Error(ctx, "Failed to update task status",
				logger.Int("task_id", int(task.ID)),
				logger.ErrorField(updateErr),
			)
			// Continue with next task even if status update fails
			continue
		}

		processedCount++
		if processErr != nil {
			errorCount++
		}
	}

	// Record overall execution metrics

	logger.Info(ctx, "Task processing completed",
		logger.Int("processed", processedCount),
		logger.Int("errors", errorCount),
	)

	return nil
}

func (a *AutosyncManager) processTask(ctx context.Context, task *repo.TaskListingDB, job *repo.CronJobListingDB) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	processor, ok := processorMap[job.Method]
	if !ok {
		return fmt.Errorf("processor for method '%s' not found", job.Method)
	}

	logger.Info(ctx, "Executing processor for task",
		logger.Int("task_id", int(task.ID)),
		logger.String("method", job.Method),
	)

	recovery := handler.NewStorxRecovery(a.store, job)
	if strings.TrimSpace(a.store.CronJobRepo.ResolvedStorxToken(job)) == "" {
		if _, continueOK, preErr := recovery.OnStorxError(ctx, handler.ErrStorxGrantMissing); preErr != nil || !continueOK {
			if preErr != nil {
				return preErr
			}
			return handler.ErrStorxGrantMissing
		}
	}

	input := ProcessorInput{
		InputData:     job.InputData,
		Job:           job,
		Task:          task,
		Database:      a.store,
		StorxRecovery: recovery,
		HeartBeatFunc: func() error {
			currentTask, hbErr := a.store.TaskRepo.GetTaskByID(task.ID)
			if hbErr != nil {
				return fmt.Errorf("failed to get task status: %w", hbErr)
			}
			if currentTask.Status != repo.TaskStatusRunning {
				return fmt.Errorf("task status changed to '%s', stopping execution", currentTask.Status)
			}
			if hbErr := a.store.TaskRepo.UpdateHeartBeatForTask(task.ID); hbErr != nil {
				return fmt.Errorf("failed to update heartbeat: %w", hbErr)
			}
			return nil
		},
	}

	uplinkRecoveries := 0
	for {
		err = processor.Run(input)
		if err == nil {
			return nil
		}
		if !handler.IsStorxUplinkError(err) {
			return err
		}
		uplinkRecoveries++
		if uplinkRecoveries > handler.MaxStorxUplinkRecoveriesPerRun() {
			return err
		}
		_, continueOK, recErr := recovery.OnStorxError(ctx, err)
		if recErr != nil {
			return recErr
		}
		if !continueOK {
			return err
		}
	}
}

func (a *AutosyncManager) UpdateTaskStatus(task *repo.TaskListingDB, job *repo.CronJobListingDB, processErr error) error {
	ctx := context.Background() // You might want to pass context here
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Initialize default values for success case
	task.Status = repo.TaskStatusSuccess
	task.Message = "Automatic backup completed successfully"

	if task.StartTime != nil {
		task.Execution = uint64(time.Since(*task.StartTime).Seconds())
	}

	if job != nil {
		job.Message = "Automatic backup completed successfully"
		job.MessageStatus = repo.JobMessageStatusInfo
		now := time.Now()
		job.LastRun = &now
	}

	// Handle error case
	if processErr != nil {
		task.Status = repo.TaskStatusFailed
		task.RetryCount++

		// Record task failure
		if job != nil {
			job.Message = "Last task execution failed"
			job.MessageStatus = repo.JobMessageStatusError
			now := time.Now()
			job.LastRun = &now

			a.handleErrorScenarios(processErr, job, task)
			emailOverride := applyIntervalFailurePeriod(job, task)
			emailMessage := a.determineErrorMessage(processErr, job, task)
			if emailOverride != "" {
				emailMessage = emailOverride
			}

			// Send email notification
			go satellite.SendEmailForBackupFailure(context.Background(), job.Name, emailMessage, job.Method)

			// Send generic notification with level 4
			priority := "high"
			data := map[string]interface{}{
				"event":     "cron_failed",
				"level":     4,
				"task_id":   task.ID,
				"job_id":    job.ID,
				"method":    job.Method,
				"name":      job.Name,
				"error":     processErr.Error(),
				"execution": task.Execution,
			}
			satellite.SendNotificationAsync(context.Background(), job.UserID, "Automatic Backup Failed", fmt.Sprintf("Automatic backup for %s failed: %s", job.Name, emailMessage), &priority, data, nil)
		} else {
			task.Message = processErr.Error()
		}
	} else {
		// Handle success case - send notification
		if job != nil {
			job.FailurePeriods = 0
			priority := "normal"
			data := map[string]interface{}{
				"event":     "cron_successfully_completed",
				"level":     2,
				"task_id":   task.ID,
				"job_id":    job.ID,
				"method":    job.Method,
				"name":      job.Name,
				"execution": task.Execution,
			}
			satellite.SendNotificationAsync(context.Background(), job.UserID, "Automatic Backup Completed", fmt.Sprintf("Automatic backup for %s completed successfully in %d seconds", job.Name, task.Execution), &priority, data, nil)
		}
	}

	// Save task to database
	if err := a.store.TaskRepo.UpdateTaskByID(task.ID, map[string]interface{}{
		"status":    task.Status,
		"message":   task.Message,
		"execution": task.Execution,
	}); err != nil {
		logger.Error(ctx, "Failed to save task status",
			logger.Int("task_id", int(task.ID)),
			logger.ErrorField(err),
		)
		return fmt.Errorf("failed to save task: %w", err)
	}

	// Save job to database if job exists
	if job != nil {
		updateMap := map[string]interface{}{
			"message":          job.Message,
			"message_status":   job.MessageStatus,
			"last_run":         job.LastRun,
			"storx_token":      job.StorxToken,
			"active":           job.Active,
			"auto_deactivated": job.AutoDeactivated,
		}
		if job.InputData != nil {
			updateMap["input_data"] = job.InputData
		}

		// Update cron job status based on task status (StorX credential deactivation preserves success).
		if handler.IsStorxSatelliteRefreshError(processErr) || handler.IsStorxStorageLimitError(processErr) {
			if job.Status != repo.JobStatusSuccess {
				updateMap["status"] = repo.JobStatusCancelled
			}
		} else {
			switch task.Status {
			case repo.TaskStatusSuccess:
				updateMap["status"] = repo.JobStatusSuccess
			case repo.TaskStatusFailed:
				updateMap["status"] = repo.JobStatusFailed
			}
		}

		// Use cron-specific update function to safely handle one-time jobs
		if err := a.store.CronJobRepo.UpdateCronJobFieldsForCron(job.ID, updateMap); err != nil {
			logger.Error(ctx, "Failed to save job status",
				logger.Int("job_id", int(job.ID)),
				logger.ErrorField(err),
			)
			return fmt.Errorf("failed to save job: %w", err)
		}
	}

	logger.Info(ctx, "Task status updated",
		logger.Int("task_id", int(task.ID)),
		logger.String("status", string(task.Status)),
		logger.Int("retry_count", int(task.RetryCount)),
	)

	return nil
}

// applyIntervalFailurePeriod increments FailurePeriods when per-task retries are exhausted; deactivates the job after MaxFailurePeriods.
// Returns a non-empty email body override when the job was deactivated for this reason.
func applyIntervalFailurePeriod(job *repo.CronJobListingDB, task *repo.TaskListingDB) string {
	if job == nil || task == nil || task.RetryCount < repo.MaxRetryCount {
		return ""
	}
	job.FailurePeriods++
	if job.FailurePeriods < repo.MaxFailurePeriods {
		return ""
	}
	if !job.Active {
		return ""
	}
	job.Active = false
	job.AutoDeactivated = true
	job.Message = cronJobFailurePeriodsExhausted
	task.Message = cronTaskFailurePeriodsExhausted
	return cronEmailFailurePeriodsExhausted
}

func (a *AutosyncManager) gmailStorxMissing(job *repo.CronJobListingDB) bool {
	if job == nil {
		return true
	}
	if repo.IsGoogleMediaOrGmailMethod(job.Method) {
		return strings.TrimSpace(a.store.CronJobRepo.ResolvedStorxToken(job)) == ""
	}
	return strings.TrimSpace(job.StorxToken) == ""
}

func (a *AutosyncManager) clearStorxOnUplinkFailure(job *repo.CronJobListingDB) {
	if job == nil {
		return
	}
	job.StorxToken = ""
}

// gmailRefreshOrTokenExchangeFailure: Gmail-only Google OAuth refresh / token-endpoint errors.
// invalid_grant must not hit the Outlook branch (same substring for Microsoft and Google).
func gmailRefreshOrTokenExchangeFailure(job *repo.CronJobListingDB, errMsg string) bool {
	if job == nil || job.Method != "gmail" {
		return false
	}
	e := strings.ToLower(errMsg)
	return strings.Contains(e, "invalid_grant") ||
		strings.Contains(errMsg, "error while generating auth token") ||
		strings.Contains(e, "error parsing response json")
}

func isPersonalGmailCronJob(job *repo.CronJobListingDB) bool {
	if job == nil || job.Method != "gmail" {
		return false
	}
	email := strings.TrimSpace(job.Name)
	if job.InputData != nil && job.InputData.Json() != nil {
		if s, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(s) != "" {
			email = strings.TrimSpace(s)
		}
	}
	e := strings.ToLower(email)
	return strings.HasSuffix(e, "@gmail.com") || strings.HasSuffix(e, "@googlemail.com")
}

func isGmailOAuthOrDelegationFailure(errMsg string) bool {
	e := strings.ToLower(errMsg)
	return strings.Contains(e, "unauthorized_client") ||
		(strings.Contains(e, "oauth2:") && strings.Contains(e, "cannot fetch token")) ||
		strings.Contains(e, "delegated gmail access failed") ||
		strings.Contains(e, "domain-wide delegation") ||
		strings.Contains(e, "reconnecting the same google account")
}

func (a *AutosyncManager) deactivatePersonalGmailAuth(job *repo.CronJobListingDB, task *repo.TaskListingDB) {
	if err := a.store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(job, cronJobPersonalGoogleAuthDeactivate); err != nil {
		logger.Warn(context.Background(), "Failed to deactivate jobs after personal Gmail auth failure",
			logger.Int("job_id", int(job.ID)), logger.ErrorField(err))
	}
	repo.StripGmailRefreshTokenFromCronJobInputData(job)
	job.Active = false
	job.AutoDeactivated = true
	job.Message = cronJobPersonalGoogleAuthDeactivate
	task.Message = cronTaskPersonalGoogleAuthDeactivated
}

func (a *AutosyncManager) deactivateAllJobsForStorageLimit(job *repo.CronJobListingDB, task *repo.TaskListingDB) {
	if job == nil {
		return
	}
	if err := a.store.CronJobRepo.DeactivateAllActiveJobsForUser(job.UserID, cronJobStorxStorageLimitFinal); err != nil {
		logger.Warn(context.Background(), "Failed to deactivate jobs after storage limit exceeded",
			logger.String("user_id", job.UserID),
			logger.Int("job_id", int(job.ID)),
			logger.ErrorField(err))
	}
	job.Active = false
	job.AutoDeactivated = true
	job.Message = cronJobStorxStorageLimitFinal
	if task != nil {
		task.Message = cronTaskStorxStorageLimitDeactivated
		task.RetryCount = repo.MaxRetryCount
	}
}

func (a *AutosyncManager) determineErrorMessage(processErr error, job *repo.CronJobListingDB, task *repo.TaskListingDB) string {
	errMsg := processErr.Error()
	errLower := strings.ToLower(errMsg)

	switch {
	case handler.IsStorxStorageLimitError(processErr):
		return cronEmailStorxStorageLimitFinal

	case handler.IsStorxSatelliteRefreshError(processErr):
		return cronEmailStorxSatelliteRefreshFinal

	case handler.IsStorxRefreshFailedError(processErr):
		return cronEmailStorxRefreshRetry

	case handler.IsStorxUplinkError(processErr):
		return cronEmailStorxRefreshRetry

	case job != nil && job.Method == "gmail" && isPersonalGmailCronJob(job) &&
		(isGmailOAuthOrDelegationFailure(errMsg) || gmailRefreshOrTokenExchangeFailure(job, errMsg)):
		return cronEmailGoogleAuthFinal

	case job != nil && job.Method == "gmail" && isGmailOAuthOrDelegationFailure(errMsg):
		return cronEmailDelegationFinal

	case job != nil && job.Method == "gmail" &&
		(strings.Contains(errLower, "access_token_scope_insufficient") ||
			strings.Contains(errLower, "insufficient authentication scopes")):
		return cronEmailGoogleInsufficientScope

	case strings.Contains(errMsg, "googleapi: Error 401") ||
		strings.Contains(errMsg, "oauth credential not found") ||
		strings.Contains(errMsg, "refresh token not found"):
		// gmailRefreshOrTokenExchangeFailure(job, errMsg):
		if task.RetryCount == repo.MaxRetryCount-1 {
			return cronEmailGoogleAuthFinal
		}
		return cronEmailGoogleAuthRetry(task.RetryCount)

	case strings.Contains(errMsg, "Access is denied") ||
		(job != nil && job.Method == "outlook" && strings.Contains(strings.ToLower(errMsg), "invalid_grant")) ||
		(strings.Contains(errMsg, "microsoftgraph") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))) ||
		(strings.Contains(errMsg, "Microsoft Graph API") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))):
		if task.RetryCount == repo.MaxRetryCount-1 {
			return cronEmailOutlookAuthFinal
		}
		return cronEmailOutlookAuthRetry(task.RetryCount)

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		return cronEmailNetworkFinal

	default:
		return cronEmailGenericRetry(task.RetryCount, processErr)
	}
}

func (a *AutosyncManager) handleErrorScenarios(processErr error, job *repo.CronJobListingDB, task *repo.TaskListingDB) {
	errMsg := processErr.Error()
	errLower := strings.ToLower(errMsg)

	switch {
	case handler.IsStorxStorageLimitError(processErr):
		a.deactivateAllJobsForStorageLimit(job, task)

	case handler.IsStorxSatelliteRefreshError(processErr):
		job.Message = cronJobStorxSatelliteRefreshFinal
		task.Message = cronTaskStorxSatelliteRefreshFinal

	case handler.IsStorxRefreshFailedError(processErr):
		job.Message = "StorX refresh failed; automatic backup will retry"
		task.Message = processErr.Error()

	case handler.IsStorxUplinkError(processErr) || a.gmailStorxMissing(job):
		job.Message = "StorX access issue; refresh attempted during backup"
		task.Message = processErr.Error()

	case job != nil && job.Method == "gmail" && isPersonalGmailCronJob(job) &&
		(isGmailOAuthOrDelegationFailure(errMsg) || gmailRefreshOrTokenExchangeFailure(job, errMsg)):
		a.deactivatePersonalGmailAuth(job, task)

	case job != nil && job.Method == "gmail" && isGmailOAuthOrDelegationFailure(errMsg):
		job.Active = false
		job.AutoDeactivated = true
		job.Message = cronJobDelegationFinal
		task.Message = cronTaskDelegationFinal

	case job != nil && job.Method == "gmail" &&
		(strings.Contains(errLower, "access_token_scope_insufficient") ||
			strings.Contains(errLower, "insufficient authentication scopes")):
		if err := a.store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(job, cronJobGoogleInsufficientScope); err != nil {
			logger.Warn(context.Background(), "Failed to deactivate jobs after insufficient Gmail scopes",
				logger.Int("job_id", int(job.ID)), logger.ErrorField(err))
		}
		repo.StripGmailRefreshTokenFromCronJobInputData(job)
		job.Active = false
		job.AutoDeactivated = true
		job.Message = cronJobGoogleInsufficientScope
		task.Message = cronTaskGoogleInsufficientScope

	case strings.Contains(errMsg, "googleapi: Error 401") ||
		strings.Contains(errMsg, "oauth credential not found") ||
		strings.Contains(errMsg, "refresh token not found"):
		// gmailRefreshOrTokenExchangeFailure(job, errMsg):
		if task.RetryCount == repo.MaxRetryCount-1 {
			if repo.IsGoogleMediaOrGmailMethod(job.Method) {
				if err := a.store.CronJobRepo.DeactivateJobsForCredentialOrLegacyGoogleAuth(job, cronJobGoogleAuthDeactivate); err != nil {
					logger.Warn(context.Background(), "Failed to deactivate jobs after Google auth failure",
						logger.Int("job_id", int(job.ID)), logger.ErrorField(err))
				}
				repo.StripGmailRefreshTokenFromCronJobInputData(job)
			} else if job.InputData != nil && job.InputData.Json() != nil {
				if _, hasKey := (*job.InputData.Json())["refresh_token"]; hasKey {
					(*job.InputData.Json())["refresh_token"] = ""
				}
			}
			job.Active = false
			job.AutoDeactivated = true
			job.Message = cronJobGoogleAuthDeactivate
			task.Message = cronTaskGoogleAuthDeactivated
		} else {
			job.Message = cronJobGoogleAuthRetry(task.RetryCount)
			task.Message = cronTaskGoogleAuthRetry(task.RetryCount)
		}

	case strings.Contains(errMsg, "Access is denied") ||
		(job != nil && job.Method == "outlook" && strings.Contains(strings.ToLower(errMsg), "invalid_grant")) ||
		(strings.Contains(errMsg, "microsoftgraph") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))) ||
		(strings.Contains(errMsg, "Microsoft Graph API") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))):
		if task.RetryCount == repo.MaxRetryCount-1 {
			if job.InputData != nil && job.InputData.Json() != nil {
				if _, hasKey := (*job.InputData.Json())["refresh_token"]; hasKey {
					(*job.InputData.Json())["refresh_token"] = ""
				}
			}
			job.Active = false
			job.AutoDeactivated = true
			job.Message = cronJobOutlookAuthDeactivate
			task.Message = cronTaskOutlookAuthDeactivated
		} else {
			job.Message = cronJobOutlookAuthRetry(task.RetryCount)
			task.Message = cronTaskOutlookAuthRetry(task.RetryCount)
		}

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		job.Message = cronJobNetworkFinal
		task.Message = cronTaskNetworkDeactivated

	default:
		job.Message = cronJobGenericRetry(task.RetryCount, processErr)
		task.Message = cronTaskGenericRetry(task.RetryCount, processErr)
	}
}

// Autosync processors: gmail, outlook, google_drive, google_photos, google_contacts, google_calendar.
// processorMap dispatches by job.Method; CreateTaskForAllPendingJobs + ProcessTask are method-agnostic.
