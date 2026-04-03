package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
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
}

type Processor interface {
	Run(ProcessorInput) error
}

var processorMap = map[string]Processor{
	"gmail":         NewGmailProcessor(),
	"outlook":       NewOutlookProcessor(),
	"psql_database": NewPsqlDatabaseProcessor(),
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
	c := cron.New()

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

	// Record job execution start

	err = processor.Run(ProcessorInput{
		InputData: job.InputData,
		Job:       job,
		Task:      task,
		Database:  a.store,
		HeartBeatFunc: func() error {
			// Check if task is still running
			currentTask, err := a.store.TaskRepo.GetTaskByID(task.ID)
			if err != nil {
				return fmt.Errorf("failed to get task status: %w", err)
			}

			if currentTask.Status != repo.TaskStatusRunning {
				return fmt.Errorf("task status changed to '%s', stopping execution", currentTask.Status)
			}

			// Update heartbeat
			if err := a.store.TaskRepo.UpdateHeartBeatForTask(task.ID); err != nil {
				return fmt.Errorf("failed to update heartbeat: %w", err)
			}

			return nil
		},
	})

	// Record completion status
	if err != nil {
		// Error handling
	} else {
		// Success handling
	}

	return err
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
		task.Message = processErr.Error()
		task.RetryCount++

		// Record task failure
		if job != nil {
			job.Message = "Last task execution failed"
			job.MessageStatus = repo.JobMessageStatusError
			now := time.Now()
			job.LastRun = &now

			emailMessage := a.determineErrorMessage(processErr, job, task)
			a.handleErrorScenarios(processErr, job, task)

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
		}
	} else {
		// Handle success case - send notification
		if job != nil {
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

		// Update cron job status based on task status
		switch task.Status {
		case repo.TaskStatusSuccess:
			updateMap["status"] = repo.JobStatusSuccess
		case repo.TaskStatusFailed:
			updateMap["status"] = repo.JobStatusFailed
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

func (a *AutosyncManager) gmailStorxMissing(job *repo.CronJobListingDB) bool {
	if job == nil {
		return true
	}
	if job.Method != "gmail" {
		return strings.TrimSpace(job.StorxToken) == ""
	}
	return strings.TrimSpace(a.store.CronJobRepo.GmailResolvedStorxToken(job)) == ""
}

func (a *AutosyncManager) clearStorxOnUplinkFailure(job *repo.CronJobListingDB) {
	if job == nil {
		return
	}
	job.StorxToken = ""
}

func (a *AutosyncManager) determineErrorMessage(processErr error, job *repo.CronJobListingDB, task *repo.TaskListingDB) string {
	errMsg := processErr.Error()

	switch {
	case a.gmailStorxMissing(job):
		return "Your automatic backup has been temporarily disabled due to insufficient permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	case strings.Contains(errMsg, "Delegation denied") ||
		(strings.Contains(errMsg, "googleapi: Error 403") && strings.Contains(errMsg, "Delegation")):
		// Workspace: Admin must authorize OAuth client for domain-wide delegation + Gmail scopes for impersonated users.
		if task.RetryCount >= repo.MaxRetryCount {
			return "Google Workspace blocked access to a mailbox (domain-wide delegation). Ask your admin to add this app's OAuth client in Admin Console → Security → API controls → Domain-wide delegation, with the required Gmail scopes, or remove mailboxes that cannot be delegated."
		}
		return fmt.Sprintf("Google Workspace delegation issue while accessing a mailbox. Retrying (attempt %d of %d). If this continues, your admin must enable domain-wide delegation for this app.", task.RetryCount, repo.MaxRetryCount)

	case strings.Contains(errMsg, "googleapi: Error 401") ||
		strings.Contains(errMsg, "oauth credential not found") ||
		strings.Contains(errMsg, "refresh token not found"):
		if task.RetryCount == repo.MaxRetryCount-1 {
			return "Your automatic backup has been temporarily disabled due to invalid Google credentials. Please update your Google account permissions and reactivate the backup from your dashboard."
		}
		return fmt.Sprintf("Your automatic backup encountered an authentication issue with Google. We're retrying automatically (attempt %d of %d).", task.RetryCount, repo.MaxRetryCount)

	case strings.Contains(errMsg, "Access is denied") ||
		strings.Contains(errMsg, "invalid_grant") ||
		(strings.Contains(errMsg, "microsoftgraph") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))) ||
		(strings.Contains(errMsg, "Microsoft Graph API") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))):
		if task.RetryCount == repo.MaxRetryCount-1 {
			return "Your automatic backup has been temporarily disabled due to invalid Microsoft Outlook credentials. Please update your Outlook account permissions and reactivate the backup from your dashboard."
		}
		return fmt.Sprintf("Your automatic backup encountered an authentication issue with Microsoft Outlook. We're retrying automatically (attempt %d of %d).", task.RetryCount, repo.MaxRetryCount)

	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		return "Your automatic backup has been temporarily disabled due to insufficient StorX permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		return "Your automatic backup has been temporarily disabled due to network connectivity issues. Please check your internet connection and reactivate the backup from your dashboard."

	default:
		return fmt.Sprintf("Your automatic backup encountered a technical issue. We're retrying automatically (attempt %d of %d).", task.RetryCount, repo.MaxRetryCount)
	}
}

