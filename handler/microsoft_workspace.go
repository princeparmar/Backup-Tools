package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

const (
	microsoftDirectoryDefaultPageSize = 50
	microsoftDirectoryMaxPageSize     = 500
)

// Seams (overridden in tests).
var (
	msSatelliteUserIDFn    = satellite.GetUserdetails
	msRefreshReachesTenant = mstenant.RefreshTokenReachesTenant
)

func microsoftDB(c echo.Context) *db.PostgresDb {
	return c.Get(middleware.DbContextKey).(*db.PostgresDb)
}

func hasValidBackupToolsAPIKey(c echo.Context) bool {
	expected := strings.TrimSpace(utils.GetEnvWithKey("BACKUP_TOOLS_API_KEY"))
	got := strings.TrimSpace(c.Request().Header.Get("X-API-Key"))
	return expected != "" && got != "" && got == expected
}

// authorizeMicrosoftTenantRequest allows tenant consent routes for Satellite server calls
// (X-API-Key), a Satellite user with an account linked to the tenant, or a delegated REFRESH_TOKEN
// that can mint a token in the tenant (admin consent during onboarding, before any link exists).
func authorizeMicrosoftTenantRequest(c echo.Context, database *db.PostgresDb, tenantID string) (string, *echo.HTTPError) {
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	if tenantID == "" {
		return "", echo.NewHTTPError(http.StatusBadRequest, "tenant id is required")
	}
	if hasValidBackupToolsAPIKey(c) {
		userID, _ := msSatelliteUserIDFn(c)
		return userID, nil
	}
	userID, err := msSatelliteUserIDFn(c)
	if err != nil {
		return "", echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
	linked, err := database.MicrosoftLinkRepo.UserHasLink(userID, tenantID)
	if err != nil {
		return "", echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if linked {
		return userID, nil
	}
	if refresh := strings.TrimSpace(c.Request().Header.Get("REFRESH_TOKEN")); refresh != "" && msRefreshReachesTenant(refresh, tenantID) {
		return userID, nil
	}
	return "", echo.NewHTTPError(http.StatusForbidden, "no access to this Microsoft tenant")
}

func httpErrorJSON(c echo.Context, he *echo.HTTPError) error {
	return c.JSON(he.Code, map[string]interface{}{"error": he.Message})
}

func orgAccessErrorJSON(c echo.Context, err error) error {
	var accessErr *MicrosoftOrgAccessError
	if errors.As(err, &accessErr) {
		status := accessErr.HTTPStatus
		if status == 0 {
			status = http.StatusForbidden
		}
		return c.JSON(status, accessErr.Body())
	}
	return microsoftErrorJSON(c, err)
}

// HandleMicrosoftWorkspace returns the shared workspace contract for the selected tenant of the
// caller's Microsoft account.
// GET /microsoft/workspace (token_key, MICROSOFT_* headers, optional REFRESH_TOKEN; tenant_id / project_id query)
func HandleMicrosoftWorkspace(c echo.Context) error {
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
	state, err := mstenant.TenantAccessState(database, cred.ID, tid)
	if mstenant.IsCode(err, mstenant.CodeTenantNotLinked) && tid == cred.HomeTenantID() {
		if lerr := ensureMicrosoftHomeLink(ctx, database, cred, id.RefreshToken); lerr != nil {
			return microsoftErrorJSON(c, lerr)
		}
		state, err = mstenant.TenantAccessState(database, cred.ID, tid)
	}
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	var tenant *repo.MicrosoftTenantDB
	if !outlook.IsMSATenant(tid) {
		if tenant, err = database.MicrosoftTenantRepo.Get(tid); err != nil {
			return microsoftErrorJSON(c, err)
		}
	}
	return c.JSON(http.StatusOK, buildMicrosoftWorkspaceContract(cred.Email, state, tenant))
}

type microsoftConsentRequest struct {
	TenantName  string `json:"tenant_name"`
	ConsentedBy string `json:"consented_by"`
}

// HandleMicrosoftTenantConsent records admin consent and evaluates capabilities.
// POST /microsoft/tenants/:tid/consent
func HandleMicrosoftTenantConsent(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := microsoftDB(c)
	tid := strings.ToLower(strings.TrimSpace(c.Param("tid")))
	if _, he := authorizeMicrosoftTenantRequest(c, database, tid); he != nil {
		return httpErrorJSON(c, he)
	}
	var req microsoftConsentRequest
	_ = c.Bind(&req)

	tenant, err := newMicrosoftTenantService(database).CheckConsent(ctx, tid, strings.TrimSpace(req.TenantName), strings.TrimSpace(req.ConsentedBy), true)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, buildMicrosoftWorkspaceContract("", nil, tenant))
}

// HandleMicrosoftTenantCapabilitiesRefresh re-checks consent and re-probes capabilities.
// POST /microsoft/tenants/:tid/capabilities/refresh
func HandleMicrosoftTenantCapabilitiesRefresh(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := microsoftDB(c)
	tid := strings.ToLower(strings.TrimSpace(c.Param("tid")))
	if _, he := authorizeMicrosoftTenantRequest(c, database, tid); he != nil {
		return httpErrorJSON(c, he)
	}
	tenant, err := newMicrosoftTenantService(database).RefreshCapabilities(ctx, tid)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	if tenant == nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "tenant has not been onboarded; grant admin consent first"})
	}
	return c.JSON(http.StatusOK, buildMicrosoftWorkspaceContract("", nil, tenant))
}

