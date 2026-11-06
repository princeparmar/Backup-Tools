package crons

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// mailFolderSyncParallelism caps concurrent folder delta walks (shared Graph rate limits).
var mailFolderSyncParallelism = 3

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

	drainMicrosoftWebhooks(input, storx, "outlook")

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
	if err := ensureMicrosoftPlaceholder(ctx, input, storx, satellite.ReserveBucket_Outlook, keyPrefix, "outlook"); err != nil {
		return err
	}

	index, extras, err := loadOutlookMailIndex(ctx, input, storx, keyPrefix)
	if err != nil {
		return err
	}
	run := newOutlookMailRun(input, &satelliteMailStore{input: input, storx: storx}, accessToken, userBase, keyPrefix, index)
	run.duplicateKeys = extras
	// The mailbox size pre-check is disabled for now, like the other Microsoft 365 quota checks.
	// run.precheck = func(ctx context.Context, estimateBytes int64) error {
	// 	return enforceStoragePrecheck(ctx, input, estimateBytes)
	// }
	if err := run.syncAll(ctx, &input.Job.TaskMemory); err != nil {
		return err
	}

	return saveMicrosoftTaskMemory(input)
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
// extras are older copies of the same message id; the run deletes them.
func loadOutlookMailIndex(ctx context.Context, input ProcessorInput, storx, keyPrefix string) (map[string]string, []string, error) {
	keys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_Outlook, keyPrefix+"/", input.Job.UserID, "outlook", "outlook", input.StorxRecovery)
	if err != nil {
		return nil, nil, fmt.Errorf("list backed-up mail: %w", err)
	}
	index, extras := buildOutlookMailIndex(keys)
	return index, extras, nil
}

// buildOutlookMailIndex indexes mail backups by message id. Single-object backups win over
// legacy meta keys for the same id; legacy data keys are reached through their meta key.
// A message id that already has more than one object returns the extra keys so they can be removed.
func buildOutlookMailIndex(keys map[string]bool) (map[string]string, []string) {
	grouped := make(map[string][]string, len(keys))
	for key := range keys {
		if obj, ok := outlook.ParseOutlookMailObjectKey(key); ok {
			id := outlook.MailKeyID(obj.MessageID)
			grouped[id] = append(grouped[id], key)
		}
	}
	index := make(map[string]string, len(grouped))
	var extras []string
	for id, list := range grouped {
		sort.Strings(list)
		keep := list[0]
		for _, key := range list[1:] {
			keep = betterMailBackupKey(keep, key)
		}
		index[id] = keep
		for _, key := range list {
			if key != keep {
				extras = append(extras, key)
			}
		}
	}
	for key := range keys {
		if k, ok := outlook.ParseOutlookMailLegacyKey(key); ok && k.IsMeta {
			if _, exists := index[k.MessageID]; !exists {
				index[k.MessageID] = key
			}
		}
	}
	sort.Strings(extras)
	return index, extras
}

// betterMailBackupKey prefers a real received date over the 1970 fallback, then the later key.
func betterMailBackupKey(a, b string) string {
	if a == "" {
		return b
	}
	af, bf := mailKeyFallbackDate(a), mailKeyFallbackDate(b)
	if af && !bf {
		return b
	}
	if bf && !af {
		return a
	}
	if a < b {
		return b
	}
	return a
}

