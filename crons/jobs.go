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

// func (a *AutosyncManager) RefreshGoogleAuthToken() error {
// 	jobs, err := a.store.GetAllCronJobs()
// 	if err != nil {
// 		return err
// 	}

// 	errGroup := errs.Group{}

// 	for _, job := range jobs {
// 		if job.RefreshToken == "" || !job.Active {
// 			continue
// 		}

// 		if !google.IsGoogleTokenExpired(job.AuthToken) {
// 			continue
// 		}

// 		newToken, err := google.AuthTokenUsingRefreshToken(job.RefreshToken)
// 		if err != nil {
// 			errGroup.Add(err)
// 			continue
// 		}

// 		err = a.store.UpdateCronJobByID(job.ID, map[string]interface{}{
// 			"auth_token": newToken,
// 		})
// 		if err != nil {
// 			errGroup.Add(err)
// 			fmt.Println("Failed to update job", job.ID, err)
// 			continue
// 		}

// 		fmt.Println("Updated Google Auth Token for job", job.ID)
// 	}

// 	return errGroup.Err()
// }

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
		job.LastRun = time.Now()
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
			job.LastRun = time.Now()

			emailMessage := a.determineErrorMessage(processErr, job, task)
			a.handleErrorScenarios(processErr, job, task)

			// Send email notification
			go satellite.SendEmailForBackupFailure(context.Background(), job.Name, emailMessage, job.Method)
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
			"message":        job.Message,
			"message_status": job.MessageStatus,
			"last_run":       job.LastRun,
		}

		// Update cron job status based on task status
		switch task.Status {
		case repo.TaskStatusSuccess:
			updateMap["status"] = repo.JobStatusSuccess
		case repo.TaskStatusFailed:
			updateMap["status"] = repo.JobStatusFailed
		}

		if err := a.store.CronJobRepo.UpdateCronJobByID(job.ID, updateMap); err != nil {
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

func (a *AutosyncManager) determineErrorMessage(processErr error, job *repo.CronJobListingDB, task *repo.TaskListingDB) string {
	errMsg := processErr.Error()

	switch {
	case job.StorxToken == "":
		return "Your automatic backup has been temporarily disabled due to insufficient permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	case strings.Contains(errMsg, "googleapi: Error 401"):
		if task.RetryCount == repo.MaxRetryCount-1 {
			return "Your automatic backup has been temporarily disabled due to invalid Google credentials. Please update your Google account permissions and reactivate the backup from your dashboard."
		}
		return "Your automatic backup encountered an authentication issue with Google. We're retrying the backup automatically."

	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		return "Your automatic backup has been temporarily disabled due to insufficient StorX permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		return "Your automatic backup has been temporarily disabled due to network connectivity issues. Please check your internet connection and reactivate the backup from your dashboard."

	default:
		return "Your automatic backup has been temporarily disabled due to a technical issue. Please check your backup configuration and reactivate from your dashboard."
	}
}

func (a *AutosyncManager) handleErrorScenarios(processErr error, job *repo.CronJobListingDB, task *repo.TaskListingDB) {
	errMsg := processErr.Error()

	switch {
	case job.StorxToken == "":
		job.Active = false
		job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
		task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"

	case strings.Contains(errMsg, "googleapi: Error 401"):
		if task.RetryCount == repo.MaxRetryCount-1 {
			(*job.InputData.Json())["refresh_token"] = ""
			job.Active = false
			job.Message = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"
			task.Message = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"
		} else {
			job.Message = "Invalid google credentials. Retrying..."
			task.Message = "Google Credentials are invalid. Retrying..."
		}

	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		job.StorxToken = ""
		job.Active = false
		job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
		task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		job.Active = false
		job.Message = "Automatic backup failed due to network issues. Please check your connection and reactivate."
		task.Message = "Task failed due to network connectivity issues. Job has been deactivated."

	default:
		job.Active = false
		job.Message = "Automatic backup failed. Please check the configuration and reactivate."
		task.Message = "Task failed. Job has been deactivated."

	}
}
