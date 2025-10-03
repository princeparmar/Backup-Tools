package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
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
	err := input.HeartBeatFunc()
	if err != nil {
		return err
	}

	refreshToken, ok := input.Job.InputData["refresh_token"].(string)
	if !ok {
		return fmt.Errorf("refresh token not found")
	}

	token, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return fmt.Errorf("error while getting token from refresh token: %s", err)
	}

	outlookClient, err := outlook.NewOutlookClientUsingToken(token)
	if err != nil {
		return fmt.Errorf("error while creating outlook client: %s", err)
	}

	// Get user details for creating folder structure
	userDetails, err := outlookClient.GetCurrentUser()
	if err != nil {
		return fmt.Errorf("error getting user details: %s", err)
	}

	// Create placeholder file to initialize bucket
	err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Outlook, userDetails.Mail+"/.file_placeholder", nil)
	if err != nil {
		return err
	}

	// Get list of already synced emails
	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Outlook, userDetails.Mail+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return err
	}

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	emptyLoopCount := 0

	for {
		messages, err := outlookClient.GetMessageWithDetails(int32(input.Job.TaskMemory.OutlookSkipCount), int32(OutlookLimit))
		if err != nil {
			return err
		}

		if len(messages) == 0 {
			break
		}

		syncedData := false
		for _, message := range messages {
			err := input.HeartBeatFunc()
			if err != nil {
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
				continue
			}

			b, err := json.Marshal(fullMsg)
			if err != nil {
				continue
			}

			syncedData = true
			err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Outlook, messagePath, b)
			if err != nil {
				continue
			}

			emptyLoopCount = 0
			input.Job.TaskMemory.OutlookSyncCount++
		}

		if !syncedData {
			emptyLoopCount++
		}

		if emptyLoopCount > 20 {
			// If we get 20 empty loops, we can break
			input.Job.TaskMemory.OutlookSkipCount = 0
			break
		}

		input.Job.TaskMemory.OutlookSkipCount += OutlookLimit
		if len(messages) < OutlookLimit {
			// If we get fewer messages than the limit, we've reached the end
			input.Job.TaskMemory.OutlookSkipCount = 0
			break
		}
	}

	return nil
}
