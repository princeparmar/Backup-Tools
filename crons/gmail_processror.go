package crons

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type gmailProcessor struct{}

func NewGmailProcessor() *gmailProcessor {
	return &gmailProcessor{}
}

func (g *gmailProcessor) Run(input ProcessorInput) error {

	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Process webhook events using access grant from database (auto-sync)
	// Run in background, non-blocking - process at beginning so webhooks are handled even if sync fails
	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, input.Job.StorxToken, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync",
				logger.ErrorField(processErr))
		}
	}()

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	// Resolve refresh_token: from oauth_credentials (credential_id) or from job input_data (legacy).
	var refreshToken string
	inputData := input.Job.InputData.Json()
	if inputData != nil {
		if credID, ok := (*inputData)["credential_id"].(float64); ok && credID > 0 {
			refreshToken, err = input.Database.OAuthCredentialRepo.GetRefreshTokenByID(uint(credID))
			if err != nil {
				return fmt.Errorf("oauth credential not found: %w", err)
			}
		}
	}
	if refreshToken == "" && inputData != nil {
		refreshToken, _ = (*inputData)["refresh_token"].(string)
	}
	if refreshToken == "" {
		return fmt.Errorf("refresh token not found (set credential_id or refresh_token in job)")
	}

	newToken, err := google.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return fmt.Errorf("error while generating auth token: %s", err)
	}

	gmailClient, err := google.NewGmailClientUsingToken(newToken)
	if err != nil {
		return err
	}

	// One job = one account. userID from job input_data email or job name (storage path same).
	userID := "me"
	pathPrefix := input.Job.Name
	if input.Job.InputData != nil && input.Job.InputData.Json() != nil {
		if email, ok := (*input.Job.InputData.Json())["email"].(string); ok && email != "" {
			userID = email
			pathPrefix = email
		}
	}

	err = handler.UploadObjectAndSync(context.Background(), input.Database, input.Job.StorxToken, satellite.ReserveBucket_Gmail, pathPrefix+"/.file_placeholder", nil, input.Job.UserID)
	if err != nil {
		return err
	}

	// Get synced objects from database instead of listing from Satellite (OPTIMIZATION)
	// This is much faster and avoids unnecessary API calls to Satellite
	// Uses common function that ensures bucket exists and queries database
	prefix := pathPrefix + "/"
	emailListFromBucket, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, input.Job.StorxToken, satellite.ReserveBucket_Gmail, prefix, input.Job.UserID, "google", "gmail")
	if err != nil {
		return fmt.Errorf("failed to get synced objects: %w", err)
	}

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	if input.Job.TaskMemory.GmailNextToken == nil {
		input.Job.TaskMemory.GmailNextToken = new(string)
	}

	emptyLoopCount := 0

	for {
		res, err := gmailClient.GetUserMessagesWithUserID(userID, *input.Job.TaskMemory.GmailNextToken, "CATEGORY_PERSONAL", 500, nil)
		if err != nil {
			return err
		}

		syncedData := false
		for _, message := range res.Messages {
			err := input.HeartBeatFunc()
			if err != nil {
				return err
			}

			if !utils.Contains(message.LabelIds, "CATEGORY_PERSONAL") {
				// only sync personal emails
				continue
			}

			messagePath := pathPrefix + "/" + utils.GenerateTitleFromGmailMessage(message)
			_, synced := emailListFromBucket[messagePath]
			if synced {
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				return err
			}

			syncedData = true
			err = handler.UploadObjectAndSync(context.TODO(), input.Database, input.Job.StorxToken, "gmail", messagePath, b, input.Job.UserID)
			if err != nil {
				return err
			}

			input.Job.TaskMemory.GmailSyncCount++
			emptyLoopCount = 0
		}

		if !syncedData {
			// if we don't get any new data, we can break
			emptyLoopCount++
		}

		if emptyLoopCount > 20 {
			// if we get 5 empty loops, we can break
			*input.Job.TaskMemory.GmailNextToken = ""
			break
		}

		*input.Job.TaskMemory.GmailNextToken = res.NextPageToken
		if *input.Job.TaskMemory.GmailNextToken == "" {
			break
		}
	}
	return nil
}
