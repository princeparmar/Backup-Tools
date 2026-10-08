package crons

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type outlookOneDriveProcessor struct{}

func NewOutlookOneDriveProcessor() *outlookOneDriveProcessor {
	return &outlookOneDriveProcessor{}
}

func (p *outlookOneDriveProcessor) Run(input ProcessorInput) error {
	return runOutlookOneDriveAutosync(input)
}

// Graph seams (overridden in tests).
var (
	oneDriveDeltaPageFn = outlook.FetchOneDriveDeltaPage
	oneDriveRootIDFn    = outlook.FetchOneDriveRootID
	oneDriveItemFn      = outlook.FetchOneDriveItem
	oneDriveContentFn   = outlook.OpenOneDriveItemContentStream
)

func runOutlookOneDriveAutosync(input ProcessorInput) error {
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
			logger.Warn(processCtx, "Failed to process webhook events from onedrive auto-sync", logger.ErrorField(processErr))
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
		return fmt.Errorf("mailbox email is required for onedrive backup")
	}
	driveRoot, err := client.OneDriveDriveRootURL(mailbox)
	if err != nil {
		return err
	}
	keyPrefix, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookOneDrive, keyPrefix+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("outlook_onedrive", fmt.Errorf("setup storage placeholder: %w", err))
	}
	keys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_OutlookOneDrive, keyPrefix+"/", input.Job.UserID, "outlook", "outlook_onedrive", input.StorxRecovery)
	if err != nil {
		return fmt.Errorf("list backed-up onedrive files: %w", err)
	}

	run := newOneDriveRun(input, &satelliteOneDriveStore{input: input, storx: storx}, accessToken, driveRoot, keyPrefix, keys)
	run.quota = newDriveQuotaSession(ctx, input)
	if err := run.syncAll(ctx, &input.Job.TaskMemory); err != nil {
		return err
	}
	if q := run.quota; q != nil && q.skippedQuota > 0 {
		ApplyDriveStorageSkipMessage(input, q.synced, q.skippedQuota, q.skippedBytes, q.remaining)
	}
	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

// oneDriveStored is a backup object's custom metadata and size.
type oneDriveStored struct {
	meta map[string]string
	size int64
}

// oneDriveStore is the backup bucket for one OneDrive job.
type oneDriveStore interface {
	stat(ctx context.Context, key string) (oneDriveStored, error)
	download(ctx context.Context, key string) ([]byte, error)
	put(ctx context.Context, key string, body io.Reader, size int64, meta map[string]string) error
	// copy writes the stored bytes of src to dst with new metadata; src is kept.
	copy(ctx context.Context, src, dst string, size int64, meta map[string]string) error
	remove(ctx context.Context, key string)
}

type satelliteOneDriveStore struct {
	input ProcessorInput
	storx string
	// bucket defaults to the OneDrive bucket; SharePoint libraries use their own.
	bucket string
	// extraMeta is added to every object written (the SharePoint drive id).
	extraMeta map[string]string
}

const oneDriveBucket = satellite.ReserveBucket_OutlookOneDrive

func (s *satelliteOneDriveStore) bucketName() string {
	return cmp.Or(s.bucket, oneDriveBucket)
}

func (s *satelliteOneDriveStore) stat(ctx context.Context, key string) (oneDriveStored, error) {
	obj, err := satellite.StatObject(ctx, s.storx, s.bucketName(), key)
	if err != nil {
		return oneDriveStored{}, err
	}
	return oneDriveStored{meta: map[string]string(obj.Custom), size: obj.System.ContentLength}, nil
}

func (s *satelliteOneDriveStore) download(ctx context.Context, key string) ([]byte, error) {
	return satellite.DownloadObject(ctx, s.storx, s.bucketName(), key)
}

func (s *satelliteOneDriveStore) put(ctx context.Context, key string, body io.Reader, size int64, meta map[string]string) error {
	if len(s.extraMeta) > 0 {
		meta = maps.Clone(meta)
		if meta == nil {
			meta = map[string]string{}
		}
		maps.Copy(meta, s.extraMeta)
	}
	if handler.ShouldUseStreamingUpload(size, meta[outlook.OneDriveMetaMimeType]) {
		return handler.UploadObjectStreamWithMetadataAndSync(ctx, s.input.Database, s.storx, s.bucketName(), key, body, meta, s.input.Job.UserID, s.input.StorxRecovery)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return handler.UploadObjectWithMetadataAndSync(ctx, s.input.Database, s.storx, s.bucketName(), key, data, meta, s.input.Job.UserID, s.input.StorxRecovery)
}

func (s *satelliteOneDriveStore) copy(ctx context.Context, src, dst string, size int64, meta map[string]string) error {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(satellite.DownloadObjectTo(ctx, s.storx, s.bucketName(), src, pw))
	}()
	err := s.put(ctx, dst, pr, size, meta)
	_ = pr.Close()
	return err
}

