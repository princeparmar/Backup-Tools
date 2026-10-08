package crons

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type outlookProcessor struct{}

func NewOutlookProcessor() *outlookProcessor {
	return &outlookProcessor{}
}

func (p *outlookProcessor) Run(input ProcessorInput) error {
	return runOutlookMailAutosync(input)
}

// Graph seams (overridden in tests).
var (
	outlookMailListFoldersFn = outlook.ListMailFolders
	outlookMailDeltaPageFn   = outlook.FetchOutlookMailMessagesDeltaPage
	outlookMailFetchFn       = outlook.FetchOutlookMailMessageRaw
	outlookMailTranslateFn   = outlook.TranslateMailIDsToImmutable
)

func jobOutlookMailbox(job *repo.CronJobListingDB) string {
	if job == nil {
		return ""
	}
	mailbox := strings.TrimSpace(job.Name)
	if job.InputData != nil && job.InputData.Json() != nil {
		if email, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(email) != "" {
			mailbox = strings.TrimSpace(email)
		}
	}
	return mailbox
}

func runOutlookMailAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return err
	}
	accessToken, storx := auth.AccessToken, auth.StorxToken

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from outlook auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	client, err := microsoftJobClient(auth, jobOutlookMailbox(input.Job))
	if err != nil {
		return fmt.Errorf("create outlook client: %w", err)
	}

	mailbox, err := microsoftJobMailbox(auth, input.Job, client)
	if err != nil {
		return err
	}
	if mailbox == "" {
		return fmt.Errorf("mailbox email is required for outlook backup")
	}

	userBase, err := client.MailUserBaseURL(mailbox)
	if err != nil {
		return err
	}

	keyPrefix, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Outlook, keyPrefix+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("outlook", fmt.Errorf("setup storage placeholder: %w", err))
	}

	index, err := loadOutlookMailIndex(ctx, input, storx, keyPrefix)
	if err != nil {
		return err
	}
	run := newOutlookMailRun(input, &satelliteMailStore{input: input, storx: storx}, accessToken, userBase, keyPrefix, index)
	run.precheck = func(ctx context.Context, estimateBytes int64) error {
		return enforceStoragePrecheck(ctx, input, estimateBytes)
	}
	if err := run.syncAll(ctx, &input.Job.TaskMemory); err != nil {
		return err
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

// outlookMailStore is the backup bucket for one mailbox job.
type outlookMailStore interface {
	download(ctx context.Context, key string) ([]byte, error)
	upload(ctx context.Context, key string, data []byte) error
	remove(ctx context.Context, key string)
}

type satelliteMailStore struct {
	input ProcessorInput
	storx string
}

func (s *satelliteMailStore) download(ctx context.Context, key string) ([]byte, error) {
	return satellite.DownloadObject(ctx, s.storx, satellite.ReserveBucket_Outlook, key)
}

func (s *satelliteMailStore) upload(ctx context.Context, key string, data []byte) error {
	return handler.UploadBufferedObjectAndSync(ctx, s.input.Database, s.storx, satellite.ReserveBucket_Outlook, key, data, s.input.Job.UserID, s.input.StorxRecovery)
}

func (s *satelliteMailStore) remove(ctx context.Context, key string) {
	if err := satellite.DeleteObject(ctx, s.storx, satellite.ReserveBucket_Outlook, key); err != nil {
		logger.Warn(ctx, "outlook mail re-key: failed to delete old object", logger.String("key", key), logger.ErrorField(err))
		return
	}
	_ = s.input.Database.SyncedObjectRepo.DeleteSyncedObject(satellite.ReserveBucket_Outlook, key)
}

// loadOutlookMailIndex maps message id -> current backup key from synced objects.
func loadOutlookMailIndex(ctx context.Context, input ProcessorInput, storx, keyPrefix string) (map[string]string, error) {
	keys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_Outlook, keyPrefix+"/", input.Job.UserID, "outlook", "outlook", input.StorxRecovery)
	if err != nil {
		return nil, fmt.Errorf("list backed-up mail: %w", err)
	}
	return buildOutlookMailIndex(keys), nil
}

