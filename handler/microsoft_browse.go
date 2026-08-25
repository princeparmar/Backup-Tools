package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/labstack/echo/v4"
)

func outlookClientFromRefreshHeader(c echo.Context) (*outlook.OutlookClient, error) {
	token, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return nil, err
	}
	return outlook.NewOutlookClientUsingToken(token)
}

func outlookAccessTokenFromRefreshHeader(c echo.Context) (string, error) {
	refresh := strings.TrimSpace(c.Request().Header.Get("REFRESH_TOKEN"))
	if refresh == "" {
		refresh = strings.TrimSpace(c.QueryParam("refresh_token"))
	}
	if refresh == "" {
		return "", fmt.Errorf("REFRESH_TOKEN header is required")
	}
	return outlook.AuthTokenUsingRefreshToken(refresh)
}

// HandleMicrosoftQueryMessages lists Outlook messages for the new Microsoft product UI.
func HandleMicrosoftQueryMessages(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	client, err := outlookClientFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}
	messages, err := client.GetMessageWithDetails(int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Outlook messages",
		"messages": messages,
	})
}

// HandleMicrosoftListContacts lists Microsoft Graph contacts.
func HandleMicrosoftListContacts(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	client, err := outlookClientFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 100
	}
	contacts, err := client.ListContacts(int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Outlook contacts",
		"contacts": contacts,
	})
}

// HandleMicrosoftListCalendars lists Microsoft Graph calendars.
func HandleMicrosoftListCalendars(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	client, err := outlookClientFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	calendars, err := client.ListCalendars()
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":   "Outlook calendars",
		"calendars": calendars,
	})
}

// HandleMicrosoftListCalendarEvents lists events for a calendar.
func HandleMicrosoftListCalendarEvents(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	client, err := outlookClientFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	calendarID := strings.TrimSpace(c.Param("calendarId"))
	if calendarID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "calendarId is required"})
	}
	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}
	events, err := client.ListCalendarEvents(calendarID, int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Outlook calendar events",
		"events":  events,
	})
}

// HandleMicrosoftCorporateDomainUsers detects account type (Google twin) and optionally lists tenant users for admins.
func HandleMicrosoftCorporateDomainUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	acctCtx, err := outlook.ResolveMicrosoftAccountContext(ctx, accessToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Account detection requires a valid Microsoft Graph access token",
		})
	}

	if acctCtx.AccountType == outlook.AccountTypePersonal {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"account_type": acctCtx.AccountType,
			"email":        acctCtx.Email,
		})
	}

	if acctCtx.AccountType == outlook.AccountTypeEmployeeWorkspace {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"account_type": acctCtx.AccountType,
			"email":        acctCtx.Email,
			"tenant_id":    acctCtx.TenantID,
			"tenant_name":  acctCtx.TenantName,
		})
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 200
	}
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	users, listErr := client.ListDomainUsers(int32(top))
	entities := make([]map[string]interface{}, 0)
	if listErr == nil {
		for _, u := range outlook.DirectoryUsersToEntities(users) {
			entities = append(entities, map[string]interface{}{
				"id":           u.ID,
				"email":        u.Email,
				"display_name": u.DisplayName,
				"enabled":      u.Enabled,
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"account":              acctCtx.Email,
		"account_type":         acctCtx.AccountType,
		"tenant_id":            acctCtx.TenantID,
		"tenant_name":          acctCtx.TenantName,
		"is_admin":             acctCtx.IsAdmin,
		"sharepoint_eligible":  acctCtx.SharePointEligible,
		"entities":             entities,
	})
}

// HandleMicrosoftDirectoryUsers lists tenant users with pagination (admin picker lazy load).
func HandleMicrosoftDirectoryUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	acctCtx, err := outlook.ResolveMicrosoftAccountContext(ctx, accessToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	if acctCtx.AccountType != outlook.AccountTypeAdminWorkspace {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "Only Microsoft 365 admins can list directory users",
		})
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	nextLink := strings.TrimSpace(c.QueryParam("next_link"))
	if nextLink == "" {
		nextLink = strings.TrimSpace(c.QueryParam("skip_token"))
	}
	page, err := outlook.ListDirectoryUsersPage(ctx, accessToken, nextLink, int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Directory listing requires Microsoft Graph User.Read.All or Directory.Read.All (admin consent)",
		})
	}

	entities := make([]map[string]interface{}, 0, len(page.Entities))
	for _, u := range page.Entities {
		entities = append(entities, map[string]interface{}{
			"id":           u.ID,
			"email":        u.Email,
			"display_name": u.DisplayName,
			"enabled":      u.Enabled,
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"entities":   entities,
		"next_link":  page.NextLink,
		"skip_token": page.SkipToken,
	})
}

// HandleMicrosoftOneDriveFlatFiles lists non-folder OneDrive files (browse twin of Google drive-flat-files).
func HandleMicrosoftOneDriveFlatFiles(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	mailbox := strings.TrimSpace(c.QueryParam("email"))
	if mailbox == "" {
		mailbox = strings.TrimSpace(c.QueryParam("mailbox"))
	}
	driveRoot, err := client.OneDriveDriveRootURL(mailbox)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	files, err := outlook.ListOneDriveFlatFilesPage(ctx, accessToken, driveRoot, int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "OneDrive flat files",
		"files":   files,
		"skip":    skip,
		"top":     top,
	})
}

