package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type gmailProcessor struct{}

func NewGmailProcessor() *gmailProcessor {
	return &gmailProcessor{}
}

func (g *gmailProcessor) Run(input ProcessorInput) error {
	start := time.Now()

	err := input.HeartBeatFunc()
	if err != nil {
		prometheus.RecordError("gmail_processor_heartbeat_failed", "cron")
		return err
	}

	refreshToken, ok := input.Job.InputData["refresh_token"].(string)
	if !ok {
		prometheus.RecordError("gmail_processor_refresh_token_missing", "cron")
		return fmt.Errorf("refresh token not found")
	}

	newToken, err := google.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		prometheus.RecordError("gmail_processor_auth_token_failed", "cron")
		return fmt.Errorf("error while generating auth token: %s", err)
	}

	gmailClient, err := google.NewGmailClientUsingToken(newToken)
	if err != nil {
		prometheus.RecordError("gmail_processor_client_creation_failed", "cron")
		return err
	}

	err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Gmail, input.Job.Name+"/.file_placeholder", nil)
	if err != nil {
		prometheus.RecordError("gmail_processor_placeholder_upload_failed", "cron")
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Gmail, input.Job.Name+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		prometheus.RecordError("gmail_processor_list_objects_failed", "cron")
		return err
	}

	err = input.HeartBeatFunc()
	if err != nil {
		prometheus.RecordError("gmail_processor_heartbeat_failed", "cron")
		return err
	}

	if input.Job.TaskMemory.GmailNextToken == nil {
		input.Job.TaskMemory.GmailNextToken = new(string)
	}

	emptyLoopCount := 0

	for {
		res, err := gmailClient.GetUserMessagesControlled(*input.Job.TaskMemory.GmailNextToken, "CATEGORY_PERSONAL", 500, nil)
		if err != nil {
			prometheus.RecordError("gmail_processor_get_messages_failed", "cron")
			return err
		}

		syncedData := false
		for _, message := range res.Messages {
			err := input.HeartBeatFunc()
			if err != nil {
				prometheus.RecordError("gmail_processor_heartbeat_failed", "cron")
				return err
			}

			if !utils.Contains(message.LabelIds, "CATEGORY_PERSONAL") {
				// only sync personal emails
				continue
			}

			messagePath := input.Job.Name + "/" + utils.GenerateTitleFromGmailMessage(message)
			_, synced := emailListFromBucket[messagePath]
			if synced {
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				prometheus.RecordError("gmail_processor_json_marshal_failed", "cron")
				return err
			}

			syncedData = true
			err = satellite.UploadObject(context.TODO(), input.Job.StorxToken, "gmail", messagePath, b)
			if err != nil {
				prometheus.RecordError("gmail_processor_upload_message_failed", "cron")
				return err
			}

			input.Job.TaskMemory.GmailSyncCount++
			emptyLoopCount = 0
			prometheus.RecordCounter("gmail_processor_messages_synced_total", 1, "processor", "gmail", "status", "success")
		}

		if !syncedData {
			// if we don't get any new data, we can break
			emptyLoopCount++
		}

		if emptyLoopCount > 20 {
			// if we get 5 empty loops, we can break
			prometheus.RecordCounter("gmail_processor_empty_loops_total", 1, "processor", "gmail", "reason", "max_empty_loops")
			*input.Job.TaskMemory.GmailNextToken = ""
			break
		}

		*input.Job.TaskMemory.GmailNextToken = res.NextPageToken
		if *input.Job.TaskMemory.GmailNextToken == "" {
			prometheus.RecordCounter("gmail_processor_empty_loops_total", 1, "processor", "gmail", "reason", "end_of_messages")
			break
		}
	}

	duration := time.Since(start)
	prometheus.RecordTimer("gmail_processor_duration_seconds", duration, "processor", "gmail")
	prometheus.RecordCounter("gmail_processor_total", 1, "processor", "gmail", "status", "success")
	prometheus.RecordCounter("gmail_processor_messages_processed_total", int64(input.Job.TaskMemory.GmailSyncCount), "processor", "gmail")

	return nil
}
