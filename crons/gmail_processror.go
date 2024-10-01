package crons

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/utils"
)

type gmailProcessor struct{}

func NewGmailProcessor() *gmailProcessor {
	return &gmailProcessor{}
}

func (g *gmailProcessor) Run(input ProcessorInput) error {

	gmailClient, err := google.NewGmailClientUsingToken(input.AuthToken)
	if err != nil {
		return err
	}

	emailListFromBucket, err := satellite.ListObjects(context.Background(), input.StorxToken, satellite.ReserveBucket_Gmail)
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return err
	}

	if input.Task.TaskMemory.NextToken == nil {
		input.Task.TaskMemory.NextToken = new(string)
	}

	emptyLoopCount := 0

	for {
		res, err := gmailClient.GetUserMessagesControlled(*input.Task.TaskMemory.NextToken, 500)
		if err != nil {
			return err
		}

		syncedData := false
		for _, message := range res.Messages {
			if !utils.Contains(message.LabelIds, "CATEGORY_PERSONAL") {
				// only sync personal emails
				continue
			}

			_, synced := emailListFromBucket[utils.GenerateTitleFromGmailMessage(message)]
			if synced {
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				return err
			}

			syncedData = true
			err = satellite.UploadObject(context.TODO(), input.StorxToken, "gmail", utils.GenerateTitleFromGmailMessage(message), b)
			if err != nil {
				return err
			}

		}

		if !syncedData {
			// if we don't get any new data, we can break
			emptyLoopCount++
		}

		if emptyLoopCount > 20 {
			// if we get 5 empty loops, we can break
			break
		}

		*input.Task.TaskMemory.NextToken = res.NextPageToken
		if *input.Task.TaskMemory.NextToken == "" {
			break
		}
	}

	return nil
}
