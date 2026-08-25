package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// microsoftManualRestoreMaxKeys matches Google select-and-restore (≤10 vault keys).
const microsoftManualRestoreMaxKeys = 10

type microsoftManualRestoreSession struct {
	accessGrant string
	accessToken string
	loginID     string
	userID      string
}

func prepareMicrosoftManualRestore(c echo.Context) (*microsoftManualRestoreSession, []string, error) {
	accessGrant, accessToken, err := getAccessTokens(c)
	if err != nil {
		return nil, nil, err
	}
	keys, err := parseMessageIDs(c)
	if err != nil {
		return nil, nil, err
	}
	if len(keys) == 0 || keys[0] == "" {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "no keys provided")
	}
	if len(keys) > microsoftManualRestoreMaxKeys {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "maximum 10 keys allowed")
	}
	client, err := createOutlookClient(accessToken)
	if err != nil {
		return nil, nil, err
	}
	loginID := ""
	if user, uerr := client.GetCurrentUser(); uerr == nil && user != nil {
		loginID = strings.TrimSpace(user.Mail)
		if loginID == "" {
			loginID = strings.TrimSpace(user.UserPrincipalName)
		}
	}
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return nil, nil, echo.NewHTTPError(http.StatusUnauthorized, "authentication failed")
	}
	return &microsoftManualRestoreSession{
		accessGrant: accessGrant,
		accessToken: accessToken,
		loginID:     loginID,
		userID:      userID,
	}, keys, nil
}

func notifyMicrosoftRestore(ctx context.Context, sess *microsoftManualRestoreSession, method, phase string, itemCount int, result *DownloadResult, restoreErr error) {
	if sess == nil {
		return
	}
	priority := "normal"
	data := map[string]interface{}{
		"event":     fmt.Sprintf("%s_restore_%s", method, phase),
		"level":     2,
		"login_id":  sess.loginID,
		"method":    method,
		"type":      "restore",
		"timestamp": "now",
	}
	title := fmt.Sprintf("Microsoft %s restore %s", method, phase)
	msg := ""
	switch phase {
	case "started":
		data["item_count"] = itemCount
		msg = fmt.Sprintf("Restore of %d items for %s has started", itemCount, sess.loginID)
	case "completed":
		if result != nil {
			data["processed_count"] = len(result.ProcessedIDs)
			data["failed_count"] = len(result.FailedIDs)
			msg = fmt.Sprintf("Restore for %s completed. %d succeeded, %d failed", sess.loginID, len(result.ProcessedIDs), len(result.FailedIDs))
		}
	case "failed":
		priority = "high"
		data["level"] = 4
		if restoreErr != nil {
			data["error"] = restoreErr.Error()
			msg = fmt.Sprintf("Restore for %s failed: %v", sess.loginID, restoreErr)
		}
	}
	satellite.SendNotificationAsync(ctx, sess.userID, title, msg, &priority, data, nil)
}

func downloadMetaFollowData(ctx context.Context, accessGrant, bucket, key string) (metaJSON, dataBytes []byte, dataKey string, err error) {
	metaJSON, err = satellite.DownloadObject(ctx, accessGrant, bucket, key)
	if err != nil {
		return nil, nil, "", err
	}
	var meta struct {
		DataObjectKey string `json:"data_object_key"`
		RemovedOD     bool   `json:"removed_from_onedrive"`
		RemovedSP     bool   `json:"removed_from_sharepoint"`
		RemovedTeams  bool   `json:"removed_from_teams"`
		IsFolder      bool   `json:"is_folder"`
	}
	_ = json.Unmarshal(metaJSON, &meta)
	if meta.RemovedOD || meta.RemovedSP || meta.RemovedTeams || meta.IsFolder {
		return metaJSON, nil, "", fmt.Errorf("object skipped (removed or folder)")
	}
	dataKey = strings.TrimSpace(meta.DataObjectKey)
	if dataKey == "" && strings.Contains(key, "/meta/") {
		dataKey = strings.Replace(key, "/meta/", "/data/", 1)
		dataKey = strings.TrimSuffix(dataKey, ".json")
	}
	if dataKey == "" {
		return metaJSON, metaJSON, key, nil
	}
	dataBytes, err = satellite.DownloadObject(ctx, accessGrant, bucket, dataKey)
	if err != nil {
		return metaJSON, nil, dataKey, err
	}
	return metaJSON, dataBytes, dataKey, nil
}

func microsoftManualRestoreHTTPError(c echo.Context, err error) error {
	if he, ok := err.(*echo.HTTPError); ok {
		return c.JSON(he.Code, map[string]interface{}{"error": he.Message})
	}
	return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
}

