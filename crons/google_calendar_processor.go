package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/calendar/v3"
)

type googleCalendarProcessor struct{}

func NewGoogleCalendarProcessor() *googleCalendarProcessor {
	return &googleCalendarProcessor{}
}

func (p *googleCalendarProcessor) Run(input ProcessorInput) error {
	return runGoogleCalendarAutosync(input)
}

func runGoogleCalendarAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := googleMediaAutosyncPreflight(input)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from calendar auto-sync", logger.ErrorField(processErr))
		}
	}()

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Calendar, task.LoginId+"/.file_placeholder", nil, task.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	service, err := google.NewCalendarServiceWithAccessToken(ctx, accessToken)
	if err != nil {
		return err
	}

	calendars, err := google.ListCalendarsWithService(service)
	if err != nil {
		return err
	}
	if input.Job.TaskMemory.CalendarCalendars == nil {
		input.Job.TaskMemory.CalendarCalendars = make(map[string]repo.CalendarCalendarState)
	}

	for _, cal := range calendars.Calendars {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		if err := syncOneCalendar(ctx, input, task, service, storx, cal); err != nil {
			return err
		}
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func syncOneCalendar(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, storx string, cal google.FlatCalendar) error {
	calendarID := strings.TrimSpace(cal.ID)
	if calendarID == "" {
		return nil
	}

	meta, err := loadCalendarMeta(ctx, storx, task.LoginId, calendarID, cal.Summary)
	if err != nil {
		logger.Warn(ctx, "calendar metadata load failed, continuing fresh", logger.String("calendar_id", calendarID), logger.ErrorField(err))
		meta = google.CalendarMetadata{ID: calendarID}
	}
	if strings.TrimSpace(meta.Summary) == "" {
		meta.Summary = cal.Summary
	}
	if strings.TrimSpace(meta.TimeZone) == "" {
		meta.TimeZone = cal.TimeZone
	}
	meta.ID = calendarID

	state := input.Job.TaskMemory.CalendarCalendars[calendarID]
	syncToken := strings.TrimSpace(state.SyncToken)
	if syncToken == "" {
		syncToken = strings.TrimSpace(meta.NextSyncToken)
	}

	var nextSyncToken string
	if !state.BaselineDone && syncToken == "" {
		nextSyncToken, err = runCalendarBaselineSync(ctx, input, task, service, calendarID, meta.Summary)
		if err != nil {
			if google.IsSyncTokenGone(err) {
				nextSyncToken, err = runCalendarBaselineSync(ctx, input, task, service, calendarID, meta.Summary)
			}
			if err != nil {
				return fmt.Errorf("calendar %s baseline: %w", calendarID, err)
			}
		}
		state.BaselineDone = true
	} else {
		token := syncToken
		nextSyncToken, err = runCalendarIncrementalSync(ctx, input, task, service, calendarID, meta.Summary, token)
		if err != nil {
			if google.IsSyncTokenGone(err) {
				state.BaselineDone = false
				state.SyncToken = ""
				meta.NextSyncToken = ""
				nextSyncToken, err = runCalendarBaselineSync(ctx, input, task, service, calendarID, meta.Summary)
				if err != nil {
					return fmt.Errorf("calendar %s baseline after 410: %w", calendarID, err)
				}
				state.BaselineDone = true
			} else {
				return fmt.Errorf("calendar %s incremental: %w", calendarID, err)
			}
		}
	}

	if strings.TrimSpace(nextSyncToken) != "" {
		state.SyncToken = nextSyncToken
		meta.NextSyncToken = nextSyncToken
	}
	input.Job.TaskMemory.CalendarCalendars[calendarID] = state

	return saveCalendarMeta(ctx, input, task, storx, meta)
}

func runCalendarBaselineSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, calendarID, calendarSummary string) (string, error) {
	pageToken := ""
	var lastSyncToken string
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		page, err := google.ListCalendarEventsWithService(service, calendarID, pageToken, "")
		if err != nil {
			return "", err
		}
		if err := processCalendarEventsPage(ctx, input, task, calendarID, calendarSummary, page.RawEvents); err != nil {
			return "", err
		}
		if t := strings.TrimSpace(page.NextSyncToken); t != "" {
			lastSyncToken = t
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return lastSyncToken, nil
}

func runCalendarIncrementalSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, calendarID, calendarSummary, syncToken string) (string, error) {
	if strings.TrimSpace(syncToken) == "" {
		return runCalendarBaselineSync(ctx, input, task, service, calendarID, calendarSummary)
	}
	pageToken := ""
	var lastSyncToken string
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		page, err := google.ListCalendarEventsWithService(service, calendarID, pageToken, syncToken)
		if err != nil {
			return "", err
		}
		if err := processCalendarEventsPage(ctx, input, task, calendarID, calendarSummary, page.RawEvents); err != nil {
			return "", err
		}
		if t := strings.TrimSpace(page.NextSyncToken); t != "" {
			lastSyncToken = t
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
		syncToken = ""
	}
	return lastSyncToken, nil
}

func processCalendarEventsPage(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, calendarID, calendarSummary string, events []*calendar.Event) error {
	for _, ev := range events {
		if !google.EventShouldBackup(ev) {
			continue
		}
		if err := retrySyncCalendarEvent(ctx, input, task, calendarID, calendarSummary, ev); err != nil {
			logger.Warn(ctx, "calendar event sync failed",
				logger.String("calendar_id", calendarID),
				logger.String("event_id", ev.Id),
				logger.ErrorField(err),
			)
		}
	}
	return nil
}

func syncCalendarEvent(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, calendarID, calendarSummary string, ev *calendar.Event) error {
	objectKey := google.CalendarObjectKey(task.LoginId, calendarID, calendarSummary, ev.Id, ev.Summary, ev.Created)
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	// Legacy direct upload:
	// return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Calendar, objectKey, b, task.UserID, input.StorxRecovery)
	return handler.UploadBufferedObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Calendar, objectKey, b, task.UserID, input.StorxRecovery)
}

func retrySyncCalendarEvent(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, calendarID, calendarSummary string, ev *calendar.Event) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := syncCalendarEvent(ctx, input, task, calendarID, calendarSummary, ev); err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}

func loadCalendarMeta(ctx context.Context, storx, email, calendarID, summary string) (google.CalendarMetadata, error) {
	b, err := downloadCalendarMetaBytes(ctx, storx, email, calendarID, summary)
	if err != nil {
		return google.CalendarMetadata{}, err
	}
	var meta google.CalendarMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return google.CalendarMetadata{}, err
	}
	return meta, nil
}

func downloadCalendarMetaBytes(ctx context.Context, storx, email, calendarID, summary string) ([]byte, error) {
	key := google.CalendarMetaObjectKey(email, calendarID, summary)
	return satellite.DownloadObject(ctx, storx, satellite.ReserveBucket_Calendar, key)
}

func saveCalendarMeta(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, storx string, meta google.CalendarMetadata) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal calendar metadata: %w", err)
	}
	key := google.CalendarMetaObjectKey(task.LoginId, meta.ID, meta.Summary)
	// Legacy direct upload:
	// return handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Calendar, key, b, task.UserID, input.StorxRecovery)
	return handler.UploadBufferedObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Calendar, key, b, task.UserID, input.StorxRecovery)
}