func (s *satelliteOneDriveStore) remove(ctx context.Context, key string) {
	if err := satellite.DeleteObject(ctx, s.storx, s.bucketName(), key); err != nil {
		logger.Warn(ctx, "onedrive: failed to delete old object", logger.String("key", key), logger.ErrorField(err))
		return
	}
	_ = s.input.Database.SyncedObjectRepo.DeleteSyncedObject(s.bucketName(), key)
	logger.Info(ctx, "Removed backup object", logger.String("bucket", s.bucketName()), logger.String("object_key", key), logger.Int64("job_id", int64(s.input.Job.ID)))
}

// oneDriveFolder is a folder the run knows, either from the delta or Graph (parentID) or only
// from its placeholder key (chain: parent ids from the root).
type oneDriveFolder struct {
	name     string
	parentID string
	chain    []string
	fromKey  bool
}

// oneDriveRun syncs one drive in one job run.
type oneDriveRun struct {
	input     ProcessorInput
	store     oneDriveStore
	token     string
	driveRoot string
	prefix    string
	// service names the job in storage abort errors.
	service string
	quota   *driveQuotaSession
	rootID  string
	// baseline is true when the delta started from the initial URL and listed every item.
	baseline bool
	// index maps item id -> current backup key.
	index map[string]string
	// keys is every backed-up key at the start of the run.
	keys map[string]bool
	// legacy maps item id -> old meta/data meta keys; set only while migrating.
	legacy  map[string][]string
	folders map[string]*oneDriveFolder
	// paths caches folder id -> folder ids from the root down to it.
	paths   map[string][]string
	fetched []*outlook.OneDriveItem
	seen    map[string]bool
	now     string
}

func newOneDriveRun(input ProcessorInput, store oneDriveStore, token, driveRoot, prefix string, keys map[string]bool) *oneDriveRun {
	r := &oneDriveRun{
		input: input, store: store, token: token, driveRoot: driveRoot, prefix: prefix, service: "outlook_onedrive",
		index: map[string]string{}, keys: keys, legacy: map[string][]string{},
		folders: map[string]*oneDriveFolder{}, paths: map[string][]string{}, seen: map[string]bool{},
		now: time.Now().UTC().Format(time.RFC3339),
	}
	if r.keys == nil {
		r.keys = map[string]bool{}
	}
	for key := range r.keys {
		if p, ok := outlook.ParseOneDriveObjectKey(key); ok {
			// A file restored in OneDrive after deletion may still have its BIN copy; MY_DRIVE wins.
			if cur, exists := r.index[p.ItemID]; !exists || oneDriveInBin(cur) {
				r.index[p.ItemID] = key
			}
			if p.IsFolder {
				r.folders[p.ItemID] = &oneDriveFolder{name: p.Name, chain: p.ParentIDs, fromKey: true}
			}
			continue
		}
		if k, ok := outlook.ParseOneDriveLegacyKey(key); ok && k.IsMeta {
			r.legacy[k.ItemID] = append(r.legacy[k.ItemID], key)
		}
	}
	return r
}

func oneDriveInBin(key string) bool {
	p, ok := outlook.ParseOneDriveObjectKey(key)
	return ok && p.Section == outlook.OneDriveSectionBin
}

func (r *oneDriveRun) heartbeat() error {
	if r.input.HeartBeatFunc == nil {
		return nil
	}
	return r.input.HeartBeatFunc()
}

// syncAll runs the OneDrive delta and updates tm.
func (r *oneDriveRun) syncAll(ctx context.Context, tm *repo.TaskMemory) error {
	return r.syncDrive(ctx, &tm.OneDriveDeltaLink, &tm.OneDriveLayout)
}

