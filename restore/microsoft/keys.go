package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/restore"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// msDownloadObject reads a backup object (overridden in tests).
var msDownloadObject = satellite.DownloadObject

// errObjectSkipped is a meta object marked removed or folder: there is nothing to restore.
var errObjectSkipped = errors.New("object skipped (removed or folder)")

// DownloadMetaFollowData loads a meta JSON object and its linked data payload.
func DownloadMetaFollowData(ctx context.Context, accessGrant, bucket, key string) (metaJSON, dataBytes []byte, dataKey string, err error) {
	return metaFollowData(key, func(k string) ([]byte, error) { return msDownloadObject(ctx, accessGrant, bucket, k) })
}

func metaFollowData(key string, download func(key string) ([]byte, error)) (metaJSON, dataBytes []byte, dataKey string, err error) {
	metaJSON, err = download(key)
	if err != nil {
		return nil, nil, "", err
	}
	var meta map[string]interface{}
	_ = json.Unmarshal(metaJSON, &meta)
	if b, _ := metaBool(meta, "removed_from_onedrive", "removed_from_sharepoint", "removed_from_teams", "removed_from_mailbox", "is_folder"); b {
		return metaJSON, nil, "", errObjectSkipped
	}
	dataKey = metaString(meta, "data_object_key")
	if dataKey == "" && strings.Contains(key, "/meta/") {
		dataKey = strings.Replace(key, "/meta/", "/data/", 1)
		dataKey = strings.TrimSuffix(dataKey, ".json")
	}
	if dataKey == "" {
		return metaJSON, metaJSON, key, nil
	}
	dataBytes, err = download(dataKey)
	if err != nil {
		return metaJSON, nil, dataKey, err
	}
	return metaJSON, dataBytes, dataKey, nil
}

func metaString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func metaBool(m map[string]interface{}, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if b, ok := v.(bool); ok {
				return b, true
			}
		}
	}
	return false, false
}

func isOutlookMetaKey(objectKey string) bool {
	return strings.Contains(objectKey, "/meta/") && strings.HasSuffix(objectKey, ".json")
}

func isOutlookDataKey(objectKey string) bool {
	return strings.Contains(objectKey, "/data/")
}

func shouldRestoreOutlookMailKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) {
		return false
	}
	if _, ok := outlook.ParseOutlookMailObjectKey(objectKey); ok {
		return true
	}
	// Legacy layout: meta keys only; the message is in the data twin.
	k, ok := outlook.ParseOutlookMailLegacyKey(objectKey)
	return ok && k.IsMeta
}

func shouldRestoreOutlookCalendarKey(objectKey string) bool {
	return !restore.ShouldSkipObjectKey(objectKey) && outlook.IsPIMItemKey(objectKey)
}

func shouldRestoreOutlookContactsKey(objectKey string) bool {
	return !restore.ShouldSkipObjectKey(objectKey) && outlook.IsPIMItemKey(objectKey)
}

func shouldRestoreOutlookDriveMetaKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) || isOutlookDataKey(objectKey) {
		return false
	}
	return isOutlookMetaKey(objectKey)
}

// shouldRestoreOutlookTeamsKey selects channel and chat messages (.teams objects) and old channel
// keys not yet converted. Meetings, calendar events, call records, phone logs, pointers and
// hosted or attachment objects are download only.
func shouldRestoreOutlookTeamsKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) || isOutlookDataKey(objectKey) {
		return false
	}
	if outlook.IsTeamsRestoreKey(objectKey) {
		return true
	}
	k, ok := outlook.ParseTeamsObjectKey(objectKey)
	return ok && k.Kind == outlook.TeamsKindLegacyOther && k.ChannelID != "" &&
		!strings.Contains(objectKey, "/attachments/") && !strings.HasSuffix(objectKey, "/.file_placeholder")
}

// RestoreOutlookMailKey restores one mail meta key into its original folder of the backed-up
// mailbox. App-only tokens target the mailbox owner from the key; delegated tokens use /me.
// A migration writes into the destination account instead of the backed-up user.
func RestoreOutlookMailKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	if deps == nil {
		return fmt.Errorf("restore deps are required")
	}
	userBase, err := restoreWriteUserBase(ctx, deps, objectKey)
	if err != nil {
		return err
	}
	return outlook.RestoreMailFromBackup(ctx, deps.MicrosoftToken, userBase, objectKey, func(key string) ([]byte, error) {
		return msDownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_Outlook, key)
	})
}

// restoreUserBase is the Graph user a mailbox or OneDrive backup restores into.
func restoreUserBase(application bool, objectKey string) (string, error) {
	if !application {
		return outlook.UserBaseURL("", "", "", false)
	}
	_, _, resourceType, resourceID, _, ok := outlook.SplitResourceKey(objectKey)
	if !ok || resourceType != "user" {
		return "", fmt.Errorf("key %q has no user owner", objectKey)
	}
	return outlook.UserBaseURL(resourceID, "", "", true)
}

// RestoreOutlookCalendarKey restores one backed-up event as a new event in its original calendar
// of the backed-up user.
func RestoreOutlookCalendarKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	userBase, err := restoreWriteUserBase(ctx, deps, objectKey)
	if err != nil {
		return err
	}
	raw, err := msDownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_OutlookCalendar, objectKey)
	if err != nil {
		return err
	}
	calendarID := RestoreCalendarID(ctx, deps.AccessGrant, deps.MicrosoftToken, userBase, objectKey, &deps.ContainerIDs)
	if migrationWrite(deps) {
		calendarID = migratedCalendarID(ctx, deps.AccessGrant, deps.MicrosoftToken, userBase, objectKey, &deps.ContainerIDs)
	}
	return outlook.RestoreCalendarEvent(ctx, deps.MicrosoftToken, userBase, calendarID, raw)
}

