package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

// Satellite → Backup-Tools Microsoft identity headers.
const (
	headerMicrosoftAccountID    = "MICROSOFT_ACCOUNT_ID"
	headerMicrosoftHomeTenantID = "MICROSOFT_HOME_TENANT_ID"
	headerMicrosoftTenantID     = "MICROSOFT_TENANT_ID"
)

// Seams (overridden in tests).
var (
	msResolveTenantFn = mstenant.Resolve
	msSignInFn        = mstenant.SignInFromRefreshToken
)

// microsoftRequestIdentity is the Microsoft selection of a Satellite request.
type microsoftRequestIdentity struct {
	UserID       string
	AccountID    string
	HomeTenantID string
	TenantID     string
	RefreshToken string
}

// microsoftIdentityFromRequest reads token_key, the MICROSOFT_* headers and REFRESH_TOKEN. The
// selected tenant is MICROSOFT_TENANT_ID, else the :tid path parameter, else tenant_id query.
// Without MICROSOFT_ACCOUNT_ID (older callers) the account is read from the refresh token.
func microsoftIdentityFromRequest(c echo.Context) (*microsoftRequestIdentity, error) {
	userID, err := msSatelliteUserIDFn(c)
	if err != nil {
		return nil, &mstenant.Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: err.Error()}
	}
	h := c.Request().Header
	id := &microsoftRequestIdentity{
		UserID:       strings.TrimSpace(userID),
		AccountID:    strings.ToLower(strings.TrimSpace(h.Get(headerMicrosoftAccountID))),
		HomeTenantID: strings.ToLower(strings.TrimSpace(h.Get(headerMicrosoftHomeTenantID))),
		TenantID:     strings.ToLower(strings.TrimSpace(h.Get(headerMicrosoftTenantID))),
		RefreshToken: strings.TrimSpace(h.Get("REFRESH_TOKEN")),
	}
	pathTenant := strings.ToLower(strings.TrimSpace(c.Param("tid")))
	if pathTenant != "" {
		if id.TenantID != "" && id.TenantID != pathTenant {
			return nil, &mstenant.Error{HTTPStatus: http.StatusForbidden, Code: mstenant.CodeTenantMismatch,
				Message: "MICROSOFT_TENANT_ID does not match the tenant in the path"}
		}
		id.TenantID = pathTenant
	}
	if id.TenantID == "" {
		id.TenantID = strings.ToLower(strings.TrimSpace(c.QueryParam("tenant_id")))
	}
	if id.AccountID == "" && id.RefreshToken != "" {
		if in, serr := msSignInFn(id.RefreshToken); serr == nil {
			id.AccountID = in.ObjectID
			if id.HomeTenantID == "" {
				id.HomeTenantID = in.HomeTenantID
			}
		}
	}
	return id, nil
}

// microsoftTenantContextFromRequest resolves the request's tenant for capability. requireApp
// refuses delegated links (organization-wide routes).
func microsoftTenantContextFromRequest(c echo.Context, capability string, requireApp bool) (*mstenant.Context, error) {
	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return nil, err
	}
	return resolveMicrosoftTenant(c, id, capability, requireApp)
}

// resolveMicrosoftTenant resolves id's tenant. Delegated (non-organization) requests may use a
// discovered home link, and a first request with a verified REFRESH_TOKEN creates the credential
// and its home link so browsing works before onboarding.
func resolveMicrosoftTenant(c echo.Context, id *microsoftRequestIdentity, capability string, requireApp bool) (*mstenant.Context, error) {
	ctx, database := c.Request().Context(), microsoftDB(c)
	req := mstenant.Request{
		UserID:             id.UserID,
		ExternalAccountID:  id.AccountID,
		HomeTenantID:       id.HomeTenantID,
		TenantID:           id.TenantID,
		Capability:         capability,
		RequireApplication: requireApp,
		AllowDiscovered:    !requireApp,
		RefreshToken:       id.RefreshToken,
	}
	tc, err := msResolveTenantFn(ctx, database, req)
	if requireApp || id.RefreshToken == "" ||
		!(mstenant.IsCode(err, mstenant.CodeCredentialNotFound) || mstenant.IsCode(err, mstenant.CodeTenantNotLinked)) {
		return tc, err
	}
	cred, cerr := microsoftCredentialFromRequest(c, id, strings.TrimSpace(c.QueryParam("project_id")))
	if cerr != nil {
		return nil, err
	}
	if id.TenantID != "" && id.TenantID != cred.HomeTenantID() {
		return nil, err
	}
	if lerr := ensureMicrosoftHomeLink(ctx, database, cred, id.RefreshToken); lerr != nil {
		return nil, lerr
	}
	req.CredentialID = cred.ID
	return msResolveTenantFn(ctx, database, req)
}

// ensureMicrosoftHomeLink creates the credential's home-tenant link (discovered) when missing.
func ensureMicrosoftHomeLink(ctx context.Context, database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, refresh string) error {
	tid := cred.HomeTenantID()
	if tid == "" {
		return &mstenant.Error{HTTPStatus: http.StatusNotFound, Code: mstenant.CodeTenantNotLinked, Message: "the Microsoft credential has no home tenant"}
	}
	link, err := database.MicrosoftLinkRepo.Get(cred.ID, tid)
	if err != nil || link != nil {
		return err
	}
	category := repo.MicrosoftTenantCategoryHome
	if outlook.IsMSATenant(tid) {
		category = repo.MicrosoftTenantCategoryPersonal
	}
	if _, err := database.MicrosoftLinkRepo.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{
		TenantID: tid, TenantName: cred.TenantName, Category: category, HomeTenantID: tid,
	}); err != nil {
		return err
	}
	msCheckTenantAccessFn(ctx, database, cred, tid, refresh)
	return nil
}

// microsoftCredentialFromRequest loads (or, with a verified REFRESH_TOKEN, creates) the caller's
// credential for MICROSOFT_ACCOUNT_ID.
func microsoftCredentialFromRequest(c echo.Context, id *microsoftRequestIdentity, projectID string) (*repo.GoogleBackupCredentialDB, error) {
	database := microsoftDB(c)
	if id.AccountID != "" {
		cred, found, err := database.CredentialRepo.FindMicrosoftByAccount(id.UserID, id.AccountID)
		if err != nil {
			return nil, err
		}
		if found && id.RefreshToken == "" {
			return cred, nil
		}
	}
	if id.RefreshToken == "" {
		return nil, &mstenant.Error{HTTPStatus: http.StatusNotFound, Code: mstenant.CodeCredentialNotFound,
			Message: "no Microsoft credential for this account; MICROSOFT_ACCOUNT_ID and REFRESH_TOKEN are required"}
	}
	return msUpsertCredentialFn(database, id.UserID, id.AccountID, projectID, id.RefreshToken)
}

var msUpsertCredentialFn = mstenant.UpsertCredentialFromSignIn

// microsoftErrorJSON writes resolver errors with their status and code; anything else is a 500.
func microsoftErrorJSON(c echo.Context, err error) error {
	if e, ok := mstenant.AsError(err); ok {
		status := e.HTTPStatus
		if status == 0 {
			status = http.StatusForbidden
		}
		return c.JSON(status, e.Body())
	}
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
}
