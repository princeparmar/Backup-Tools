package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
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
	msDelegatedAccessFn    = outlookAccessTokenFromRefreshHeader
	msResolveAccountFn     = outlook.ResolveMicrosoftAccountContextFromRefreshToken
	msAuthTokenFromRefresh = outlook.AuthTokenUsingRefreshToken
	msTenantIDFromAccessFn = outlook.TenantIDFromAccessToken
)

func microsoftDB(c echo.Context) *db.PostgresDb {
	return c.Get(middleware.DbContextKey).(*db.PostgresDb)
}

func hasValidBackupToolsAPIKey(c echo.Context) bool {
	expected := strings.TrimSpace(utils.GetEnvWithKey("BACKUP_TOOLS_API_KEY"))
	got := strings.TrimSpace(c.Request().Header.Get("X-API-Key"))
	return expected != "" && got != "" && got == expected
}

// authorizeMicrosoftTenantRequest allows tenant routes for Satellite server calls (X-API-Key), a
// Satellite user owning a credential in the tenant, or a delegated REFRESH_TOKEN from the tenant.
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
	owns, err := database.CredentialRepo.UserHasTenant(userID, tenantID)
	if err != nil {
		return "", echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if owns {
		return userID, nil
	}
	if refresh := strings.TrimSpace(c.Request().Header.Get("REFRESH_TOKEN")); refresh != "" {
		if access, err := msAuthTokenFromRefresh(refresh); err == nil {
			if tid, err := msTenantIDFromAccessFn(access); err == nil && strings.EqualFold(tid, tenantID) {
				return userID, nil
			}
		}
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
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
}

// HandleMicrosoftWorkspace returns the shared workspace contract.
// GET /microsoft/workspace (REFRESH_TOKEN header, or tenant_id / email / project_id query).
func HandleMicrosoftWorkspace(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := microsoftDB(c)
	var acct *outlook.MicrosoftAccountContext

	if refresh := strings.TrimSpace(c.Request().Header.Get("REFRESH_TOKEN")); refresh != "" {
		acct, err = msResolveAccountFn(ctx, refresh)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}
	} else {
		userID, uerr := msSatelliteUserIDFn(c)
		if uerr != nil {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": uerr.Error()})
		}
		cred, cerr := findMicrosoftWorkspaceCredential(database, userID,
			c.QueryParam("tenant_id"), c.QueryParam("email"), c.QueryParam("project_id"))
		if cerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": cerr.Error()})
		}
		if cred == nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "no Microsoft credential found for this user"})
		}
		acct = accountContextFromCredential(cred)
		if strings.TrimSpace(cred.RefreshToken) != "" {
			if live, lerr := msResolveAccountFn(ctx, cred.RefreshToken); lerr == nil {
				acct = live
			}
		}
	}

	var tenant *repo.MicrosoftTenantDB
	if acct.TenantID != "" && !outlook.IsMSATenant(acct.TenantID) {
		tenant, err = database.MicrosoftTenantRepo.Get(acct.TenantID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}
	}
	appTokenOK := tenant != nil && tenant.ConsentStatus == repo.MicrosoftConsentGranted &&
		microsoftAppTokenUsable(ctx, tenant.TenantID)
	return c.JSON(http.StatusOK, buildMicrosoftWorkspaceContract(acct, tenant, appTokenOK))
}

func findMicrosoftWorkspaceCredential(database *db.PostgresDb, userID, tenantID, email, projectID string) (*repo.GoogleBackupCredentialDB, error) {
	var rows []repo.GoogleBackupCredentialDB
	var err error
	if strings.TrimSpace(tenantID) != "" {
		rows, err = database.CredentialRepo.ListByUserAndTenant(userID, tenantID)
	} else {
		rows, err = database.CredentialRepo.ListMicrosoftByUser(userID)
	}
	if err != nil {
		return nil, err
	}
	email = strings.TrimSpace(email)
	projectID = strings.TrimSpace(projectID)
	for i := range rows {
		if email != "" && !strings.EqualFold(strings.TrimSpace(rows[i].Email), email) {
			continue
		}
		if projectID != "" && strings.TrimSpace(rows[i].StorjProjectID) != projectID {
			continue
		}
		return &rows[i], nil
	}
	return nil, nil
}

func accountContextFromCredential(cred *repo.GoogleBackupCredentialDB) *outlook.MicrosoftAccountContext {
	acct := &outlook.MicrosoftAccountContext{
		Email:         strings.TrimSpace(cred.Email),
		AccountType:   outlook.AccountTypeWorkAccount,
		WorkspaceKind: outlook.WorkspaceKindOrganization,
		TenantID:      strings.ToLower(strings.TrimSpace(cred.TenantID)),
		TenantName:    strings.TrimSpace(cred.TenantName),
		AdminRoles:    []string{},
	}
	if outlook.IsMSATenant(acct.TenantID) {
		acct.AccountType = outlook.AccountTypePersonal
		acct.WorkspaceKind = outlook.WorkspaceKindPersonal
	}
	return acct
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
	return c.JSON(http.StatusOK, buildMicrosoftWorkspaceContract(nil, tenant, true))
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
	return c.JSON(http.StatusOK, buildMicrosoftWorkspaceContract(nil, tenant, tenant.ConsentStatus == repo.MicrosoftConsentGranted))
}

// HandleMicrosoftTenantDirectoryUsers lists tenant users live from Microsoft Graph.
// GET /microsoft/tenants/:tid/directory/users?search=&department=&org_unit_path=&page=&page_size=
func HandleMicrosoftTenantDirectoryUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := microsoftDB(c)
	tid := strings.ToLower(strings.TrimSpace(c.Param("tid")))
	if _, he := authorizeMicrosoftTenantRequest(c, database, tid); he != nil {
		return httpErrorJSON(c, he)
	}
	return respondMicrosoftDirectoryUsers(c, database, tid)
}

func respondMicrosoftDirectoryUsers(c echo.Context, database *db.PostgresDb, tid string) error {
	ctx := c.Request().Context()
	token, tenant, err := MicrosoftOrgAccess(ctx, database, tid, "")
	if err != nil {
		return orgAccessErrorJSON(c, err)
	}
	all, err := msListDirectoryUsersFn(ctx, token)
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
	roles, rolesErr := microsoftTenantUserRoles(ctx, token)
	resp := map[string]interface{}{
		"tenant_id": tenant.TenantID,
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

	database := microsoftDB(c)
	tid := strings.ToLower(strings.TrimSpace(c.Param("tid")))
	if _, he := authorizeMicrosoftTenantRequest(c, database, tid); he != nil {
		return httpErrorJSON(c, he)
	}
	return respondMicrosoftOrgStructure(c, database, tid)
}

func respondMicrosoftOrgStructure(c echo.Context, database *db.PostgresDb, tid string) error {
	ctx := c.Request().Context()
	token, tenant, err := MicrosoftOrgAccess(ctx, database, tid, "")
	if err != nil {
		return orgAccessErrorJSON(c, err)
	}
	users, err := msListDirectoryUsersFn(ctx, token)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"tenant_id": tenant.TenantID, "org_units": microsoftOrgUnitViews(users)})
}
