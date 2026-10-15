package crons

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Graph seam (overridden in tests).
var pimCalendarsFn = outlook.ListPIMCalendars

type outlookCalendarProcessor struct{}

func NewOutlookCalendarProcessor() *outlookCalendarProcessor {
	return &outlookCalendarProcessor{}
}

func (p *outlookCalendarProcessor) Run(input ProcessorInput) error {
	return runOutlookPIMJob(input, "outlook_calendar", satellite.ReserveBucket_OutlookCalendar, google.CalendarMinEstimateBytes, syncOutlookCalendars)
}

// syncOutlookCalendars backs up every calendar of the user. A calendar that cannot be listed is
// skipped; the job fails only when none can be backed up. Backed-up calendars missing from the
// listing are then marked removed.
func syncOutlookCalendars(ctx context.Context, run *pimRun, userBase, keyPrefix string) error {
	calendars, err := pimCalendarsFn(ctx, run.accessToken, userBase)
	if err != nil {
		return err
	}
	collections := make([]pimCollection, 0, len(calendars))
	for _, cal := range calendars {
		dir := outlook.PIMCalendarDir(keyPrefix, cal.ID)
		collections = append(collections, pimCollection{
			name: cal.Name, dir: dir, listURL: outlook.PIMEventsURL(userBase, cal.ID),
			metaKey: dir + outlook.PIMCalendarMetaName, meta: cal,
			userBase: userBase, calendarID: cal.ID,
		})
	}
	if err := run.syncCollections(ctx, collections); err != nil {
		return err
	}
	return run.markRemovedCalendars(ctx, keyPrefix, calendars)
}

// markRemovedCalendars sets removed_at on the _calendar.json of every backed-up calendar missing
// from calendars, a complete listing, and marks its events removed in its index. The backup is
// kept. A listing without the default calendar is not trusted.
func (r *pimRun) markRemovedCalendars(ctx context.Context, keyPrefix string, calendars []outlook.FlatCalendar) error {
	listed, hasDefault := map[string]bool{}, false
	for _, cal := range calendars {
		listed[outlook.PIMCalendarDir(keyPrefix, cal.ID)+outlook.PIMCalendarMetaName] = true
		hasDefault = hasDefault || cal.IsDefault
	}
	if !hasDefault {
		logger.Warn(ctx, "calendar listing has no default calendar; removed calendars not checked")
		return nil
	}
	root := strings.TrimRight(keyPrefix, "/")
	var gone []string
	for key := range r.synced {
		if path.Base(key) == outlook.PIMCalendarMetaName && path.Dir(path.Dir(key)) == root && !listed[key] {
			gone = append(gone, key)
		}
	}
	sort.Strings(gone)
	for _, metaKey := range gone {
		if err := r.markCalendarRemoved(ctx, metaKey); err != nil {
			return err
		}
	}
	return nil
}

func (r *pimRun) markCalendarRemoved(ctx context.Context, metaKey string) error {
	raw, err := r.store.get(ctx, metaKey)
	var cal outlook.FlatCalendar
	if err == nil {
		err = json.Unmarshal(raw, &cal)
	}
	if err != nil {
		logger.Warn(ctx, "removed calendar not marked", logger.String("key", metaKey), logger.ErrorField(err))
		return nil
	}
	if cal.RemovedAt != "" {
		return nil
	}
	removedAt := r.now().UTC().Format(time.RFC3339)
	indexKey := path.Dir(metaKey) + "/" + outlook.PIMIndexName
	index, err := r.loadIndex(ctx, indexKey)
	if err == nil && len(index.Items) > 0 {
		for id, e := range index.Items {
			if e.RemovedAt == "" {
				e.RemovedAt = removedAt
				index.Items[id] = e
				r.removed++
			}
		}
		err = r.saveIndex(ctx, indexKey, index)
	}
	if err != nil {
		if asErrStorageQuota(err) != nil {
			return err
		}
		logger.Warn(ctx, "removed calendar not marked", logger.String("key", metaKey), logger.ErrorField(err))
		return nil
	}
	cal.RemovedAt = removedAt
	logger.Info(ctx, "calendar removed from Microsoft; backup kept", logger.String("key", metaKey))
	return r.putJSON(ctx, metaKey, cal)
}
