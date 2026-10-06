package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

func outlookClientFromRefreshHeader(c echo.Context) (*outlook.OutlookClient, error) {
	token, err := outlookAccessTokenFromRefreshHeader(c)
	if err != nil {
		return nil, err
	}
	return outlook.NewOutlookClientUsingToken(token)
}

func refreshTokenFromRequest(c echo.Context) (string, error) {
	refresh := strings.TrimSpace(c.Request().Header.Get("REFRESH_TOKEN"))
	if refresh == "" {
		refresh = strings.TrimSpace(c.QueryParam("refresh_token"))
	}
	if refresh == "" {
		return "", fmt.Errorf("REFRESH_TOKEN header is required")
	}
	return refresh, nil
}

func outlookAccessTokenFromRefreshHeader(c echo.Context) (string, error) {
	refresh, err := refreshTokenFromRequest(c)
	if err != nil {
		return "", err
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

// HandleMicrosoftCorporateDomainUsers detects the delegated account and returns the shared workspace
// contract. Tenant consent/capabilities are read-only here; directory entities are listed live from
// Graph only when the tenant is authorized for org backup.
func HandleMicrosoftCorporateDomainUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	refreshToken, err := refreshTokenFromRequest(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	acctCtx, err := msResolveAccountFn(ctx, refreshToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Account detection requires a valid Microsoft Graph access token",
		})
	}

	database := microsoftDB(c)
	var tenant *repo.MicrosoftTenantDB
	if acctCtx.AccountType != outlook.AccountTypePersonal && acctCtx.TenantID != "" {
		tenant, err = database.MicrosoftTenantRepo.Get(acctCtx.TenantID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}
	}
	appTokenOK := tenant != nil && tenant.ConsentStatus == repo.MicrosoftConsentGranted &&
		microsoftAppTokenUsable(ctx, tenant.TenantID)
	contract := buildMicrosoftWorkspaceContract(acctCtx, tenant, appTokenOK)

	entities := make([]map[string]interface{}, 0)
	if contract.AccountType == outlook.AccountTypeAdminWorkspace {
		top, _ := strconv.Atoi(c.QueryParam("top"))
		if top <= 0 {
			top = 200
		}
		if token, _, aerr := MicrosoftOrgAccess(ctx, database, tenant.TenantID, ""); aerr == nil {
			if users, lerr := msListDirectoryUsersFn(ctx, token); lerr == nil {
				users = filterMicrosoftDirectoryUsers(users, microsoftDirectoryFilter{EnabledOnly: true})
				roles, _ := microsoftTenantUserRoles(ctx, token)
				entities = microsoftDirectoryUserViews(users[:min(top, len(users))], roles)
			}
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"account":           contract.Email,
		"email":             contract.Email,
		"account_type":      contract.AccountType,
		"workspace_kind":    contract.WorkspaceKind,
		"tenant_id":         contract.TenantID,
		"tenant_name":       contract.TenantName,
		"is_admin":          contract.IsAdmin,
		"admin_roles":       contract.AdminRoles,
		"consent":           contract.Consent,
		"capabilities":      contract.Capabilities,
		"capability_errors": contract.CapabilityErrors,
		"entities":          entities,
	})
}

// HandleMicrosoftDirectoryUsers lists tenant users live for the delegated caller's tenant.
func HandleMicrosoftDirectoryUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, err := msDelegatedAccessFn(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	tid, err := msTenantIDFromAccessFn(accessToken)
	if err != nil || tid == "" || outlook.IsMSATenant(tid) {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "directory listing requires a Microsoft 365 work or school tenant"})
	}
	return respondMicrosoftDirectoryUsers(c, microsoftDB(c), strings.ToLower(tid))
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

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilitySharePoint)
	if accessToken == "" {
		return respErr
	}

	search := strings.TrimSpace(c.QueryParam("search"))
	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	sites, err := outlook.ListTenantSharePointSites(ctx, accessToken, search, top)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "SharePoint site listing requires the Sites.Read.All application permission",
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

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilitySharePoint)
	if accessToken == "" {
		return respErr
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

// microsoftOrgBrowseToken resolves the tenant (tenant_id query, else the delegated token's tenant),
// authorizes the caller for it, and returns an app-only token gated on consent + capability.
func microsoftOrgBrowseToken(c echo.Context, capability string) (string, error) {
	database := microsoftDB(c)
	tid := strings.ToLower(strings.TrimSpace(c.QueryParam("tenant_id")))
	if tid == "" {
		access, err := msDelegatedAccessFn(c)
		if err != nil {
			return "", c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "tenant_id query or REFRESH_TOKEN header is required"})
		}
		tid, err = msTenantIDFromAccessFn(access)
		if err != nil || tid == "" {
			return "", c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "could not determine Microsoft tenant"})
		}
		tid = strings.ToLower(tid)
	} else if _, he := authorizeMicrosoftTenantRequest(c, database, tid); he != nil {
		return "", httpErrorJSON(c, he)
	}
	token, _, err := MicrosoftOrgAccess(c.Request().Context(), database, tid, capability)
	if err != nil {
		return "", orgAccessErrorJSON(c, err)
	}
	return token, nil
}

// HandleMicrosoftTeamsList lists Teams for team picker.
func HandleMicrosoftTeamsList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilityTeamsChannel)
	if accessToken == "" {
		return respErr
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	teams, err := outlook.ListTenantTeams(ctx, accessToken, top)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Teams listing requires the Group.Read.All application permission",
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

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilityTeamsChannel)
	if accessToken == "" {
		return respErr
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

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilityTeamsChannel)
	if accessToken == "" {
		return respErr
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

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilityGroups)
	if accessToken == "" {
		return respErr
	}

	top, _ := strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = 50
	}

	groups, err := outlook.ListTenantGroups(ctx, accessToken, top)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
			"hint":  "Groups listing requires the Group.Read.All application permission",
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

	accessToken, respErr := microsoftOrgBrowseToken(c, outlook.CapabilityGroups)
	if accessToken == "" {
		return respErr
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
