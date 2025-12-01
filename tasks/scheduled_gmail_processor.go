package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/pkg/worker"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/gmail/v1"
)

// GmailProcessor handles Gmail scheduled tasks
type GmailProcessor struct {
	BaseProcessor
	workerPool *worker.WorkerPool
}

func NewScheduledGmailProcessor(deps *TaskProcessorDeps) *GmailProcessor {
	return &GmailProcessor{
		BaseProcessor: BaseProcessor{Deps: deps},
		workerPool:    worker.NewWorkerPool(15),
	}
}

func (g *GmailProcessor) Run(input ScheduledTaskProcessorInput) error {
	ctx := input.Ctx
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if err = input.HeartBeatFunc(); err != nil {
		return err
	}

	accessToken, ok := input.InputData["access_token"].(string)
	if !ok {
		return g.handleError(input.Task, "Access token not found in task data", nil)
	}

	gmailClient, err := google.NewGmailClientUsingToken(accessToken)
	if err != nil {
		return g.handleError(input.Task, fmt.Sprintf("Failed to create Gmail client: %s", err), nil)
	}

	// Create placeholder and get existing emails
	if err := g.setupStorage(ctx, input.Task, satellite.ReserveBucket_Gmail); err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(ctx, input.Task.StorxToken, satellite.ReserveBucket_Gmail, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return g.handleError(input.Task, fmt.Sprintf("Failed to list existing emails: %s", err), nil)
	}

	return g.processEmails(input, gmailClient, emailListFromBucket)
}

func (g *GmailProcessor) setupStorage(ctx context.Context, task *repo.ScheduledTasks, bucket string) error {
	return satellite.UploadObject(ctx, task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil)
}

func (g *GmailProcessor) processEmails(input ScheduledTaskProcessorInput, client *google.GmailClient, existingEmails map[string]bool) error {
	// Get pending emails
	pendingEmails := input.Memory["pending"]
	if pendingEmails == nil {
		pendingEmails = []string{}
	}

	// Deduplicate pending emails to prevent processing the same email multiple times
	seen := make(map[string]bool)
	var uniquePendingEmails []string
	for _, emailID := range pendingEmails {
		emailID = strings.TrimSpace(emailID)
		if emailID != "" && !seen[emailID] {
			seen[emailID] = true
			uniquePendingEmails = append(uniquePendingEmails, emailID)
		}
	}
	pendingEmails = uniquePendingEmails
	// Update memory with deduplicated list
	input.Memory["pending"] = uniquePendingEmails

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	if len(uniquePendingEmails) == 0 {
		return g.updateTaskStats(&input, 0, 0, []string{})
	}

	// Process emails in parallel with worker pool
	var mu sync.Mutex
	successCount := 0
	failedCount := 0
	var failedEmails []string

	// Track submitted tasks to wait for completion
	var batchWg sync.WaitGroup

	// Submit each email processing task to worker pool
	for _, emailID := range uniquePendingEmails {
		emailID := emailID // Capture loop variable
		taskWg, submitErr := g.workerPool.SubmitAndWait(func() error {
			// Heartbeat check
			if err := input.HeartBeatFunc(); err != nil {
				mu.Lock()
				failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: heartbeat failed: %v", emailID, err))
				failedCount++
				mu.Unlock()
				return err
			}

			// Get the full gmail.Message
			message, err := client.Service.Users.Messages.Get("me", emailID).Format("full").Do()
			if err != nil {
				mu.Lock()
				failedEmails, failedCount = g.trackFailure(emailID, err, failedEmails, failedCount, input)
				mu.Unlock()
				return err
			}

			// Use the same filename format as direct uploads for consistency
			messagePath := input.Task.LoginId + "/" + utils.GenerateTitleFromGmailMessage(message)

			mu.Lock()
			if _, exists := existingEmails[messagePath]; exists {
				moveEmailToStatus(&input.Memory, emailID, "pending", "skipped: already exists in storage")
				successCount++
				mu.Unlock()
				return nil
			}
			mu.Unlock()

			if err := g.uploadEmail(input.Ctx, input, message, messagePath, "gmail"); err != nil {
				mu.Lock()
				failedEmails, failedCount = g.trackFailure(emailID, err, failedEmails, failedCount, input)
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

	return g.updateTaskStats(&input, successCount, failedCount, failedEmails)
}

// Helper function to ensure a status array exists in the map
func ensureStatusArray(memory *map[string][]string, status string) {
	if (*memory)[status] == nil {
		(*memory)[status] = []string{}
	}
}

// Helper function to move an email ID from one status to another
func moveEmailToStatus(memory *map[string][]string, emailID, fromStatus, toStatus string) {
	// Remove from source status (remove all occurrences to handle duplicates)
	if arr, exists := (*memory)[fromStatus]; exists {
		var newArr []string
		for _, id := range arr {
			if id != emailID {
				newArr = append(newArr, id)
			}
		}
		(*memory)[fromStatus] = newArr
	}

	// Add to target status only if not already present (prevent duplicates)
	ensureStatusArray(memory, toStatus)
	targetArr := (*memory)[toStatus]

	// Check if emailID already exists in target array
	found := false
	for _, id := range targetArr {
		if id == emailID {
			found = true
			break
		}
	}

	// Only add if not already present
	if !found {
		(*memory)[toStatus] = append(targetArr, emailID)
	}
}

func (g *GmailProcessor) trackFailure(emailID string, err error, failedEmails []string, failedCount int, input ScheduledTaskProcessorInput) ([]string, int) {
	failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: %v", emailID, err))
	failedCount++
	moveEmailToStatus(&input.Memory, emailID, "pending", fmt.Sprintf("error: %v", err))
	return failedEmails, failedCount
}

func (g *GmailProcessor) uploadEmail(ctx context.Context, input ScheduledTaskProcessorInput, message *gmail.Message, messagePath, bucket string) error {
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
