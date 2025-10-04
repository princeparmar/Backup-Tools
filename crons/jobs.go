package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type ProcessorInput struct {
	InputData     map[string]interface{}
	Task          *storage.TaskListingDB
	Job           *storage.CronJobListingDB
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
	store *storage.PosgresStore
}

func NewAutosyncManager(store *storage.PosgresStore) *AutosyncManager {
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
		startTime := time.Now()
		logger.Info(ctx, "Checking for missed heartbeats")

		err := a.store.MissedHeartbeatForTask()
		if err != nil {
			logger.Error(ctx, "Failed to check for missed heartbeats", logger.ErrorField(err))
			prometheus.RecordCronJobError("missed_heartbeat_check", "system", "database_error")
		} else {
			logger.Info(ctx, "Successfully checked for missed heartbeats")
			// Note: MissedHeartbeatForTask doesn't return count, but we can record the check
			prometheus.RecordHeartbeatMiss("system", "heartbeat_check")
		}

		prometheus.RecordCronJobExecution("missed_heartbeat_check", "system", "completed")
		prometheus.RecordCronJobDuration("missed_heartbeat_check", "system", time.Since(startTime))
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
	startTime := time.Now()

	jobIDs, err := a.store.GetJobsToProcess()
	if err != nil {
		prometheus.RecordCronJobError("create_tasks", "system", "database_error")
		return fmt.Errorf("failed to get jobs to process: %w", err)
	}

	if len(jobIDs) == 0 {
		logger.Info(ctx, "No jobs to process")
		prometheus.RecordCronJobExecution("create_tasks", "system", "no_jobs")
		prometheus.RecordCronJobDuration("create_tasks", "system", time.Since(startTime))
		return nil
	}

	successCount := 0
	errorCount := 0

	for _, jobID := range jobIDs {
		logger.Info(ctx, "Creating task for job",
			logger.Int("job_id", int(jobID.ID)),
			logger.String("job_name", jobID.Name),
		)

		_, err := a.store.CreateTaskForCronJob(jobID.ID)
		if err != nil {
			// Log error but continue with other jobs
			logger.Error(ctx, "Failed to create task for job",
				logger.Int("job_id", int(jobID.ID)),
				logger.ErrorField(err),
			)
			prometheus.RecordTaskFailure(jobID.Name, jobID.Method, "creation_failed")
			errorCount++
			continue
		}

		logger.Info(ctx, "Successfully created task for job",
			logger.Int("job_id", int(jobID.ID)),
		)
		prometheus.RecordTaskCreation(jobID.Name, jobID.Method)
		successCount++
	}

	// Record overall execution metrics
	prometheus.RecordCronJobExecution("create_tasks", "system", "completed")
	prometheus.RecordCronJobDuration("create_tasks", "system", time.Since(startTime))

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
	startTime := time.Now()
	processedCount := 0
	errorCount := 0

	for {
		task, err := a.store.GetPushedTask()
		if err != nil {
			if strings.Contains(err.Error(), "record not found") {
				logger.Info(ctx, "No tasks to process")
				break
			}
			prometheus.RecordCronJobError("process_tasks", "system", "database_error")
			return fmt.Errorf("failed to get pushed task: %w", err)
		}

		logger.Info(ctx, "Processing task",
			logger.Int("task_id", int(task.ID)),
			logger.Int("job_id", int(task.CronJobID)),
		)

		job, err := a.store.GetCronJobByID(task.CronJobID)
		if err != nil {
			logger.Error(ctx, "Failed to get cron job for task",
				logger.Int("task_id", int(task.ID)),
				logger.Int("job_id", int(task.CronJobID)),
				logger.ErrorField(err),
			)
			prometheus.RecordTaskFailure("unknown", "unknown", "job_fetch_failed")
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
	prometheus.RecordCronJobExecution("process_tasks", "system", "completed")
	prometheus.RecordCronJobDuration("process_tasks", "system", time.Since(startTime))

	logger.Info(ctx, "Task processing completed",
		logger.Int("processed", processedCount),
		logger.Int("errors", errorCount),
	)

	return nil
}

func (a *AutosyncManager) processTask(ctx context.Context, task *storage.TaskListingDB, job *storage.CronJobListingDB) error {
	startTime := time.Now()

	processor, ok := processorMap[job.Method]
	if !ok {
		prometheus.RecordCronJobError(job.Name, job.Method, "processor_not_found")
		return fmt.Errorf("processor for method '%s' not found", job.Method)
	}

	logger.Info(ctx, "Executing processor for task",
		logger.Int("task_id", int(task.ID)),
		logger.String("method", job.Method),
	)

	// Record job execution start
	prometheus.RecordCronJobExecution(job.Name, job.Method, "started")

	err := processor.Run(ProcessorInput{
		InputData: job.InputData,
		Job:       job,
		Task:      task,
		HeartBeatFunc: func() error {
			// Check if task is still running
			currentTask, err := a.store.GetTaskByID(task.ID)
			if err != nil {
				return fmt.Errorf("failed to get task status: %w", err)
			}

			if currentTask.Status != storage.TaskStatusRunning {
				return fmt.Errorf("task status changed to '%s', stopping execution", currentTask.Status)
			}

			// Update heartbeat
			if err := a.store.UpdateHeartBeatForTask(task.ID); err != nil {
				return fmt.Errorf("failed to update heartbeat: %w", err)
			}

			return nil
		},
	})

	// Record execution duration
	duration := time.Since(startTime)
	prometheus.RecordCronJobDuration(job.Name, job.Method, duration)

	// Record completion status
	if err != nil {
		prometheus.RecordCronJobExecution(job.Name, job.Method, "failed")
		prometheus.RecordCronJobError(job.Name, job.Method, "execution_failed")
	} else {
		prometheus.RecordCronJobExecution(job.Name, job.Method, "completed")
	}

	return err
}

func (a *AutosyncManager) UpdateTaskStatus(task *storage.TaskListingDB, job *storage.CronJobListingDB, processErr error) error {
	ctx := context.Background() // You might want to pass context here

	// Initialize default values for success case
	task.Status = storage.TaskStatusSuccess
	task.Message = "Automatic backup completed successfully"

	if task.StartTime != nil {
		task.Execution = uint64(time.Since(*task.StartTime).Seconds())
	}

	if job != nil {
		job.Message = "Automatic backup completed successfully"
		job.MessageStatus = storage.JobMessageStatusInfo
		job.LastRun = time.Now()
	}

	// Handle error case
	if processErr != nil {
		task.Status = storage.TaskStatusFailed
		task.Message = processErr.Error()
		task.RetryCount++

		// Record retry if applicable
		if task.RetryCount > 0 {
			prometheus.RecordCronJobRetry(job.Name, job.Method)
		}

		// Record task failure
		errorType := a.categorizeError(processErr)
		prometheus.RecordTaskFailure(job.Name, job.Method, errorType)

		if job != nil {
			job.Message = "Last task execution failed"
			job.MessageStatus = storage.JobMessageStatusError
			job.LastRun = time.Now()

			emailMessage := a.determineErrorMessage(processErr, job, task)
			a.handleErrorScenarios(processErr, job, task)

			// Send email notification
			go satellite.SendEmailForBackupFailure(context.Background(), job.Name, emailMessage, job.Method)
		}
	} else {
		// Record successful task completion
		if job != nil {
			prometheus.RecordTaskCompletion(job.Name, job.Method)
		}
	}

	// Save task to database
	if err := a.store.DB.Save(task).Error; err != nil {
		logger.Error(ctx, "Failed to save task status",
			logger.Int("task_id", int(task.ID)),
			logger.ErrorField(err),
		)
		return fmt.Errorf("failed to save task: %w", err)
	}

	// Save job to database if job exists
	if job != nil {
		if err := a.store.DB.Save(job).Error; err != nil {
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

func (a *AutosyncManager) determineErrorMessage(processErr error, job *storage.CronJobListingDB, task *storage.TaskListingDB) string {
	errMsg := processErr.Error()

	switch {
	case job.StorxToken == "":
		return "Your automatic backup has been temporarily disabled due to insufficient permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	case strings.Contains(errMsg, "googleapi: Error 401"):
		if task.RetryCount == storage.MaxRetryCount-1 {
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

func (a *AutosyncManager) categorizeError(processErr error) string {
	errMsg := processErr.Error()

	switch {
	case strings.Contains(errMsg, "googleapi: Error 401"):
		return "google_auth_error"
	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		return "storx_permission_error"
	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		return "network_error"
	case strings.Contains(errMsg, "processor for method"):
		return "processor_not_found"
	default:
		return "unknown_error"
	}
}

func (a *AutosyncManager) handleErrorScenarios(processErr error, job *storage.CronJobListingDB, task *storage.TaskListingDB) {
	errMsg := processErr.Error()
	wasActive := job.Active

	switch {
	case job.StorxToken == "":
		job.Active = false
		job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
		task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"
		if wasActive {
			prometheus.RecordJobDeactivation(job.Name, job.Method, "storx_permission")
		}

	case strings.Contains(errMsg, "googleapi: Error 401"):
		if task.RetryCount == storage.MaxRetryCount-1 {
			job.InputData["refresh_token"] = ""
			job.Active = false
			job.Message = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"
			task.Message = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"
			if wasActive {
				prometheus.RecordJobDeactivation(job.Name, job.Method, "google_auth")
			}
		} else {
			job.Message = "Invalid google credentials. Retrying..."
			task.Message = "Google Credentials are invalid. Retrying..."
		}

	case strings.Contains(errMsg, "uplink: permission") || strings.Contains(errMsg, "uplink: invalid access"):
		job.StorxToken = ""
		job.Active = false
		job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
		task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"
		if wasActive {
			prometheus.RecordJobDeactivation(job.Name, job.Method, "storx_permission")
		}

	case strings.Contains(errMsg, "could not create bucket") ||
		strings.Contains(errMsg, "tcp connector failed") ||
		strings.Contains(errMsg, "connection attempt failed"):
		job.Active = false
		job.Message = "Automatic backup failed due to network issues. Please check your connection and reactivate."
		task.Message = "Task failed due to network connectivity issues. Job has been deactivated."
		if wasActive {
			prometheus.RecordJobDeactivation(job.Name, job.Method, "network_error")
		}

	default:
		job.Active = false
		job.Message = "Automatic backup failed. Please check the configuration and reactivate."
		task.Message = "Task failed. Job has been deactivated."
		if wasActive {
			prometheus.RecordJobDeactivation(job.Name, job.Method, "unknown_error")
		}
	}
}
