package microsoft

import (
	"github.com/StorX2-0/Backup-Tools/restore"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

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
	// Prefer meta keys; data is pulled via data_object_key.
	return isOutlookMetaKey(objectKey)
}

func shouldRestoreOutlookCalendarKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) {
		return false
	}
	base := path.Base(objectKey)
	if base == "_calendar.json" || strings.HasSuffix(objectKey, "/_calendar.json") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(objectKey), ".json")
}

func shouldRestoreOutlookContactsKey(objectKey string) bool {
	if restore.ShouldSkipObjectKey(objectKey) {
		return false
	}
	return strings.HasSuffix(strings.ToLower(objectKey), ".json")
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

// RestoreOutlookMailKey restores one mail meta key as a new Graph message.
func RestoreOutlookMailKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	metaJSON, dataBytes, _, err := msDownloadMetaFollowData(ctx, accessGrant, satellite.ReserveBucket_Outlook, objectKey)
	if err != nil {
		return err
	}
	var meta outlook.OutlookMailCronBackupMeta
	_ = json.Unmarshal(metaJSON, &meta)
	if meta.RemovedFromMailbox {
		return fmt.Errorf("mail tombstone skipped")
	}
	payload := dataBytes
	if len(payload) == 0 {
		payload = metaJSON
	}
	var msg outlook.OutlookMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return fmt.Errorf("parse outlook message: %w", err)
	}
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return err
	}
	_, err = client.InsertMessage(&msg)
	return err
}

// RestoreOutlookCalendarKey restores one calendar event JSON as a new Graph event.
func RestoreOutlookCalendarKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	raw, err := msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_OutlookCalendar, objectKey)
	if err != nil {
		return err
	}
	ev, err := outlook.ParseRestoreCalendarEvent(raw)
	if err != nil {
		return err
	}
	return outlook.CreateCalendarEvent(ctx, accessToken, ev)
}

// RestoreOutlookContactKey restores one contact JSON as a new Graph contact.
func RestoreOutlookContactKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	raw, err := msDownloadObject(ctx, accessGrant, satellite.ReserveBucket_OutlookContacts, objectKey)
	if err != nil {
		return err
	}
	contact, err := outlook.ParseRestoreContact(raw)
	if err != nil {
		return err
	}
	return outlook.CreateContact(ctx, accessToken, contact)
}

// RestoreOutlookOneDriveKey restores one OneDrive meta key as a new file in the user's drive root.
func RestoreOutlookOneDriveKey(ctx context.Context, accessGrant, accessToken, objectKey string) error {
	metaJSON, dataBytes, _, err := msDownloadMetaFollowData(ctx, accessGrant, satellite.ReserveBucket_OutlookOneDrive, objectKey)
	if err != nil {
		return err
	}
	var meta outlook.OneDriveCronBackupMeta
	_ = json.Unmarshal(metaJSON, &meta)
	if meta.RemovedFromOneDrive || meta.IsFolder {
		return fmt.Errorf("onedrive object skipped")
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = path.Base(objectKey)
	}
	if len(dataBytes) == 0 {
		return fmt.Errorf("onedrive data missing")
	}
	return outlook.UploadDriveFile(ctx, accessToken, name, dataBytes)
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
