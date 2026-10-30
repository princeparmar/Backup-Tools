package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

// HandleMicrosoftAccountDetect classifies the caller's Microsoft account for sign-in: the workspace
// contract of the selected tenant (default: home), without directory entities.
// GET /microsoft/account/detect
func HandleMicrosoftAccountDetect(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ws, err := microsoftWorkspaceFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, microsoftDomainUsersBody(ws.contract, make([]map[string]interface{}, 0)))
}

// HandleMicrosoftCorporateDomainUsers is HandleMicrosoftAccountDetect plus the tenant's directory
// users, listed live only when the tenant is connected for organization backup.
// GET /microsoft/outlook/corporate/domain-users
func HandleMicrosoftCorporateDomainUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ws, err := microsoftWorkspaceFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	state := ws.state
	entities := make([]map[string]interface{}, 0)
	if state.CanOrganizationBackup && state.ConnectionState == repo.MicrosoftConnectionConnected && state.AuthMode == repo.MicrosoftAuthModeApplication {
		_, top := microsoftBrowsePage(c, 200)
		ws.id.TenantID = ws.tenantID
		if tc, rerr := resolveMicrosoftTenant(c, ws.id, "", true); rerr == nil {
			if users, lerr := msListDirectoryUsersOptsFn(ctx, tc.Token, outlook.ListTenantDirectoryUsersOptions{}); lerr == nil {
				users = filterMicrosoftDirectoryUsers(users, microsoftDirectoryFilter{EnabledOnly: true})
				// Role membership listing is expensive; browse preview omits it (use include_roles on directory/users).
				entities = microsoftDirectoryUserViews(users[:min(top, len(users))], nil)
			}
		}
	}
	return c.JSON(http.StatusOK, microsoftDomainUsersBody(ws.contract, entities))
}

func microsoftDomainUsersBody(contract MicrosoftWorkspaceContract, entities []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
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
	}
}

// HandleMicrosoftDirectoryUsers lists users of the selected tenant (organization mode only).
func HandleMicrosoftDirectoryUsers(c echo.Context) error {
	return HandleMicrosoftTenantDirectoryUsers(c)
}

func microsoftBrowsePage(c echo.Context, defaultTop int) (skip, top int) {
	skip, _ = strconv.Atoi(c.QueryParam("skip"))
	top, _ = strconv.Atoi(c.QueryParam("top"))
	if top <= 0 {
		top = defaultTop
	}
	return skip, top
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
	_, top := microsoftBrowsePage(c, 50)

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

	_, top := microsoftBrowsePage(c, 50)

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

	skip, top := microsoftBrowsePage(c, 50)

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