// syncDrive runs the drive delta from *deltaLink and saves the new cursor and layout. The layout
// migration re-baselines the drive so every legacy meta/data pair is rewritten in the folder tree.
func (r *oneDriveRun) syncDrive(ctx context.Context, deltaLink **string, layout *int) error {
	migrating := *layout < repo.OneDriveLayoutTree
	if !migrating {
		r.legacy = nil
	}
	rootID, err := oneDriveRootIDFn(ctx, r.token, r.driveRoot)
	if err != nil {
		return fmt.Errorf("onedrive root: %w", err)
	}
	r.rootID = rootID

	start := ""
	if !migrating && *deltaLink != nil {
		start = strings.TrimSpace(**deltaLink)
	}
	items, link, err := r.collect(ctx, start)
	if err != nil {
		return err
	}
	if err := r.apply(ctx, items); err != nil {
		return err
	}
	if r.baseline {
		r.sweepUnseen(ctx)
	}
	if migrating {
		r.finishMigration(ctx)
	}
	*deltaLink = &link
	*layout = repo.OneDriveLayoutTree
	return nil
}

// collect reads the whole delta. An unusable saved cursor falls back to a baseline.
func (r *oneDriveRun) collect(ctx context.Context, start string) ([]outlook.OneDriveItem, string, error) {
	initial := outlook.OneDriveInitialDeltaURL(r.driveRoot)
	if start == "" {
		start = initial
	}
	items, link, err := r.readDelta(ctx, start)
	if errors.Is(err, outlook.ErrOneDriveDeltaInvalid) && start != initial {
		logger.Warn(ctx, "onedrive delta invalid; re-baselining", logger.String("prefix", r.prefix))
		start = initial
		items, link, err = r.readDelta(ctx, start)
	}
	r.baseline = start == initial
	return items, link, err
}

func (r *oneDriveRun) readDelta(ctx context.Context, requestURL string) ([]outlook.OneDriveItem, string, error) {
	var items []outlook.OneDriveItem
	for {
		if err := r.heartbeat(); err != nil {
			return nil, "", err
		}
		page, err := oneDriveDeltaPageFn(ctx, r.token, requestURL)
		if err != nil {
			return nil, "", err
		}
		items = append(items, page.Items...)
		if next := strings.TrimSpace(page.NextLink); next != "" {
			requestURL = next
			continue
		}
		final := strings.TrimSpace(page.DeltaLink)
		if final == "" {
			return nil, "", fmt.Errorf("onedrive delta finished without @odata.deltaLink")
		}
		return items, final, nil
	}
}

// apply writes folders (parents first), then files, then deletions. Graph may list an item more
// than once; the last entry is its current state.
func (r *oneDriveRun) apply(ctx context.Context, items []outlook.OneDriveItem) error {
	last := make(map[string]int, len(items))
	for i := range items {
		last[strings.TrimSpace(items[i].ID)] = i
	}
	var folders, files []*outlook.OneDriveItem
	var deleted []string
	for i := range items {
		it := &items[i]
		id := strings.TrimSpace(it.ID)
		if id == "" || last[id] != i {
			continue
		}
		switch {
		case it.IsRoot:
			if r.rootID == "" {
				r.rootID = id
			}
		case it.IsDeleted:
			deleted = append(deleted, id)
		case it.IsFolder:
			r.folders[id] = &oneDriveFolder{name: it.Name, parentID: it.ParentID}
			folders = append(folders, it)
		case it.IsPackage:
			logger.Info(ctx, "onedrive: package item skipped (no downloadable content)",
				logger.String("item_id", id), logger.String("name", it.Name))
		default:
			files = append(files, it)
		}
	}

	depth := make(map[string]int, len(folders))
	for _, f := range folders {
		ids, _ := r.pathTo(ctx, f.ParentID, 0)
		depth[f.ID] = len(ids)
	}
	sort.SliceStable(folders, func(i, j int) bool { return depth[folders[i].ID] < depth[folders[j].ID] })

	for _, f := range folders {
		if err := r.heartbeat(); err != nil {
			return err
		}
		if err := r.syncFolder(ctx, f); err != nil {
			if shouldAbortOnItemError(err) {
				return wrapStorageAbort(r.service, err)
			}
			logger.Warn(ctx, "onedrive folder sync failed", logger.String("item_id", f.ID), logger.ErrorField(err))
		}
	}
	for _, f := range files {
		if err := r.heartbeat(); err != nil {
			return err
		}
		if err := r.syncFile(ctx, f); err != nil {
			if shouldAbortOnItemError(err) {
				return wrapStorageAbort(r.service, err)
			}
			logger.Warn(ctx, "onedrive file sync failed", logger.String("item_id", f.ID), logger.ErrorField(err))
		}
	}
	for _, id := range deleted {
		if !r.seen[id] {
			r.removeItem(ctx, id)
		}
	}
	return nil
}