// RestoreOutlookContactKey restores one backed-up contact as a new contact in its original folder
// of the backed-up user.
func RestoreOutlookContactKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	userBase, err := restoreWriteUserBase(ctx, deps, objectKey)
	if err != nil {
		return err
	}
	raw, err := msDownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_OutlookContacts, objectKey)
	if err != nil {
		return err
	}
	folderID := ContactFolderIDForKey(ctx, deps.AccessGrant, objectKey, &deps.ContainerIDs)
	if migrationWrite(deps) {
		folderID = migratedContactFolderID(ctx, deps.AccessGrant, deps.MicrosoftToken, userBase, objectKey, &deps.ContainerIDs)
	}
	return outlook.RestoreContact(ctx, deps.MicrosoftToken, userBase, folderID, raw)
}

var (
	ensureCalendarFn      = outlook.EnsureCalendar
	ensureContactFolderFn = outlook.EnsureContactFolder
	// calendarCreateMu keeps concurrent restores of one deleted calendar from creating it twice.
	calendarCreateMu      sync.Mutex
	contactFolderCreateMu sync.Mutex
)

func migrationWrite(deps *restore.RestoreDeps) bool {
	return deps != nil && deps.Job != nil && repo.IsRestoreJobMigration(deps.Job)
}

// restoreWriteUserBase is the Graph user the restore writes into. In place, that is the backed-up
// user. A migration with a delegated token uses /me (the destination sign-in). A migration with an
// app-only token uses the destination user found in the connected organization.
func restoreWriteUserBase(ctx context.Context, deps *restore.RestoreDeps, objectKey string) (string, error) {
	if deps == nil || !migrationWrite(deps) || !deps.MicrosoftApplication {
		application := false
		if deps != nil {
			application = deps.MicrosoftApplication
		}
		return restoreUserBase(application, objectKey)
	}
	if deps.Store == nil || deps.Job == nil {
		return "", fmt.Errorf("migration restore has no destination lookup")
	}
	_, user, err := findOrganizationTargetFn(ctx, deps.Store, deps.Job.UserID, deps.Job.TargetEmail)
	if err != nil {
		return "", err
	}
	if user == nil || strings.TrimSpace(user.ObjectID) == "" {
		return "", fmt.Errorf("restore destination %s was not found in a connected organization", deps.Job.TargetEmail)
	}
	return outlook.UserBaseURL(user.ObjectID, "", "", true)
}

// RestoreCalendarID is the Graph id of the calendar an event restores into, read from the
// calendar's _calendar.json: the calendar it was backed up from or, when that calendar was deleted
// from Microsoft, a calendar of the same name, created when missing. "" (the default calendar)
// when unknown. cache may be nil.
func RestoreCalendarID(ctx context.Context, accessGrant, accessToken, userBase, objectKey string, cache *sync.Map) string {
	metaKey := path.Dir(objectKey) + "/" + outlook.PIMCalendarMetaName
	meta := readContainerMeta(ctx, accessGrant, satellite.ReserveBucket_OutlookCalendar, metaKey, cache)
	if meta.RemovedAt == "" || strings.TrimSpace(meta.Name) == "" {
		return meta.ID
	}
	calendarCreateMu.Lock()
	defer calendarCreateMu.Unlock()
	return rememberCreatedID(ctx, cache, "restored-calendar:"+metaKey, metaKey,
		"deleted calendar not recreated; restoring into the default calendar",
		func() (string, error) {
			return ensureCalendarFn(ctx, accessToken, userBase, strings.TrimSpace(meta.Name))
		})
}

// migratedCalendarID is the destination calendar for a restore into another mailbox. The source
// calendar id is never reused. The destination's default calendar is used for a default calendar;
// any other calendar is matched or created by name. "" means the destination default calendar.
func migratedCalendarID(ctx context.Context, accessGrant, accessToken, userBase, objectKey string, cache *sync.Map) string {
	metaKey := path.Dir(objectKey) + "/" + outlook.PIMCalendarMetaName
	meta := readContainerMeta(ctx, accessGrant, satellite.ReserveBucket_OutlookCalendar, metaKey, cache)
	name := strings.TrimSpace(meta.Name)
	if meta.IsDefault || name == "" {
		return ""
	}
	calendarCreateMu.Lock()
	defer calendarCreateMu.Unlock()
	return rememberCreatedID(ctx, cache, "migrated-calendar:"+metaKey, metaKey,
		"destination calendar not created; restoring into the default calendar",
		func() (string, error) { return ensureCalendarFn(ctx, accessToken, userBase, name) })
}

// migratedContactFolderID is the destination contact folder for a restore into another mailbox.
// The source folder id is never reused. "" is the destination's default Contacts folder.
func migratedContactFolderID(ctx context.Context, accessGrant, accessToken, userBase, objectKey string, cache *sync.Map) string {
	dir, inFolder := outlook.ParsePIMContactKey(objectKey)
	if !inFolder {
		return ""
	}
	metaKey := dir + outlook.PIMFolderMetaName
	name := readContainerMeta(ctx, accessGrant, satellite.ReserveBucket_OutlookContacts, metaKey, cache).label()
	if name == "" {
		return ""
	}
	contactFolderCreateMu.Lock()
	defer contactFolderCreateMu.Unlock()
	return rememberCreatedID(ctx, cache, "migrated-folder:"+metaKey, metaKey,
		"destination contact folder not created; restoring into the default folder",
		func() (string, error) { return ensureContactFolderFn(ctx, accessToken, userBase, name) })
}

