package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/pkg/worker"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// OutlookProcessor handles Outlook scheduled tasks
type OutlookProcessor struct {
	BaseProcessor
	workerPool *worker.WorkerPool
}

func NewScheduledOutlookProcessor(deps *TaskProcessorDeps) *OutlookProcessor {
	return &OutlookProcessor{
		BaseProcessor: BaseProcessor{Deps: deps},
		workerPool:    worker.NewWorkerPool(15),
	}
}

func (o *OutlookProcessor) Run(input ScheduledTaskProcessorInput) error {
	ctx := input.Ctx
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if err = input.HeartBeatFunc(); err != nil {
		return err
	}

	accessToken, ok := input.InputData["access_token"].(string)
	if !ok {
		return o.handleError(input.Task, "Access token not found for Outlook method", nil)
	}

	outlookClient, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return o.handleError(input.Task, fmt.Sprintf("Failed to create Outlook client: %s", err), nil)
	}

	// Create placeholder and get existing emails
	if err := o.setupStorage(ctx, input.Task, satellite.ReserveBucket_Outlook); err != nil {
		return o.handleError(input.Task, fmt.Sprintf("Failed to create placeholder: %s", err), nil)
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(ctx, input.Task.StorxToken, satellite.ReserveBucket_Outlook, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return o.handleError(input.Task, fmt.Sprintf("Failed to list existing emails: %s", err), nil)
	}

	return o.processEmails(input, outlookClient, emailListFromBucket)
}

func (o *OutlookProcessor) setupStorage(ctx context.Context, task *repo.ScheduledTasks, bucket string) error {
	return satellite.UploadObject(ctx, task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil)
}

func (o *OutlookProcessor) processEmails(input ScheduledTaskProcessorInput, client *outlook.OutlookClient, existingEmails map[string]bool) error {
	// Get pending emails
	pendingEmails := input.Memory["pending"]
	if pendingEmails == nil {
		pendingEmails = []string{}
	}

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	if len(pendingEmails) == 0 {
		return o.updateTaskStats(&input, 0, 0, []string{})
	}

	// Process emails in parallel with worker pool
	var mu sync.Mutex
	successCount := 0
	failedCount := 0
	var failedEmails []string

	// Track submitted tasks to wait for completion
	var batchWg sync.WaitGroup

	// Submit each email processing task to worker pool
	for _, emailID := range pendingEmails {
		emailID := emailID // Capture loop variable
		taskWg, submitErr := o.workerPool.SubmitAndWait(func() error {
			// Heartbeat check
			if err := input.HeartBeatFunc(); err != nil {
				mu.Lock()
				failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: heartbeat failed: %v", emailID, err))
				failedCount++
				mu.Unlock()
				return err
			}

			message, err := client.GetMessage(emailID)
			if err != nil {
				mu.Lock()
				failedEmails, failedCount = o.trackFailure(emailID, err, failedEmails, failedCount, input)
				mu.Unlock()
				return err
			}

			messagePath := input.Task.LoginId + "/" + utils.GenerateTitleFromOutlookMessage(&utils.OutlookMinimalMessage{
				ID:               message.ID,
				Subject:          message.Subject,
				From:             message.From,
				ReceivedDateTime: message.ReceivedDateTime,
			})

			mu.Lock()
			if _, exists := existingEmails[messagePath]; exists {
				moveEmailToStatus(&input.Memory, emailID, "pending", "skipped: already exists in storage")
				successCount++
				mu.Unlock()
				return nil
			}
			mu.Unlock()

			if err := o.uploadEmail(input.Ctx, input, message, messagePath, "outlook"); err != nil {
				mu.Lock()
				failedEmails, failedCount = o.trackFailure(emailID, err, failedEmails, failedCount, input)
				mu.Unlock()
				return err
			}

			mu.Lock()
			moveEmailToStatus(&input.Memory, emailID, "pending", "synced")
			successCount++
			mu.Unlock()
			return nil
		})

		if submitErr != nil {
			mu.Lock()
			failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: failed to submit to worker pool: %v", emailID, submitErr))
			failedCount++
			mu.Unlock()
			continue
		}

		// Track this task's completion
		batchWg.Add(1)
		go func(wg *sync.WaitGroup) {
			defer batchWg.Done()
			taskWg.Wait()
		}(taskWg)
	}

	// Wait for all email processing tasks to complete
	batchWg.Wait()

	// Clear pending array after processing
	input.Memory["pending"] = []string{}

	return o.updateTaskStats(&input, successCount, failedCount, failedEmails)
}

func (o *OutlookProcessor) trackFailure(emailID string, err error, failedEmails []string, failedCount int, input ScheduledTaskProcessorInput) ([]string, int) {
	failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: %v", emailID, err))
	failedCount++
	moveEmailToStatus(&input.Memory, emailID, "pending", fmt.Sprintf("error: %v", err))
	return failedEmails, failedCount
}

func (o *OutlookProcessor) uploadEmail(ctx context.Context, input ScheduledTaskProcessorInput, message interface{}, messagePath, bucket string) error {
	// Marshal message
	b, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal: %v", err)
	}

	// Upload to satellite
	err = satellite.UploadObject(ctx, input.Task.StorxToken, bucket, messagePath, b)

	// Clear message data from memory after upload to free memory
	message = nil
	b = nil

	return err
}
