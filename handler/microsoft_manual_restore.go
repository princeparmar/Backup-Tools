package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	msrestore "github.com/StorX2-0/Backup-Tools/restore/microsoft"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// microsoftManualRestoreMaxKeys matches Google select-and-restore (≤10 vault keys).
const microsoftManualRestoreMaxKeys = 10

type microsoftManualRestoreSession struct {
	accessGrant string
	accessToken string
	tenantID    string
	loginID     string
	userID      string
}

// prepareMicrosoftManualRestore checks the request before any write. checkToken refuses an
// access token the restore can't write with.
func prepareMicrosoftManualRestore(c echo.Context, checkToken ...func(accessToken string) error) (*microsoftManualRestoreSession, []string, error) {
	accessGrant, accessToken, err := getAccessTokens(c)
	if err != nil {
		return nil, nil, err
	}
	for _, check := range checkToken {
		if err := check(accessToken); err != nil {
			return nil, nil, echo.NewHTTPError(http.StatusForbidden, err.Error())
		}
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
	tenantID, err := checkMicrosoftManualRestoreKeys(c, accessToken, keys)
	if err != nil {
		return nil, nil, err
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
		tenantID:    tenantID,
		loginID:     loginID,
		userID:      userID,
	}, keys, nil
}

// checkMicrosoftManualRestoreKeys keeps a manual restore inside one tenant: every key must be in the
// {tenant}/{type}/{id}/ layout, in the access token's tenant and in MICROSOFT_TENANT_ID when sent.
// It returns that tenant.
func checkMicrosoftManualRestoreKeys(c echo.Context, accessToken string, keys []string) (string, error) {
	headerTenant := strings.ToLower(strings.TrimSpace(c.Request().Header.Get("MICROSOFT_TENANT_ID")))
	claims, _ := outlook.IdentityClaimsFromTokens("", accessToken)
	tokenTenant := strings.ToLower(strings.TrimSpace(claims.TenantID))
	if tokenTenant == "" {
		tokenTenant = headerTenant
	}
	keyTenants := make([]string, 0, len(keys))
	for _, key := range keys {
		_, keyTenant, _, _, _, ok := outlook.SplitResourceKey(key)
		if !ok {
			return "", echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("key %q is not a tenant-scoped Microsoft backup key", key))
		}
		keyTenants = append(keyTenants, keyTenant)
	}
	if msg, code := msrestore.ManualRestoreIdentityIssue(tokenTenant, headerTenant, keyTenants); msg != "" {
		return "", echo.NewHTTPError(code, msg)
	}
	return tokenTenant, nil
}

// microsoftManualKeysNeedTenantCheck reports whether every non-empty key is a tenant backup key.
// Legacy flat mail keys are not tenant-scoped and stay on the signed-in mailbox path.
func microsoftManualKeysNeedTenantCheck(keys []string) bool {
	saw := false
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		saw = true
		if _, _, _, _, _, ok := outlook.SplitResourceKey(key); !ok {
			return false
		}
	}
	return saw
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
	msg := microsoftManualRestoreActivityMessage(sess.loginID, phase, itemCount, result, restoreErr)
	switch phase {
	case "started":
		data["item_count"] = itemCount
	case "completed":
		if result != nil {
			data["processed_count"] = len(result.ProcessedIDs)
			data["failed_count"] = len(result.FailedIDs)
		}
	case "failed":
		priority = "high"
		data["level"] = 4
		if restoreErr != nil {
			data["error"] = microsoftManualRestoreErrorText(restoreErr)
		}
	}
	satellite.SendNotificationAsync(ctx, sess.userID, title, msg, &priority, data, nil)
}

func notifyMicrosoftManualRestoreDenied(c echo.Context, method string, restoreErr error) {
	if restoreErr == nil {
		return
	}
	userID, err := satellite.GetUserdetails(c)
	if err != nil || strings.TrimSpace(userID) == "" {
		return
	}
	accessToken := strings.TrimSpace(strings.TrimPrefix(c.Request().Header.Get(echo.HeaderAuthorization), "Bearer "))
	claims, _ := outlook.IdentityClaimsFromTokens("", accessToken)
	sess := &microsoftManualRestoreSession{userID: userID, loginID: claims.Email}
	notifyMicrosoftRestore(c.Request().Context(), sess, method, "failed", 0, nil, errors.New(microsoftManualRestoreErrorText(restoreErr)))
}

func microsoftManualRestoreErrorText(err error) string {
	if err == nil {
		return ""
	}
	if he, ok := err.(*echo.HTTPError); ok {
		return strings.TrimSpace(fmt.Sprint(he.Message))
	}
	return strings.TrimSpace(err.Error())
}

