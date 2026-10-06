package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Graph seams (overridden in tests).
var (
	msAppOnlyTokenFn         = outlook.AppOnlyToken
	msInvalidateAppOnlyFn    = outlook.InvalidateAppOnlyToken
	msEvaluateCapabilitiesFn = outlook.EvaluateCapabilities
)

// AAD errors meaning the platform service principal is gone from the tenant.
var microsoftServicePrincipalMissingCodes = []string{"AADSTS700016", "AADSTS7000229", "AADSTS500011"}

// MicrosoftConsentContract is the shared contract consent block.
type MicrosoftConsentContract struct {
	Status       string     `json:"status"`
	ConsentedBy  string     `json:"consented_by,omitempty"`
	ConsentedAt  *time.Time `json:"consented_at,omitempty"`
	GrantedRoles []string   `json:"granted_roles"`
	LastError    string     `json:"last_error,omitempty"`
}

// MicrosoftWorkspaceContract is the shared Satellite ↔ Backup-Tools workspace contract.
// account_type and is_admin are labels only; org backup is authorized by consent + capabilities.
type MicrosoftWorkspaceContract struct {
	Email            string                                   `json:"email,omitempty"`
	AccountType      string                                   `json:"account_type,omitempty"`
	WorkspaceKind    string                                   `json:"workspace_kind,omitempty"`
	TenantID         string                                   `json:"tenant_id"`
	TenantName       string                                   `json:"tenant_name,omitempty"`
	IsAdmin          bool                                     `json:"is_admin"`
	AdminRoles       []string                                 `json:"admin_roles"`
	Consent          MicrosoftConsentContract                 `json:"consent"`
	Capabilities     map[string]bool                          `json:"capabilities"`
	CapabilityErrors map[string]repo.MicrosoftCapabilityError `json:"capability_errors"`
}

// buildMicrosoftWorkspaceContract merges delegated account labels (acct may be nil) with the
// tenant authority row (tenant may be nil). appTokenOK reports a usable app-only token.
func buildMicrosoftWorkspaceContract(acct *outlook.MicrosoftAccountContext, tenant *repo.MicrosoftTenantDB, appTokenOK bool) MicrosoftWorkspaceContract {
	out := MicrosoftWorkspaceContract{
		AdminRoles:       []string{},
		Consent:          MicrosoftConsentContract{Status: repo.MicrosoftConsentNotRequested, GrantedRoles: []string{}},
		Capabilities:     map[string]bool{},
		CapabilityErrors: map[string]repo.MicrosoftCapabilityError{},
	}
	for _, name := range outlook.CapabilityOrder {
		out.Capabilities[name] = false
	}
	if acct != nil {
		out.Email = acct.Email
		out.AccountType = acct.AccountType
		out.WorkspaceKind = acct.WorkspaceKind
		out.TenantID = strings.ToLower(acct.TenantID)
		out.TenantName = acct.TenantName
		out.IsAdmin = acct.IsAdmin
		if acct.AdminRoles != nil {
			out.AdminRoles = acct.AdminRoles
		}
	}
	if tenant == nil {
		return out
	}
	out.TenantID = tenant.TenantID
	if out.TenantName == "" {
		out.TenantName = tenant.TenantName
	}
	out.Consent = MicrosoftConsentContract{
		Status:       tenant.ConsentStatus,
		ConsentedBy:  tenant.ConsentedBy,
		ConsentedAt:  tenant.ConsentedAt,
		GrantedRoles: tenant.GrantedRoleList(),
		LastError:    tenant.LastError,
	}
	granted := tenant.ConsentStatus == repo.MicrosoftConsentGranted
	for name, v := range tenant.CapabilityMap() {
		out.Capabilities[name] = v && granted
	}
	out.CapabilityErrors = tenant.CapabilityErrorMap()
	if out.AccountType != outlook.AccountTypePersonal &&
		granted && tenant.Capability(outlook.CapabilityListUsers) && appTokenOK {
		out.AccountType = outlook.AccountTypeAdminWorkspace
	}
	return out
}