// pathTo returns the folder ids from the drive root down to and including folderID; nil for
// the root. Folders missing from the run are fetched from Graph and backed up as well.
func (r *oneDriveRun) pathTo(ctx context.Context, folderID string, depth int) ([]string, error) {
	folderID = strings.TrimSpace(folderID)
	if folderID == "" || folderID == r.rootID {
		return nil, nil
	}
	if p, ok := r.paths[folderID]; ok {
		return p, nil
	}
	if depth > 64 {
		return nil, fmt.Errorf("onedrive folder nesting too deep at %s", folderID)
	}
	f := r.folders[folderID]
	if f == nil {
		item, err := oneDriveItemFn(ctx, r.token, r.driveRoot, folderID)
		if err != nil {
			return nil, fmt.Errorf("onedrive parent folder %s: %w", folderID, err)
		}
		if item.IsRoot {
			r.rootID = item.ID
			return nil, nil
		}
		f = &oneDriveFolder{name: item.Name, parentID: item.ParentID}
		r.folders[folderID] = f
		r.fetched = append(r.fetched, item)
	}
	var parent []string
	if f.fromKey {
		parent = f.chain
	} else {
		var err error
		if parent, err = r.pathTo(ctx, f.parentID, depth+1); err != nil {
			return nil, err
		}
	}
	p := append(slices.Clone(parent), folderID)
	r.paths[folderID] = p
	return p, nil
}

func (r *oneDriveRun) folderNames(ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		if f := r.folders[id]; f != nil && strings.TrimSpace(f.name) != "" {
			name = strings.TrimSpace(f.name)
		}
		names = append(names, name)
	}
	return strings.Join(names, "/")
}

// flushFetched backs up parent folders that were looked up in Graph.
func (r *oneDriveRun) flushFetched(ctx context.Context) {
	for len(r.fetched) > 0 {
		item := r.fetched[0]
		r.fetched = r.fetched[1:]
		if _, known := r.index[item.ID]; known {
			continue
		}
		if err := r.syncFolder(ctx, item); err != nil {
			logger.Warn(ctx, "onedrive parent folder backup failed", logger.String("item_id", item.ID), logger.ErrorField(err))
		}
	}
}

// syncFolder keeps a placeholder per folder. A moved folder takes its contents along.
func (r *oneDriveRun) syncFolder(ctx context.Context, item *outlook.OneDriveItem) error {
	ids, err := r.pathTo(ctx, item.ParentID, 0)
	if err != nil {
		return err
	}
	defer r.flushFetched(ctx)
	r.seen[item.ID] = true
	key := outlook.OneDriveFolderKey(r.prefix, ids, item.ID, item.Name)
	old := r.index[item.ID]
	if old == key {
		return nil
	}
	if err := r.store.put(ctx, key, strings.NewReader(""), 0, outlook.OneDriveObjectMeta(item, r.folderNames(ids))); err != nil {
		return err
	}
	r.index[item.ID] = key
	if old == "" {
		return nil
	}
	r.store.remove(ctx, old)
	if prev, ok := outlook.ParseOneDriveObjectKey(old); ok && prev.IsFolder && !slices.Equal(prev.ParentIDs, ids) {
		r.moveContents(ctx,
			outlook.OneDriveFolderContentsPrefix(r.prefix, prev.ParentIDs, item.ID),
			outlook.OneDriveFolderContentsPrefix(r.prefix, ids, item.ID))
	}
	return nil
}

// moveContents re-keys everything under oldPrefix to newPrefix from the stored objects.
func (r *oneDriveRun) moveContents(ctx context.Context, oldPrefix, newPrefix string) {
	for id, key := range r.index {
		rest, inside := strings.CutPrefix(key, oldPrefix)
		if !inside {
			continue
		}
		dst := newPrefix + rest
		st, err := r.store.stat(ctx, key)
		if err != nil {
			logger.Warn(ctx, "onedrive: moved folder item not found", logger.String("key", key), logger.ErrorField(err))
			continue
		}
		if p, ok := outlook.ParseOneDriveObjectKey(dst); ok && p.IsFolder {
			err = r.store.put(ctx, dst, strings.NewReader(""), 0, st.meta)
			if f := r.folders[id]; f != nil && f.fromKey {
				f.chain = p.ParentIDs
			}
		} else {
			err = r.store.copy(ctx, key, dst, st.size, st.meta)
		}
		if err != nil {
			logger.Warn(ctx, "onedrive: moved folder item not re-keyed", logger.String("key", key), logger.ErrorField(err))
			continue
		}
		r.store.remove(ctx, key)
		r.index[id] = dst
	}
	clear(r.paths)
}