func mailKeyFallbackDate(key string) bool {
	obj, ok := outlook.ParseOutlookMailObjectKey(key)
	return ok && obj.DatePath == "1970/01/01"
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
	// duplicateKeys are extra objects for an id already in index. collapseDuplicateMail deletes them.
	duplicateKeys []string
	// mailRef and byInternet remember the RFC Message-ID of a backed-up message so a later
	// Graph id for the same email in the same folder replaces the old object.
	mailRef    map[string]mailIDRef
	byInternet map[string]string
	locks      map[string]*sync.Mutex
	// seen and removed collect ids across folders so a move (removed here, added there) is
	// not recorded as a deletion.
	seen    map[string]struct{}
	removed map[string]struct{}
	mu      sync.Mutex
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
	folders = dedupeOutlookFolders(folders)
	r.collapseDuplicateMail(ctx)

	migrating := tm.OutlookMailLayout < repo.OutlookMailLayoutFiles
	savedDeltas := tm.OutlookMailFolderDeltas
	if migrating {
		r.translateLegacyIDs(ctx)
		savedDeltas = nil
	}
	r.writeFolderList(ctx, folders, tm.OutlookMailFolderPaths, migrating)

	type folderOutcome struct {
		folder outlook.MailFolder
		start  string
		link   string
		err    error
	}
	outcomes := make([]folderOutcome, len(folders))
	workers := mailFolderSyncParallelism
	if workers > len(folders) {
		workers = len(folders)
	}
	if workers < 1 {
		workers = 1
	}
	syncCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if syncCtx.Err() != nil {
					outcomes[i] = folderOutcome{folder: folders[i], err: syncCtx.Err()}
					continue
				}
				f := folders[i]
				start := ""
				if saved := strings.TrimSpace(savedDeltas[f.ID]); saved != "" && tm.OutlookMailFolderPaths[f.ID] == f.Path {
					start = saved
				}
				link, err := r.syncFolder(syncCtx, f, start)
				outcomes[i] = folderOutcome{folder: f, start: start, link: link, err: err}
				if err != nil && shouldAbortOnItemError(err) {
					cancel()
				}
			}
		}()
	}
	for i := range folders {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	deltas := make(map[string]string, len(folders))
	paths := make(map[string]string, len(folders))
	allFolders := true
	for _, o := range outcomes {
		f := o.folder
		if o.err != nil {
			if shouldAbortOnItemError(o.err) {
				return wrapStorageAbort("outlook", o.err)
			}
			if f.WellKnownName == "inbox" {
				return fmt.Errorf("outlook mail folder %s: %w", f.Path, o.err)
			}
			allFolders = false
			logger.Warn(ctx, "outlook mail folder sync failed; retrying next run",
				logger.String("folder", f.Path), logger.ErrorField(o.err))
			if o.start != "" {
				deltas[f.ID], paths[f.ID] = o.start, f.Path
			}
			continue
		}
		deltas[f.ID], paths[f.ID] = o.link, f.Path
	}
	// A message moved into a folder that failed would look deleted; wait for a full run.
	if allFolders {
		r.flushRemoved(ctx)
	}

	tm.OutlookMailFolderDeltas = deltas
	tm.OutlookMailFolderPaths = paths
	tm.OutlookMailLayout = repo.OutlookMailLayoutFiles
	r.saveMailIDIndex(ctx)
	return nil
}

