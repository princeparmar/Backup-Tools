package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/gmail/v1"
)

// GmailProcessor handles Gmail scheduled tasks
type GmailProcessor struct {
	BaseProcessor
}

func NewScheduledGmailProcessor(deps *TaskProcessorDeps) *GmailProcessor {
	return &GmailProcessor{BaseProcessor{Deps: deps}}
}

func (g *GmailProcessor) Run(input ScheduledTaskProcessorInput) error {
	ctx := context.Background()
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
	if err := g.setupStorage(input.Task, satellite.ReserveBucket_Gmail); err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(ctx, input.Task.StorxToken, satellite.ReserveBucket_Gmail, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return g.handleError(input.Task, fmt.Sprintf("Failed to list existing emails: %s", err), nil)
	}

	err = g.processEmails(input, gmailClient, emailListFromBucket)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Deps.Store, input.Task.StorxToken, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync",
				logger.ErrorField(processErr))
		}
	}()

	return nil
}

func (g *GmailProcessor) setupStorage(task *repo.ScheduledTasks, bucket string) error {
	return handler.UploadObjectAndSync(context.Background(), g.Deps.Store, task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil, task.UserID)
}

func (g *GmailProcessor) processEmails(input ScheduledTaskProcessorInput, client *google.GmailClient, existingEmails map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedEmails []string

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

	for _, emailID := range pendingEmails {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		// Get the full gmail.Message (same as direct upload) to ensure consistent filename generation
		message, err := client.Service.Users.Messages.Get("me", emailID).Format("full").Do()
		if err != nil {
			failedEmails, failedCount = g.trackFailure(emailID, err, failedEmails, failedCount, input)
			continue
		}

		// Use the same filename format as direct uploads for consistency
		messagePath := input.Task.LoginId + "/" + utils.GenerateTitleFromGmailMessage(message)
		if _, exists := existingEmails[messagePath]; exists {
			moveEmailToStatus(&input.Memory, emailID, "pending", "skipped: already exists in storage")
			successCount++
			continue
		}

		if err := g.uploadEmail(input, message, messagePath, "gmail"); err != nil {
			failedEmails, failedCount = g.trackFailure(emailID, err, failedEmails, failedCount, input)
		} else {
			moveEmailToStatus(&input.Memory, emailID, "pending", "synced")
			successCount++
		}
	}

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

func (g *GmailProcessor) uploadEmail(input ScheduledTaskProcessorInput, message *gmail.Message, messagePath, bucket string) error {
	b, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal: %v", err)
	}
	return handler.UploadObjectAndSync(context.TODO(), input.Deps.Store, input.Task.StorxToken, bucket, messagePath, b, input.Task.UserID)
}
