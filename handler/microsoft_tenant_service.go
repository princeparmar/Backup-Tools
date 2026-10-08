package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Graph seams (overridden in tests).
var (
	msAppOnlyTokenFn         = mstenant.AppToken
	msInvalidateAppOnlyFn    = outlook.InvalidateAppOnlyToken
	msEvaluateCapabilitiesFn = outlook.EvaluateCapabilities
	msServicePrincipalFn     = mstenant.ServicePrincipal
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

// MicrosoftWorkspaceContract is the shared Satellite ↔ Backup-Tools workspace contract for one
// tenant. account_type and is_admin are labels only; org backup is authorized by consent + capabilities.
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
	AccessState      *mstenant.AccessState                    `json:"access_state,omitempty"`
}

// buildMicrosoftWorkspaceContract builds the contract from the account's tenant access state
// (state may be nil for tenant-only answers such as the consent callback).
func buildMicrosoftWorkspaceContract(email string, state *mstenant.AccessState, tenant *repo.MicrosoftTenantDB) MicrosoftWorkspaceContract {
	if state == nil {
		link := &repo.MicrosoftAccountTenantDB{ConnectionState: repo.MicrosoftConnectionDiscovered, RoleStatus: repo.MicrosoftStatusUnknown, TokenStatus: repo.MicrosoftStatusUnknown}
		if tenant != nil {
			link.TenantID, link.TenantName, link.HomeTenantID = tenant.TenantID, tenant.TenantName, tenant.TenantID
		}
		s := mstenant.BuildAccessState(link, tenant)
		s.AccountType = ""
		state = &s
	}
	out := MicrosoftWorkspaceContract{
		Email:            strings.TrimSpace(email),
		AccountType:      state.AccountType,
		WorkspaceKind:    outlook.WorkspaceKindOrganization,
		TenantID:         state.TenantID,
		TenantName:       state.TenantName,
		IsAdmin:          state.IsAdmin,
		AdminRoles:       []string{},
		Consent:          MicrosoftConsentContract{Status: state.Consent.Status, ConsentedBy: state.Consent.ConsentedBy, ConsentedAt: state.Consent.ConsentedAt, GrantedRoles: state.Consent.GrantedRoles, LastError: state.Consent.LastError},
		Capabilities:     state.Capabilities,
		CapabilityErrors: state.CapabilityErrors,
		AccessState:      state,
	}
	if outlook.IsMSATenant(state.TenantID) {
		out.WorkspaceKind = outlook.WorkspaceKindPersonal
	}
	for _, r := range state.Roles {
		if r.Scope == "/" && r.Name != "" {
			out.AdminRoles = append(out.AdminRoles, r.Name)
		}
	}
	return out
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
	if tokErr != nil {
		mstenant.HandleAppTokenFailure(s.database, tenantID, tokErr)
	}
	outcome := classifyConsent(prev, roles, tokErr)
	if tokErr == nil {
		spID, found, spErr := msServicePrincipalFn(ctx, token)
		switch {
		case spErr != nil:
			logger.Warn(ctx, "service principal lookup failed", logger.String("tenant_id", tenantID), logger.ErrorField(spErr))
			spID = prev.ServicePrincipalID
		case !found:
			outcome = consentOutcome{Status: repo.MicrosoftConsentNotRequested, GrantedRoles: []string{}, LastError: "the StorX application is not installed in this tenant"}
			if prev.ConsentedAt != nil || prev.ConsentStatus == repo.MicrosoftConsentGranted {
				outcome.Status = repo.MicrosoftConsentRevoked
			}
		}
		if err := s.tenants.SaveServicePrincipal(tenantID, spID); err != nil {
			return nil, err
		}
	} else if outcome.Status == repo.MicrosoftConsentRevoked {
		if err := s.tenants.SaveServicePrincipal(tenantID, ""); err != nil {
			return nil, err
		}
	}

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

// MicrosoftOrgAccessError explains why an organization request was refused after authorization
// (directory lookups, unknown users).
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

// MicrosoftCapabilityForService returns the capability required by a service or job method.
func MicrosoftCapabilityForService(service string) string {
	return mstenant.CapabilityForService(service)
}
