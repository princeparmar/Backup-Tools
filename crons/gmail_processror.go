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

var Err error

func (g *gmailProcessor) Run(input ProcessorInput) error {

	ctx := context.Background()
	defer monitor.Mon.Task()(&ctx)(&Err)

	err := input.HeartBeatFunc()
	if err != nil {
		return err
	}

	refreshToken, ok := input.Job.InputData["refresh_token"].(string)
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

	err = satellite.UploadObject(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Gmail, input.Job.Name+"/.file_placeholder", nil)
	if err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(), input.Job.StorxToken, satellite.ReserveBucket_Gmail, input.Job.Name+"/")
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
		res, err := gmailClient.GetUserMessagesControlled(*input.Job.TaskMemory.GmailNextToken, "CATEGORY_PERSONAL", 500, nil)
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

			messagePath := input.Job.Name + "/" + utils.GenerateTitleFromGmailMessage(message)
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
			// if we get 20 empty loops, we can break
			*input.Job.TaskMemory.GmailNextToken = ""
			// Mark as complete for one-time sync
			if input.Job.SyncType == "one_time" {
				input.Job.TaskMemory.GmailSyncComplete = true
			}
			break
		}

		*input.Job.TaskMemory.GmailNextToken = res.NextPageToken
		if *input.Job.TaskMemory.GmailNextToken == "" {
			// No more pages, mark as complete for one-time sync
			if input.Job.SyncType == "one_time" {
				input.Job.TaskMemory.GmailSyncComplete = true
			}
			break
		}
	}
	return nil
}
