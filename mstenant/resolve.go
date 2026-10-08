// Package mstenant resolves which Microsoft tenant a request or job acts on and mints the
// tenant-scoped Graph token for it. It is the only place that calls AuthTokenUsingRefreshToken,
// AuthTokenForTenant or AppOnlyToken for Microsoft backup, browse and restore.
package mstenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Seams (overridden in tests).
var (
	delegatedTokenFn = outlook.AuthTokenForTenantScope
	appOnlyTokenFn   = outlook.AppOnlyToken
)

// Request selects a credential and tenant.
type Request struct {
	// UserID is the Satellite user. The credential must belong to it.
	UserID string
	// CredentialID is set by internal callers (jobs, restore). Otherwise ExternalAccountID
	// (MICROSOFT_ACCOUNT_ID, the home oid) selects the credential.
	CredentialID      uint
	ExternalAccountID string
	// HomeTenantID (MICROSOFT_HOME_TENANT_ID) is only a cross-check.
	HomeTenantID string
	// TenantID is the selected tenant (MICROSOFT_TENANT_ID); empty means the home tenant.
	TenantID string
	// Capability is required in application mode ("" checks consent and list_users only).
	Capability string
	// RequireApplication refuses delegated links (organization-wide routes).
	RequireApplication bool
	// AllowDiscovered lets interactive delegated previews (browse, estimates) use a link that was
	// discovered but never connected, before onboarding connects it. Disconnected links are still
	// refused, and the access is always delegated.
	AllowDiscovered bool
	// RefreshToken overrides the stored delegated refresh token (fresher token from Satellite,
	// or a restore grant).
	RefreshToken string
	// Scope requests specific delegated scopes ("" keeps the original grant).
	Scope string
}

// Context is a resolved tenant: the link is connected and the token belongs to the tenant.
type Context struct {
	Credential  *repo.GoogleBackupCredentialDB
	Link        *repo.MicrosoftAccountTenantDB
	Tenant      *repo.MicrosoftTenantDB
	TenantID    string
	Token       string
	TokenScope  string
	Application bool
	Endpoints   outlook.CloudEndpoints
}

// Resolve checks, in this order: the credential belongs to the user, the link exists and is
// connected, consent (application mode), the capability, then mints the token. The token's tid
// must equal the requested tenant.
func Resolve(ctx context.Context, database *db.PostgresDb, req Request) (*Context, error) {
	cred, err := loadCredential(database, req)
	if err != nil {
		return nil, err
	}
	home := cred.HomeTenantID()
	if h := normalize(req.HomeTenantID); h != "" && home != "" && h != home {
		return nil, codeError(CodeTenantMismatch, "MICROSOFT_HOME_TENANT_ID does not match the credential's home tenant")
	}
	tid := normalize(req.TenantID)
	if tid == "" {
		tid = home
	}
	if tid == "" {
		return nil, codeError(CodeTenantNotLinked, "no tenant selected and the credential has no home tenant")
	}

	link, err := database.MicrosoftLinkRepo.Get(cred.ID, tid)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, codeError(CodeTenantNotLinked, "this Microsoft account is not linked to tenant "+tid)
	}
	preview := !link.Connected() && req.AllowDiscovered && !req.RequireApplication &&
		link.ConnectionState == repo.MicrosoftConnectionDiscovered
	if !link.Connected() && !preview {
		state := link.ConnectionState
		if state == "" {
			state = repo.MicrosoftConnectionDiscovered
		}
		return nil, codeError(CodeTenantDisconnected, "tenant "+tid+" is not connected for this Microsoft account ("+state+")")
	}

	var tenant *repo.MicrosoftTenantDB
	if !outlook.IsMSATenant(tid) {
		if tenant, err = database.MicrosoftTenantRepo.Get(tid); err != nil {
			return nil, err
		}
	}
	if tenant != nil && tenant.Availability != "" && tenant.Availability != repo.MicrosoftTenantAvailable {
		return nil, codeError(CodeTenantUnavailable, "tenant "+tid+" is "+tenant.Availability)
	}
	if req.RequireApplication && !(link.Connected() && link.Application()) {
		if !link.Connected() {
			return nil, codeError(CodeOrganizationModeRequired, "tenant "+tid+" is not connected for organization backup ("+link.ConnectionState+"); connect it with backup_mode=organization")
		}
		return nil, codeError(CodeOrganizationModeRequired, "tenant "+tid+" is connected for personal backup; organization backup is not enabled")
	}

	out := &Context{Credential: cred, Link: link, Tenant: tenant, TenantID: tid, Application: link.Connected() && link.Application()}
	if tenant != nil {
		out.Endpoints = outlook.EndpointsForCloud(tenant.Cloud)
	} else {
		out.Endpoints = outlook.EndpointsForCloud(outlook.MicrosoftCloudGlobal)
	}

	if out.Application {
		if !ConsentEffective(tenant) {
			status := repo.MicrosoftConsentNotRequested
			if tenant != nil {
				status = tenant.ConsentStatus
			}
			return nil, codeError(CodeConsentRequired, "tenant admin consent is not granted (status: "+status+")")
		}
		for _, capability := range []string{outlook.CapabilityListUsers, strings.TrimSpace(req.Capability)} {
			if capability != "" && !tenant.Capability(capability) {
				return nil, CapabilityDenied(tenant, capability)
			}
		}
		if err := mintApplication(ctx, database, out); err != nil {
			return nil, err
		}
		return out, nil
	}

	refresh := strings.TrimSpace(req.RefreshToken)
	if refresh == "" {
		refresh = strings.TrimSpace(cred.RefreshToken)
	}
	if err := mintDelegated(database, out, refresh, req.Scope); err != nil {
		return nil, err
	}
	return out, nil
}