// rememberCreatedID returns a cached Graph id, or creates it once and remembers it. "" on failure.
func rememberCreatedID(ctx context.Context, cache *sync.Map, cacheKey, metaKey, warn string, create func() (string, error)) string {
	if cache != nil {
		if v, ok := cache.Load(cacheKey); ok {
			return v.(string)
		}
	}
	id, err := create()
	if err != nil {
		logger.Warn(ctx, warn, logger.String("key", metaKey), logger.ErrorField(err))
		return ""
	}
	if cache != nil {
		cache.Store(cacheKey, id)
	}
	return id
}

// ContactFolderIDForKey is the Graph id of the contact folder a contact was backed up from; ""
// for the default Contacts folder or when unknown. cache may be nil.
func ContactFolderIDForKey(ctx context.Context, accessGrant, objectKey string, cache *sync.Map) string {
	dir, inFolder := outlook.ParsePIMContactKey(objectKey)
	if !inFolder {
		return ""
	}
	return readContainerMeta(ctx, accessGrant, satellite.ReserveBucket_OutlookContacts, dir+outlook.PIMFolderMetaName, cache).ID
}

// containerMeta is the stored description of a backed-up calendar or contact folder.
type containerMeta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	IsDefault   bool   `json:"is_default"`
	RemovedAt   string `json:"removed_at"`
}

func (m containerMeta) label() string {
	if name := strings.TrimSpace(m.Name); name != "" {
		return name
	}
	return strings.TrimSpace(m.DisplayName)
}

func readContainerMeta(ctx context.Context, accessGrant, bucket, metaKey string, cache *sync.Map) containerMeta {
	if cache != nil {
		if v, ok := cache.Load(metaKey); ok {
			return v.(containerMeta)
		}
	}
	var meta containerMeta
	if raw, err := msDownloadObject(ctx, accessGrant, bucket, metaKey); err == nil {
		if json.Unmarshal(raw, &meta) != nil {
			meta = containerMeta{}
		}
		meta.ID = strings.TrimSpace(meta.ID)
	}
	if cache != nil {
		cache.Store(metaKey, meta)
	}
	return meta
}

func shouldRestoreOutlookOneDriveKey(objectKey string) bool {
	return !restore.ShouldSkipObjectKey(objectKey) && outlook.IsOneDriveRestoreKey(objectKey)
}

// RestoreOutlookOneDriveKey restores one OneDrive backup into its original folder in the backed-up
// user's drive. App-only tokens target the user from the key; delegated tokens use /me.
func RestoreOutlookOneDriveKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	userBase, err := restoreWriteUserBase(ctx, deps, objectKey)
	if err != nil {
		return err
	}
	src := OneDriveRestoreSource(deps.AccessGrant)
	src.FolderNames = deps.DriveFolderNameMap(func() map[string]string {
		return oneDriveFolderNamesFromSynced(deps, objectKey)
	})
	return outlook.RestoreOneDriveBackup(ctx, deps.MicrosoftToken, userBase, objectKey, src)
}

// OneDriveRestoreSource reads OneDrive backups with a StorX access grant.
func OneDriveRestoreSource(accessGrant string) outlook.OneDriveRestoreSource {
	return DriveRestoreSource(accessGrant, satellite.ReserveBucket_OutlookOneDrive)
}

// DriveRestoreSource reads tree-layout drive backups (OneDrive, SharePoint, group files) from bucket.
func DriveRestoreSource(accessGrant, bucket string) outlook.OneDriveRestoreSource {
	return outlook.OneDriveRestoreSource{
		Stat: func(ctx context.Context, key string) (map[string]string, int64, error) {
			obj, err := satellite.StatObject(ctx, accessGrant, bucket, key)
			if err != nil {
				return nil, 0, err
			}
			return map[string]string(obj.Custom), obj.System.ContentLength, nil
		},
		DownloadTo: func(ctx context.Context, key string, w io.Writer) error {
			return satellite.DownloadObjectTo(ctx, accessGrant, bucket, key, w)
		},
	}
}

// restoreLibraryKey restores a tree-layout SharePoint or group library backup into its library.
func restoreLibraryKey(ctx context.Context, deps *restore.RestoreDeps, bucket, objectType, objectKey string) error {
	src := DriveRestoreSource(deps.AccessGrant, bucket)
	src.FolderNames = deps.DriveFolderNameMap(func() map[string]string {
		return driveFolderNamesFromSynced(deps, bucket, objectType, objectKey)
	})
	if siteID := sharePointDestinationSite(deps); siteID != "" {
		src.DriveRoot = func(ctx context.Context, key string, meta map[string]string) (string, error) {
			return sharePointDestinationRoot(ctx, deps, siteID, key, meta)
		}
	}
	return outlook.RestoreSharePointBackup(ctx, deps.MicrosoftToken, objectKey, src)
}

// sharePointDestinationSite is the Graph site id a SharePoint restore writes into. Empty means
// the files go back to the libraries they were backed up from.
func sharePointDestinationSite(deps *restore.RestoreDeps) string {
	if deps == nil || deps.Job == nil || deps.Job.Method != "outlook_sharepoint" || !repo.IsRestoreJobMigration(deps.Job) {
		return ""
	}
	return strings.TrimSpace(deps.Job.TargetEmail)
}