func microsoftManualRestoreActivityMessage(loginID, phase string, itemCount int, result *DownloadResult, restoreErr error) string {
	who := strings.TrimSpace(loginID)
	if who == "" {
		who = "this Microsoft account"
	}
	switch phase {
	case "started":
		return fmt.Sprintf("Restore of %d items for %s has started", itemCount, who)
	case "completed":
		nOK, nFail := 0, 0
		note := ""
		if result != nil {
			nOK, nFail = len(result.ProcessedIDs), len(result.FailedIDs)
			note = microsoftManualRestoreFailureNote(result.Message)
		}
		msg := fmt.Sprintf("Restore for %s completed. %d succeeded, %d failed", who, nOK, nFail)
		if nFail > 0 && note != "" {
			return msg + ". " + note
		}
		return msg
	case "failed":
		if text := microsoftManualRestoreErrorText(restoreErr); text != "" {
			return fmt.Sprintf("Restore for %s failed: %s", who, text)
		}
		return fmt.Sprintf("Restore for %s failed", who)
	}
	return ""
}

func microsoftManualRestoreFailureNote(message string) string {
	m := strings.TrimSpace(message)
	switch m {
	case "", "sharepoint restore processed", teamsRepostNotice:
		return ""
	}
	return m
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

	userBase, err := outlook.UserBaseURL("", "", "", false)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		if !outlook.IsPIMItemKey(key) {
			failed.Add(key)
			continue
		}
		raw, derr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookCalendar, key)
		if derr != nil {
			logger.Error(ctx, "calendar restore download failed", logger.ErrorField(derr), logger.String("key", key))
			failed.Add(key)
			continue
		}
		calendarID := msrestore.RestoreCalendarID(ctx, sess.accessGrant, sess.accessToken, userBase, key, nil)
		if cerr := outlook.RestoreCalendarEvent(ctx, sess.accessToken, userBase, calendarID, raw); cerr != nil {
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

	userBase, err := outlook.UserBaseURL("", "", "", false)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		if !outlook.IsPIMItemKey(key) {
			failed.Add(key)
			continue
		}
		raw, derr := satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookContacts, key)
		if derr != nil {
			failed.Add(key)
			continue
		}
		folderID := msrestore.RestoreContactFolderID(ctx, sess.accessGrant, sess.accessToken, userBase, key, nil)
		if cerr := outlook.RestoreContact(ctx, sess.accessToken, userBase, folderID, raw); cerr != nil {
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

	userBase, err := outlook.UserBaseURL("", "", "", false)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	src := msrestore.OneDriveRestoreSource(sess.accessGrant)
	src.FolderNames = map[string]string{}
	listed := map[string]bool{}
	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	for _, key := range keys {
		if !outlook.IsOneDriveRestoreKey(key) {
			failed.Add(key)
			continue
		}
		addDriveFolderNames(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookOneDrive, key, src.FolderNames, listed)
		if uerr := outlook.RestoreOneDriveBackup(ctx, sess.accessToken, userBase, key, src); uerr != nil {
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

// addDriveFolderNames adds the folder names of key's drive backup to names, listing each resource once.
func addDriveFolderNames(ctx context.Context, accessGrant, bucket, key string, names map[string]string, listed map[string]bool) {
	p, ok := outlook.ParseOneDriveObjectKey(key)
	if !ok || listed[p.Prefix] {
		return
	}
	listed[p.Prefix] = true
	folderKeys, err := satellite.ListObjectsWithPrefix(ctx, accessGrant, bucket, p.Prefix+"/"+outlook.OneDriveSectionMyDrive+"/")
	if err != nil {
		logger.Warn(ctx, "drive restore folder listing failed", logger.String("bucket", bucket), logger.ErrorField(err))
	}
	for id, name := range outlook.OneDriveFolderNames(slices.Collect(maps.Keys(folderKeys))) {
		names[id] = name
	}
}

// libraryRestore restores tree-layout SharePoint library backups into their library.
type libraryRestore struct {
	accessGrant, bucket string
	src                 outlook.OneDriveRestoreSource
	listed              map[string]bool
}

func newLibraryRestore(accessGrant, bucket string) *libraryRestore {
	src := msrestore.DriveRestoreSource(accessGrant, bucket)
	src.FolderNames = map[string]string{}
	return &libraryRestore{accessGrant: accessGrant, bucket: bucket, src: src, listed: map[string]bool{}}
}

// restore reports handled=false for keys that are not in the folder layout.
func (l *libraryRestore) restore(ctx context.Context, accessToken, key string) (handled bool, err error) {
	if _, ok := outlook.ParseOneDriveObjectKey(key); !ok {
		return false, nil
	}
	addDriveFolderNames(ctx, l.accessGrant, l.bucket, key, l.src.FolderNames, l.listed)
	return true, outlook.RestoreSharePointBackup(ctx, accessToken, key, l.src)
}

// HandleSharePointDownloadAndInsert restores selected SharePoint library files (≤10 keys).
func HandleSharePointDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c)
	if err != nil {
		notifyMicrosoftManualRestoreDenied(c, "outlook_sharepoint", err)
		return microsoftManualRestoreHTTPError(c, err)
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_sharepoint", "started", len(keys), nil, nil)

	lib := newLibraryRestore(sess.accessGrant, satellite.ReserveBucket_OutlookSharePoint)
	vaultGet := func(ctx context.Context, key string) ([]byte, error) {
		return satellite.DownloadObject(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookSharePoint, key)
	}
	processed, failed := utils.NewLockedArray(), utils.NewLockedArray()
	var firstErr error
	for _, key := range keys {
		if outlook.IsSharePointListItemKey(key) {
			if _, uerr := outlook.RestoreSharePointListItemFromVault(ctx, sess.accessToken, "", key, vaultGet); uerr != nil {
				noteSharePointManualFailure(ctx, failed, key, uerr, &firstErr)
			} else {
				processed.Add(key)
			}
			continue
		}
		if outlook.IsSharePointPageKey(key) {
			siteID := strings.TrimSpace(c.Request().Header.Get("MICROSOFT_SITE_ID"))
			if siteID == "" {
				siteID = strings.TrimSpace(c.QueryParam("site_id"))
			}
			if _, uerr := outlook.RestoreSharePointPageFromVault(ctx, sess.accessToken, siteID, key, vaultGet); uerr != nil {
				noteSharePointManualFailure(ctx, failed, key, uerr, &firstErr)
			} else {
				processed.Add(key)
			}
			continue
		}
		if handled, uerr := lib.restore(ctx, sess.accessToken, key); handled {
			if uerr != nil {
				noteSharePointManualFailure(ctx, failed, key, uerr, &firstErr)
			} else {
				processed.Add(key)
			}
			continue
		}
		metaJSON, dataBytes, _, derr := msrestore.DownloadMetaFollowData(ctx, sess.accessGrant, satellite.ReserveBucket_OutlookSharePoint, key)
		if derr != nil || len(dataBytes) == 0 {
			if derr == nil {
				derr = errors.New("sharepoint backup object could not be read")
			}
			noteSharePointManualFailure(ctx, failed, key, derr, &firstErr)
			continue
		}
		var meta outlook.SharePointCronBackupMeta
		_ = json.Unmarshal(metaJSON, &meta)
		if strings.TrimSpace(meta.Name) == "" {
			meta.Name = path.Base(key)
		}
		if uerr := outlook.RestoreSharePointLegacyMetaFile(ctx, sess.accessToken, "", meta, dataBytes); uerr != nil {
			noteSharePointManualFailure(ctx, failed, key, uerr, &firstErr)
			continue
		}
		processed.Add(key)
	}
	failedIDs := failed.Get()
	msg := "sharepoint restore processed"
	if len(failedIDs) > 0 && firstErr != nil {
		msg = firstErr.Error()
	}
	result := &DownloadResult{ProcessedIDs: processed.Get(), FailedIDs: failedIDs, Message: msg}
	notifyMicrosoftRestore(ctx, sess, "outlook_sharepoint", "completed", len(keys), result, nil)
	return c.JSON(http.StatusOK, result)
}

func noteSharePointManualFailure(ctx context.Context, failed *utils.LockedArray, key string, uerr error, first *error) {
	uerr = msrestore.WrapWorkspaceWriteError("sharepoint", uerr)
	logger.Error(ctx, "sharepoint restore failed", logger.ErrorField(uerr), logger.String("key", key))
	failed.Add(key)
	if first != nil && *first == nil {
		*first = uerr
	}
}

// HandleTeamsDownloadAndInsert restores selected Teams channel messages (≤10 keys) by posting them
// as *new* messages as the signed-in user (ChannelMessage.Send, delegated only); a reply goes
// under its reposted root, else the original root, else a new post. Optional query: team_id,
// channel_id. Old channel keys keep their legacy restore.
func HandleTeamsDownloadAndInsert(c echo.Context) error {
	return handleTeamsManualRestore(c, outlook.TeamsKindChannelMessage, "ChannelMessage.Send")
}

// HandleTeamsChatsDownloadAndInsert restores selected Teams chat messages (≤10 keys) by posting
// them as new messages into the existing chat (ChatMessage.Send, delegated only; the signed-in
// user must be a member). Meetings, calendar events and call records are download only.
func HandleTeamsChatsDownloadAndInsert(c echo.Context) error {
	return handleTeamsManualRestore(c, outlook.TeamsKindChatMessage, "ChatMessage.Send")
}

// teamsRepostNotice is returned with every Teams restore result.
const teamsRepostNotice = "Messages are posted as new messages by the signed-in user."

// teamsManualRestoreResult is the manual Teams restore answer: the usual id lists (skipped keys
// apart) and one outcome per key.
type teamsManualRestoreResult struct {
	DownloadResult
	SkippedIDs []string                        `json:"skipped_ids"`
	Items      []msrestore.TeamsRestoreOutcome `json:"items"`
}

func handleTeamsManualRestore(c echo.Context, kind, scope string) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	sess, keys, err := prepareMicrosoftManualRestore(c, func(accessToken string) error {
		return msrestore.CheckTeamsRestoreToken(accessToken, scope)
	})
	if err != nil {
		notifyMicrosoftManualRestoreDenied(c, "outlook_teams", err)
		return microsoftManualRestoreHTTPError(c, err)
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_teams", "started", len(keys), nil, nil)
	session := &msrestore.TeamsRestoreSession{AccessGrant: sess.accessGrant, Token: sess.accessToken, TenantID: sess.tenantID}
	if kind == outlook.TeamsKindChannelMessage {
		session.OverrideTeamID = strings.TrimSpace(c.QueryParam("team_id"))
		session.OverrideChannelID = strings.TrimSpace(c.QueryParam("channel_id"))
	}
	result := restoreTeamsManualKeys(ctx, session, keys, kind)
	notify := result.DownloadResult
	if detail := firstTeamsManualFailureDetail(result); detail != "" {
		notify.Message = detail
	}
	notifyMicrosoftRestore(ctx, sess, "outlook_teams", "completed", len(keys), &notify, nil)
	return c.JSON(http.StatusOK, result)
}

func firstTeamsManualFailureDetail(result teamsManualRestoreResult) string {
	for _, it := range result.Items {
		if it.Status == msrestore.TeamsFailed && strings.TrimSpace(it.Detail) != "" {
			return it.Detail
		}
	}
	return ""
}

// restoreTeamsManualKeys restores the keys an endpoint accepts (channel endpoint: channel messages
// and old channel keys; chat endpoint: chat messages), thread roots first.
func restoreTeamsManualKeys(ctx context.Context, session *msrestore.TeamsRestoreSession, keys []string, kind string) teamsManualRestoreResult {
	result := teamsManualRestoreResult{
		DownloadResult: DownloadResult{ProcessedIDs: []string{}, FailedIDs: []string{}, Message: teamsRepostNotice},
		SkippedIDs:     []string{},
		Items:          []msrestore.TeamsRestoreOutcome{},
	}
	for _, key := range msrestore.OrderTeamsRestoreKeys(keys) {
		var o msrestore.TeamsRestoreOutcome
		if k, ok := outlook.ParseTeamsObjectKey(key); ok && !teamsManualKindAccepted(k.Kind, kind) {
			o = msrestore.TeamsRestoreOutcome{Key: key, Status: msrestore.TeamsUnsupported,
				Detail: fmt.Sprintf("this endpoint restores %s keys, not %s keys", strings.ReplaceAll(kind, "_", " "), strings.ReplaceAll(k.Kind, "_", " "))}
		} else {
			o = session.RestoreItem(ctx, key)
		}
		switch o.Status {
		case msrestore.TeamsRestored:
			result.ProcessedIDs = append(result.ProcessedIDs, key)
		case msrestore.TeamsSkipped:
			result.SkippedIDs = append(result.SkippedIDs, key)
		default:
			logger.Error(ctx, "teams restore item not restored", logger.String("key", key),
				logger.String("status", o.Status), logger.String("detail", o.Detail))
			result.FailedIDs = append(result.FailedIDs, key)
		}
		result.Items = append(result.Items, o)
	}
	return result
}

func teamsManualKindAccepted(keyKind, kind string) bool {
	if keyKind == kind {
		return true
	}
	return kind == outlook.TeamsKindChannelMessage && (keyKind == outlook.TeamsKindLegacyMeta || keyKind == outlook.TeamsKindLegacyOther)
}