// microsoftAppTokenUsable reports whether the platform app can get a tenant token. Temporary
// failures (429/5xx/network) count as usable so a Microsoft blip does not flip the label.
func microsoftAppTokenUsable(ctx context.Context, tenantID string) bool {
	_, _, err := msAppOnlyTokenFn(ctx, tenantID)
	return err == nil || isTemporaryAppTokenError(err)
}

func isTemporaryAppTokenError(err error) bool {
	if err == nil {
		return false
	}
	var tokErr *outlook.AppOnlyTokenError
	if errors.As(err, &tokErr) {
		return tokErr.Temporary()
	}
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout")
}

// consentOutcome is the mapped result of an app-only token attempt.
type consentOutcome struct {
	Status       string
	GrantedRoles []string
	LastError    string
	// KeepPrevious leaves status/roles untouched (temporary Microsoft failure).
	KeepPrevious bool
}

// classifyConsent maps an app-only token result onto a consent status.
func classifyConsent(prev *repo.MicrosoftTenantDB, roles []string, tokenErr error) consentOutcome {
	previouslyConsented := prev != nil && (prev.ConsentedAt != nil || prev.ConsentStatus == repo.MicrosoftConsentGranted)
	if tokenErr != nil {
		if isTemporaryAppTokenError(tokenErr) {
			return consentOutcome{KeepPrevious: true, LastError: tokenErr.Error()}
		}
		var tokErr *outlook.AppOnlyTokenError
		msg := tokenErr.Error()
		if errors.As(tokenErr, &tokErr) {
			for _, code := range microsoftServicePrincipalMissingCodes {
				if strings.Contains(tokErr.Message, code) || strings.Contains(tokErr.Code, code) {
					if previouslyConsented {
						return consentOutcome{Status: repo.MicrosoftConsentRevoked, GrantedRoles: []string{}, LastError: msg}
					}
					break
				}
			}
		}
		return consentOutcome{Status: repo.MicrosoftConsentAuthError, GrantedRoles: []string{}, LastError: msg}
	}
	if roles == nil {
		roles = []string{}
	}
	if len(roles) == 0 && previouslyConsented {
		return consentOutcome{Status: repo.MicrosoftConsentRevoked, GrantedRoles: roles, LastError: "application roles were removed from the tenant"}
	}
	if missing := outlook.MissingApplicationRoles(roles, outlook.ConsentRequiredRoles...); len(missing) > 0 {
		return consentOutcome{
			Status:       repo.MicrosoftConsentInsufficient,
			GrantedRoles: roles,
			LastError:    "missing application roles: " + strings.Join(missing, ", "),
		}
	}
	return consentOutcome{Status: repo.MicrosoftConsentGranted, GrantedRoles: roles}
}

// microsoftTenantService owns every write to tenant consent/capability state.
type microsoftTenantService struct {
	database *db.PostgresDb
	tenants  *repo.MicrosoftTenantRepository
}

func newMicrosoftTenantService(database *db.PostgresDb) *microsoftTenantService {
	return &microsoftTenantService{database: database, tenants: database.MicrosoftTenantRepo}
}

