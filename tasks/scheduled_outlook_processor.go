package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// OutlookProcessor handles Outlook scheduled tasks
type OutlookProcessor struct {
	BaseProcessor
}

func NewScheduledOutlookProcessor(deps *TaskProcessorDeps) *OutlookProcessor {
	return &OutlookProcessor{BaseProcessor{Deps: deps}}
}

func (o *OutlookProcessor) Run(input ScheduledTaskProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if err = input.HeartBeatFunc(); err != nil {
		return err
	}

	refreshToken, ok := input.InputData["refresh_token"].(string)
	if !ok {
		return o.handleError(input.Task, "Refresh token not found for Outlook method", nil)
	}

	accessToken, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return o.handleError(input.Task, fmt.Sprintf("Authentication failed: %s", err), nil)
	}

	outlookClient, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return o.handleError(input.Task, fmt.Sprintf("Failed to create Outlook client: %s", err), nil)
	}

	// Create placeholder and get existing emails
	if err := o.setupStorage(input.Task, satellite.ReserveBucket_Outlook); err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(ctx, input.Task.StorxToken, satellite.ReserveBucket_Outlook, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return o.handleError(input.Task, fmt.Sprintf("Failed to list existing emails: %s", err), nil)
	}

	return o.processEmails(input, outlookClient, emailListFromBucket)
}

func (o *OutlookProcessor) setupStorage(task *repo.ScheduledTasks, bucket string) error {
	return satellite.UploadObject(context.Background(), task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil)
}

func (o *OutlookProcessor) processEmails(input ScheduledTaskProcessorInput, client *outlook.OutlookClient, existingEmails map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedEmails []string

	// Get pending emails
	pendingEmails := input.Memory["pending"]
	if pendingEmails == nil {
		pendingEmails = []string{}
	}

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	for _, emailID := range pendingEmails {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		message, err := client.GetMessage(emailID)
		if err != nil {
			failedEmails, failedCount = o.trackFailure(emailID, err, failedEmails, failedCount, input)
			continue
		}

		messagePath := input.Task.LoginId + "/" + utils.GenerateTitleFromOutlookMessage(&utils.OutlookMinimalMessage{
			ID:               message.ID,
			Subject:          message.Subject,
			From:             message.From,
			ReceivedDateTime: message.ReceivedDateTime,
		})

		if _, exists := existingEmails[messagePath]; exists {
			moveEmailToStatus(&input.Memory, emailID, "pending", "skipped: already exists in storage")
			successCount++
			continue
		}

		if err := o.uploadEmail(input, message, messagePath, "outlook"); err != nil {
			failedEmails, failedCount = o.trackFailure(emailID, err, failedEmails, failedCount, input)
		} else {
			moveEmailToStatus(&input.Memory, emailID, "pending", "synced")
			successCount++
		}
	}

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

func (o *OutlookProcessor) uploadEmail(input ScheduledTaskProcessorInput, message interface{}, messagePath, bucket string) error {
	b, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal: %v", err)
	}
	return satellite.UploadObject(context.TODO(), input.Task.StorxToken, bucket, messagePath, b)
}
