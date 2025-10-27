package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
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

	return g.processEmails(input, gmailClient, emailListFromBucket)
}

func (g *GmailProcessor) setupStorage(task *repo.ScheduledTasks, bucket string) error {
	return satellite.UploadObject(context.Background(), task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil)
}

func (g *GmailProcessor) processEmails(input ScheduledTaskProcessorInput, client *google.GmailClient, existingEmails map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedEmails []string

	for emailID, status := range input.Memory {
		if status == "synced" || status == "skipped" || strings.HasPrefix(status, "error:") {
			continue
		}

		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		message, err := client.GetMessage(emailID)
		if err != nil {
			failedEmails, failedCount = g.trackFailure(emailID, err, failedEmails, failedCount, input)
			continue
		}

		messagePath := input.Task.LoginId + "/" + g.generateTitleFromGmailMessage(message)
		if _, exists := existingEmails[messagePath]; exists {
			input.Memory[emailID] = "skipped: already exists in storage"
			successCount++
			continue
		}

		if err := g.uploadEmail(input, message, messagePath, "gmail"); err != nil {
			failedEmails, failedCount = g.trackFailure(emailID, err, failedEmails, failedCount, input)
		} else {
			input.Memory[emailID] = "synced"
			successCount++
		}
	}

	return g.updateTaskStats(&input, successCount, failedCount, failedEmails)
}

func (g *GmailProcessor) trackFailure(emailID string, err error, failedEmails []string, failedCount int, input ScheduledTaskProcessorInput) ([]string, int) {
	failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: %v", emailID, err))
	failedCount++
	input.Memory[emailID] = fmt.Sprintf("error: %v", err)
	return failedEmails, failedCount
}

func (g *GmailProcessor) uploadEmail(input ScheduledTaskProcessorInput, message interface{}, messagePath, bucket string) error {
	b, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal: %v", err)
	}
	return satellite.UploadObject(context.TODO(), input.Task.StorxToken, bucket, messagePath, b)
}

func (g *GmailProcessor) generateTitleFromGmailMessage(message *google.GmailMessage) string {
	if message == nil {
		return "unknown_message"
	}

	subject := message.Subject
	if subject == "" {
		subject = "no_subject"
	}

	// Replace invalid filename characters
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, char := range invalidChars {
		subject = strings.ReplaceAll(subject, char, "_")
	}

	if len(subject) > 50 {
		subject = subject[:50]
	}

	return fmt.Sprintf("%s_%s.json", subject, message.ID)
}