// CheckConsent re-acquires an app-only token, maps consent, persists it and refreshes capabilities.
// markConsented is true only for the admin-consent callback.
func (s *microsoftTenantService) CheckConsent(ctx context.Context, tenantID, tenantName, consentedBy string, markConsented bool) (*repo.MicrosoftTenantDB, error) {
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if outlook.IsMSATenant(tenantID) {
		return nil, fmt.Errorf("personal Microsoft accounts cannot grant tenant consent")
	}
	prev, err := s.tenants.GetOrCreate(tenantID, tenantName)
	if err != nil {
		return nil, err
	}

	msInvalidateAppOnlyFn(tenantID)
	token, roles, tokErr := msAppOnlyTokenFn(ctx, tenantID)
	outcome := classifyConsent(prev, roles, tokErr)

	update := repo.MicrosoftConsentUpdate{
		Status:       outcome.Status,
		GrantedRoles: outcome.GrantedRoles,
		LastError:    outcome.LastError,
	}
	if outcome.KeepPrevious {
		update.Status = prev.ConsentStatus
		update.GrantedRoles = prev.GrantedRoleList()
	}
	if markConsented && tokErr == nil {
		update.MarkConsented = true
		update.ConsentedBy = consentedBy
	}
	if err := s.tenants.SaveConsent(tenantID, update); err != nil {
		return nil, err
	}

	switch {
	case tokErr == nil && (outcome.Status == repo.MicrosoftConsentGranted || outcome.Status == repo.MicrosoftConsentInsufficient):
		if err := s.refreshCapabilities(ctx, tenantID, token, roles, prev.CapabilityMap()); err != nil {
			return nil, err
		}
	case !outcome.KeepPrevious:
		if err := s.clearCapabilities(tenantID, outcome.Status); err != nil {
			return nil, err
		}
	}

	return s.tenants.Get(tenantID)
}

// RefreshCapabilities re-probes capabilities with the cached app-only token (no consent writes
// unless the token fails, which is a consent change).
func (s *microsoftTenantService) RefreshCapabilities(ctx context.Context, tenantID string) (*repo.MicrosoftTenantDB, error) {
	tenant, err := s.tenants.Get(tenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, nil
	}
	return s.CheckConsent(ctx, tenant.TenantID, tenant.TenantName, "", false)
}

func (s *microsoftTenantService) refreshCapabilities(ctx context.Context, tenantID, token string, roles []string, previous map[string]bool) error {
	var samples []string
	if users, err := msListDirectoryUsersFn(ctx, token); err != nil {
		logger.Warn(ctx, "capability probe: sample users unavailable", logger.String("tenant_id", tenantID), logger.ErrorField(err))
	} else {
		samples = microsoftCapabilitySampleUsers(users, outlook.MaxCapabilitySampleUsers)
	}
	res := msEvaluateCapabilitiesFn(ctx, outlook.CapabilityInput{
		AccessToken:   token,
		GrantedRoles:  roles,
		SampleUserIDs: samples,
		Previous:      previous,
	})
	status := outlook.CapabilityStatusRolesOnly
	if res.Probed {
		status = outlook.CapabilityStatusProbed
	}
	errs := make(map[string]repo.MicrosoftCapabilityError, len(res.Errors))
	for k, e := range res.Errors {
		errs[k] = repo.MicrosoftCapabilityError{Code: e.Code, Role: e.Role, Message: e.Message}
	}
	return s.tenants.SaveCapabilities(tenantID, repo.MicrosoftCapabilityUpdate{
		Capabilities: res.Capabilities,
		Errors:       errs,
		Status:       status,
		ProbeVersion: outlook.CapabilityProbeVersion,
	})
}

func (s *microsoftTenantService) clearCapabilities(tenantID, consentStatus string) error {
	caps := map[string]bool{}
	errs := map[string]repo.MicrosoftCapabilityError{}
	for _, name := range outlook.CapabilityOrder {
		caps[name] = false
		errs[name] = repo.MicrosoftCapabilityError{Code: outlook.CapabilityErrForbidden, Message: "tenant consent " + consentStatus}
	}
	return s.tenants.SaveCapabilities(tenantID, repo.MicrosoftCapabilityUpdate{
		Capabilities: caps,
		Errors:       errs,
		Status:       outlook.CapabilityStatusRolesOnly,
		ProbeVersion: outlook.CapabilityProbeVersion,
	})
}

// MicrosoftOrgAccessError explains why org (application) access was refused.
type MicrosoftOrgAccessError struct {
	HTTPStatus int
	Code       string
	Capability string
	Message    string
}

func (e *MicrosoftOrgAccessError) Error() string { return e.Message }

