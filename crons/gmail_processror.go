package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
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

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	refreshToken, ok := (*input.Job.InputData.Json())["refresh_token"].(string)
	if !ok {
		return fmt.Errorf("refresh token not found")
	}

	newToken, err := google.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return fmt.Errorf("error while generating auth token: %s", err)
	}

	gmailClient, err := google.NewGmailClientUsingToken(newToken)
	if err != nil {
		return err
	}

	// Check if backup_email is specified (for corporate users backing up other accounts)
	backupEmail, ok := (*input.Job.InputData.Json())["backup_email"].(string)
	if !ok || backupEmail == "" {
		// Use the job's name as the default email
		backupEmail = input.Job.Name
	}

	// Determine the user ID for Gmail API calls
	// For corporate users with domain-wide delegation, use the email directly
	// Otherwise, "me" refers to the authenticated user
	userID := "me"
	if backupEmail != input.Job.Name && backupEmail != "" {
		// If backup_email is different from job name, use the email directly
		// This requires domain-wide delegation to work
		userID = backupEmail
	}

	// Use backup_email for storage path to organize backups by account
	storagePath := backupEmail

	err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Gmail, storagePath+"/.file_placeholder", nil)
	if err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Gmail, storagePath+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return err
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
		// Use userID instead of hardcoded "me" for corporate account access
		res, err := gmailClient.GetUserMessagesControlledWithUserID(userID, *input.Job.TaskMemory.GmailNextToken, "CATEGORY_PERSONAL", 500, nil)
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

			messagePath := storagePath + "/" + utils.GenerateTitleFromGmailMessage(message)
			_, synced := emailListFromBucket[messagePath]
			if synced {
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				return err
			}

			syncedData = true
			err = satellite.UploadObject(context.TODO(), input.Job.StorxToken, "gmail", messagePath, b)
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
