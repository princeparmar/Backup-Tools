package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

const outlookContactsPageSize int32 = 100

type outlookContactsProcessor struct{}

func NewOutlookContactsProcessor() *outlookContactsProcessor {
	return &outlookContactsProcessor{}
}

func (p *outlookContactsProcessor) Run(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := outlookAutosyncPreflight(input)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from outlook contacts auto-sync", logger.ErrorField(processErr))
		}
	}()

	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return err
	}
	user, err := client.GetCurrentUser()
	if err != nil {
		return err
	}
	mailbox := strings.TrimSpace(user.Mail)
	if mailbox == "" {
		mailbox = strings.TrimSpace(input.Job.Name)
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookContacts, mailbox+"/.file_placeholder", nil, input.Job.UserID); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	prefix := mailbox + "/"
	synced, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_OutlookContacts, prefix, input.Job.UserID, "outlook", "outlook_contacts")
	if err != nil {
		return fmt.Errorf("load synced contacts: %w", err)
	}

	var skip int32
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		contacts, err := client.ListContacts(skip, outlookContactsPageSize)
		if err != nil {
			return err
		}
		if len(contacts) == 0 {
			break
		}
		for _, c := range contacts {
			path := prefix + sanitizeObjectKey(c.ID) + ".json"
			if _, ok := synced[path]; ok {
				continue
			}
			payload, err := json.Marshal(c)
			if err != nil {
				continue
			}
			if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookContacts, path, payload, input.Job.UserID); err != nil {
				logger.Warn(ctx, "outlook contacts upload failed", logger.String("path", path), logger.ErrorField(err))
				continue
			}
			synced[path] = true
		}
		if int32(len(contacts)) < outlookContactsPageSize {
			break
		}
		skip += outlookContactsPageSize
	}

	input.Job.TaskMemory.ContactsBaselineDone = true
	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}
