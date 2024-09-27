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

	nextPageToken := ""

	for {
		res, err := gmailClient.GetUserMessagesControlled(nextPageToken, 500)
		if err != nil {
			return err
		}

		for _, message := range res.Messages {
			_, synced := emailListFromBucket[utils.GenerateTitleFromGmailMessage(message)]
			if synced {
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				return err
			}

			err = satellite.UploadObject(context.TODO(), input.StorxToken, "gmail", utils.GenerateTitleFromGmailMessage(message), b)
			if err != nil {
				return err
			}

		}
		nextPageToken = res.NextPageToken
		if nextPageToken == "" {
			break
		}
	}

	return nil
}
