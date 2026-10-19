package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Graph seams (overridden in tests).
var (
	pimListPageFn  = outlook.ListPIMPage
	pimSeriesFn    = outlook.AddSeriesOccurrences
	pimInstancesFn = outlook.ListSeriesInstances
)

// Occurrences of a series kept in the calendar index: from a year back to 18 months ahead, read
// again once less than a year ahead is covered.
const (
	seriesInstancesBack  = -1
	seriesInstancesAhead = 18
	seriesInstancesKeep  = 365 * 24 * time.Hour
)

// pimStore is the backup bucket of one calendar or contacts job.
type pimStore interface {
	get(ctx context.Context, key string) ([]byte, error)
	put(ctx context.Context, key string, data []byte) error
}

type satellitePIMStore struct {
	input  ProcessorInput
	storx  string
	bucket string
}

func (s *satellitePIMStore) get(ctx context.Context, key string) ([]byte, error) {
	return satellite.DownloadObject(ctx, s.storx, s.bucket, key)
}

func (s *satellitePIMStore) put(ctx context.Context, key string, data []byte) error {
	return handler.UploadObjectAndSync(ctx, s.input.Database, s.storx, s.bucket, key, data, s.input.Job.UserID, s.input.StorxRecovery)
}

// pimRun backs up Graph events or contacts, one collection (calendar or contact folder) at a time.
type pimRun struct {
	store       pimStore
	accessToken string
	method      string
	// synced holds every backed-up key of the job; an index missing from it starts empty.
	synced    map[string]bool
	heartbeat func() error
	now       func() time.Time

	uploaded, unchanged, removed int
}

// pimCollection is one calendar or contact folder to back up. calendarID is set for calendars:
// their index gets event summaries and their series masters are stored with their occurrences.
type pimCollection struct {
	name, dir, listURL   string
	metaKey              string
	meta                 any
	userBase, calendarID string
}

// runOutlookPIMJob resolves auth, the backed-up user and storage for a calendar or contacts job,
// then runs sync and saves task memory.
func runOutlookPIMJob(input ProcessorInput, method, bucket string, minEstimate int64, sync func(ctx context.Context, run *pimRun, userBase, keyPrefix string) error) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return err
	}
	storx := auth.StorxToken

	drainMicrosoftWebhooks(input, storx, method)

	client, err := microsoftJobClient(auth, jobOutlookMailbox(input.Job))
	if err != nil {
		return err
	}
	mailbox, err := microsoftJobMailbox(auth, input.Job, client)
	if err != nil {
		return err
	}
	userBase, err := client.MailUserBaseURL(mailbox)
	if err != nil {
		return err
	}
	keyPrefix, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}
	// The storage pre-check is disabled for now, like the other Microsoft 365 quota checks.
	// if err := enforceStoragePrecheck(ctx, input, minEstimate); err != nil {
	// 	return err
	// }
	if err := ensureMicrosoftPlaceholder(ctx, input, storx, bucket, keyPrefix, method); err != nil {
		return err
	}
	synced, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, bucket, keyPrefix+"/", input.Job.UserID, "outlook", method, input.StorxRecovery)
	if err != nil {
		return fmt.Errorf("load backed-up %s objects: %w", method, err)
	}

	run := &pimRun{
		store:       &satellitePIMStore{input: input, storx: storx, bucket: bucket},
		accessToken: auth.AccessToken,
		method:      method,
		synced:      synced,
		heartbeat:   input.HeartBeatFunc,
		now:         time.Now,
	}
	if err := sync(ctx, run, userBase, keyPrefix); err != nil {
		return err
	}
	logger.Info(ctx, "outlook backup finished", logger.String("method", method),
		logger.Int("uploaded", run.uploaded), logger.Int("unchanged", run.unchanged), logger.Int("removed", run.removed))

	return saveMicrosoftTaskMemory(input)
}

// syncCollections backs up every collection after writing its descriptor. A collection that
// fails is skipped; the run fails only when all fail or storage is full.
func (r *pimRun) syncCollections(ctx context.Context, collections []pimCollection) error {
	var firstErr error
	failed := 0
	for _, c := range collections {
		if err := r.heartbeat(); err != nil {
			return err
		}
		if c.metaKey != "" {
			if err := r.putJSON(ctx, c.metaKey, c.meta); err != nil {
				return err
			}
		}
		err := r.syncCollection(ctx, c)
		if err == nil {
			continue
		}
		if asErrStorageQuota(err) != nil {
			return err
		}
		if hbErr := r.heartbeat(); hbErr != nil {
			return hbErr
		}
		logger.Warn(ctx, "outlook collection backup failed", logger.String("method", r.method), logger.String("collection", c.name), logger.ErrorField(err))
		failed++
		if firstErr == nil {
			firstErr = err
		}
	}
	if failed > 0 && failed == len(collections) {
		return firstErr
	}
	return nil
}

// syncCollection backs up every item listed from c.listURL under c.dir. Items whose changeKey
// matches the collection index are skipped; items no longer listed are marked removed in the index
// and their backup kept.
func (r *pimRun) syncCollection(ctx context.Context, c pimCollection) error {
	indexKey := c.dir + outlook.PIMIndexName
	index, err := r.loadIndex(ctx, indexKey)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	dirty := false
	for next := c.listURL; next != ""; {
		if err := r.heartbeat(); err != nil {
			return err
		}
		items, nextLink, err := pimListPageFn(ctx, r.accessToken, next)
		if err != nil {
			return err
		}
		for _, it := range items {
			seen[it.ID] = true
			entry, changed, err := r.syncItem(ctx, c, index.Items[it.ID], it)
			if err != nil {
				if dirty {
					r.saveIndex(ctx, indexKey, index)
				}
				return err
			}
			if changed {
				index.Items[it.ID] = entry
				dirty = true
			}
		}
		next = nextLink
	}
	removedAt := r.now().UTC().Format(time.RFC3339)
	for id, e := range index.Items {
		if !seen[id] && e.RemovedAt == "" {
			e.RemovedAt = removedAt
			index.Items[id] = e
			r.removed++
			dirty = true
		}
	}
	if dirty {
		return r.saveIndex(ctx, indexKey, index)
	}
	return nil
}