// sharePointSourceLibrary reports whether the backed-up drive is the site's default library and,
// for any other library, the name to recreate on the destination site.
func sharePointSourceLibrary(deps *restore.RestoreDeps, sourceDriveID, objectKey string) (sourceDefault bool, libraryName string) {
	sourceDefault = true
	if deps != nil && deps.CronJob != nil {
		if st, ok := deps.CronJob.TaskMemory.SharePointLibraries[strings.TrimSpace(sourceDriveID)]; ok {
			sourceDefault = st.Default
			libraryName = st.Name
		}
	}
	if p, ok := outlook.ParseOneDriveObjectKey(objectKey); ok && strings.Contains(p.Prefix, "/"+outlook.SharePointLibrarySection+"/") {
		sourceDefault = false
	}
	return sourceDefault, libraryName
}

func sharePointDestinationRoot(ctx context.Context, deps *restore.RestoreDeps, siteID, objectKey string, meta map[string]string) (string, error) {
	sourceDrive := ""
	if meta != nil {
		sourceDrive = strings.TrimSpace(meta[outlook.OneDriveMetaDriveID])
	}
	driveID, err := sharePointDestinationDriveID(ctx, deps, siteID, sourceDrive, objectKey)
	if err != nil {
		return "", err
	}
	return outlook.DriveRootURLFromDriveID(driveID), nil
}

// sharePointDestinationDriveID is the destination drive for one source library.
func sharePointDestinationDriveID(ctx context.Context, deps *restore.RestoreDeps, siteID, sourceDrive, objectKey string) (string, error) {
	sourceDefault, libraryName := sharePointSourceLibrary(deps, sourceDrive, objectKey)
	return sharePointDestinationDrive(ctx, deps.MicrosoftToken, siteID, sourceDrive, sourceDefault, libraryName, &deps.ContainerIDs)
}

// Graph seams for a SharePoint restore into another site (overridden in tests).
var (
	sharePointDefaultDriveFn  = outlook.SiteDefaultDriveID
	sharePointEnsureLibraryFn = outlook.EnsureSharePointLibrary
)

// sharePointDestinationDrive is the drive on siteID that receives one source library. The source
// site's default library uses the destination site's default library. Any other library is matched
// or created on the destination site by its name.
func sharePointDestinationDrive(ctx context.Context, token, siteID, sourceDriveID string, sourceDefault bool, libraryName string, cache *sync.Map) (string, error) {
	siteID = strings.TrimSpace(siteID)
	if sourceDefault {
		return cachedSharePointDrive(cache, "sp-default:"+siteID, func() (string, error) {
			return sharePointDefaultDriveFn(ctx, token, siteID)
		})
	}
	name := strings.TrimSpace(libraryName)
	if name == "" {
		name = "Restored library"
		if id := strings.TrimSpace(sourceDriveID); id != "" {
			name = "Restored " + id
		}
		if len(name) > 80 {
			name = name[:80]
		}
	}
	return cachedSharePointDrive(cache, "sp-lib:"+siteID+":"+strings.ToLower(name), func() (string, error) {
		return sharePointEnsureLibraryFn(ctx, token, siteID, name)
	})
}

func cachedSharePointDrive(cache *sync.Map, key string, load func() (string, error)) (string, error) {
	if cache != nil {
		if v, ok := cache.Load(key); ok {
			return v.(string), nil
		}
	}
	id, err := load()
	if err != nil {
		return "", err
	}
	if cache != nil && id != "" {
		cache.Store(key, id)
	}
	return id, nil
}

// oneDriveFolderNamesFromSynced maps the folder placeholders backed up for the key's resource.
func oneDriveFolderNamesFromSynced(deps *restore.RestoreDeps, objectKey string) map[string]string {
	return driveFolderNamesFromSynced(deps, satellite.ReserveBucket_OutlookOneDrive, "outlook_onedrive", objectKey)
}

func driveFolderNamesFromSynced(deps *restore.RestoreDeps, bucket, objectType, objectKey string) map[string]string {
	prefix, _, _, _, _, ok := outlook.SplitResourceKey(objectKey)
	if !ok || deps.Store == nil || deps.Store.SyncedObjectRepo == nil || deps.Job == nil {
		return map[string]string{}
	}
	rows, err := deps.Store.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(deps.Job.UserID, bucket, "outlook", objectType)
	if err != nil {
		return map[string]string{}
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.HasPrefix(row.ObjectKey, prefix+"/") {
			keys = append(keys, row.ObjectKey)
		}
	}
	return outlook.OneDriveFolderNames(keys)
}

func shouldRestoreOutlookSharePointKey(objectKey string) bool {
	return !restore.ShouldSkipObjectKey(objectKey) && outlook.IsOneDriveRestoreKey(objectKey)
}

// RestoreOutlookSharePointKey restores one SharePoint meta key into the recorded drive, or into
// the destination site's matching library when the job names one.
func RestoreOutlookSharePointKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	if deps == nil {
		return fmt.Errorf("restore deps are required")
	}
	metaJSON, dataBytes, _, err := DownloadMetaFollowData(ctx, deps.AccessGrant, satellite.ReserveBucket_OutlookSharePoint, objectKey)
	if err != nil {
		return err
	}
	var meta outlook.SharePointCronBackupMeta
	_ = json.Unmarshal(metaJSON, &meta)
	if meta.RemovedFromSharePoint {
		return fmt.Errorf("sharepoint tombstone skipped")
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = path.Base(objectKey)
	}
	driveID := strings.TrimSpace(meta.DriveID)
	if siteID := sharePointDestinationSite(deps); siteID != "" {
		driveID, err = sharePointDestinationDriveID(ctx, deps, siteID, driveID, objectKey)
		if err != nil {
			return err
		}
	}
	if driveID == "" {
		return fmt.Errorf("sharepoint drive_id missing")
	}
	if len(dataBytes) == 0 {
		return fmt.Errorf("sharepoint data missing")
	}
	return outlook.UploadSharePointDriveFile(ctx, deps.MicrosoftToken, driveID, name, dataBytes)
}

