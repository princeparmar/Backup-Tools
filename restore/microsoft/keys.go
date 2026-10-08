package microsoft

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/restore"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

func msDownloadObject(ctx context.Context, accessGrant, bucket, objectKey string) ([]byte, error) {
	return satellite.DownloadObject(ctx, accessGrant, bucket, objectKey)
}

// msDownloadMetaFollowData loads a meta JSON object and its linked data payload (create-as-new restore).
func msDownloadMetaFollowData(ctx context.Context, accessGrant, bucket, key string) (metaJSON, dataBytes []byte, dataKey string, err error) {
	metaJSON, err = msDownloadObject(ctx, accessGrant, bucket, key)
	if err != nil {
		return nil, nil, "", err
	}
	var meta map[string]interface{}
	_ = json.Unmarshal(metaJSON, &meta)
	if b, _ := metaBool(meta, "removed_from_onedrive", "removed_from_sharepoint", "removed_from_teams", "removed_from_mailbox", "is_folder"); b {
		return metaJSON, nil, "", fmt.Errorf("object skipped (removed or folder)")
	}
	dataKey = metaString(meta, "data_object_key")
	if dataKey == "" && strings.Contains(key, "/meta/") {
		dataKey = strings.Replace(key, "/meta/", "/data/", 1)
		dataKey = strings.TrimSuffix(dataKey, ".json")
	}
	if dataKey == "" {
		return metaJSON, metaJSON, key, nil
	}
	dataBytes, err = msDownloadObject(ctx, accessGrant, bucket, dataKey)
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

func shouldRestoreOutlookTeamsKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) || isOutlookDataKey(objectKey) {
		return false
	}
	if strings.HasSuffix(objectKey, "/_team.json") {
		return false
	}
	return isOutlookMetaKey(objectKey) || strings.Contains(objectKey, "/channels/")
}

func shouldRestoreOutlookGroupsKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) || isOutlookDataKey(objectKey) {
		return false
	}
	if strings.HasSuffix(objectKey, "/_group.json") {
		return false
	}
	return true
}

// RestoreOutlookMailKey restores one mail meta key into its original folder of the backed-up
// mailbox. App-only tokens target the mailbox owner from the key; delegated tokens use /me.
func RestoreOutlookMailKey(ctx context.Context, accessGrant, accessToken string, application bool, objectKey string) error {
	userBase, err := restoreUserBase(application, objectKey)
	if err != nil {
		return err
	}
	return outlook.RestoreMailFromBackup(ctx, accessToken, userBase, objectKey, func(key string) ([]byte, error) {
		return msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_Outlook, key)
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
	userBase, err := restoreUserBase(deps.MicrosoftApplication, objectKey)
	if err != nil {
		return err
	}
	raw, err := msDownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_OutlookCalendar, objectKey)
	if err != nil {
		return err
	}
	calendarID := CalendarIDForEventKey(ctx, deps.AccessGrant, objectKey, &deps.ContainerIDs)
	return outlook.RestoreCalendarEvent(ctx, deps.MicrosoftToken, userBase, calendarID, raw)
}

// RestoreOutlookContactKey restores one backed-up contact as a new contact in its original folder
// of the backed-up user.
func RestoreOutlookContactKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	userBase, err := restoreUserBase(deps.MicrosoftApplication, objectKey)
	if err != nil {
		return err
	}
	raw, err := msDownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_OutlookContacts, objectKey)
	if err != nil {
		return err
	}
	folderID := ContactFolderIDForKey(ctx, deps.AccessGrant, objectKey, &deps.ContainerIDs)
	return outlook.RestoreContact(ctx, deps.MicrosoftToken, userBase, folderID, raw)
}

// CalendarIDForEventKey is the Graph id of the calendar an event was backed up from, read from the
// calendar's _calendar.json; "" (the default calendar) when unknown. cache may be nil.
func CalendarIDForEventKey(ctx context.Context, accessGrant, objectKey string, cache *sync.Map) string {
	return containerID(ctx, accessGrant, satellite.ReserveBucket_OutlookCalendar, path.Dir(objectKey)+"/"+outlook.PIMCalendarMetaName, cache)
}

// ContactFolderIDForKey is the Graph id of the contact folder a contact was backed up from; ""
// for the default Contacts folder or when unknown. cache may be nil.
func ContactFolderIDForKey(ctx context.Context, accessGrant, objectKey string, cache *sync.Map) string {
	dir, inFolder := outlook.ParsePIMContactKey(objectKey)
	if !inFolder {
		return ""
	}
	return containerID(ctx, accessGrant, satellite.ReserveBucket_OutlookContacts, dir+outlook.PIMFolderMetaName, cache)
}

func containerID(ctx context.Context, accessGrant, bucket, metaKey string, cache *sync.Map) string {
	if cache != nil {
		if v, ok := cache.Load(metaKey); ok {
			return v.(string)
		}
	}
	id := ""
	if raw, err := msDownloadObject(ctx, accessGrant, bucket, metaKey); err == nil {
		var meta struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &meta) == nil {
			id = strings.TrimSpace(meta.ID)
		}
	}
	if cache != nil {
		cache.Store(metaKey, id)
	}
	return id
}

