package crons

import (
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/robfig/cron/v3"
)

type ProcessorInput struct {
	StorxToken string
	AuthToken  string
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

	c.Start()
}

func (a *AutosyncManager) CreateTaskForAllPendingJobs() error {
	jobs, err := a.store.GetJobsToProcess()
	if err != nil {
		return err
	}

	if len(jobs) == 0 {
		fmt.Println("No job to process")
		return nil
	}
	for _, job := range jobs {
		fmt.Println("Creating task for job", job.ID)

		_, err := a.store.CreateTaskForCronJob(job.ID)
		if err != nil {
			return a.UpdateJobStatus(job.ID, "failed to push task "+err.Error(), "error")
		}
	}

	return nil
}

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
			return a.UpdateTaskStatus(task.ID, job.ID, err, time.Since(startime))
		}

		processor, ok := m[job.Method]
		if !ok {
			return a.UpdateTaskStatus(task.ID, job.ID, fmt.Errorf("method %s not found", job.Method), time.Since(startime))
		}

		err = processor.Run(ProcessorInput{
			StorxToken: job.StorxToken,
			AuthToken:  job.AuthToken,
		})
		if err != nil {
			return a.UpdateTaskStatus(task.ID, job.ID, err, time.Since(startime))
		}
	}

	return nil
}

func (a *AutosyncManager) UpdateTaskStatus(taskID, jobID uint, err error, processtime time.Duration) error {
	status := "success"
	message := ""
	jobMessage := "Task completed successfully at " + time.Now().Format("2006-01-02 15:04:05")
	jobMessageStatus := "info"
	if err != nil {
		status = "failed"
		message = err.Error()
		jobMessage = "Task failed at " + time.Now().Format("2006-01-02 15:04:05")
		jobMessageStatus = "error"
	}

	err = a.store.UpdateTaskByID(taskID, map[string]interface{}{
		"status":   status,
		"message":  message,
		"exection": processtime,
	})
	if err != nil {
		return err
	}

	return a.UpdateJobStatus(jobID, jobMessage, jobMessageStatus)
}

func (a *AutosyncManager) UpdateJobStatus(jobID uint, message, messageStatus string) error {
	return a.store.UpdateCronJobByID(jobID, map[string]interface{}{
		"message":        message,
		"message_status": messageStatus,
		"last_run":       time.Now(),
	})
}