// HandleMicrosoftTenantDirectoryUsers lists tenant users live from Microsoft Graph.
// GET /microsoft/tenants/:tid/directory/users?search=&department=&org_unit_path=&page=&page_size=
func HandleMicrosoftTenantDirectoryUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	tc, err := microsoftTenantContextFromRequest(c, "", true)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return respondMicrosoftDirectoryUsers(c, tc)
}

func respondMicrosoftDirectoryUsers(c echo.Context, tc *mstenant.Context) error {
	ctx := c.Request().Context()
	all, err := msListDirectoryUsersFn(ctx, tc.Token)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.QueryParam("page_size"))
	if size <= 0 {
		size, _ = strconv.Atoi(c.QueryParam("top"))
	}
	if size <= 0 {
		size = microsoftDirectoryDefaultPageSize
	}
	if size > microsoftDirectoryMaxPageSize {
		size = microsoftDirectoryMaxPageSize
	}
	filtered := filterMicrosoftDirectoryUsers(all, microsoftDirectoryFilter{
		Search:      c.QueryParam("search"),
		Department:  c.QueryParam("department"),
		OrgUnitPath: c.QueryParam("org_unit_path"),
		EnabledOnly: strings.EqualFold(c.QueryParam("enabled_only"), "true"),
	})
	start := min((page-1)*size, len(filtered))
	end := min(start+size, len(filtered))
	roles, rolesErr := microsoftTenantUserRoles(ctx, tc.Token)
	resp := map[string]interface{}{
		"tenant_id": tc.TenantID,
		"users":     microsoftDirectoryUserViews(filtered[start:end], roles),
		"page":      page,
		"page_size": size,
		"total":     len(filtered),
	}
	if rolesErr != "" {
		resp["roles_error"] = rolesErr
	}
	return c.JSON(http.StatusOK, resp)
}

// HandleMicrosoftTenantOrgStructure returns org units derived live from user departments.
// GET /microsoft/tenants/:tid/org-structure
func HandleMicrosoftTenantOrgStructure(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	tc, err := microsoftTenantContextFromRequest(c, "", true)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	users, err := msListDirectoryUsersFn(ctx, tc.Token)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"tenant_id": tc.TenantID, "org_units": microsoftOrgUnitViews(users)})
}