// buildOutlookMailIndex indexes mail backups by message id. Single-object backups win over
// legacy meta keys for the same id; legacy data keys are reached through their meta key.
func buildOutlookMailIndex(keys map[string]bool) map[string]string {
	index := make(map[string]string, len(keys))
	for key := range keys {
		if obj, ok := outlook.ParseOutlookMailObjectKey(key); ok {
			index[obj.MessageID] = key
		}
	}
	for key := range keys {
		if k, ok := outlook.ParseOutlookMailLegacyKey(key); ok && k.IsMeta {
			if _, exists := index[k.MessageID]; !exists {
				index[k.MessageID] = key
			}
		}
	}
	return index
}

func isLegacyOutlookMailKey(key string) bool {
	_, ok := outlook.ParseOutlookMailLegacyKey(key)
	return ok
}

// outlookMailRun syncs every folder of one mailbox in one job run.
type outlookMailRun struct {
	input       ProcessorInput
	store       outlookMailStore
	accessToken string
	userBase    string
	prefix      string
	// index maps message id -> backup key; updated as messages are written or re-keyed.
	index map[string]string
	// seen and removed collect ids across folders so a move (removed here, added there) is
	// not recorded as a deletion.
	seen    map[string]struct{}
	removed map[string]struct{}
	// precheck blocks the run when the estimated mailbox size does not fit the remaining
	// storage, like the Gmail backup. nil skips the check.
	precheck func(ctx context.Context, estimateBytes int64) error
}

func newOutlookMailRun(input ProcessorInput, store outlookMailStore, accessToken, userBase, prefix string, index map[string]string) *outlookMailRun {
	if index == nil {
		index = map[string]string{}
	}
	return &outlookMailRun{
		input: input, store: store, accessToken: accessToken, userBase: userBase, prefix: prefix,
		index: index, seen: map[string]struct{}{}, removed: map[string]struct{}{},
	}
}

func (r *outlookMailRun) heartbeat() error {
	if r.input.HeartBeatFunc == nil {
		return nil
	}
	return r.input.HeartBeatFunc()
}

// syncAll runs the per-folder deltas and updates tm. A changed folder path (rename/move) or the
// layout migration re-baselines the folder so its messages are re-keyed.
func (r *outlookMailRun) syncAll(ctx context.Context, tm *repo.TaskMemory) error {
	folders, err := outlookMailListFoldersFn(ctx, r.accessToken, r.userBase)
	if err != nil {
		return fmt.Errorf("list mail folders: %w", err)
	}
	if r.precheck != nil {
		if err := r.precheck(ctx, outlook.EstimateMailBytesFromFolders(folders)); err != nil {
			return err
		}
	}

	migrating := tm.OutlookMailLayout < repo.OutlookMailLayoutFiles
	savedDeltas := tm.OutlookMailFolderDeltas
	if migrating {
		r.translateLegacyIDs(ctx)
		savedDeltas = nil
	}
	r.writeFolderList(ctx, folders, tm.OutlookMailFolderPaths, migrating)

	deltas := make(map[string]string, len(folders))
	paths := make(map[string]string, len(folders))
	allFolders := true
	for _, f := range folders {
		start := ""
		if saved := strings.TrimSpace(savedDeltas[f.ID]); saved != "" && tm.OutlookMailFolderPaths[f.ID] == f.Path {
			start = saved
		}
		link, err := r.syncFolder(ctx, f, start)
		if err != nil {
			if shouldAbortOnItemError(err) {
				return wrapStorageAbort("outlook", err)
			}
			if f.WellKnownName == "inbox" {
				return fmt.Errorf("outlook mail folder %s: %w", f.Path, err)
			}
			allFolders = false
			logger.Warn(ctx, "outlook mail folder sync failed; retrying next run",
				logger.String("folder", f.Path), logger.ErrorField(err))
			if start != "" {
				deltas[f.ID], paths[f.ID] = start, f.Path
			}
			continue
		}
		deltas[f.ID], paths[f.ID] = link, f.Path
	}
	// A message moved into a folder that failed would look deleted; wait for a full run.
	if allFolders {
		r.flushRemoved(ctx)
	}

	tm.OutlookMailFolderDeltas = deltas
	tm.OutlookMailFolderPaths = paths
	tm.OutlookMailLayout = repo.OutlookMailLayoutFiles
	return nil
}