// ErrTeamsRestoreNeedsUser refuses Teams restore with an organization (app-only) token: messages
// are reposted as the signed-in user.
var ErrTeamsRestoreNeedsUser = errors.New("teams restore posts messages as a signed-in user; organization connections can't restore Teams messages")

// ErrTeamsRestoreMissingScope refuses a Teams restore whose delegated token can't send messages.
var ErrTeamsRestoreMissingScope = errors.New("teams restore permission missing")

// Outcomes of one Teams restore key.
const (
	TeamsRestored    = "restored"
	TeamsSkipped     = "skipped"
	TeamsUnsupported = "unsupported"
	TeamsFailed      = "failed"
)

// Where a restored message was posted.
const (
	TeamsPlacedPost         = "new_post"
	TeamsPlacedRestoredRoot = "reply_under_restored_root"
	TeamsPlacedOriginalRoot = "reply_under_original_root"
	TeamsPlacedRootMissing  = "new_post_root_missing"
	TeamsPlacedChat         = "chat"
	TeamsPlacedLegacy       = "legacy_post"
)

// TeamsRestoreOutcome is the result of one Teams restore key. OutcomeUnknown marks a failed post
// that Graph may still have created; the same restore never posts that message again.
type TeamsRestoreOutcome struct {
	Key            string `json:"key"`
	Status         string `json:"status"`
	Placement      string `json:"placement,omitempty"`
	Ref            string `json:"ref,omitempty"`
	Detail         string `json:"detail,omitempty"`
	OutcomeUnknown bool   `json:"outcome_unknown,omitempty"`
	repeat         bool
	err            error
}

func (o TeamsRestoreOutcome) failed(err error) TeamsRestoreOutcome {
	o.Status, o.Placement, o.Detail, o.err = TeamsFailed, "", err.Error(), err
	if errors.Is(err, outlook.ErrTeamsPostOutcomeUnknown) {
		o.OutcomeUnknown = true
		if o.Ref != "" {
			o.Detail += "; Graph may have created the message, search Teams for ref " + o.Ref + " before restoring it again"
		} else {
			o.Detail += "; Graph may have created the message, check the channel before restoring it again"
		}
	}
	return o
}

func (o TeamsRestoreOutcome) with(status, detail string) TeamsRestoreOutcome {
	o.Status, o.Detail = status, detail
	return o
}

func (o TeamsRestoreOutcome) placed(placement, detail string) TeamsRestoreOutcome {
	o.Status, o.Placement, o.Detail = TeamsRestored, placement, detail
	return o
}

// TeamsRestoreError is a Teams key that was not restored.
type TeamsRestoreError struct{ Outcome TeamsRestoreOutcome }

func (e *TeamsRestoreError) Error() string { return e.Outcome.Status + ": " + e.Outcome.Detail }
func (e *TeamsRestoreError) Unwrap() error { return e.Outcome.err }

// RestoreErrorCode keeps the restore engine from repeating a post whose outcome is unknown.
func (e *TeamsRestoreError) RestoreErrorCode() string {
	switch {
	case e.Outcome.OutcomeUnknown:
		return restore.ErrorCodeOutcomeUnknown
	case e.Outcome.Status == TeamsSkipped, e.Outcome.Status == TeamsUnsupported:
		return e.Outcome.Status
	}
	return ""
}

// TeamsRestoreSession restores Teams backup objects as new messages for one restore. State holds
// the reposted thread roots and the messages already handled, so later replies go under their
// reposted root and a message is posted at most once per restore; the caller keeps it for the
// whole restore. TenantID, when set, is the only tenant whose keys are restored.
type TeamsRestoreSession struct {
	AccessGrant       string
	Token             string
	Application       bool
	TenantID          string
	OverrideTeamID    string
	OverrideChannelID string
	State             *sync.Map
	Download          func(ctx context.Context, key string) ([]byte, error)
}

func (s *TeamsRestoreSession) download(ctx context.Context, key string) ([]byte, error) {
	if s.Download != nil {
		return s.Download(ctx, key)
	}
	return teamsDownloadFn(ctx, s.AccessGrant, satellite.ReserveBucket_OutlookTeams, key)
}

// Vault reads and Graph writes of the Teams restore (replaced in tests).
var (
	teamsDownloadFn    = msDownloadObject
	teamsPostChannelFn = outlook.PostTeamsChannelMessageID
	teamsPostReplyFn   = outlook.PostTeamsChannelReply
	teamsPostChatFn    = outlook.PostTeamsChatMessage
)

func teamsRootKey(k outlook.TeamsObjectKey, rootID string) string {
	return "teams-root|" + k.Prefix + "|" + k.ChannelID + "|" + rootID
}

func teamsMessageItem(k outlook.TeamsObjectKey) string {
	return "channel|" + k.Prefix + "|" + k.ChannelID + "|" + k.MessageID
}

func isTeamsGraphStatus(err error, status int) bool {
	var ge *outlook.TeamsGraphError
	return errors.As(err, &ge) && ge.Status == status
}