// dedupeOutlookFolders keeps one entry per folder id. ListMailFolders already does this;
// a saved folder list from an older run can still repeat an id.
func dedupeOutlookFolders(in []outlook.MailFolder) []outlook.MailFolder {
	seen := make(map[string]struct{}, len(in))
	out := make([]outlook.MailFolder, 0, len(in))
	for _, f := range in {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, f)
	}
	return out
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := r.heartbeat(); err != nil {
			return "", err
		}
		page, err := outlookMailDeltaPageFn(ctx, r.accessToken, requestURL)
		if err != nil {
			return "", err
		}
		for i := range page.Messages {
			if err := ctx.Err(); err != nil {
				return "", err
			}
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
// Like Gmail, a message whose key and changeKey are unchanged is skipped on a baseline and drafts
// are always fetched again. Otherwise the stored object is reused when changeKey matches: moved or
// renamed messages are copied to the new key, and changed read/flag/category state is written in place.
func (r *outlookMailRun) syncMessage(ctx context.Context, f outlook.MailFolder, msg *outlook.OutlookMailDeltaMessage, baseline bool) error {
	rawID := strings.TrimSpace(msg.ID)
	if rawID == "" {
		return nil
	}
	id := outlook.MailKeyID(rawID)
	unlock := r.lockMessage(messageLockToken(f.Path, id, msg))
	defer unlock()
	if msg.IsRemoved {
		r.mu.Lock()
		r.removed[id] = struct{}{}
		r.mu.Unlock()
		return nil
	}
	r.mu.Lock()
	r.seen[id] = struct{}{}
	oldKey, oldID := r.existingMailKey(id, rawID, f.Path, msg.InternetMessageID)
	r.mu.Unlock()

	state := outlook.OutlookMailBackupState{
		FolderID: f.ID, FolderPath: f.Path, WellKnownFolder: f.WellKnownName, ChangeKey: msg.ChangeKey,
	}
	legacy := oldKey != "" && isLegacyOutlookMailKey(oldKey)
	from, subject, conversationID := msg.From, msg.Subject, msg.ConversationID
	if prev, ok := outlook.ParseOutlookMailObjectKey(oldKey); ok {
		from = cmp.Or(from, prev.From)
		subject = cmp.Or(subject, prev.Subject)
		conversationID = cmp.Or(conversationID, prev.ConversationID)
	}
	newKey := r.keyFor(f, msg.ReceivedDateTime, msg.LastModifiedDateTime, from, subject, conversationID, rawID)

	var stored []byte
	if oldKey != "" && !msg.IsDraft {
		src := oldKey
		if legacy {
			src = outlook.OutlookMailLegacyDataKey(oldKey)
		}
		b, err := r.store.download(ctx, src)
		if err == nil {
			storedCK := outlook.MailBackupChangeKey(b)
			// Missing stored changeKey with a delta changeKey means an older backup — refresh once.
			contentMatches := msg.ChangeKey == "" || (storedCK != "" && storedCK == msg.ChangeKey)
			// Baseline with matching key and changeKey: nothing to do.
			if !legacy && oldKey == newKey && baseline && contentMatches {
				r.mu.Lock()
				r.noteInternet(id, f.Path, cmp.Or(strings.TrimSpace(msg.InternetMessageID), outlook.InternetMessageIDOf(b)), newKey)
				r.mu.Unlock()
				return nil
			}
			if contentMatches || legacy {
				stored = b
			}
			// changeKey drifted (or was never stored): re-fetch full content below.
		}
	}

	if stored == nil {
		raw, detail, err := outlookMailFetchFn(ctx, r.accessToken, r.userBase, rawID)
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
				cmp.Or(detail.ConversationID, conversationID), rawID)
			if detail.ChangeKey != "" {
				state.ChangeKey = detail.ChangeKey
				if msg.ChangeKey == "" {
					msg.ChangeKey = detail.ChangeKey
				}
			}
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
	r.mu.Lock()
	if oldID != "" && oldID != id {
		if prev := r.mailRef[oldID]; prev.Internet != "" && r.byInternet[mailInternetKey(prev.Folder, prev.Internet)] == oldID {
			delete(r.byInternet, mailInternetKey(prev.Folder, prev.Internet))
		}
		delete(r.index, oldID)
		delete(r.mailRef, oldID)
	}
	r.index[id] = newKey
	r.noteInternet(id, f.Path, cmp.Or(strings.TrimSpace(msg.InternetMessageID), outlook.InternetMessageIDOf(b)), newKey)
	r.mu.Unlock()
	return nil
}

func (r *outlookMailRun) keyFor(f outlook.MailFolder, received, modified, from, subject, conversationID, id string) string {
	return outlook.OutlookMailObjectKey(r.prefix, f.Path, cmp.Or(received, modified), from, subject, conversationID, id)
}

// mailIDRef is one backed-up message in {prefix}/_mail_index.json.
type mailIDRef struct {
	Internet string `json:"i,omitempty"`
	Folder   string `json:"f,omitempty"`
	Key      string `json:"k,omitempty"`
}

type mailIDIndexFile struct {
	V     int                  `json:"v"`
	Items map[string]mailIDRef `json:"items"`
}

func mailInternetKey(folder, internet string) string {
	return folder + "\x00" + strings.TrimSpace(internet)
}

func messageLockToken(folder, id string, msg *outlook.OutlookMailDeltaMessage) string {
	if msg != nil && !msg.IsRemoved {
		if inet := strings.TrimSpace(msg.InternetMessageID); inet != "" {
			return "inet\x00" + mailInternetKey(folder, inet)
		}
	}
	return "id\x00" + id
}

func (r *outlookMailRun) lockMessage(token string) func() {
	r.mu.Lock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	lk := r.locks[token]
	if lk == nil {
		lk = &sync.Mutex{}
		r.locks[token] = lk
	}
	r.mu.Unlock()
	lk.Lock()
	return lk.Unlock
}

// existingMailKey finds the object for this message: the same Graph id, or the same
// RFC Message-ID already stored in this folder under an older Graph id.
func (r *outlookMailRun) existingMailKey(id, rawID, folder, internet string) (key, oldID string) {
	if key = r.index[id]; key != "" {
		return key, id
	}
	if rawID != id {
		if key = r.index[rawID]; key != "" {
			return key, rawID
		}
	}
	internet = strings.TrimSpace(internet)
	if internet == "" || folder == "" || r.byInternet == nil {
		return "", ""
	}
	other := r.byInternet[mailInternetKey(folder, internet)]
	if other == "" || other == id {
		return "", ""
	}
	if key = r.index[other]; key == "" {
		return "", ""
	}
	return key, other
}

// noteInternet records the RFC Message-ID. Caller holds r.mu.
func (r *outlookMailRun) noteInternet(id, folder, internet, key string) {
	if r.mailRef == nil {
		r.mailRef = map[string]mailIDRef{}
	}
	prev := r.mailRef[id]
	if internet == "" {
		internet = prev.Internet
	}
	if prev.Internet != "" && prev.Folder != "" && (prev.Folder != folder || prev.Internet != internet) {
		if r.byInternet[mailInternetKey(prev.Folder, prev.Internet)] == id {
			delete(r.byInternet, mailInternetKey(prev.Folder, prev.Internet))
		}
	}
	r.mailRef[id] = mailIDRef{Internet: internet, Folder: folder, Key: key}
	if internet != "" && folder != "" {
		if r.byInternet == nil {
			r.byInternet = map[string]string{}
		}
		r.byInternet[mailInternetKey(folder, internet)] = id
	}
}

func (r *outlookMailRun) mailIndexKey() string {
	return strings.TrimSuffix(r.prefix, "/") + "/_mail_index.json"
}

func (r *outlookMailRun) loadMailIDIndex(ctx context.Context) {
	raw, err := r.store.download(ctx, r.mailIndexKey())
	if err != nil || len(raw) == 0 {
		return
	}
	var file mailIDIndexFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return
	}
	r.mailRef = map[string]mailIDRef{}
	r.byInternet = map[string]string{}
	for id, ref := range file.Items {
		id = outlook.MailKeyID(id)
		if r.index[id] == "" || ref.Key != r.index[id] {
			continue
		}
		r.mailRef[id] = ref
		if ref.Internet != "" && ref.Folder != "" {
			r.byInternet[mailInternetKey(ref.Folder, ref.Internet)] = id
		}
	}
}

