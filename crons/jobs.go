package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		fmt.Println("Creating task for all pending jobs")
		err := a.CreateTaskForAllPendingJobs()
		if err != nil {
			fmt.Println("Failed to create task for all pending jobs", err)
			return
		}

		fmt.Println("Task created for all pending jobs")
	})

	c.AddFunc("@every 1m", func() {
		fmt.Println("Processing task")
		err := a.ProcessTask()
		if err != nil {
			fmt.Println("Failed to process task", err)
			return
		}

		fmt.Println("Task processed")
	})

	c.AddFunc("@every 1m", func() {
		fmt.Println("Checking for missed heartbeat")
		err := a.store.MissedHeartbeatForTask()
		if err != nil {
			fmt.Println("Failed to check for missed heartbeat", err)
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
		fmt.Println("No job to process")
		return nil
	}
	for _, jobID := range jobIDs {
		fmt.Println("Creating task for job", jobID)

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
				fmt.Println("No task to process")
				break
			}
			return err
		}

		fmt.Println("Processing task", task.ID)
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
				return fmt.Errorf("exit task because status changed")
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

		if strings.Contains(err.Error(), "googleapi: Error 401") {
			if task.RetryCount == storage.MaxRetryCount-1 {
				job.InputData["refresh_token"] = ""
				job.Active = false
				job.Message = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"
				task.Message = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"
				emailMessage = "Google Credentials are invalid. Please update the credentials and reactivate the automatic backup"
			} else {
				job.Message = "Invalid google credentials. Retrying..."
				task.Message = "Google Credentials are invalid. Retrying..."
				emailMessage = "Google Credentials are invalid. Retrying..."
			}
		} else if strings.Contains(err.Error(), "uplink: permission") || strings.Contains(err.Error(), "uplink: invalid access") {
			job.Message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
			job.StorxToken = ""
			job.Active = false
			task.Message = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"
			emailMessage = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
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
