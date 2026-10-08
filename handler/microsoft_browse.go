package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

// microsoftBrowseClient resolves the request's tenant and returns a Graph client for it. Delegated
// links browse the signed-in user (/me); application links must name the mailbox (mailbox or email query).
func microsoftBrowseClient(c echo.Context, capability string) (*outlook.OutlookClient, *mstenant.Context, error) {
	tc, err := microsoftTenantContextFromRequest(c, capability, false)
	if err != nil {
		return nil, nil, err
	}
	if tc.Application {
		client, cerr := outlook.NewOutlookClientForUser(tc.Token, browseMailbox(c))
		return client, tc, cerr
	}
	client, cerr := outlook.NewOutlookClientUsingToken(tc.Token)
	return client, tc, cerr
}

func browseMailbox(c echo.Context) string {
	if m := strings.TrimSpace(c.QueryParam("mailbox")); m != "" {
		return m
	}
	return strings.TrimSpace(c.QueryParam("email"))
}

func browseErrorJSON(c echo.Context, err error) error {
	if _, ok := mstenant.AsError(err); ok {
		return microsoftErrorJSON(c, err)
	}
	return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
}

// HandleMicrosoftQueryMessages lists Outlook messages for the new Microsoft product UI.
func HandleMicrosoftQueryMessages(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	client, _, err := microsoftBrowseClient(c, outlook.CapabilityMail)
	if err != nil {
		return browseErrorJSON(c, err)
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

	client, _, err := microsoftBrowseClient(c, outlook.CapabilityContacts)
	if err != nil {
		return browseErrorJSON(c, err)
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

	client, _, err := microsoftBrowseClient(c, outlook.CapabilityCalendar)
	if err != nil {
		return browseErrorJSON(c, err)
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

	client, _, err := microsoftBrowseClient(c, outlook.CapabilityCalendar)
	if err != nil {
		return browseErrorJSON(c, err)
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

// HandleMicrosoftCorporateDomainUsers returns the workspace contract of the selected tenant for the
// caller's Microsoft account (creating the credential and home link from REFRESH_TOKEN when needed).
// Directory entities are listed live only when the tenant is connected for organization backup.
func HandleMicrosoftCorporateDomainUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := microsoftDB(c)
	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	cred, err := microsoftCredentialFromRequest(c, id, strings.TrimSpace(c.QueryParam("project_id")))
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	tid := id.TenantID
	if tid == "" {
		tid = cred.HomeTenantID()
	}
	if _, serr := mstenant.TenantAccessState(database, cred.ID, tid); mstenant.IsCode(serr, mstenant.CodeTenantNotLinked) && tid == cred.HomeTenantID() {
		if lerr := ensureMicrosoftHomeLink(ctx, database, cred, id.RefreshToken); lerr != nil {
			return microsoftErrorJSON(c, lerr)
		}
	}
	state, err := mstenant.TenantAccessState(database, cred.ID, tid)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	var tenant *repo.MicrosoftTenantDB
	if !outlook.IsMSATenant(tid) {
		if tenant, err = database.MicrosoftTenantRepo.Get(tid); err != nil {
			return microsoftErrorJSON(c, err)
		}
	}
	contract := buildMicrosoftWorkspaceContract(cred.Email, state, tenant)

	entities := make([]map[string]interface{}, 0)
	if state.CanOrganizationBackup && state.ConnectionState == repo.MicrosoftConnectionConnected && state.AuthMode == repo.MicrosoftAuthModeApplication {
		top, _ := strconv.Atoi(c.QueryParam("top"))
		if top <= 0 {
			top = 200
		}
		id.TenantID = tid
		if tc, rerr := resolveMicrosoftTenant(c, id, "", true); rerr == nil {
			if users, lerr := msListDirectoryUsersFn(ctx, tc.Token); lerr == nil {
				users = filterMicrosoftDirectoryUsers(users, microsoftDirectoryFilter{EnabledOnly: true})
				roles, _ := microsoftTenantUserRoles(ctx, tc.Token)
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
		"access_state":      contract.AccessState,
		"entities":          entities,
	})
}

// HandleMicrosoftDirectoryUsers lists users of the selected tenant (organization mode only).
func HandleMicrosoftDirectoryUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	tc, err := microsoftTenantContextFromRequest(c, "", true)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return respondMicrosoftDirectoryUsers(c, tc)
}

// HandleMicrosoftOneDriveFlatFiles lists non-folder OneDrive files (browse twin of Google drive-flat-files).
func HandleMicrosoftOneDriveFlatFiles(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	client, tc, err := microsoftBrowseClient(c, outlook.CapabilityOneDrive)
	if err != nil {
		return browseErrorJSON(c, err)
	}
	accessToken := tc.Token

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

	client, tc, err := microsoftBrowseClient(c, outlook.CapabilityMail)
	if err != nil {
		return browseErrorJSON(c, err)
	}
	accessToken := tc.Token

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

// microsoftOrgBrowseToken resolves the selected tenant in organization mode and returns its app-only
// token gated on consent + capability. On failure the error response has been written.
func microsoftOrgBrowseToken(c echo.Context, capability string) (string, error) {
	tc, err := microsoftTenantContextFromRequest(c, capability, true)
	if err != nil {
		return "", microsoftErrorJSON(c, err)
	}
	return tc.Token, nil
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
