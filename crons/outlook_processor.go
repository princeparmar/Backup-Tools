package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

const (
	OutlookLimit = 100
)

type outlookProcessor struct{}

func NewOutlookProcessor() *outlookProcessor {
	return &outlookProcessor{}
}

func (o *outlookProcessor) Run(input ProcessorInput) error {
	start := time.Now()

	err := input.HeartBeatFunc()
	if err != nil {
		prometheus.RecordError("outlook_processor_heartbeat_failed", "cron")
		return err
	}

	refreshToken, ok := input.Job.InputData["refresh_token"].(string)
	if !ok {
		prometheus.RecordError("outlook_processor_refresh_token_missing", "cron")
		return fmt.Errorf("refresh token not found")
	}

	token, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		prometheus.RecordError("outlook_processor_auth_token_failed", "cron")
		return fmt.Errorf("error while getting token from refresh token: %s", err)
	}

	outlookClient, err := outlook.NewOutlookClientUsingToken(token)
	if err != nil {
		prometheus.RecordError("outlook_processor_client_creation_failed", "cron")
		return fmt.Errorf("error while creating outlook client: %s", err)
	}

	// Get user details for creating folder structure
	userDetails, err := outlookClient.GetCurrentUser()
	if err != nil {
		prometheus.RecordError("outlook_processor_get_user_details_failed", "cron")
		return fmt.Errorf("error getting user details: %s", err)
	}

	// Create placeholder file to initialize bucket
	err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Outlook, userDetails.Mail+"/.file_placeholder", nil)
	if err != nil {
		prometheus.RecordError("outlook_processor_placeholder_upload_failed", "cron")
		return err
	}

	// Get list of already synced emails
	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Outlook, userDetails.Mail+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		prometheus.RecordError("outlook_processor_list_objects_failed", "cron")
		return err
	}

	err = input.HeartBeatFunc()
	if err != nil {
		prometheus.RecordError("outlook_processor_heartbeat_failed", "cron")
		return err
	}

	emptyLoopCount := 0

	for {
		messages, err := outlookClient.GetMessageWithDetails(int32(input.Job.TaskMemory.OutlookSkipCount), int32(OutlookLimit))
		if err != nil {
			prometheus.RecordError("outlook_processor_get_messages_failed", "cron")
			return err
		}

		if len(messages) == 0 {
			break
		}

		syncedData := false
		for _, message := range messages {
			err := input.HeartBeatFunc()
			if err != nil {
				prometheus.RecordError("outlook_processor_heartbeat_failed", "cron")
				return err
			}

			messagePath := userDetails.Mail + "/" + utils.GenerateTitleFromOutlookMessage(&utils.OutlookMinimalMessage{
				ID:               message.ID,
				Subject:          message.Subject,
				From:             message.From,
				ReceivedDateTime: message.ReceivedDateTime,
			})
			_, synced := emailListFromBucket[messagePath]
			if synced {
				continue
			}

			// Get full message with attachments
			fullMsg, err := outlookClient.GetMessage(message.ID)
			if err != nil {
				prometheus.RecordError("outlook_processor_get_message_details_failed", "cron")
				continue
			}

			b, err := json.Marshal(fullMsg)
			if err != nil {
				prometheus.RecordError("outlook_processor_json_marshal_failed", "cron")
				continue
			}

			syncedData = true
			err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Outlook, messagePath, b)
			if err != nil {
				prometheus.RecordError("outlook_processor_upload_message_failed", "cron")
				continue
			}

			emptyLoopCount = 0
			input.Job.TaskMemory.OutlookSyncCount++
			prometheus.RecordCounter("outlook_processor_messages_synced_total", 1, "processor", "outlook", "status", "success")
		}

		if !syncedData {
			emptyLoopCount++
		}

		if emptyLoopCount > 20 {
			// If we get 20 empty loops, we can break
			prometheus.RecordCounter("outlook_processor_empty_loops_total", 1, "processor", "outlook", "reason", "max_empty_loops")
			input.Job.TaskMemory.OutlookSkipCount = 0
			break
		}

		input.Job.TaskMemory.OutlookSkipCount += OutlookLimit
		if len(messages) < OutlookLimit {
			// If we get fewer messages than the limit, we've reached the end
			prometheus.RecordCounter("outlook_processor_empty_loops_total", 1, "processor", "outlook", "reason", "end_of_messages")
			input.Job.TaskMemory.OutlookSkipCount = 0
			break
		}
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_processor_duration_seconds", duration, "processor", "outlook")
	prometheus.RecordCounter("outlook_processor_total", 1, "processor", "outlook", "status", "success")
	prometheus.RecordCounter("outlook_processor_messages_processed_total", int64(input.Job.TaskMemory.OutlookSyncCount), "processor", "outlook")

	return nil
}