// clearGmailRefreshTokensOnAuthFailure clears refresh_token on this job; if a parent admin row exists, clears it there too (shared OAuth).
func (a *AutosyncManager) clearGmailRefreshTokensOnAuthFailure(job *repo.CronJobListingDB) {
	if job == nil || job.Method != "gmail" || job.InputData == nil || job.InputData.Json() == nil {
		return
	}
	inputData := job.InputData.Json()
	if _, hasKey := (*inputData)["refresh_token"]; hasKey {
		(*inputData)["refresh_token"] = ""
	}
	parent, err := a.store.CronJobRepo.GmailParentRowForCorporateChild(job)
	if err == nil && parent != nil && parent.InputData != nil && parent.InputData.Json() != nil {
		(*parent.InputData.Json())["refresh_token"] = ""
		_ = a.store.CronJobRepo.UpdateCronJobByID(parent.ID, map[string]interface{}{"input_data": parent.InputData})
	}
}

func (a *AutosyncManager) handleErrorScenarios(processErr error, job *repo.CronJobListingDB, task *repo.TaskListingDB) {
	errMsg := processErr.Error()

	switch {
	case a.gmailStorxMissing(job):
		job.Active = false
		job.AutoDeactivated = true
		job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
		task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"

	case strings.Contains(errMsg, "Delegation denied") ||
		(strings.Contains(errMsg, "googleapi: Error 403") && strings.Contains(errMsg, "Delegation")):
		// Not an invalid refresh token — admin must allow domain-wide delegation for this OAuth client + scopes.
		if task.RetryCount >= repo.MaxRetryCount {
			job.Active = false
			job.AutoDeactivated = true
			job.Message = "Google Workspace denied access to this mailbox (delegation). Ask your admin to enable domain-wide delegation for this app or adjust which accounts are backed up."
			task.Message = "Delegation denied by Google Workspace. Your admin must authorize this app for domain-wide delegation (Gmail scopes) for the affected users."
		} else {
			job.Message = fmt.Sprintf("Google Workspace delegation denied. Attempt %d of %d failed. Retrying...", task.RetryCount, repo.MaxRetryCount)
			task.Message = fmt.Sprintf("Delegation denied for a mailbox (see logs). Attempt %d of %d. Retrying...", task.RetryCount, repo.MaxRetryCount)
		}

	case strings.Contains(errMsg, "googleapi: Error 401") ||
		strings.Contains(errMsg, "oauth credential not found") ||
		strings.Contains(errMsg, "refresh token not found"):
		if task.RetryCount == repo.MaxRetryCount-1 {
			a.clearGmailRefreshTokensOnAuthFailure(job)
			job.Active = false
			job.AutoDeactivated = true
			job.Message = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"
			task.Message = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"
		} else {
			job.Message = fmt.Sprintf("Invalid Google credentials. Attempt %d of %d failed. Retrying automatically...", task.RetryCount, repo.MaxRetryCount)
			task.Message = fmt.Sprintf("Google credentials invalid. Attempt %d of %d failed. Retrying automatically...", task.RetryCount, repo.MaxRetryCount)
		}

	case strings.Contains(errMsg, "Access is denied") ||
		strings.Contains(errMsg, "invalid_grant") ||
		(strings.Contains(errMsg, "microsoftgraph") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))) ||
		(strings.Contains(errMsg, "Microsoft Graph API") && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"))):
		if task.RetryCount == repo.MaxRetryCount-1 {
			(*job.InputData.Json())["refresh_token"] = ""
			job.Active = false
			job.AutoDeactivated = true
			job.Message = "Invalid Microsoft Outlook credentials. Please update the credentials and reactivate the automatic backup"
			task.Message = "Microsoft Outlook Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"
		} else {
			job.Message = fmt.Sprintf("Invalid Microsoft Outlook credentials. Attempt %d of %d failed. Retrying automatically...", task.RetryCount, repo.MaxRetryCount)
			task.Message = fmt.Sprintf("Microsoft Outlook credentials invalid. Attempt %d of %d failed. Retrying automatically...", task.RetryCount, repo.MaxRetryCount)
		}

	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		a.clearStorxOnUplinkFailure(job)
		job.Active = false
		job.AutoDeactivated = true
		job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
		task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		job.Message = "Automatic backup failed due to network issues. Please check your connection and reactivate."
		task.Message = "Task failed due to network connectivity issues. Job has been deactivated."

	default:
		job.Message = "Automatic backup encountered an error. Job will be retried automatically..."
		task.Message = "Task encountered an error. Task will be retried automatically..."
	}
}
