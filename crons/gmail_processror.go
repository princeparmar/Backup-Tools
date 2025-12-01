package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
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
	pageCount := 0
	totalProcessed := 0

	for {
		pageCount++
		res, err := gmailClient.GetUserMessagesControlled(ctx, *input.Job.TaskMemory.GmailNextToken, "CATEGORY_PERSONAL", 500, nil)
		if err != nil {
			logger.Error(ctx, "Failed to get user messages from Gmail",
				logger.ErrorField(err),
				logger.Int("page", pageCount),
			)
			return err
		}

		logger.Info(ctx, "Processing Gmail batch",
			logger.Int("page", pageCount),
			logger.Int("messages_in_batch", len(res.Messages)),
			logger.String("next_page_token", res.NextPageToken),
			logger.Int("total_synced", int(input.Job.TaskMemory.GmailSyncCount)),
		)

		// Collect messages to upload in batch
		var uploads []satellite.UploadItem
		syncedData := false
		skippedAlreadySynced := 0
		skippedNotPersonal := 0
		newMessagesToSync := 0

		for _, message := range res.Messages {
			err := input.HeartBeatFunc()
			if err != nil {
				return err
			}

			// Check if message has CATEGORY_PERSONAL label
			// Note: API already filters by label, but some messages might not have it in LabelIds
			if !utils.Contains(message.LabelIds, "CATEGORY_PERSONAL") {
				// only sync personal emails
				skippedNotPersonal++
				continue
			}

			messagePath := input.Job.Name + "/" + utils.GenerateTitleFromGmailMessage(message)
			_, synced := emailListFromBucket[messagePath]
			if synced {
				skippedAlreadySynced++
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				logger.Error(ctx, "Failed to marshal message",
					logger.ErrorField(err),
					logger.String("message_id", message.Id),
				)
				continue
			}

			syncedData = true
			newMessagesToSync++
			uploads = append(uploads, satellite.UploadItem{
				ObjectKey: messagePath,
				Data:      b,
			})

			input.Job.TaskMemory.GmailSyncCount++
		}

		totalProcessed += len(res.Messages)

		// Batch upload all messages in parallel
		if len(uploads) > 0 {
			if err := satellite.UploadBatch(ctx, input.Job.StorxToken, "gmail", uploads, 10); err != nil {
				logger.Warn(ctx, "Batch upload failed, falling back to individual uploads",
					logger.ErrorField(err),
					logger.Int("upload_count", len(uploads)),
				)
				// If batch upload fails, fall back to individual uploads
				for _, upload := range uploads {
					if uploadErr := satellite.UploadObject(ctx, input.Job.StorxToken, "gmail", upload.ObjectKey, upload.Data); uploadErr != nil {
						logger.Error(ctx, "Failed to upload message individually",
							logger.ErrorField(uploadErr),
							logger.String("object_key", upload.ObjectKey),
						)
						continue
					}
				}
			}
			emptyLoopCount = 0
		}

		// Update next page token
		*input.Job.TaskMemory.GmailNextToken = res.NextPageToken

		// Primary exit condition: No more pages (NextPageToken is empty)
		if *input.Job.TaskMemory.GmailNextToken == "" {
			logger.Info(ctx, "Reached end of Gmail messages",
				logger.Int("total_pages", pageCount),
				logger.Int("total_messages_processed", totalProcessed),
				logger.Int("total_synced", int(input.Job.TaskMemory.GmailSyncCount)),
			)
			break
		}

		// Safety mechanism: Track consecutive empty batches
		// An empty batch means no new emails were synced (all were already synced or filtered out)
		if !syncedData {
			emptyLoopCount++
			logger.Info(ctx, "Empty batch detected (no new emails to sync)",
				logger.Int("page", pageCount),
				logger.Int("consecutive_empty_batches", emptyLoopCount),
				logger.String("next_page_token", res.NextPageToken),
			)

			// Safety: Only break if we have 100+ consecutive empty batches AND no messages returned
			// This prevents infinite loops while allowing processing to continue through already-synced emails
			if emptyLoopCount > 100 && len(res.Messages) == 0 {
				logger.Warn(ctx, "Breaking loop: 100+ consecutive empty batches with no messages",
					logger.Int("consecutive_empty_batches", emptyLoopCount),
					logger.Int("total_pages", pageCount),
				)
				*input.Job.TaskMemory.GmailNextToken = ""
				break
			}
		} else {
			// Reset counter when we find new data
			emptyLoopCount = 0
		}
	}

	logger.Info(ctx, "Gmail cron processing completed",
		logger.Int("total_pages_processed", pageCount),
		logger.Int("total_messages_processed", totalProcessed),
		logger.Int("total_emails_synced", int(input.Job.TaskMemory.GmailSyncCount)),
	)

	return nil
}