// OrderTeamsRestoreKeys puts channel thread roots first, keeping the order of the rest, so a reply
// restored together with its root goes under the reposted root.
func OrderTeamsRestoreKeys(keys []string) []string {
	roots := make([]string, 0, len(keys))
	var rest []string
	for _, key := range keys {
		if k, ok := outlook.ParseTeamsObjectKey(key); ok && k.Kind == outlook.TeamsKindChannelMessage && (k.RootID == "" || k.RootID == k.MessageID) {
			roots = append(roots, key)
		} else {
			rest = append(rest, key)
		}
	}
	return append(roots, rest...)
}

// Restore restores one key for the restore engine: nil when the message was posted, or was
// already posted earlier in this restore; a *TeamsRestoreError otherwise.
func (s *TeamsRestoreSession) Restore(ctx context.Context, objectKey string) error {
	o := s.RestoreItem(ctx, objectKey)
	if o.Status == TeamsRestored || o.repeat {
		return nil
	}
	return &TeamsRestoreError{Outcome: o}
}

// RestoreItem reposts one object as the signed-in user: a channel post (a reply goes under the
// root reposted in this restore, else the original root, else a new post with an "In reply to"
// line) or a chat message (into its existing chat). Each repost starts with a note naming the
// original sender, time and ref; original ids, authors, times, reactions and hosted content are
// not recreated.
func (s *TeamsRestoreSession) RestoreItem(ctx context.Context, objectKey string) TeamsRestoreOutcome {
	out := TeamsRestoreOutcome{Key: objectKey, Ref: outlook.TeamsRestoreRef(objectKey)}
	if s.Application || microsoftTokenIsAppOnly(s.Token) {
		return out.failed(ErrTeamsRestoreNeedsUser)
	}
	if s.State == nil {
		s.State = &sync.Map{}
	}
	k, ok := outlook.ParseTeamsObjectKey(objectKey)
	if !ok {
		return out.failed(errors.New("not a Teams backup key"))
	}
	if s.TenantID != "" && !strings.EqualFold(k.TenantID, s.TenantID) {
		return out.failed(fmt.Errorf("key belongs to tenant %s, not the restore's tenant %s", k.TenantID, s.TenantID))
	}
	switch k.Kind {
	case outlook.TeamsKindChannelMessage:
		return s.once(ctx, out, teamsMessageItem(k), func() TeamsRestoreOutcome { return s.restoreChannelMessage(ctx, k, out) })
	case outlook.TeamsKindChatMessage:
		return s.once(ctx, out, "chat|"+k.Prefix+"|"+k.MessageID, func() TeamsRestoreOutcome { return s.restoreChatMessage(ctx, k, out) })
	case outlook.TeamsKindLegacyMeta, outlook.TeamsKindLegacyOther:
		if k.ResourceType == outlook.TeamsKeyTeam && k.ChannelID != "" {
			item := "key|" + objectKey
			if k.MessageID != "" {
				item = teamsMessageItem(k)
			}
			out.Ref = ""
			return s.once(ctx, out, item, func() TeamsRestoreOutcome { return s.restoreLegacyKey(ctx, objectKey, out) })
		}
	}
	return out.with(TeamsUnsupported, fmt.Sprintf("teams %s backups are download only", strings.ReplaceAll(k.Kind, "_", " ")))
}

type teamsItem struct {
	done    chan struct{}
	outcome TeamsRestoreOutcome
}

// once restores one message at most once per restore. A definite failure releases the message so
// a later attempt can post it; a post whose outcome is unknown is never repeated.
func (s *TeamsRestoreSession) once(ctx context.Context, out TeamsRestoreOutcome, item string, restoreFn func() TeamsRestoreOutcome) TeamsRestoreOutcome {
	id := "teams-item|" + item
	for {
		it := &teamsItem{done: make(chan struct{})}
		v, loaded := s.State.LoadOrStore(id, it)
		if !loaded {
			it.outcome = restoreFn()
			if it.outcome.Status == TeamsFailed && !it.outcome.OutcomeUnknown {
				s.State.Delete(id)
			}
			close(it.done)
			return it.outcome
		}
		prev := v.(*teamsItem)
		select {
		case <-prev.done:
		case <-ctx.Done():
			return out.failed(ctx.Err())
		}
		switch {
		case prev.outcome.Status == TeamsFailed && !prev.outcome.OutcomeUnknown:
			continue
		case prev.outcome.Status == TeamsRestored:
			o := out.with(TeamsSkipped, "already restored earlier in this restore (key "+prev.outcome.Key+")")
			o.Placement, o.repeat = prev.outcome.Placement, true
			return o
		case prev.outcome.OutcomeUnknown:
			o := out.with(TeamsFailed, "an earlier post of this message in this restore may have been created (ref "+prev.outcome.Ref+"); it is not posted again")
			o.OutcomeUnknown, o.err = true, prev.outcome.err
			return o
		}
		o := prev.outcome
		o.Key, o.Ref = out.Key, out.Ref
		return o
	}
}

// teamsRestoreMessage is what the restore checks in a stored message before posting it.
type teamsRestoreMessage struct {
	teamID, chatID       string
	messageType, deleted string
	hasContent           bool
}

