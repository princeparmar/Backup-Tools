package crons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Graph seams (overridden in tests).
var (
	pimListPageFn         = outlook.ListPIMPage
	pimSeriesFn           = outlook.AddSeriesOccurrences
	pimInstancesFn        = outlook.ListSeriesInstances
	pimContactPhotoFn     = outlook.FetchContactPhoto
	pimContactsDeltaFn    = outlook.FetchContactsDeltaPage
	pimContactFetchRawFn  = outlook.FetchOutlookContactRaw
	pimEventAttachmentsFn = outlook.EnsureEventAttachmentBytes
)

const (
	pimCoverageInputKey  = "pim_coverage"
	pimCoverageSampleCap = 10
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

type pimCoverageSample struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// pimCoverage is durable per-run stats written to job input_data (calendar and contacts).
type pimCoverage struct {
	Uploaded          int                 `json:"uploaded"`
	Unchanged         int                 `json:"unchanged"`
	Removed           int                 `json:"removed"`
	FailedCollections int                 `json:"failed_collections"`
	FailedItems       int                 `json:"failed_items"`
	CollectionsTotal  int                 `json:"collections_total"`
	Samples           []pimCoverageSample `json:"samples,omitempty"`
}

func (c *pimCoverage) sample(name, reason string) {
	if c == nil || len(c.Samples) >= pimCoverageSampleCap {
		return
	}
	c.Samples = append(c.Samples, pimCoverageSample{
		Name:   strings.TrimSpace(name),
		Reason: strings.TrimSpace(reason),
	})
}

// pimRun backs up Graph events or contacts, one collection (calendar or contact folder) at a time.
type pimRun struct {
	store       pimStore
	accessToken string
	method      string
	input       ProcessorInput
	// synced holds every backed-up key of the job; an index missing from it starts empty.
	synced    map[string]bool
	heartbeat func() error
	now       func() time.Time
	tm        *repo.TaskMemory
	// fetchPhotos embeds contact photos when backing up contacts (layout gate).
	fetchPhotos bool

	uploaded, unchanged, removed int
	coverage                     pimCoverage
}

// pimCollection is one calendar or contact folder to back up. calendarID is set for calendars:
// their index gets event summaries and their series masters are stored with their occurrences.
// contactFolderID is set for contacts ("" = default Contacts) for photo URLs and delta keys.
// eventRoot / attachmentRoot override Graph paths for M365 group calendars.
type pimCollection struct {
	name, dir, listURL    string
	metaKey               string
	meta                  any
	userBase, calendarID  string
	eventRoot             string // {user}/calendars/{id} or /groups/{id}/calendar
	attachmentRoot        string // {user} or /groups/{id} for attachment GETs
	contactFolderID       string
	useContactsDelta      bool
	contactsDeltaStart    string // saved deltaLink or empty for baseline
	contactsDeltaLink     string // written after a successful walk
	contactsDeltaBaseline bool   // true when starting from the initial delta URL
}

func (c pimCollection) seriesMasterURL(eventID string) string {
	if strings.TrimSpace(c.eventRoot) != "" {
		return outlook.PIMSeriesURLFromRoot(c.eventRoot, eventID)
	}
	return outlook.PIMSeriesURL(c.userBase, c.calendarID, eventID)
}

func (c pimCollection) instancesURL(eventID string, from, until time.Time) string {
	if strings.TrimSpace(c.eventRoot) != "" {
		return outlook.PIMInstancesURLFromRoot(c.eventRoot, eventID, from, until)
	}
	return outlook.PIMInstancesURL(c.userBase, c.calendarID, eventID, from, until)
}

func (c pimCollection) attachBase() string {
	if strings.TrimSpace(c.attachmentRoot) != "" {
		return c.attachmentRoot
	}
	return c.userBase
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
	if err := enforceStoragePrecheck(ctx, input, minEstimate); err != nil {
		return err
	}
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
		input:       input,
		synced:      synced,
		heartbeat:   input.HeartBeatFunc,
		now:         time.Now,
		tm:          &input.Job.TaskMemory,
		fetchPhotos: method == "outlook_contacts",
	}
	if err := sync(ctx, run, userBase, keyPrefix); err != nil {
		run.persistCoverage()
		return err
	}
	run.persistCoverage()
	logger.Info(ctx, "outlook backup finished", logger.String("method", method),
		logger.Int("uploaded", run.uploaded), logger.Int("unchanged", run.unchanged), logger.Int("removed", run.removed),
		logger.Int("failed_collections", run.coverage.FailedCollections), logger.Int("failed_items", run.coverage.FailedItems))

	return saveMicrosoftTaskMemory(input)
}