// translateLegacyIDs re-points legacy index entries to immutable ids so the migration baseline
// converts existing backups instead of downloading them again.
func (r *outlookMailRun) translateLegacyIDs(ctx context.Context) {
	var legacy []string
	for id, key := range r.index {
		if isLegacyOutlookMailKey(key) {
			legacy = append(legacy, id)
		}
	}
	if len(legacy) == 0 {
		return
	}
	mapped, err := outlookMailTranslateFn(ctx, r.accessToken, r.userBase, legacy)
	if err != nil {
		logger.Warn(ctx, "outlook mail: legacy message ids not translated; those messages are backed up again",
			logger.Int("translated", len(mapped)), logger.Int("legacy", len(legacy)), logger.ErrorField(err))
	}
	for src, dst := range mapped {
		if dst == src {
			continue
		}
		if key, ok := r.index[src]; ok {
			if _, taken := r.index[dst]; !taken {
				r.index[dst] = key
			}
			delete(r.index, src)
		}
	}
}

// outlookMailFolderList is stored at {prefix}/_folders.json for the vault and restore.
type outlookMailFolderList struct {
	Folders   []outlook.MailFolder `json:"folders"`
	UpdatedAt string               `json:"updated_at"`
}

func (r *outlookMailRun) writeFolderList(ctx context.Context, folders []outlook.MailFolder, prevPaths map[string]string, force bool) {
	changed := force || len(prevPaths) != len(folders)
	for _, f := range folders {
		if prevPaths[f.ID] != f.Path {
			changed = true
		}
	}
	if !changed {
		return
	}
	b, err := json.Marshal(outlookMailFolderList{Folders: folders, UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return
	}
	if err := r.store.upload(ctx, r.prefix+"/_folders.json", b); err != nil {
		logger.Warn(ctx, "outlook mail: folder list not saved", logger.ErrorField(err))
	}
}

func (r *outlookMailRun) syncFolder(ctx context.Context, f outlook.MailFolder, start string) (string, error) {
	initial := outlook.MessagesDeltaURL(r.userBase, f.ID)
	if start == "" {
		start = initial
	}
	link, err := r.deltaSync(ctx, f, start, start == initial)
	if errors.Is(err, outlook.ErrOutlookMailDeltaInvalid) && start != initial {
		logger.Warn(ctx, "outlook mail delta invalid; re-baselining folder", logger.String("folder", f.Path))
		link, err = r.deltaSync(ctx, f, initial, true)
	}
	return link, err
}

// deltaSync walks one delta. baseline is true when it starts from the initial URL: every
// message in the folder is returned, changed or not.
func (r *outlookMailRun) deltaSync(ctx context.Context, f outlook.MailFolder, requestURL string, baseline bool) (string, error) {
	for {
		if err := r.heartbeat(); err != nil {
			return "", err
		}
		page, err := outlookMailDeltaPageFn(ctx, r.accessToken, requestURL)
		if err != nil {
			return "", err
		}
		for i := range page.Messages {
			if err := r.heartbeat(); err != nil {
				return "", err
			}
			if err := r.syncMessage(ctx, f, &page.Messages[i], baseline); err != nil {
				if shouldAbortOnItemError(err) {
					return "", wrapStorageAbort("outlook", err)
				}
				logger.Warn(ctx, "outlook mail message sync failed",
					logger.String("folder", f.Path), logger.String("message_id", page.Messages[i].ID), logger.ErrorField(err))
			}
		}
		if next := strings.TrimSpace(page.NextLink); next != "" {
			requestURL = next
			continue
		}
		final := strings.TrimSpace(page.DeltaLink)
		if final == "" {
			return "", fmt.Errorf("outlook mail delta finished without @odata.deltaLink")
		}
		return final, nil
	}
}

// syncMessage keeps one object per message at {folder}/{date}/{from - subject - conv - id}.
// Like Gmail, a message whose key is unchanged is skipped on a baseline and drafts are always
// fetched again. Otherwise the stored object is reused: moved or renamed messages are copied to
// the new key, and changed read/flag/category state is written in place.
func (r *outlookMailRun) syncMessage(ctx context.Context, f outlook.MailFolder, msg *outlook.OutlookMailDeltaMessage, baseline bool) error {
	id := strings.TrimSpace(msg.ID)
	if id == "" {
		return nil
	}
	if msg.IsRemoved {
		r.removed[id] = struct{}{}
		return nil
	}
	r.seen[id] = struct{}{}

	state := outlook.OutlookMailBackupState{FolderID: f.ID, FolderPath: f.Path, WellKnownFolder: f.WellKnownName}
	oldKey := r.index[id]
	legacy := oldKey != "" && isLegacyOutlookMailKey(oldKey)
	from, subject, conversationID := msg.From, msg.Subject, msg.ConversationID
	if prev, ok := outlook.ParseOutlookMailObjectKey(oldKey); ok {
		from = cmp.Or(from, prev.From)
		subject = cmp.Or(subject, prev.Subject)
		conversationID = cmp.Or(conversationID, prev.ConversationID)
	}
	newKey := r.keyFor(f, msg.ReceivedDateTime, msg.LastModifiedDateTime, from, subject, conversationID, id)

	var stored []byte
	if oldKey != "" && !msg.IsDraft {
		if !legacy && oldKey == newKey && baseline {
			return nil
		}
		src := oldKey
		if legacy {
			src = outlook.OutlookMailLegacyDataKey(oldKey)
		}
		if b, err := r.store.download(ctx, src); err == nil {
			stored = b
		}
	}

	if stored == nil {
		raw, detail, err := outlookMailFetchFn(ctx, r.accessToken, r.userBase, id)
		if err != nil {
			return err
		}
		if detail != nil {
			detailFrom := ""
			if detail.From != nil && detail.From.EmailAddress != nil {
				detailFrom = detail.From.EmailAddress.Address
			}
			newKey = r.keyFor(f, detail.ReceivedDateTime, detail.LastModifiedDateTime,
				cmp.Or(detailFrom, from), cmp.Or(detail.Subject, subject),
				cmp.Or(detail.ConversationID, conversationID), id)
		}
		stored = raw
		state.BackedUpAt = time.Now().UTC().Format(time.RFC3339)
	}

	b, err := outlook.PatchOutlookMailBackup(stored, msg, state)
	if err != nil {
		return err
	}
	if oldKey != newKey || !bytes.Equal(b, stored) {
		if err := r.store.upload(ctx, newKey, b); err != nil {
			return err
		}
	}
	if oldKey != "" && oldKey != newKey {
		r.store.remove(ctx, oldKey)
		if legacy {
			r.store.remove(ctx, outlook.OutlookMailLegacyDataKey(oldKey))
		}
	}
	r.index[id] = newKey
	return nil
}

func (r *outlookMailRun) keyFor(f outlook.MailFolder, received, modified, from, subject, conversationID, id string) string {
	return outlook.OutlookMailObjectKey(r.prefix, f.Path, cmp.Or(received, modified), from, subject, conversationID, id)
}

// flushRemoved marks messages deleted from the mailbox (and not moved to another folder this
// run). The backup stays restorable under its last folder.
func (r *outlookMailRun) flushRemoved(ctx context.Context) {
	now := time.Now().UTC().Format(time.RFC3339)
	for id := range r.removed {
		if _, moved := r.seen[id]; moved {
			continue
		}
		key := r.index[id]
		if key == "" || isLegacyOutlookMailKey(key) {
			continue
		}
		raw, err := r.store.download(ctx, key)
		if err != nil {
			continue
		}
		state := outlook.ReadOutlookMailBackupState(raw)
		if state.RemovedFromMailbox {
			continue
		}
		state.RemovedFromMailbox, state.RemovedAt = true, now
		b, err := outlook.PatchOutlookMailBackup(raw, nil, state)
		if err != nil {
			continue
		}
		if err := r.store.upload(ctx, key, b); err != nil {
			logger.Warn(ctx, "outlook mail: removed flag not saved", logger.String("key", key), logger.ErrorField(err))
		}
	}
}