// syncFile keeps one object per file at its folder path. Unchanged files are skipped on a
// baseline and checked by cTag otherwise; renamed, moved or undeleted files are copied from
// the stored object when their content is unchanged.
func (r *oneDriveRun) syncFile(ctx context.Context, item *outlook.OneDriveItem) error {
	ids, err := r.pathTo(ctx, item.ParentID, 0)
	if err != nil {
		return err
	}
	r.flushFetched(ctx)
	r.seen[item.ID] = true
	key := outlook.OneDriveFileKey(r.prefix, outlook.OneDriveSectionMyDrive, ids, item.ID, item.Name)
	meta := outlook.OneDriveObjectMeta(item, r.folderNames(ids))
	old := r.index[item.ID]

	if old == key {
		if r.baseline {
			return nil
		}
		if st, err := r.store.stat(ctx, key); err == nil && outlook.OneDriveContentUnchanged(st.meta, item) {
			return nil
		}
		_, err := r.fetch(ctx, item, key, meta)
		return err
	}
	if old != "" {
		if st, err := r.store.stat(ctx, old); err == nil && outlook.OneDriveContentUnchanged(st.meta, item) {
			meta[outlook.OneDriveMetaBackedUpAt] = st.meta[outlook.OneDriveMetaBackedUpAt]
			if err := r.store.copy(ctx, old, key, st.size, meta); err != nil {
				return err
			}
			r.index[item.ID] = key
		} else if stored, err := r.fetch(ctx, item, key, meta); err != nil || !stored {
			return err
		}
		r.store.remove(ctx, old)
		return nil
	}
	if r.migrateLegacy(ctx, item, key, meta) {
		return nil
	}
	_, err = r.fetch(ctx, item, key, meta)
	return err
}

// fetch downloads the file from OneDrive into key. stored is false when the file was skipped
// because it does not fit the remaining storage.
func (r *oneDriveRun) fetch(ctx context.Context, item *outlook.OneDriveItem, key string, meta map[string]string) (stored bool, err error) {
	if r.quota.shouldSkipFile(item.Size) {
		r.quota.recordQuotaSkip(item.Size)
		logger.Warn(ctx, "onedrive file skipped: size exceeds remaining CyberLS storage",
			logger.String("item_id", item.ID), logger.Int64("file_bytes", item.Size))
		return false, nil
	}
	body, _, err := oneDriveContentFn(ctx, r.token, r.driveRoot, item.ID)
	if err != nil {
		return false, err
	}
	defer body.Close()
	meta[outlook.OneDriveMetaBackedUpAt] = r.now
	if err := r.store.put(ctx, key, body, item.Size, meta); err != nil {
		return false, err
	}
	r.quota.accountUpload(item.Size)
	r.index[item.ID] = key
	return true, nil
}

// migrateLegacy copies an unchanged file from its old meta/data backup instead of downloading it.
func (r *oneDriveRun) migrateLegacy(ctx context.Context, item *outlook.OneDriveItem, key string, meta map[string]string) bool {
	for _, metaKey := range r.legacy[item.ID] {
		m, ok := r.legacyMeta(ctx, metaKey)
		if !ok || m.RemovedFromOneDrive || !outlook.OneDriveContentUnchanged(outlook.OneDriveLegacyMetaToObjectMeta(m), item) {
			continue
		}
		dataKey := r.legacyDataKey(metaKey, m)
		if dataKey == "" {
			continue
		}
		meta[outlook.OneDriveMetaBackedUpAt] = cmp.Or(m.UpdatedAt, r.now)
		if err := r.store.copy(ctx, dataKey, key, m.Size, meta); err != nil {
			logger.Warn(ctx, "onedrive: legacy backup not copied; downloading again", logger.String("key", dataKey), logger.ErrorField(err))
			continue
		}
		r.index[item.ID] = key
		return true
	}
	return false
}

func (r *oneDriveRun) legacyMeta(ctx context.Context, metaKey string) (outlook.OneDriveCronBackupMeta, bool) {
	var m outlook.OneDriveCronBackupMeta
	raw, err := r.store.download(ctx, metaKey)
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	return m, true
}