// persistCoverage writes pim_coverage onto the job input_data for UI/job detail.
func (r *pimRun) persistCoverage() {
	if r == nil || r.input.Job == nil || r.input.Database == nil || r.input.Database.CronJobRepo == nil {
		return
	}
	job := r.input.Job
	input := map[string]interface{}{}
	if job.InputData != nil && job.InputData.Json() != nil {
		for k, v := range *job.InputData.Json() {
			input[k] = v
		}
	}
	cov := r.coverage
	cov.Uploaded, cov.Unchanged, cov.Removed = r.uploaded, r.unchanged, r.removed
	input[pimCoverageInputKey] = cov
	_ = r.input.Database.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"input_data": database.NewDbJsonFromValue(input),
	})
}

// syncCollections backs up every collection after writing its descriptor. A collection that
// fails is skipped; the run fails only when all fail or storage is full. Partial failures are
// recorded on pim_coverage so job detail can show which calendars/folders were skipped.
func (r *pimRun) syncCollections(ctx context.Context, collections []pimCollection) error {
	var firstErr error
	failed := 0
	r.coverage.CollectionsTotal = len(collections)
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
		r.coverage.FailedCollections++
		r.coverage.sample(c.name, "collection_failed")
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
// and their backup kept. Contacts collections may walk contacts/delta instead of a full list.
func (r *pimRun) syncCollection(ctx context.Context, c pimCollection) error {
	if c.useContactsDelta {
		return r.syncContactsDelta(ctx, c)
	}
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

// syncContactsDelta walks one contacts folder via Graph delta. Baseline (initial URL) marks
// contacts missing from the walk as removed; incremental only applies @removed rows.
func (r *pimRun) syncContactsDelta(ctx context.Context, c pimCollection) error {
	indexKey := c.dir + outlook.PIMIndexName
	index, err := r.loadIndex(ctx, indexKey)
	if err != nil {
		return err
	}
	initial := outlook.ContactsDeltaURL(c.userBase, c.contactFolderID)
	start := strings.TrimSpace(c.contactsDeltaStart)
	baseline := c.contactsDeltaBaseline || start == "" || start == initial
	if start == "" {
		start = initial
		baseline = true
	}
	link, dirty, err := r.walkContactsDelta(ctx, c, &index, start, baseline)
	if err != nil && errors.Is(err, outlook.ErrOutlookContactsDeltaInvalid) && start != initial {
		logger.Warn(ctx, "outlook contacts delta invalid; re-baselining folder", logger.String("folder", c.name))
		link, dirty, err = r.walkContactsDelta(ctx, c, &index, initial, true)
	}
	if err != nil {
		return err
	}
	if r.tm != nil {
		if r.tm.OutlookContactFolderDeltas == nil {
			r.tm.OutlookContactFolderDeltas = map[string]string{}
		}
		r.tm.OutlookContactFolderDeltas[c.contactFolderID] = link
	}
	if dirty {
		return r.saveIndex(ctx, indexKey, index)
	}
	return nil
}

func (r *pimRun) walkContactsDelta(ctx context.Context, c pimCollection, index *outlook.PIMIndex, requestURL string, baseline bool) (string, bool, error) {
	seen := map[string]bool{}
	dirty := false
	for {
		if err := r.heartbeat(); err != nil {
			return "", dirty, err
		}
		page, err := pimContactsDeltaFn(ctx, r.accessToken, requestURL)
		if err != nil {
			return "", dirty, err
		}
		for _, it := range page.Items {
			if it.Removed {
				if e, ok := index.Items[it.ID]; ok && e.RemovedAt == "" {
					e.RemovedAt = r.now().UTC().Format(time.RFC3339)
					index.Items[it.ID] = e
					r.removed++
					dirty = true
				}
				continue
			}
			seen[it.ID] = true
			entry, changed, err := r.syncItem(ctx, c, index.Items[it.ID], it)
			if err != nil {
				if dirty {
					_ = r.saveIndex(ctx, c.dir+outlook.PIMIndexName, *index)
				}
				return "", dirty, err
			}
			if changed {
				index.Items[it.ID] = entry
				dirty = true
			}
		}
		if next := strings.TrimSpace(page.NextLink); next != "" {
			requestURL = next
			continue
		}
		final := strings.TrimSpace(page.DeltaLink)
		if final == "" {
			return "", dirty, fmt.Errorf("contacts delta finished without @odata.deltaLink")
		}
		if baseline {
			removedAt := r.now().UTC().Format(time.RFC3339)
			for id, e := range index.Items {
				if !seen[id] && e.RemovedAt == "" {
					e.RemovedAt = removedAt
					index.Items[id] = e
					r.removed++
					dirty = true
				}
			}
		}
		return final, dirty, nil
	}
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
	needFullContact := !calendar && r.fetchPhotos && r.tm != nil && r.tm.OutlookContactsLayout < outlook.OutlookContactsLayoutFull
	needPhoto := !calendar && r.fetchPhotos && r.contactNeedsPhoto(ctx, key, prev)
	current := prev.Key == key && prev.ChangeKey == it.ChangeKey && prev.RemovedAt == "" && it.ChangeKey != "" && !needFullContact && !needPhoto
	upload := !current || (series && !prev.Occurrences)
	entry := prev
	if upload {
		raw, withOccurrences := []byte(it.Raw), false
		if series {
			if err := r.heartbeat(); err != nil {
				return prev, false, err
			}
			if full, err := pimSeriesFn(ctx, r.accessToken, c.seriesMasterURL(it.ID), it.Raw); err != nil {
				logger.Warn(ctx, "outlook series occurrences not read", logger.String("key", key), logger.ErrorField(err))
			} else {
				raw, withOccurrences = full, true
			}
		}
		if calendar && c.attachBase() != "" {
			if filled, attErr := pimEventAttachmentsFn(ctx, r.accessToken, c.attachBase(), it.ID, raw); attErr != nil {
				logger.Warn(ctx, "outlook event attachments not read", logger.String("key", key), logger.ErrorField(attErr))
			} else {
				raw = filled
			}
		}
		if !calendar && c.userBase != "" {
			// Full GET so Anniversary (extended prop) and all Outlook card fields are stored.
			// Never fall back to a sparse delta row — that drops spouse/categories/IM/website/etc.
			full, err := pimContactFetchRawFn(ctx, r.accessToken, c.userBase, c.contactFolderID, it.ID)
			if err != nil {
				logger.Warn(ctx, "outlook contact full fetch failed; skipping until next run",
					logger.String("contact_id", it.ID), logger.ErrorField(err))
				r.coverage.FailedItems++
				r.coverage.sample(key, "contact_fetch_failed")
				return prev, false, nil
			}
			raw = full
			if r.fetchPhotos {
				raw = r.withContactPhoto(ctx, c, it.ID, raw)
			}
		}
		if err := r.store.put(ctx, key, raw); err != nil {
			if shouldAbortOnItemError(err) {
				return prev, false, wrapStorageAbort(r.method, err)
			}
			logger.Warn(ctx, "outlook item backup failed", logger.String("method", r.method), logger.String("key", key), logger.ErrorField(err))
			r.coverage.FailedItems++
			r.coverage.sample(key, "item_failed")
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
	instances, err := pimInstancesFn(ctx, r.accessToken, c.instancesURL(it.ID, from, until))
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

// contactNeedsPhoto is true when the vault object has never recorded a photo fetch.
func (r *pimRun) contactNeedsPhoto(ctx context.Context, key string, prev outlook.PIMIndexEntry) bool {
	if prev.Key != key || prev.RemovedAt != "" || !r.synced[key] {
		return false // new/missing uploads already go through withContactPhoto
	}
	raw, err := r.store.get(ctx, key)
	if err != nil {
		return true
	}
	return !outlook.ContactPhotoChecked(raw)
}

// withContactPhoto embeds the Graph contact photo into raw when present. Failures are warn-only.
// When Graph has no photo (404), marks the field null so we do not re-fetch every run.
func (r *pimRun) withContactPhoto(ctx context.Context, c pimCollection, contactID string, raw []byte) []byte {
	photo, ok, err := pimContactPhotoFn(ctx, r.accessToken, c.userBase, c.contactFolderID, contactID)
	if err != nil {
		logger.Warn(ctx, "outlook contact photo not fetched",
			logger.String("contact_id", contactID), logger.ErrorField(err))
		return raw
	}
	if !ok {
		out, markErr := outlook.MarkContactPhotoChecked(raw)
		if markErr != nil {
			logger.Warn(ctx, "outlook contact photo check not marked",
				logger.String("contact_id", contactID), logger.ErrorField(markErr))
			return raw
		}
		return out
	}
	out, err := outlook.EmbedContactPhoto(raw, photo)
	if err != nil {
		logger.Warn(ctx, "outlook contact photo not embedded",
			logger.String("contact_id", contactID), logger.ErrorField(err))
		return raw
	}
	return out
}

// putJSON writes a small descriptor object (calendar or contact folder). Unchanged JSON is skipped.
func (r *pimRun) putJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if r.synced[key] {
		if prev, getErr := r.store.get(ctx, key); getErr == nil && bytes.Equal(prev, raw) {
			return nil
		}
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