// Body is the JSON error payload for HTTP handlers.
func (e *MicrosoftOrgAccessError) Body() map[string]interface{} {
	body := map[string]interface{}{"error": e.Message, "code": e.Code}
	if e.Capability != "" {
		body["capability"] = e.Capability
	}
	return body
}

// MicrosoftOrgAccess returns an app-only token when the tenant is authorized for org backup of
// capability ("" checks only consent + list_users). This is the single org authorization rule:
// consent granted, token obtainable, list_users true, capability true.
func MicrosoftOrgAccess(ctx context.Context, database *db.PostgresDb, tenantID, capability string) (string, *repo.MicrosoftTenantDB, error) {
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	if tenantID == "" || outlook.IsMSATenant(tenantID) {
		return "", nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusForbidden, Code: "personal_account", Message: "organization backup requires a Microsoft 365 work or school tenant"}
	}
	tenant, err := database.MicrosoftTenantRepo.Get(tenantID)
	if err != nil {
		return "", nil, err
	}
	if tenant == nil || tenant.ConsentStatus != repo.MicrosoftConsentGranted {
		status := repo.MicrosoftConsentNotRequested
		if tenant != nil {
			status = tenant.ConsentStatus
		}
		return "", tenant, &MicrosoftOrgAccessError{HTTPStatus: http.StatusForbidden, Code: "consent_" + status, Message: "tenant admin consent is not granted (status: " + status + ")"}
	}
	token, _, err := msAppOnlyTokenFn(ctx, tenantID)
	if err != nil {
		if isTemporaryAppTokenError(err) {
			return "", tenant, &MicrosoftOrgAccessError{HTTPStatus: http.StatusServiceUnavailable, Code: "temporary", Message: "Microsoft is temporarily unavailable: " + err.Error()}
		}
		return "", tenant, &MicrosoftOrgAccessError{HTTPStatus: http.StatusForbidden, Code: "app_token_failed", Message: "tenant app-only token failed: " + err.Error()}
	}
	if !tenant.Capability(outlook.CapabilityListUsers) {
		return "", tenant, capabilityDenied(tenant, outlook.CapabilityListUsers)
	}
	if capability != "" && !tenant.Capability(capability) {
		return "", tenant, capabilityDenied(tenant, capability)
	}
	return token, tenant, nil
}

func capabilityDenied(tenant *repo.MicrosoftTenantDB, capability string) *MicrosoftOrgAccessError {
	e := &MicrosoftOrgAccessError{HTTPStatus: http.StatusForbidden, Code: "capability_unavailable", Capability: capability,
		Message: "tenant capability " + capability + " is not available"}
	if ce, ok := tenant.CapabilityErrorMap()[capability]; ok {
		e.Code = ce.Code
		switch {
		case ce.Role != "":
			e.Message += ": missing application role " + ce.Role
		case ce.Message != "":
			e.Message += ": " + ce.Message
		}
	}
	return e
}

// microsoftCapabilityForService maps an onboarding/job service to its capability.
var microsoftCapabilityForService = map[string]string{
	"outlook":            outlook.CapabilityMail,
	"mail":               outlook.CapabilityMail,
	"outlook_calendar":   outlook.CapabilityCalendar,
	"calendar":           outlook.CapabilityCalendar,
	"outlook_contacts":   outlook.CapabilityContacts,
	"contacts":           outlook.CapabilityContacts,
	"outlook_onedrive":   outlook.CapabilityOneDrive,
	"onedrive":           outlook.CapabilityOneDrive,
	"outlook_sharepoint": outlook.CapabilitySharePoint,
	"sharepoint":         outlook.CapabilitySharePoint,
	"outlook_teams":      outlook.CapabilityTeamsChannel,
	"teams":              outlook.CapabilityTeamsChannel,
	"outlook_groups":     outlook.CapabilityGroups,
	"groups":             outlook.CapabilityGroups,
}

// MicrosoftCapabilityForService returns the capability required by a service or job method.
func MicrosoftCapabilityForService(service string) string {
	return microsoftCapabilityForService[strings.ToLower(strings.TrimSpace(service))]
}