// readTeamsRestoreMessage checks that a stored message belongs to its key (tenant, message, team
// and channel, or chat) and returns its Graph team or chat id: key folders are sanitized, so the
// ids come from the object and must map back to the key.
func readTeamsRestoreMessage(raw []byte, k outlook.TeamsObjectKey) (teamsRestoreMessage, error) {
	var m struct {
		ID              string `json:"id"`
		MessageType     string `json:"messageType"`
		DeletedDateTime string `json:"deletedDateTime"`
		ChatID          string `json:"chatId"`
		Body            *struct {
			Content string `json:"content"`
		} `json:"body"`
		Attachments     []json.RawMessage `json:"attachments"`
		ChannelIdentity *struct {
			TeamID    string `json:"teamId"`
			ChannelID string `json:"channelId"`
		} `json:"channelIdentity"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return teamsRestoreMessage{}, fmt.Errorf("backup object is not a Teams message: %w", err)
	}
	b, _ := outlook.ReadTeamsBackup(raw)
	if t := strings.TrimSpace(b.TenantID); t != "" && !strings.EqualFold(t, k.TenantID) {
		return teamsRestoreMessage{}, fmt.Errorf("backup object belongs to tenant %s, not the key's tenant %s", t, k.TenantID)
	}
	if id := strings.TrimSpace(m.ID); id != "" && id != k.MessageID {
		return teamsRestoreMessage{}, fmt.Errorf("backup object is message %s, not the key's message %s", id, k.MessageID)
	}
	out := teamsRestoreMessage{
		messageType: strings.TrimSpace(m.MessageType),
		deleted:     strings.TrimSpace(m.DeletedDateTime),
		hasContent:  (m.Body != nil && strings.TrimSpace(m.Body.Content) != "") || len(m.Attachments) > 0,
	}
	if k.Kind == outlook.TeamsKindChatMessage {
		out.chatID = teamsFirst(b.ChatID, m.ChatID)
		if out.chatID == "" {
			return teamsRestoreMessage{}, errors.New("chat id missing in the backup object")
		}
		if outlook.TeamsChatPrefix(k.TenantID, out.chatID) != k.Prefix {
			return teamsRestoreMessage{}, fmt.Errorf("backup object is in chat %s, not the key's chat", out.chatID)
		}
		return out, nil
	}
	team, channel := b.TeamID, b.ChannelID
	if m.ChannelIdentity != nil {
		team, channel = teamsFirst(team, m.ChannelIdentity.TeamID), teamsFirst(channel, m.ChannelIdentity.ChannelID)
	}
	out.teamID = k.ResourceID
	if team = strings.TrimSpace(team); team != "" {
		if outlook.TeamsTeamPrefix(k.TenantID, team) != k.Prefix {
			return teamsRestoreMessage{}, fmt.Errorf("backup object is in team %s, not the key's team", team)
		}
		out.teamID = team
	}
	if channel = strings.TrimSpace(channel); channel != "" && channel != k.ChannelID {
		return teamsRestoreMessage{}, fmt.Errorf("backup object is in channel %s, not the key's channel %s", channel, k.ChannelID)
	}
	return out, nil
}

func teamsFirst(a, b string) string {
	if a = strings.TrimSpace(a); a != "" {
		return a
	}
	return strings.TrimSpace(b)
}

// restorable is empty when the message can be reposted, else its outcome and the reason.
func (m teamsRestoreMessage) restorable() (status, detail string) {
	if m.messageType != "" && !strings.EqualFold(m.messageType, "message") {
		return TeamsUnsupported, fmt.Sprintf("Teams %s messages are system events and can't be reposted", m.messageType)
	}
	if m.deleted != "" && !m.hasContent {
		return TeamsSkipped, "the message was deleted before it was backed up; there is no content to restore"
	}
	return "", ""
}

func (s *TeamsRestoreSession) restoreChannelMessage(ctx context.Context, k outlook.TeamsObjectKey, out TeamsRestoreOutcome) TeamsRestoreOutcome {
	raw, err := s.download(ctx, out.Key)
	if err != nil {
		return out.failed(fmt.Errorf("download backup object: %w", err))
	}
	m, err := readTeamsRestoreMessage(raw, k)
	if err != nil {
		return out.failed(err)
	}
	if status, detail := m.restorable(); status != "" {
		return out.with(status, detail)
	}
	teamID, channelID := m.teamID, k.ChannelID
	if s.OverrideTeamID != "" {
		teamID = s.OverrideTeamID
	}
	if s.OverrideChannelID != "" {
		channelID = s.OverrideChannelID
	}
	body := outlook.TeamsRepostBody(raw, out.Key, "")
	if k.RootID == "" || k.RootID == k.MessageID {
		id, err := teamsPostChannelFn(ctx, s.Token, teamID, channelID, body)
		if err != nil {
			return out.failed(err)
		}
		if id != "" {
			s.State.Store(teamsRootKey(k, k.MessageID), id)
		}
		return out.placed(TeamsPlacedPost, "")
	}
	if v, ok := s.State.Load(teamsRootKey(k, k.RootID)); ok {
		err := teamsPostReplyFn(ctx, s.Token, teamID, channelID, v.(string), body)
		if err == nil {
			return out.placed(TeamsPlacedRestoredRoot, "")
		}
		if !isTeamsGraphStatus(err, 404) {
			return out.failed(err)
		}
	}
	if teamID == m.teamID && channelID == k.ChannelID {
		err := teamsPostReplyFn(ctx, s.Token, teamID, channelID, k.RootID, body)
		if err == nil {
			return out.placed(TeamsPlacedOriginalRoot, "the thread root was not reposted in this restore; the reply was added to the original thread")
		}
		if !isTeamsGraphStatus(err, 404) {
			return out.failed(err)
		}
	}
	if _, err := teamsPostChannelFn(ctx, s.Token, teamID, channelID, outlook.TeamsRepostBody(raw, out.Key, k.RootID)); err != nil {
		return out.failed(err)
	}
	return out.placed(TeamsPlacedRootMissing, "the thread root "+k.RootID+" is not in the destination channel; the reply was posted as a new message with an \"In reply to\" line")
}

func (s *TeamsRestoreSession) restoreChatMessage(ctx context.Context, k outlook.TeamsObjectKey, out TeamsRestoreOutcome) TeamsRestoreOutcome {
	raw, err := s.download(ctx, out.Key)
	if err != nil {
		return out.failed(fmt.Errorf("download backup object: %w", err))
	}
	m, err := readTeamsRestoreMessage(raw, k)
	if err != nil {
		return out.failed(err)
	}
	if status, detail := m.restorable(); status != "" {
		return out.with(status, detail)
	}
	if _, err := teamsPostChatFn(ctx, s.Token, m.chatID, outlook.TeamsRepostBody(raw, out.Key, "")); err != nil {
		if isTeamsGraphStatus(err, 403) || isTeamsGraphStatus(err, 404) {
			err = fmt.Errorf("chat restore needs an existing chat the signed-in user is a member of: %w", err)
		}
		return out.failed(err)
	}
	return out.placed(TeamsPlacedChat, "")
}

// restoreLegacyKey restores one old-layout Teams channel key (meta/data or replies snapshot) as
// new channel messages, as before the tenant layout: the stored body only, without a repost note.
func (s *TeamsRestoreSession) restoreLegacyKey(ctx context.Context, objectKey string, out TeamsRestoreOutcome) TeamsRestoreOutcome {
	switch {
	case strings.Contains(objectKey, "/meetings/"):
		return out.with(TeamsUnsupported, "teams meeting snapshot is metadata-only; restore is not supported")
	case strings.Contains(objectKey, "/attachments/"):
		return out.with(TeamsUnsupported, "teams attachment snapshots are download only; Graph can't add hosted content to a posted message")
	case strings.Contains(objectKey, "/replies/"):
		return s.restoreLegacyReplies(ctx, objectKey, out)
	}
	metaJSON, dataBytes, _, err := metaFollowData(objectKey, func(key string) ([]byte, error) { return s.download(ctx, key) })
	if errors.Is(err, errObjectSkipped) {
		return out.with(TeamsSkipped, "the message was removed from Teams before it was backed up")
	}
	if err != nil {
		return out.failed(err)
	}
	var meta outlook.TeamsCronBackupMeta
	_ = json.Unmarshal(metaJSON, &meta)
	if meta.RemovedFromTeams {
		return out.with(TeamsSkipped, "teams tombstone skipped")
	}
	payload := dataBytes
	if len(payload) == 0 {
		payload = metaJSON
	}
	teamID, channelID, err := outlook.ResolveTeamsGraphIDs(meta, s.legacyTeamSnapshot(ctx, objectKey), objectKey, s.OverrideTeamID, s.OverrideChannelID)
	if err != nil {
		return out.failed(err)
	}
	if _, err := teamsPostChannelFn(ctx, s.Token, teamID, channelID, outlook.ExtractTeamsMessageBody(payload)); err != nil {
		return out.failed(err)
	}
	return out.placed(TeamsPlacedLegacy, "")
}

// restoreLegacyReplies posts the replies of an old replies snapshot under their original parent.
// Replies posted earlier in this restore are not posted again when the snapshot is retried.
func (s *TeamsRestoreSession) restoreLegacyReplies(ctx context.Context, objectKey string, out TeamsRestoreOutcome) TeamsRestoreOutcome {
	raw, err := s.download(ctx, objectKey)
	if err != nil {
		return out.failed(err)
	}
	snap, err := outlook.ParseTeamsReplySnapshot(raw)
	if err != nil {
		return out.failed(err)
	}
	teamID, channelID, err := outlook.ResolveTeamsGraphIDs(outlook.TeamsCronBackupMeta{}, s.legacyTeamSnapshot(ctx, objectKey), objectKey, "", "")
	if err != nil {
		return out.failed(err)
	}
	for i, reply := range snap.Replies {
		id := fmt.Sprintf("teams-legacy-reply|%s|%d", objectKey, i)
		if _, done := s.State.Load(id); done {
			continue
		}
		body := strings.TrimSpace(reply.Subject)
		if body == "" {
			body = strings.TrimSpace(reply.From)
		}
		if body == "" {
			body = "(restored reply)"
		}
		if err := teamsPostReplyFn(ctx, s.Token, teamID, channelID, snap.ParentMessageID, body); err != nil {
			return out.failed(fmt.Errorf("reply %d of %d: %w", i+1, len(snap.Replies), err))
		}
		s.State.Store(id, true)
	}
	return out.placed(TeamsPlacedLegacy, fmt.Sprintf("%d replies posted under the original message", len(snap.Replies)))
}

// legacyTeamSnapshot is the _team.json of an old-layout key, loaded once per restore.
func (s *TeamsRestoreSession) legacyTeamSnapshot(ctx context.Context, objectKey string) *outlook.TeamsTeamSnapshot {
	teamKey, _ := outlook.ParseTeamsIDsFromKey(objectKey)
	if teamKey == "" {
		return nil
	}
	id := "teams-legacy-team|" + teamKey
	if v, ok := s.State.Load(id); ok {
		return v.(*outlook.TeamsTeamSnapshot)
	}
	var snap *outlook.TeamsTeamSnapshot
	if raw, err := s.download(ctx, teamKey+"/_team.json"); err == nil {
		snap, _ = outlook.ParseTeamsTeamSnapshot(raw)
	}
	s.State.Store(id, snap)
	return snap
}