// HandleMicrosoftOutlookFlatFiles lists inbox messages for a mailbox (Outlook mail browse).
func HandleMicrosoftOutlookFlatFiles(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	mailbox := strings.TrimSpace(c.QueryParam("mailbox"))
	if mailbox == "" {
		mailbox = strings.TrimSpace(c.QueryParam("email"))
	}
	userBase, err := client.MailUserBaseURL(mailbox)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	messages, err := outlook.ListOutlookMailFlatMessagesPage(ctx, accessToken, userBase, int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Outlook flat messages",
		"messages": messages,
		"mailbox":  mailbox,
		"skip":     skip,
		"top":      top,
	})
}

// HandleMicrosoftSharePointSites lists SharePoint sites for site picker (Sites.Read.All, admin only).
func HandleMicrosoftSharePointSites(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftSharePointAdmin(ctx, c); err != nil {
		return sharePointBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	search := strings.TrimSpace(c.QueryParam("search"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	sites, err := outlook.ListSharePointSites(ctx, accessToken, search, int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "SharePoint site listing requires Microsoft Graph Sites.Read.All (admin consent)",
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "SharePoint sites",
		"sites":   sites,
		"top":     top,
	})
}

// HandleMicrosoftSharePointFlatFiles lists non-folder files in a document library drive (browse, admin only).
func HandleMicrosoftSharePointFlatFiles(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftSharePointAdmin(ctx, c); err != nil {
		return sharePointBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	driveID := strings.TrimSpace(c.QueryParam("drive_id"))
	if driveID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "drive_id is required"})
	}

	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	files, err := outlook.ListSharePointFlatFilesPage(ctx, accessToken, driveID, int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "SharePoint flat files",
		"files":    files,
		"drive_id": driveID,
		"skip":     skip,
		"top":      top,
	})
}

func requireMicrosoftSharePointAdmin(ctx context.Context, c echo.Context) (*outlook.MicrosoftAccountContext, error) {
	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return nil, err
	}
	acctCtx, err := outlook.ResolveMicrosoftAccountContext(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	if acctCtx.AccountType != outlook.AccountTypeAdminWorkspace {
		return nil, fmt.Errorf("SharePoint backup requires an organization admin account")
	}
	return acctCtx, nil
}

func sharePointBrowseForbidden(c echo.Context, err error) error {
	if err == nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "SharePoint backup requires an organization admin account",
		})
	}
	return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
}

func requireMicrosoftOrgResourceAccess(ctx context.Context, c echo.Context) (*outlook.MicrosoftAccountContext, error) {
	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return nil, err
	}
	acctCtx, err := outlook.ResolveMicrosoftAccountContext(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	if acctCtx.AccountType == "personal" {
		return nil, fmt.Errorf("Teams and Groups backup are not available for personal Microsoft accounts")
	}
	return acctCtx, nil
}

func orgResourceBrowseForbidden(c echo.Context, err error) error {
	if err == nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "Teams and Groups backup require a work or school account",
		})
	}
	return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
}

// HandleMicrosoftTeamsList lists Teams for team picker.
func HandleMicrosoftTeamsList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftOrgResourceAccess(ctx, c); err != nil {
		return orgResourceBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	teams, err := outlook.ListTeams(ctx, accessToken, int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Teams listing requires Team.ReadBasic.All",
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Teams",
		"teams":   teams,
		"top":     top,
	})
}

// HandleMicrosoftTeamChannels lists channels for a team.
func HandleMicrosoftTeamChannels(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftOrgResourceAccess(ctx, c); err != nil {
		return orgResourceBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	teamID := strings.TrimSpace(c.QueryParam("team_id"))
	if teamID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "team_id is required"})
	}

	channels, err := outlook.ListTeamChannels(ctx, accessToken, teamID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Team channels",
		"team_id":  teamID,
		"channels": channels,
	})
}

// HandleMicrosoftTeamsFlatMessages lists channel messages for browse.
func HandleMicrosoftTeamsFlatMessages(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftOrgResourceAccess(ctx, c); err != nil {
		return orgResourceBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	teamID := strings.TrimSpace(c.QueryParam("team_id"))
	channelID := strings.TrimSpace(c.QueryParam("channel_id"))
	if teamID == "" || channelID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "team_id and channel_id are required"})
	}

	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	messages, err := outlook.ListTeamsFlatMessagesPage(ctx, accessToken, teamID, channelID, int32(skip), int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":    "Teams flat messages",
		"team_id":    teamID,
		"channel_id": channelID,
		"messages":   messages,
		"skip":       skip,
		"top":        top,
	})
}

// HandleMicrosoftGroupsList lists M365 groups for group picker.
func HandleMicrosoftGroupsList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftOrgResourceAccess(ctx, c); err != nil {
		return orgResourceBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	groups, err := outlook.ListGroups(ctx, accessToken, int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Groups listing requires Group.Read.All",
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Groups",
		"groups":  groups,
		"top":     top,
	})
}

// HandleMicrosoftGroupsFlatConversations lists group conversation threads for browse.
func HandleMicrosoftGroupsFlatConversations(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if _, err := requireMicrosoftOrgResourceAccess(ctx, c); err != nil {
		return orgResourceBrowseForbidden(c, err)
	}

	accessToken, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	groupID := strings.TrimSpace(c.QueryParam("group_id"))
	if groupID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "group_id is required"})
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	threads, err := outlook.ListGroupsFlatConversationsPage(ctx, accessToken, groupID, int32(top))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "Groups flat conversations",
		"group_id":      groupID,
		"conversations": threads,
		"top":           top,
	})
}