// legacyDataKey is the data object of a legacy meta key when it is in the bucket.
func (r *oneDriveRun) legacyDataKey(metaKey string, m outlook.OneDriveCronBackupMeta) string {
	for _, k := range []string{strings.TrimSpace(m.DataObjectKey), outlook.OneDriveLegacyDataKey(metaKey)} {
		if k != "" && r.keys[k] {
			return k
		}
	}
	return ""
}

// removeItem handles an item deleted from OneDrive: files move to BIN, folder placeholders are
// dropped after their contents moved to BIN.
func (r *oneDriveRun) removeItem(ctx context.Context, id string) {
	key := r.index[id]
	p, ok := outlook.ParseOneDriveObjectKey(key)
	if !ok {
		return
	}
	if p.IsFolder {
		contents := outlook.OneDriveFolderContentsPrefix(r.prefix, p.ParentIDs, p.ItemID)
		var inside []string
		for childID, childKey := range r.index {
			if childID != id && strings.HasPrefix(childKey, contents) {
				inside = append(inside, childID)
			}
		}
		for _, childID := range inside {
			r.removeItem(ctx, childID)
		}
		r.store.remove(ctx, key)
		delete(r.index, id)
		return
	}
	if oneDriveInBin(key) {
		return
	}
	st, err := r.store.stat(ctx, key)
	if err != nil {
		logger.Warn(ctx, "onedrive: deleted file not found in backup", logger.String("key", key), logger.ErrorField(err))
		return
	}
	dst := outlook.OneDriveFileKey(r.prefix, outlook.OneDriveSectionBin, nil, id, cmp.Or(st.meta[outlook.OneDriveMetaName], p.Name))
	if err := r.store.copy(ctx, key, dst, st.size, outlook.OneDriveRemovedMeta(st.meta, r.now)); err != nil {
		logger.Warn(ctx, "onedrive: deleted file not moved to bin", logger.String("key", key), logger.ErrorField(err))
		return
	}
	r.store.remove(ctx, key)
	r.index[id] = dst
}

// sweepUnseen moves backups of items a full baseline no longer lists to BIN.
func (r *oneDriveRun) sweepUnseen(ctx context.Context) {
	var gone []string
	for id, key := range r.index {
		if !r.seen[id] && !oneDriveInBin(key) {
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		if _, still := r.index[id]; still {
			r.removeItem(ctx, id)
		}
	}
}

// finishMigration drops the old meta/data objects. Files backed up in the tree lose their old
// copies (including duplicates left by renames); files no longer in OneDrive move to BIN; meta
// records without data are dropped.
func (r *oneDriveRun) finishMigration(ctx context.Context) {
	for id, metaKeys := range r.legacy {
		if r.seen[id] && r.index[id] != "" {
			r.removeLegacy(ctx, metaKeys)
			continue
		}
		if r.seen[id] {
			continue
		}
		candidates, binned := 0, false
		for _, metaKey := range metaKeys {
			m, ok := r.legacyMeta(ctx, metaKey)
			dataKey := r.legacyDataKey(metaKey, m)
			if !ok || dataKey == "" {
				continue
			}
			candidates++
			meta := outlook.OneDriveRemovedMeta(outlook.OneDriveLegacyMetaToObjectMeta(m), cmp.Or(m.DeletedAt, r.now))
			meta[outlook.OneDriveMetaItemID] = id
			name := cmp.Or(strings.TrimSpace(m.Name), id)
			meta[outlook.OneDriveMetaName] = name
			dst := outlook.OneDriveFileKey(r.prefix, outlook.OneDriveSectionBin, nil, id, name)
			if err := r.store.copy(ctx, dataKey, dst, m.Size, meta); err != nil {
				logger.Warn(ctx, "onedrive: legacy backup of deleted file not moved to bin", logger.String("key", dataKey), logger.ErrorField(err))
				continue
			}
			r.index[id] = dst
			binned = true
			break
		}
		if binned || candidates == 0 {
			r.removeLegacy(ctx, metaKeys)
		}
	}
}

func (r *oneDriveRun) removeLegacy(ctx context.Context, metaKeys []string) {
	for _, metaKey := range metaKeys {
		r.store.remove(ctx, metaKey)
		if dataKey := outlook.OneDriveLegacyDataKey(metaKey); r.keys[dataKey] {
			r.store.remove(ctx, dataKey)
		}
	}
}