func shouldRestoreOutlookOneDriveKey(objectKey string) bool {
	return !restore.ShouldSkipObjectKey(objectKey) && outlook.IsOneDriveRestoreKey(objectKey)
}

// RestoreOutlookOneDriveKey restores one OneDrive backup into its original folder in the backed-up
// user's drive. App-only tokens target the user from the key; delegated tokens use /me.
func RestoreOutlookOneDriveKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	userBase, err := restoreUserBase(deps.MicrosoftApplication, objectKey)
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
	return outlook.RestoreSharePointBackup(ctx, deps.MicrosoftToken, objectKey, src)
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

// RestoreOutlookSharePointKey restores one SharePoint meta key into the recorded drive.
func RestoreOutlookSharePointKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	metaJSON, dataBytes, _, err := msDownloadMetaFollowData(ctx, accessGrant, satellite.ReserveBucket_OutlookSharePoint, objectKey)
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
	if driveID == "" {
		return fmt.Errorf("sharepoint drive_id missing")
	}
	if len(dataBytes) == 0 {
		return fmt.Errorf("sharepoint data missing")
	}
	return outlook.UploadSharePointDriveFile(ctx, accessToken, driveID, name, dataBytes)
}

// RestoreOutlookTeamsKey restores one Teams message meta key as a new channel message.
func RestoreOutlookTeamsKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	metaJSON, dataBytes, _, err := msDownloadMetaFollowData(ctx, accessGrant, satellite.ReserveBucket_OutlookTeams, objectKey)
	if err != nil {
		return err
	}
	var meta outlook.TeamsCronBackupMeta
	_ = json.Unmarshal(metaJSON, &meta)
	if meta.RemovedFromTeams {
		return fmt.Errorf("teams tombstone skipped")
	}
	payload := dataBytes
	if len(payload) == 0 {
		payload = metaJSON
	}
	teamKey, _ := outlook.ParseTeamsIDsFromKey(objectKey)
	var teamSnap *outlook.TeamsTeamSnapshot
	if teamKey != "" {
		if snapRaw, serr := msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_OutlookTeams, teamKey+"/_team.json"); serr == nil {
			teamSnap, _ = outlook.ParseTeamsTeamSnapshot(snapRaw)
		}
	}
	teamID, channelID, err := outlook.ResolveTeamsGraphIDs(meta, teamSnap, objectKey, "", "")
	if err != nil {
		return err
	}
	body := outlook.ExtractTeamsMessageBody(payload)
	return outlook.PostTeamsChannelMessage(ctx, accessToken, teamID, channelID, body)
}

// RestoreOutlookGroupsKey restores groups conversation, calendar, or drive items as new Graph objects.
func RestoreOutlookGroupsKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	groupKey := outlook.GroupKeyFromObjectKey(objectKey)
	var groupSnap *outlook.GroupsGroupSnapshot
	if groupKey != "" {
		if snapRaw, serr := msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_OutlookGroups, groupKey+"/_group.json"); serr == nil {
			groupSnap, _ = outlook.ParseGroupsGroupSnapshot(snapRaw)
		}
	}

	switch {
	case strings.Contains(objectKey, "/conversations/"):
		raw, err := msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_OutlookGroups, objectKey)
		if err != nil {
			return err
		}
		gid, err := outlook.ResolveGroupGraphID(objectKey, raw, groupSnap, "")
		if err != nil {
			return err
		}
		var post struct {
			Topic       string `json:"topic"`
			BodyPreview string `json:"body_preview"`
			Body        string `json:"body"`
		}
		_ = json.Unmarshal(raw, &post)
		topic := strings.TrimSpace(post.Topic)
		body := strings.TrimSpace(post.Body)
		if body == "" {
			body = strings.TrimSpace(post.BodyPreview)
		}
		return outlook.CreateGroupConversationThread(ctx, accessToken, gid, topic, body)
	case strings.Contains(objectKey, "/calendar/"):
		raw, err := msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_OutlookGroups, objectKey)
		if err != nil {
			return err
		}
		gid, err := outlook.ResolveGroupGraphID(objectKey, raw, groupSnap, "")
		if err != nil {
			return err
		}
		ev, err := outlook.ParseRestoreCalendarEvent(raw)
		if err != nil {
			return err
		}
		return outlook.CreateGroupCalendarEvent(ctx, accessToken, gid, ev)
	default:
		metaJSON, dataBytes, _, err := msDownloadMetaFollowData(ctx, accessGrant, satellite.ReserveBucket_OutlookGroups, objectKey)
		if err != nil {
			return err
		}
		var meta outlook.SharePointCronBackupMeta
		_ = json.Unmarshal(metaJSON, &meta)
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = path.Base(objectKey)
		}
		driveID := strings.TrimSpace(meta.DriveID)
		if driveID != "" {
			return outlook.UploadSharePointDriveFile(ctx, accessToken, driveID, name, dataBytes)
		}
		return outlook.UploadDriveFile(ctx, accessToken, "group-restore-"+name, dataBytes)
	}
}
