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
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

const outlookCalendarPageSize int32 = 50

type outlookCalendarProcessor struct{}

func NewOutlookCalendarProcessor() *outlookCalendarProcessor {
	return &outlookCalendarProcessor{}
}

func (p *outlookCalendarProcessor) Run(input ProcessorInput) error {
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
			logger.Warn(processCtx, "Failed to process webhook events from outlook calendar auto-sync", logger.ErrorField(processErr))
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

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookCalendar, mailbox+"/.file_placeholder", nil, input.Job.UserID); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	calendars, err := client.ListCalendars()
	if err != nil {
		return err
	}
	if input.Job.TaskMemory.CalendarCalendars == nil {
		input.Job.TaskMemory.CalendarCalendars = make(map[string]repo.CalendarCalendarState)
	}

	for _, cal := range calendars {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		if err := syncOutlookCalendar(ctx, input, client, storx, mailbox, cal); err != nil {
			return err
		}
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func syncOutlookCalendar(ctx context.Context, input ProcessorInput, client *outlook.OutlookClient, storx, mailbox string, cal outlook.FlatCalendar) error {
	calendarID := strings.TrimSpace(cal.ID)
	if calendarID == "" {
		return nil
	}
	prefix := mailbox + "/" + calendarID + "/"
	synced, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_OutlookCalendar, prefix, input.Job.UserID, "outlook", "outlook_calendar")
	if err != nil {
		return fmt.Errorf("load synced calendar objects: %w", err)
	}

	metaPath := mailbox + "/" + calendarID + "/_calendar.json"
	metaBytes, _ := json.Marshal(cal)
	_ = handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookCalendar, metaPath, metaBytes, input.Job.UserID)

	var skip int32
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		events, err := client.ListCalendarEvents(calendarID, skip, outlookCalendarPageSize)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			break
		}
		for _, ev := range events {
			if ev.IsCancelled {
				continue
			}
			path := prefix + sanitizeObjectKey(ev.ID) + ".json"
			// Always persist FlatEvent JSON (subject/start/end). Never json.Marshal
			// the Graph SDK Eventable — that serializes to empty `{}` (Size 2).
			// Overwrite every run so legacy empty `{}` vault objects get repaired
			// (synced-map skip left corrupt stubs forever).
			payload, err := json.Marshal(ev)
			if err != nil || len(payload) <= 2 {
				continue
			}
			if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookCalendar, path, payload, input.Job.UserID); err != nil {
				logger.Warn(ctx, "outlook calendar upload failed", logger.String("path", path), logger.ErrorField(err))
				continue
			}
			synced[path] = true
		}
		if int32(len(events)) < outlookCalendarPageSize {
			break
		}
		skip += outlookCalendarPageSize
	}

	state := input.Job.TaskMemory.CalendarCalendars[calendarID]
	state.BaselineDone = true
	input.Job.TaskMemory.CalendarCalendars[calendarID] = state
	return nil
}

func sanitizeObjectKey(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	return id
}