// syncItem backs up one listed item when it changed and returns its index entry; changed is false
// when the index stays as it is. A series master of a calendar is stored with its changed and
// cancelled occurrences; when those cannot be read it is stored without them and read again next
// run. An item that fails to upload keeps its old entry and is retried next run; err is set only
// when the job must stop.
func (r *pimRun) syncItem(ctx context.Context, c pimCollection, prev outlook.PIMIndexEntry, it outlook.PIMItem) (outlook.PIMIndexEntry, bool, error) {
	key := outlook.PIMItemKey(c.dir, it.ID)
	calendar := c.calendarID != ""
	series := calendar && it.SeriesMaster
	current := prev.Key == key && prev.ChangeKey == it.ChangeKey && prev.RemovedAt == "" && it.ChangeKey != ""
	upload := !current || (series && !prev.Occurrences)
	entry := prev
	if upload {
		raw, withOccurrences := []byte(it.Raw), false
		if series {
			if err := r.heartbeat(); err != nil {
				return prev, false, err
			}
			if full, err := pimSeriesFn(ctx, r.accessToken, outlook.PIMSeriesURL(c.userBase, c.calendarID, it.ID), it.Raw); err != nil {
				logger.Warn(ctx, "outlook series occurrences not read", logger.String("key", key), logger.ErrorField(err))
			} else {
				raw, withOccurrences = full, true
			}
		}
		if err := r.store.put(ctx, key, raw); err != nil {
			if shouldAbortOnItemError(err) {
				return prev, false, wrapStorageAbort(r.method, err)
			}
			logger.Warn(ctx, "outlook item backup failed", logger.String("method", r.method), logger.String("key", key), logger.ErrorField(err))
			return prev, false, nil
		}
		r.synced[key] = true
		r.uploaded++
		entry = outlook.PIMIndexEntry{Key: key, ChangeKey: it.ChangeKey, Occurrences: withOccurrences}
	} else {
		r.unchanged++
	}
	if !calendar || (!upload && !r.summaryStale(prev.Summary, series)) {
		return entry, upload, nil
	}
	summary, err := r.eventSummary(ctx, c, it, series)
	if err != nil {
		return prev, false, err
	}
	entry.Summary = summary
	return entry, true, nil
}

// summaryStale reports an event summary to build again: missing, or for a series without
// occurrences or covering less than seriesInstancesKeep ahead.
func (r *pimRun) summaryStale(s *outlook.EventSummary, series bool) bool {
	if s == nil {
		return true
	}
	if !series {
		return false
	}
	until, err := time.Parse(time.RFC3339, s.Until)
	return err != nil || until.Before(r.now().Add(seriesInstancesKeep))
}

// eventSummary summarizes a listed event; for a series it also reads its occurrences. When those
// cannot be read the summary has none and they are read again next run.
func (r *pimRun) eventSummary(ctx context.Context, c pimCollection, it outlook.PIMItem, series bool) (*outlook.EventSummary, error) {
	s := outlook.EventSummaryOf(it.Raw)
	if !series {
		return s, nil
	}
	if err := r.heartbeat(); err != nil {
		return nil, err
	}
	now := r.now().UTC()
	from, until := now.AddDate(seriesInstancesBack, 0, 0), now.AddDate(0, seriesInstancesAhead, 0)
	instances, err := pimInstancesFn(ctx, r.accessToken, outlook.PIMInstancesURL(c.userBase, c.calendarID, it.ID, from, until))
	if err != nil {
		logger.Warn(ctx, "outlook series occurrences not listed", logger.String("key", outlook.PIMItemKey(c.dir, it.ID)), logger.ErrorField(err))
		return s, nil
	}
	s.Instances, s.From, s.Until = instances, from.Format(time.RFC3339), until.Format(time.RFC3339)
	return s, nil
}

func (r *pimRun) loadIndex(ctx context.Context, key string) (outlook.PIMIndex, error) {
	index := outlook.PIMIndex{Items: map[string]outlook.PIMIndexEntry{}}
	if !r.synced[key] {
		return index, nil
	}
	raw, err := r.store.get(ctx, key)
	if err != nil {
		return index, fmt.Errorf("read backup index %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, &index); err != nil || index.Items == nil {
		logger.Warn(ctx, "outlook backup index unreadable; backing up the collection again", logger.String("key", key))
		index.Items = map[string]outlook.PIMIndexEntry{}
	}
	return index, nil
}

func (r *pimRun) saveIndex(ctx context.Context, key string, index outlook.PIMIndex) error {
	index.V = outlook.PIMIndexVersion
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := r.store.put(ctx, key, raw); err != nil {
		return wrapStorageAbort(r.method, fmt.Errorf("save backup index %s: %w", key, err))
	}
	r.synced[key] = true
	return nil
}

// putJSON writes a small descriptor object (calendar or contact folder).
func (r *pimRun) putJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := r.store.put(ctx, key, raw); err != nil {
		if shouldAbortOnItemError(err) {
			return wrapStorageAbort(r.method, err)
		}
		logger.Warn(ctx, "outlook descriptor backup failed", logger.String("method", r.method), logger.String("key", key), logger.ErrorField(err))
		return nil
	}
	r.synced[key] = true
	return nil
}
