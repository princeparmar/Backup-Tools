package handler

import (
	"net/http"
	"strings"

	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/labstack/echo/v4"
)

var msRefreshAccountTenantsFn = mstenant.RefreshAccountTenants

// HandleMicrosoftAccountTenants discovers every tenant the signed-in Microsoft account can reach,
// checks a tenant-scoped token and roles in each, and returns their access states.
// GET /microsoft/accounts/tenants (token_key, MICROSOFT_ACCOUNT_ID, REFRESH_TOKEN)
func HandleMicrosoftAccountTenants(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	cred, err := microsoftCredentialFromRequest(c, id, strings.TrimSpace(c.QueryParam("project_id")))
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	out, err := msRefreshAccountTenantsFn(ctx, microsoftDB(c), cred, id.RefreshToken)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

type microsoftConnectTenantRequest struct {
	BackupMode string `json:"backup_mode"`
}

// HandleMicrosoftConnectAccountTenant connects the account's link to :tid with the chosen backup
// mode. Reconnecting reuses the same link.
// POST /microsoft/accounts/tenants/:tid/connect {"backup_mode": "personal" | "organization"}
func HandleMicrosoftConnectAccountTenant(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	var req microsoftConnectTenantRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	cred, err := microsoftCredentialFromRequest(c, id, "")
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	state, err := mstenant.Connect(microsoftDB(c), cred, id.TenantID, req.BackupMode)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"tenant": state})
}

// HandleMicrosoftDisconnectAccountTenant disconnects :tid and pauses this account's jobs there.
// Backups are kept; backup, browse and restore are blocked until reconnect.
// POST /microsoft/accounts/tenants/:tid/disconnect
func HandleMicrosoftDisconnectAccountTenant(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	cred, err := microsoftCredentialFromRequest(c, &microsoftRequestIdentity{UserID: id.UserID, AccountID: id.AccountID}, "")
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	state, paused, err := mstenant.Disconnect(microsoftDB(c), cred, id.TenantID)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"tenant": state, "paused_jobs": paused})
}

// HandleMicrosoftRefreshAccountTenantRoles re-reads the account's token status and directory roles in :tid.
// POST /microsoft/accounts/tenants/:tid/roles/refresh
func HandleMicrosoftRefreshAccountTenantRoles(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	cred, err := microsoftCredentialFromRequest(c, &microsoftRequestIdentity{UserID: id.UserID, AccountID: id.AccountID}, "")
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	database := microsoftDB(c)
	if _, err := mstenant.TenantAccessState(database, cred.ID, id.TenantID); err != nil {
		return microsoftErrorJSON(c, err)
	}
	msCheckTenantAccessFn(ctx, database, cred, id.TenantID, id.RefreshToken)
	state, err := mstenant.TenantAccessState(database, cred.ID, id.TenantID)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"tenant": state})
}

var msCheckTenantAccessFn = mstenant.CheckTenantAccess
