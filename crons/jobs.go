package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
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

var m = map[string]Processor{
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

func (a *AutosyncManager) Start() {
	c := cron.New()
	c.AddFunc("@every 1m", func() {
		logger.Info("Creating task for all pending jobs")
		err := a.CreateTaskForAllPendingJobs()
		if err != nil {
			logger.Error("Failed to create task for all pending jobs", logger.ErrorField(err))
			return
		}

		logger.Info("Task created for all pending jobs")
	})

	c.AddFunc("@every 1m", func() {
		logger.Info("Processing task")
		err := a.ProcessTask()
		if err != nil {
			logger.Error("Failed to process task", logger.ErrorField(err))
			return
		}

		logger.Info("Task processed")
	})

	c.AddFunc("@every 1m", func() {
		logger.Info("Checking for missed heartbeat")
		err := a.store.MissedHeartbeatForTask()
		if err != nil {
			logger.Error("Failed to check for missed heartbeat", logger.ErrorField(err))
			return
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
}

func (a *AutosyncManager) CreateTaskForAllPendingJobs() error {
	jobIDs, err := a.store.GetJobsToProcess()
	if err != nil {
		return err
	}

	if len(jobIDs) == 0 {
		logger.Info("No job to process")
		return nil
	}
	for _, jobID := range jobIDs {
		logger.Info("Creating task for job", logger.Int("job_id", int(jobID.ID)))

		_, err := a.store.CreateTaskForCronJob(jobID.ID)
		if err != nil {
			return a.store.DB.Save(&jobID).Error
		}
	}

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

func (a *AutosyncManager) ProcessTask() error {
	for {
		task, err := a.store.GetPushedTask()
		if err != nil {
			if err.Error() == "error getting pushed task: record not found" {
				logger.Info("No task to process")
				break
			}
			return err
		}

		logger.Info("Processing task", logger.Int("task_id", int(task.ID)))
		job, err := a.store.GetCronJobByID(task.CronJobID)
		if err != nil {
			return a.UpdateTaskStatus(task, job, err)
		}

		err = a.processTask(task, job)
		if err := a.UpdateTaskStatus(task, job, err); err != nil {
			return err
		}
	}

	return nil
}

func (a *AutosyncManager) processTask(task *storage.TaskListingDB, job *storage.CronJobListingDB) error {
	processor, ok := m[job.Method]
	if !ok {
		return fmt.Errorf("method %s not found", job.Method)
	}

	return processor.Run(ProcessorInput{
		InputData: job.InputData,
		Job:       job,
		Task:      task,
		HeartBeatFunc: func() error {
			t, err := a.store.GetTaskByID(task.ID)
			if err != nil {
				return err
			}

			if t.Status != storage.TaskStatusRunning {
				return fmt.Errorf("exit task because status changed: %s", t.Status)
			}

			return a.store.UpdateHeartBeatForTask(task.ID)
		},
	})
}

func (a *AutosyncManager) UpdateTaskStatus(task *storage.TaskListingDB, job *storage.CronJobListingDB, err error) error {
	// Set initial status for success case
	task.Status = storage.TaskStatusSuccess
	task.Execution = uint64(time.Since(*task.StartTime).Seconds())
	job.Message = "Automatic backup completed successfully"
	task.Message = "Automatic backup completed successfully"
	job.MessageStatus = storage.JobMessageStatusInfo
	job.LastRun = time.Now()

	// If there's an error, update status and prepare to send email
	if err != nil {
		task.Status = storage.TaskStatusFailed
		task.Message = err.Error()
		job.Message = "Last Task Execution failed because of some error"
		job.MessageStatus = storage.JobMessageStatusError

		var emailMessage string = err.Error() // default

		// Check if job has no StorX token and deactivate it
		if job.StorxToken == "" {
			job.Active = false
			job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
			task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"
			emailMessage = "Your automatic backup has been temporarily disabled due to insufficient permissions. Please update your StorX permissions and reactivate the backup from your dashboard."
		} else if strings.Contains(err.Error(), "googleapi: Error 401") {
			if task.RetryCount == storage.MaxRetryCount-1 {
				job.InputData["refresh_token"] = ""
				job.Active = false
				job.Message = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"
				task.Message = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"
				emailMessage = "Your automatic backup has been temporarily disabled due to invalid Google credentials. Please update your Google account permissions and reactivate the backup from your dashboard."
			} else {
				job.Message = "Invalid google credentials. Retrying..."
				task.Message = "Google Credentials are invalid. Retrying..."
				emailMessage = "Your automatic backup encountered an authentication issue with Google. We're retrying the backup automatically."
			}
		} else if strings.Contains(err.Error(), "uplink: permission") || strings.Contains(err.Error(), "uplink: invalid access") {
			job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
			job.StorxToken = ""
			job.Active = false
			task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"
			emailMessage = "Your automatic backup has been temporarily disabled due to insufficient StorX permissions. Please update your StorX permissions and reactivate the backup from your dashboard."
		} else if strings.Contains(err.Error(), "could not create bucket") || strings.Contains(err.Error(), "tcp connector failed") || strings.Contains(err.Error(), "connection attempt failed") {
			// Network/connection errors
			job.Active = false
			job.Message = "Automatic backup failed due to network issues. Please check your connection and reactivate."
			task.Message = "Task failed due to network connectivity issues. Job has been deactivated."
			emailMessage = "Your automatic backup has been temporarily disabled due to network connectivity issues. Please check your internet connection and reactivate the backup from your dashboard."
		} else {
			// For any other error, deactivate the job immediately
			job.Active = false
			job.Message = "Automatic backup failed. Please check the configuration and reactivate."
			task.Message = "Task failed. Job has been deactivated."
			emailMessage = "Your automatic backup has been temporarily disabled due to a technical issue. Please check your backup configuration and reactivate from your dashboard."
		}

		// Send the appropriate error message once
		go satellite.SendEmailForBackupFailure(context.Background(), job.Name, emailMessage, job.Method)

		task.RetryCount++
	}

	// Save task and job to DB
	if err := a.store.DB.Save(task).Error; err != nil {
		go satellite.SendEmailForBackupFailure(context.Background(), job.Name, fmt.Sprintf("Failed to save task status to database: %v", err), job.Method)
		return err
	}

	if err := a.store.DB.Save(job).Error; err != nil {
		go satellite.SendEmailForBackupFailure(context.Background(), job.Name, fmt.Sprintf("Failed to save job status to database: %v", err), job.Method)
		return err
	}

	return nil
}
