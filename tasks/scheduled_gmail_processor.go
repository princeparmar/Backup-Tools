package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type scheduledGmailProcessor struct{}

func NewScheduledGmailProcessor() *scheduledGmailProcessor {
	return &scheduledGmailProcessor{}
}

func (g *scheduledGmailProcessor) Run(input ScheduledTaskProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	refreshToken, ok := input.InputData["refresh_token"].(string)
	if !ok {
		// Store error in task and return
		var existingErrors []string
		if input.Task.Errors.Json() != nil {
			existingErrors = *input.Task.Errors.Json()
		}
		existingErrors = append(existingErrors, "Refresh token not found for Gmail method")
		input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)
		return fmt.Errorf("refresh token not found for Gmail method")
	}

	newToken, err := google.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		// Store authentication error
		var existingErrors []string
		if input.Task.Errors.Json() != nil {
			existingErrors = *input.Task.Errors.Json()
		}
		existingErrors = append(existingErrors, fmt.Sprintf("Authentication failed: %s", err))
		input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)
		return fmt.Errorf("error while generating auth token: %s", err)
	}

	gmailClient, err := google.NewGmailClientUsingToken(newToken)
	if err != nil {
		// Store client creation error
		var existingErrors []string
		if input.Task.Errors.Json() != nil {
			existingErrors = *input.Task.Errors.Json()
		}
		existingErrors = append(existingErrors, fmt.Sprintf("Failed to create Gmail client: %s", err))
		input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)
		return err
	}

	// Create placeholder file
	err = satellite.UploadObject(context.Background(), input.Task.StorxToken, satellite.ReserveBucket_Gmail, input.Task.LoginId+"/.file_placeholder", nil)
	if err != nil {
		// Store storage error
		var existingErrors []string
		if input.Task.Errors.Json() != nil {
			existingErrors = *input.Task.Errors.Json()
		}
		existingErrors = append(existingErrors, fmt.Sprintf("Failed to create placeholder file: %s", err))
		input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)
		return err
	}

	// Get existing emails from bucket
	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(), input.Task.StorxToken, satellite.ReserveBucket_Gmail, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		// Store bucket listing error
		var existingErrors []string
		if input.Task.Errors.Json() != nil {
			existingErrors = *input.Task.Errors.Json()
		}
		existingErrors = append(existingErrors, fmt.Sprintf("Failed to list existing emails: %s", err))
		input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)
		return err
	}

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	// Track success and failure counts
	successCount := 0
	failedCount := 0
	var failedEmails []string

	// Add heartbeat before processing emails
	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	// Process each email ID from memory
	for emailID, status := range input.Memory {
		if status == "synced" || status == "skipped" || strings.HasPrefix(status, "error:") {
			continue // Skip already processed emails
		}

		err = input.HeartBeatFunc()
		if err != nil {
			return err
		}

		// Get the specific email by ID
		message, err := gmailClient.GetMessage(emailID)
		if err != nil {
			// Log error and track failure
			fmt.Printf("Failed to get email %s: %v\n", emailID, err)
			failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: %v", emailID, err))
			failedCount++
			input.Memory[emailID] = fmt.Sprintf("error: %v", err)
			continue
		}

		// Generate message path
		messagePath := input.Task.LoginId + "/" + generateTitleFromGmailMessage(message)
		_, synced := emailListFromBucket[messagePath]
		if synced {
			// Mark as skipped if already exists in storage
			input.Memory[emailID] = "skipped: already exists in storage"
			successCount++
			continue
		}

		// Upload the email
		b, err := json.Marshal(message)
		if err != nil {
			failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: Failed to marshal - %v", emailID, err))
			failedCount++
			input.Memory[emailID] = fmt.Sprintf("error: Failed to marshal - %v", err)
			continue
		}

		err = satellite.UploadObject(context.TODO(), input.Task.StorxToken, "gmail", messagePath, b)
		if err != nil {
			failedEmails = append(failedEmails, fmt.Sprintf("Email ID %s: Failed to upload - %v", emailID, err))
			failedCount++
			input.Memory[emailID] = fmt.Sprintf("error: Failed to upload - %v", err)
			continue
		}

		// Mark as processed and increment success count
		input.Memory[emailID] = "synced"
		successCount++
	}

	// Add heartbeat after processing all emails
	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	// Update task with counts and errors
	input.Task.SuccessCount = uint(successCount)
	input.Task.FailedCount = uint(failedCount)

	// Get existing errors and append new ones
	var existingErrors []string
	if input.Task.Errors.Json() != nil {
		existingErrors = *input.Task.Errors.Json()
	}
	existingErrors = append(existingErrors, failedEmails...)

	// Add summary message if some IDs failed
	if failedCount > 0 {
		if successCount > 0 {
			existingErrors = append(existingErrors, fmt.Sprintf("Warning: %d out of %d email IDs failed to sync", failedCount, failedCount+successCount))
		} else {
			existingErrors = append(existingErrors, fmt.Sprintf("Error: All %d email IDs failed to sync", failedCount))
		}
	}

	input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)

	// Only return error if ALL emails failed
	if failedCount > 0 && successCount == 0 {
		return fmt.Errorf("failed to process %d emails: %v", failedCount, failedEmails)
	}

	return nil
}

// generateTitleFromGmailMessage creates a filename from Gmail message
func generateTitleFromGmailMessage(message *google.GmailMessage) string {
	if message == nil {
		return "unknown_message"
	}

	subject := message.Subject
	if subject == "" {
		subject = "no_subject"
	}

	// Replace invalid filename characters
	subject = strings.ReplaceAll(subject, "/", "_")
	subject = strings.ReplaceAll(subject, "\\", "_")
	subject = strings.ReplaceAll(subject, ":", "_")
	subject = strings.ReplaceAll(subject, "*", "_")
	subject = strings.ReplaceAll(subject, "?", "_")
	subject = strings.ReplaceAll(subject, "\"", "_")
	subject = strings.ReplaceAll(subject, "<", "_")
	subject = strings.ReplaceAll(subject, ">", "_")
	subject = strings.ReplaceAll(subject, "|", "_")

	// Limit length and add message ID
	if len(subject) > 50 {
		subject = subject[:50]
	}

	return fmt.Sprintf("%s_%s.json", subject, message.ID)
}