func (r *outlookMailRun) saveMailIDIndex(ctx context.Context) {
	items := make(map[string]mailIDRef, len(r.index))
	r.mu.Lock()
	for id, key := range r.index {
		if isLegacyOutlookMailKey(key) {
			continue
		}
		ref := r.mailRef[id]
		ref.Key = key
		if ref.Folder == "" {
			if obj, ok := outlook.ParseOutlookMailObjectKey(key); ok {
				ref.Folder = obj.FolderPath
			}
		}
		items[id] = ref
	}
	r.mu.Unlock()
	raw, err := json.Marshal(mailIDIndexFile{V: 1, Items: items})
	if err != nil {
		return
	}
	if err := r.store.upload(ctx, r.mailIndexKey(), raw); err != nil {
		logger.Warn(ctx, "outlook mail: id index not saved", logger.ErrorField(err))
	}
}

// collapseDuplicateMail deletes extra copies of one message. Same Graph id keeps the best key.
// Same RFC Message-ID in the same folder (a later Graph id for the same email) keeps one object.
func (r *outlookMailRun) collapseDuplicateMail(ctx context.Context) {
	for _, key := range r.duplicateKeys {
		r.store.remove(ctx, key)
	}
	r.duplicateKeys = nil
	r.loadMailIDIndex(ctx)

	type item struct {
		id, key, folder string
	}
	groups := map[string][]item{}
	for id, key := range r.index {
		obj, ok := outlook.ParseOutlookMailObjectKey(key)
		if !ok {
			continue
		}
		g := obj.FolderPath + "\x00" + obj.DatePath + "\x00" + obj.From + "\x00" + obj.Subject
		groups[g] = append(groups[g], item{id: id, key: key, folder: obj.FolderPath})
	}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		inetOf := map[string]string{}
		for _, it := range group {
			inet := ""
			if ref, ok := r.mailRef[it.id]; ok && ref.Key == it.key {
				inet = ref.Internet
			}
			if inet == "" {
				raw, err := r.store.download(ctx, it.key)
				if err != nil {
					continue
				}
				inet = outlook.InternetMessageIDOf(raw)
				if inet != "" {
					r.noteInternet(it.id, it.folder, inet, it.key)
				}
			}
			inetOf[it.id] = inet
		}
		kept := map[string]string{}
		for _, it := range group {
			inet := inetOf[it.id]
			if inet == "" {
				continue
			}
			prev, ok := kept[inet]
			if !ok {
				kept[inet] = it.id
				continue
			}
			drop, keep := it.id, prev
			if betterMailBackupKey(r.index[prev], it.key) == it.key {
				drop, keep = prev, it.id
			}
			kept[inet] = keep
			if key := r.index[drop]; key != "" {
				r.store.remove(ctx, key)
			}
			if ref := r.mailRef[drop]; ref.Internet != "" && r.byInternet[mailInternetKey(ref.Folder, ref.Internet)] == drop {
				delete(r.byInternet, mailInternetKey(ref.Folder, ref.Internet))
			}
			delete(r.index, drop)
			delete(r.mailRef, drop)
		}
	}
}

// flushRemoved marks messages deleted from the mailbox (and not moved to another folder this
// run). The backup stays restorable under its last folder.
func (r *outlookMailRun) flushRemoved(ctx context.Context) {
	now := time.Now().UTC().Format(time.RFC3339)
	r.mu.Lock()
	removed := make([]string, 0, len(r.removed))
	for id := range r.removed {
		if _, moved := r.seen[id]; moved {
			continue
		}
		removed = append(removed, id)
	}
	r.mu.Unlock()
	for _, id := range removed {
		r.mu.Lock()
		key := r.index[id]
		r.mu.Unlock()
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
