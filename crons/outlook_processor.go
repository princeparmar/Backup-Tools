package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/pkg/worker"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

const (
	OutlookLimit = 100
)

type outlookProcessor struct {
	workerPool *worker.WorkerPool
}

func NewOutlookProcessor() *outlookProcessor {
	return &outlookProcessor{
		workerPool: worker.NewWorkerPool(10),
	}
}

func (o *outlookProcessor) Run(input ProcessorInput) error {
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

		// Collect messages to upload in batch
		var uploads []satellite.UploadItem

		// Process messages in parallel using worker pool
		var mu sync.Mutex
		var batchWg sync.WaitGroup

		// Submit each message processing task to worker pool
		for _, message := range messages {
			message := message // Capture loop variable
			taskWg, submitErr := o.workerPool.SubmitAndWait(func() error {
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

				mu.Lock()
				_, synced := emailListFromBucket[messagePath]
				mu.Unlock()

				if synced {
					return nil
				}

				// Get full message with attachments
				fullMsg, err := outlookClient.GetMessage(message.ID)
				if err != nil {
					return err
				}

				// Add nil check for fullMsg
				if fullMsg == nil {
					return nil
				}

				b, err := json.Marshal(fullMsg)
				if err != nil {
					return err
				}

				mu.Lock()
				uploads = append(uploads, satellite.UploadItem{
					ObjectKey: messagePath,
					Data:      b,
				})
				input.Job.TaskMemory.OutlookSyncCount++
				mu.Unlock()

				// Clear message from memory
				fullMsg = nil
				b = nil
				return nil
			})

			if submitErr != nil {
				continue
			}

			// Track this task's completion
			batchWg.Add(1)
			go func(wg *sync.WaitGroup) {
				defer batchWg.Done()
				wg.Wait()
			}(taskWg)
		}

		// Wait for all message processing tasks to complete
		batchWg.Wait()

		// Set syncedData after all tasks complete based on actual uploads
		syncedData := len(uploads) > 0

		// Batch upload all messages in parallel
		if len(uploads) > 0 {
			if err := satellite.UploadBatch(ctx, input.Job.StorxToken, satellite.ReserveBucket_Outlook, uploads, 10); err != nil {
				// If batch upload fails, fall back to individual uploads
				for _, upload := range uploads {
					if uploadErr := satellite.UploadObject(ctx, input.Job.StorxToken, satellite.ReserveBucket_Outlook, upload.ObjectKey, upload.Data); uploadErr != nil {
						continue
					}
				}
			}
			emptyLoopCount = 0
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