func loadCredential(database *db.PostgresDb, req Request) (*repo.GoogleBackupCredentialDB, error) {
	userID := strings.TrimSpace(req.UserID)
	if req.CredentialID != 0 {
		cred, err := database.CredentialRepo.GetByID(req.CredentialID)
		if err != nil || cred == nil {
			return nil, codeError(CodeCredentialNotFound, fmt.Sprintf("Microsoft credential %d not found", req.CredentialID))
		}
		if userID != "" && strings.TrimSpace(cred.UserID) != userID {
			return nil, codeError(CodeCredentialForbidden, "the Microsoft credential belongs to another user")
		}
		if !cred.IsMicrosoft() {
			return nil, codeError(CodeCredentialNotFound, "credential is not a Microsoft account")
		}
		return cred, nil
	}
	if userID == "" || strings.TrimSpace(req.ExternalAccountID) == "" {
		return nil, codeError(CodeCredentialNotFound, "MICROSOFT_ACCOUNT_ID and a signed-in user are required")
	}
	cred, found, err := database.CredentialRepo.FindMicrosoftByAccount(userID, req.ExternalAccountID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, codeError(CodeCredentialNotFound, "no Microsoft credential for this account")
	}
	return cred, nil
}

// ConsentEffective reports admin consent that StorX can use: granted and the platform service
// principal present. A service principal alone is not consent.
func ConsentEffective(t *repo.MicrosoftTenantDB) bool {
	return t != nil && t.ConsentStatus == repo.MicrosoftConsentGranted && strings.TrimSpace(t.ServicePrincipalID) != ""
}

// CapabilityDenied is the capability_denied error for a tenant capability.
func CapabilityDenied(tenant *repo.MicrosoftTenantDB, capability string) *Error {
	e := codeError(CodeCapabilityDenied, "tenant capability "+capability+" is not available")
	e.Capability = capability
	if ce, ok := tenant.CapabilityErrorMap()[capability]; ok {
		switch {
		case ce.Role != "":
			e.Message += ": missing application role " + ce.Role
		case ce.Message != "":
			e.Message += ": " + ce.Message
		}
	}
	return e
}

func mintApplication(ctx context.Context, database *db.PostgresDb, out *Context) error {
	token, _, err := appOnlyTokenFn(ctx, out.TenantID)
	if err != nil {
		return applicationTokenError(database, out, err)
	}
	if tid, terr := outlook.TenantIDFromAccessToken(token); terr != nil || normalize(tid) != out.TenantID {
		return codeError(CodeTenantMismatch, "app-only token was issued for another tenant")
	}
	out.Token = token
	saveWorking(database, out.Link)
	return nil
}

func applicationTokenError(database *db.PostgresDb, out *Context, err error) error {
	var ae *outlook.AppOnlyTokenError
	if errors.As(err, &ae) && ae.Temporary() {
		return codeError(CodeTemporary, "Microsoft is temporarily unavailable: "+err.Error())
	}
	switch outlook.ClassifyTokenError(err) {
	case outlook.TokenErrorSecretExpired:
		_ = database.MicrosoftLinkRepo.MarkApplicationTokenError(err.Error())
		return codeError(CodeAppSecretExpired, "the StorX Microsoft application secret has expired")
	case outlook.TokenErrorTenantNotFound:
		MarkTenantUnavailable(database, out.TenantID, err.Error())
		return codeError(CodeTenantUnavailable, "tenant "+out.TenantID+" no longer exists for Microsoft")
	}
	_ = database.MicrosoftLinkRepo.SaveTokenStatus(out.Credential.ID, out.TenantID, repo.MicrosoftStatusError, err.Error())
	return codeError(CodeConsentRequired, "tenant app-only token failed: "+err.Error())
}

