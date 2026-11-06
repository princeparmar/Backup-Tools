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

	auth, err := googleMediaAutosyncPreflight(input)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, auth.Storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from calendar auto-sync", logger.ErrorField(processErr))
		}
	}()

	task := scheduledTaskShellFromCronJob(input.Job, auth.AccessToken, auth.Storx)

	var service *calendar.Service
	if auth.UseDWD {
		service, err = google.NewCalendarServiceForBackupDWD(ctx, auth.Mailbox)
	} else {
		service, err = google.NewCalendarServiceWithAccessToken(ctx, auth.AccessToken)
	}
	if err != nil {
		return err
	}
	if err := estimateAndEnforceCalendar(ctx, input, service); err != nil {
		return err
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, auth.Storx, satellite.ReserveBucket_Calendar, task.LoginId+"/.file_placeholder", nil, task.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("google_calendar", fmt.Errorf("setup storage placeholder: %w", err))
	}

	calendars, err := google.ListCalendarsWithService(service)
	if err != nil {
		return err
	}
	if input.Job.TaskMemory.CalendarCalendars == nil {
		input.Job.TaskMemory.CalendarCalendars = make(map[string]repo.CalendarCalendarState)
	}

	index, indexReady := loadCalendarIndex(ctx, auth.Storx, task.LoginId)
	needsIndex := input.Job.TaskMemory.CalendarIndexVersion < google.CalendarIndexFileVersion
	indexBuilt := needsIndex

	for _, cal := range calendars.Calendars {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		live := (*google.CalendarIndex)(nil)
		if !needsIndex {
			live = &index
		}
		if err := syncOneCalendar(ctx, input, task, service, auth.Storx, cal, live); err != nil {
			logger.Warn(ctx, "calendar sync skipped", logger.String("calendar_id", cal.ID), logger.ErrorField(err))
		}
		if needsIndex {
			if err := rebuildCalendarIndex(ctx, input, task, service, cal, &index); err != nil {
				indexBuilt = false
				logger.Warn(ctx, "calendar index skipped", logger.String("calendar_id", cal.ID), logger.ErrorField(err))
			}
		}
	}

	if indexReady && (needsIndex || len(calendars.Calendars) > 0) {
		index.SetCalendars(calendars.Calendars)
		if err := saveCalendarIndex(ctx, input, task, auth.Storx, &index); err != nil {
			return err
		}
		if needsIndex && indexBuilt {
			input.Job.TaskMemory.CalendarIndexVersion = google.CalendarIndexFileVersion
		}
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func syncOneCalendar(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, storx string, cal google.FlatCalendar, index *google.CalendarIndex) error {
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
	if name := strings.TrimSpace(cal.DisplayName); name != "" {
		meta.DisplayName = name
	}
	meta.Primary = cal.Primary
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
		nextSyncToken, err = runCalendarBaselineSync(ctx, input, task, service, calendarID, meta.Summary, index)
		if err != nil {
			if google.IsSyncTokenGone(err) {
				nextSyncToken, err = runCalendarBaselineSync(ctx, input, task, service, calendarID, meta.Summary, index)
			}
			if err != nil {
				return fmt.Errorf("calendar %s baseline: %w", calendarID, err)
			}
		}
		state.BaselineDone = true
	} else {
		token := syncToken
		nextSyncToken, err = runCalendarIncrementalSync(ctx, input, task, service, calendarID, meta.Summary, token, index)
		if err != nil {
			if google.IsSyncTokenGone(err) {
				state.BaselineDone = false
				state.SyncToken = ""
				meta.NextSyncToken = ""
				nextSyncToken, err = runCalendarBaselineSync(ctx, input, task, service, calendarID, meta.Summary, index)
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

func runCalendarBaselineSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, calendarID, calendarSummary string, index *google.CalendarIndex) (string, error) {
	synced, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Calendar, task.LoginId+"/", task.UserID, "google", "calendar", input.StorxRecovery)
	if err != nil {
		logger.Warn(ctx, "calendar synced map load failed; continuing with empty map", logger.ErrorField(err))
		synced = map[string]bool{}
	}
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
		if err := processCalendarEventsPage(ctx, input, task, calendarID, calendarSummary, page.RawEvents, synced, index); err != nil {
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

func runCalendarIncrementalSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, calendarID, calendarSummary, syncToken string, index *google.CalendarIndex) (string, error) {
	if strings.TrimSpace(syncToken) == "" {
		return runCalendarBaselineSync(ctx, input, task, service, calendarID, calendarSummary, index)
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
		if err := processCalendarEventsPage(ctx, input, task, calendarID, calendarSummary, page.RawEvents, nil, index); err != nil {
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

func processCalendarEventsPage(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, calendarID, calendarSummary string, events []*calendar.Event, synced map[string]bool, index *google.CalendarIndex) error {
	for _, ev := range events {
		if !google.EventShouldBackup(ev) {
			if index != nil && ev != nil {
				index.Remove(calendarID, ev.Id)
			}
			continue
		}
		// Full pass: an event already in synced_objects is not uploaded again.
		// Incremental passes (synced == nil) still write events Google reports as changed.
		if synced != nil && google.IsCalendarEventSynced(synced, task.LoginId, calendarID, ev.Id, calendarSummary, ev.Summary) {
			continue
		}
		if err := retrySyncCalendarEvent(ctx, input, task, calendarID, calendarSummary, ev); err != nil {
			if shouldAbortOnItemError(err) {
				return wrapStorageAbort("google_calendar", err)
			}
			logger.Warn(ctx, "calendar event sync failed",
				logger.String("calendar_id", calendarID),
				logger.String("event_id", ev.Id),
				logger.ErrorField(err),
			)
			continue
		}
		key := google.CalendarObjectKey(task.LoginId, calendarID, calendarSummary, ev.Id, ev.Summary, ev.Created)
		if synced != nil {
			synced[key] = true
		}
		if index != nil {
			if row, ok := google.IndexEventFrom(calendarID, key, ev); ok {
				index.Put(row)
			}
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

func rebuildCalendarIndex(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *calendar.Service, cal google.FlatCalendar, index *google.CalendarIndex) error {
	calendarID := strings.TrimSpace(cal.ID)
	if calendarID == "" || index == nil {
		return nil
	}
	pageToken := ""
	rows := make([]google.CalendarIndexEvent, 0)
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListCalendarEventsWithService(service, calendarID, pageToken, "")
		if err != nil {
			return err
		}
		for _, ev := range page.RawEvents {
			if !google.EventShouldBackup(ev) {
				continue
			}
			key := google.CalendarObjectKey(task.LoginId, calendarID, cal.Summary, ev.Id, ev.Summary, ev.Created)
			row, ok := google.IndexEventFrom(calendarID, key, ev)
			if !ok {
				continue
			}
			rows = append(rows, row)
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	index.ReplaceCalendar(calendarID, rows)
	return nil
}

func loadCalendarIndex(ctx context.Context, storx, email string) (google.CalendarIndex, bool) {
	key := google.CalendarIndexObjectKey(email)
	b, err := satellite.DownloadObject(ctx, storx, satellite.ReserveBucket_Calendar, key)
	if err != nil {
		if calendarObjectMissing(err) {
			return google.CalendarIndex{}, true
		}
		logger.Warn(ctx, "calendar index load failed", logger.ErrorField(err))
		return google.CalendarIndex{}, false
	}
	var index google.CalendarIndex
	if err := json.Unmarshal(b, &index); err != nil {
		logger.Warn(ctx, "calendar index parse failed", logger.ErrorField(err))
		return google.CalendarIndex{}, true
	}
	index.Normalize()
	return index, true
}

func calendarObjectMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "nosuchkey") || strings.Contains(msg, "status code: 404")
}

func saveCalendarIndex(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, storx string, index *google.CalendarIndex) error {
	if index == nil {
		return nil
	}
	index.Normalize()
	b, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshal calendar index: %w", err)
	}
	key := google.CalendarIndexObjectKey(task.LoginId)
	return handler.UploadBufferedObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Calendar, key, b, task.UserID, input.StorxRecovery)
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
