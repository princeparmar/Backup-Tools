package crons

import (
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/robfig/cron/v3"
)

type ProcessorInput struct {
	StorxToken    string
	AuthToken     string
	Email         string
	Task          *storage.TaskListingDB
	HeartBeatFunc func() error
}

type Processor interface {
	Run(ProcessorInput) error
}

var m = map[string]Processor{
	"gmail": NewGmailProcessor(),
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
		startime := time.Now()
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
			return a.UpdateTaskStatus(task, job, err, time.Since(startime))
		}

		processor, ok := m[job.Method]
		if !ok {
			return a.UpdateTaskStatus(task, job, fmt.Errorf("method %s not found", job.Method), time.Since(startime))
		}

		newToken, err := google.AuthTokenUsingRefreshToken(job.RefreshToken)
		if err != nil {
			return a.UpdateTaskStatus(task, job, fmt.Errorf("Error while generating auth token: %s", err), time.Since(startime))
		}

		err = processor.Run(ProcessorInput{
			StorxToken: job.StorxToken,
			AuthToken:  newToken,
			Email:      job.Name,
			Task:       task,
			HeartBeatFunc: func() error {
				return a.store.UpdateHeartBeatForTask(task.ID)
			},
		})
		if err != nil {
			return a.UpdateTaskStatus(task, job, err, time.Since(startime))
		}
	}

	return nil
}

func (a *AutosyncManager) UpdateTaskStatus(task *storage.TaskListingDB, job *storage.CronJobListingDB, err error, processtime time.Duration) error {
	task.Status = "success"
	task.Message = ""
	task.Execution = uint64(processtime.Seconds())
	job.Message = "Task completed successfully at " + time.Now().Format("2006-01-02 15:04:05")
	job.MessageStatus = "info"
	if err != nil {
		task.Status = "failed"
		task.Message = err.Error()

		if strings.Contains(err.Error(), "googleapi: Error 401") {
			job.Message = "Invalid google credentials"
		}

		job.Message = "Task failed at " + time.Now().Format("2006-01-02 15:04:05")
		if strings.Contains(err.Error(), "uplink: permission") {
			job.Message = "Sync is failing because of insufficient permissions to upload to storx"
			job.StorxToken = ""
			// deactivate the job
			job.Active = false
		}
		job.MessageStatus = "error"
	}

	err = a.store.DB.Save(task).Error
	if err != nil {
		return err
	}

	err = a.store.DB.Save(job).Error
	if err != nil {
		return err
	}

	return nil
}