func mintDelegated(database *db.PostgresDb, out *Context, refresh, scope string) error {
	if refresh == "" {
		_ = database.MicrosoftLinkRepo.SaveTokenStatus(out.Credential.ID, out.TenantID, repo.MicrosoftStatusSigninRequired, "refresh token missing")
		return codeError(CodeSigninRequired, "Microsoft sign-in required: refresh token missing")
	}
	tok, err := delegatedTokenFn(refresh, authorityFor(out.TenantID), scope)
	if err != nil {
		return delegatedTokenError(database, out.Credential.ID, out.TenantID, err)
	}
	if !outlook.IsMSATenant(out.TenantID) {
		if tid, terr := outlook.TenantIDFromAccessToken(tok.AccessToken); terr != nil || normalize(tid) != out.TenantID {
			return codeError(CodeTenantMismatch, "delegated token was issued for another tenant")
		}
	}
	out.Token = tok.AccessToken
	out.TokenScope = tok.Scope
	saveWorking(database, out.Link)
	return nil
}

// delegatedTokenError records the token status on the link and maps the failure to a code.
func delegatedTokenError(database *db.PostgresDb, credentialID uint, tenantID string, err error) error {
	switch outlook.ClassifyTokenError(err) {
	case outlook.TokenErrorSigninRequired:
		_ = database.MicrosoftLinkRepo.SaveTokenStatus(credentialID, tenantID, repo.MicrosoftStatusSigninRequired, err.Error())
		return codeError(CodeSigninRequired, "Microsoft sign-in to tenant "+tenantID+" is required: "+err.Error())
	case outlook.TokenErrorConsentRequired:
		_ = database.MicrosoftLinkRepo.SaveTokenStatus(credentialID, tenantID, repo.MicrosoftStatusConsentRequired, err.Error())
		return codeError(CodeConsentRequired, "consent is required in tenant "+tenantID+": "+err.Error())
	case outlook.TokenErrorTenantNotFound:
		MarkTenantUnavailable(database, tenantID, err.Error())
		return codeError(CodeTenantUnavailable, "tenant "+tenantID+" no longer exists for Microsoft")
	case outlook.TokenErrorSecretExpired:
		_ = database.MicrosoftLinkRepo.MarkApplicationTokenError(err.Error())
		return codeError(CodeAppSecretExpired, "the StorX Microsoft application secret has expired")
	}
	var te *outlook.TokenEndpointError
	if !errors.As(err, &te) || te.StatusCode >= 500 || te.StatusCode == 429 {
		return codeError(CodeTemporary, "Microsoft token endpoint unavailable: "+err.Error())
	}
	_ = database.MicrosoftLinkRepo.SaveTokenStatus(credentialID, tenantID, repo.MicrosoftStatusError, err.Error())
	return codeError(CodeSigninRequired, "Microsoft token for tenant "+tenantID+" failed: "+err.Error())
}

func saveWorking(database *db.PostgresDb, link *repo.MicrosoftAccountTenantDB) {
	if link.TokenStatus == repo.MicrosoftStatusWorking && link.TokenError == "" {
		return
	}
	if err := database.MicrosoftLinkRepo.SaveTokenStatus(link.CredentialID, link.TenantID, repo.MicrosoftStatusWorking, ""); err == nil {
		link.TokenStatus, link.TokenError = repo.MicrosoftStatusWorking, ""
	}
}

// MarkTenantUnavailable records that Microsoft no longer knows the tenant and pauses its active jobs.
// Backups are kept.
func MarkTenantUnavailable(database *db.PostgresDb, tenantID, reason string) {
	tenantID = normalize(tenantID)
	if tenantID == "" || outlook.IsMSATenant(tenantID) {
		return
	}
	if _, err := database.MicrosoftTenantRepo.GetOrCreate(tenantID, ""); err != nil {
		return
	}
	_ = database.MicrosoftTenantRepo.SetAvailability(tenantID, repo.MicrosoftTenantUnavailable)
	if database.CronJobRepo == nil {
		return
	}
	jobs, err := database.CronJobRepo.ListActiveMicrosoftJobsByTenant(tenantID)
	if err != nil || len(jobs) == 0 {
		return
	}
	ids := make([]uint, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	_ = database.CronJobRepo.PauseJobs(ids, "Paused: Microsoft tenant is unavailable ("+strings.TrimSpace(reason)+")")
}

// authorityFor returns the token authority for a tenant. Personal accounts use /consumers.
func authorityFor(tenantID string) string {
	if outlook.IsMSATenant(tenantID) {
		return "consumers"
	}
	return tenantID
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