// HandleOutlookCalendarDownloadAndInsert restores selected Outlook calendar events (≤10 keys).
func HandleOutlookCalendarDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		return microsoftManualRestoreHTTPError(c, err)
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_calendar", "started", len(keys), nil, nil)

	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		if strings.HasSuffix(key, "/_calendar.json") {
			failed.Add(key)
			continue
		}
		raw, derr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookCalendar, key)
		if derr != nil {
			logger.Error(ctx, "calendar restore download failed", logger.ErrorField(derr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		ev, perr := outlook.ParseRestoreCalendarEvent(raw)
		if perr != nil {
			failed.Add(key)
			continue
		}
		if cerr := outlook.CreateCalendarEvent(ctx, sess.accessToken, ev); cerr != nil {
			logger.Error(ctx, "calendar restore create failed", logger.ErrorField(cerr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		processed.Add(key)
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failed.Get(), Message: "outlook calendar restore processed"}
	notifyMicrosoftRestore(ctx, sess, "outlook_calendar", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}

// HandleOutlookContactsDownloadAndInsert restores selected Outlook contacts (≤10 keys).
func HandleOutlookContactsDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		return microsoftManualRestoreHTTPError(c, err)
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_contacts", "started", len(keys), nil, nil)

	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		raw, derr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookContacts, key)
		if derr != nil {
			failed.Add(key)
			continue
		}
		contact, perr := outlook.ParseRestoreContact(raw)
		if perr != nil {
			failed.Add(key)
			continue
		}
		if cerr := outlook.CreateContact(ctx, sess.accessToken, contact); cerr != nil {
			logger.Error(ctx, "contacts restore create failed", logger.ErrorField(cerr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		processed.Add(key)
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failed.Get(), Message: "outlook contacts restore processed"}
	notifyMicrosoftRestore(ctx, sess, "outlook_contacts", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}

// HandleOneDriveDownloadAndInsert restores selected OneDrive files (≤10 meta keys).
func HandleOneDriveDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		return microsoftManualRestoreHTTPError(c, err)
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_onedrive", "started", len(keys), nil, nil)

	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		metaJSON, dataBytes, _, derr := downloadMetaFollowData(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookOneDrive, key)
		if derr != nil || len(dataBytes) == 0 {
			failed.Add(key)
			continue
		}
		var meta outlook.OneDriveCronBackupMeta
		_ = json.Unmarshal(metaJSON, &meta)
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = path.Base(key)
		}
		if uerr := outlook.UploadDriveFile(ctx, sess.accessToken, name, dataBytes); uerr != nil {
			logger.Error(ctx, "onedrive restore upload failed", logger.ErrorField(uerr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		processed.Add(key)
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failed.Get(), Message: "onedrive restore processed"}
	notifyMicrosoftRestore(ctx, sess, "outlook_onedrive", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}

// HandleSharePointDownloadAndInsert restores selected SharePoint library files (≤10 keys).
func HandleSharePointDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		return microsoftManualRestoreHTTPError(c, err)
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_sharepoint", "started", len(keys), nil, nil)

	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		metaJSON, dataBytes, _, derr := downloadMetaFollowData(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookSharePoint, key)
		if derr != nil || len(dataBytes) == 0 {
			failed.Add(key)
			continue
		}
		var meta outlook.SharePointCronBackupMeta
		_ = json.Unmarshal(metaJSON, &meta)
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = path.Base(key)
		}
		driveID := strings.TrimSpace(meta.DriveID)
		if driveID == "" {
			failed.Add(key)
			continue
		}
		if uerr := outlook.UploadSharePointDriveFile(ctx, sess.accessToken, driveID, name, dataBytes); uerr != nil {
			logger.Error(ctx, "sharepoint restore upload failed", logger.ErrorField(uerr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		processed.Add(key)
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failed.Get(), Message: "sharepoint restore processed"}
	notifyMicrosoftRestore(ctx, sess, "outlook_sharepoint", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}

// HandleTeamsDownloadAndInsert restores selected Teams channel messages by posting them as *new*
// messages (≤10 keys). Graph cannot recreate original message IDs; replies/hosted content are not restored.
// Optional query: team_id, channel_id. Otherwise IDs come from message meta / {teamKey}/_team.json.
// Idempotency: callers should track restore_job_id + source key at the processor/DB layer — retries can duplicate posts.
func HandleTeamsDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		return microsoftManualRestoreHTTPError(c, err)
	}
	overrideTeamID := strings.TrimSpace(c.QueryParam("team_id"))
	overrideChannelID := strings.TrimSpace(c.QueryParam("channel_id"))
	notifyMicrosoftRestore(ctx, sess, "outlook_teams", "started", len(keys), nil, nil)

	teamSnapCache := map[string]*outlook.TeamsTeamSnapshot{}
	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		metaJSON, dataBytes, _, derr := downloadMetaFollowData(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookTeams, key)
		if derr != nil {
			failed.Add(key)
			continue
		}
		payload := dataBytes
		if len(payload) == 0 {
			payload = metaJSON
		}
		var meta outlook.TeamsCronBackupMeta
		_ = json.Unmarshal(metaJSON, &meta)

		teamKey, _ := outlook.ParseTeamsIDsFromKey(key)
		var teamSnap *outlook.TeamsTeamSnapshot
		if teamKey != "" {
			if cached, ok := teamSnapCache[teamKey]; ok {
				teamSnap = cached
			} else {
				snapRaw, serr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookTeams, teamKey+"/_team.json")
				if serr == nil {
					if parsed, perr := outlook.ParseTeamsTeamSnapshot(snapRaw); perr == nil {
						teamSnap = parsed
						teamSnapCache[teamKey] = parsed
					}
				} else {
					teamSnapCache[teamKey] = nil
				}
			}
		}

		tid, cid, rerr := outlook.ResolveTeamsGraphIDs(meta, teamSnap, key, overrideTeamID, overrideChannelID)
		if rerr != nil {
			logger.Error(ctx, "teams restore missing graph ids", logger.ErrorField(rerr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		body := outlook.ExtractTeamsMessageBody(payload)
		if perr := outlook.PostTeamsChannelMessage(ctx, sess.accessToken, tid, cid, body); perr != nil {
			logger.Error(ctx, "teams restore-as-new post failed", logger.ErrorField(perr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		processed.Add(key)
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failed.Get(), Message: "teams restore-as-new processed (messages re-posted; original IDs not preserved)"}
	notifyMicrosoftRestore(ctx, sess, "outlook_teams", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}

// HandleGroupsDownloadAndInsert restores selected M365 Group items (conversations/calendar/drive) ≤10 keys.
// Optional query: group_id. Otherwise IDs come from object JSON / {groupKey}/_group.json.
// Conversation/calendar creates need Group.ReadWrite.All (delegated). Idempotency belongs at processor/DB layer.
func HandleGroupsDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		return microsoftManualRestoreHTTPError(c, err)
	}
	overrideGroupID := strings.TrimSpace(c.QueryParam("group_id"))
	notifyMicrosoftRestore(ctx, sess, "outlook_groups", "started", len(keys), nil, nil)

	groupSnapCache := map[string]*outlook.GroupsGroupSnapshot{}
	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		groupKey := outlook.GroupKeyFromObjectKey(key)
		var groupSnap *outlook.GroupsGroupSnapshot
		if groupKey != "" {
			if cached, ok := groupSnapCache[groupKey]; ok {
				groupSnap = cached
			} else {
				snapRaw, serr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookGroups, groupKey+"/_group.json")
				if serr == nil {
					if parsed, perr := outlook.ParseGroupsGroupSnapshot(snapRaw); perr == nil {
						groupSnap = parsed
						groupSnapCache[groupKey] = parsed
					}
				} else {
					groupSnapCache[groupKey] = nil
				}
			}
		}

		switch {
		case strings.Contains(key, "/conversations/"):
			raw, derr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookGroups, key)
			if derr != nil {
				failed.Add(key)
				continue
			}
			gid, gerr := outlook.ResolveGroupGraphID(key, raw, groupSnap, overrideGroupID)
			if gerr != nil {
				logger.Error(ctx, "groups restore missing group_id", logger.ErrorField(gerr), logger.String("key", key))
				failed.Add(key)
				continue
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
			if cerr := outlook.CreateGroupConversationThread(ctx, sess.accessToken, gid, topic, body); cerr != nil {
				failed.Add(key)
				continue
			}
			processed.Add(key)
		case strings.Contains(key, "/calendar/"):
			raw, derr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookGroups, key)
			if derr != nil {
				failed.Add(key)
				continue
			}
			gid, gerr := outlook.ResolveGroupGraphID(key, raw, groupSnap, overrideGroupID)
			if gerr != nil {
				failed.Add(key)
				continue
			}
			ev, perr := outlook.ParseRestoreCalendarEvent(raw)
			if perr != nil {
				failed.Add(key)
				continue
			}
			if cerr := outlook.CreateGroupCalendarEvent(ctx, sess.accessToken, gid, ev); cerr != nil {
				failed.Add(key)
				continue
			}
			processed.Add(key)
		default:
			metaJSON, dataBytes, _, derr := downloadMetaFollowData(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookGroups, key)
			if derr != nil || len(dataBytes) == 0 {
				failed.Add(key)
				continue
			}
			var meta outlook.SharePointCronBackupMeta
			_ = json.Unmarshal(metaJSON, &meta)
			name := strings.TrimSpace(meta.Name)
			if name == "" {
				name = path.Base(key)
			}
			driveID := strings.TrimSpace(meta.DriveID)
			var uerr error
			if driveID != "" {
				uerr = outlook.UploadSharePointDriveFile(ctx, sess.accessToken, driveID, name, dataBytes)
			} else {
				uerr = outlook.UploadDriveFile(ctx, sess.accessToken, "group-restore-"+name, dataBytes)
			}
			if uerr != nil {
				failed.Add(key)
				continue
			}
			processed.Add(key)
		}
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failed.Get(), Message: "groups restore-as-new processed"}
	notifyMicrosoftRestore(ctx, sess, "outlook_groups", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}
