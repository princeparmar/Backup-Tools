package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MSATenantID is the Entra tenant id for Microsoft personal (consumer) accounts.
const MSATenantID = "9188040d-6c67-4c5b-b112-36a304b66dad"

const (
	AccountTypePersonal = "personal"
	// AccountTypeWorkAccount is any work or school account (admins included) until the tenant
	// grants admin consent; it says nothing about the user's role (see IsAdmin).
	AccountTypeWorkAccount = "work_account"
	// AccountTypeLegacyEmployeeWorkspace is the previous name of AccountTypeWorkAccount, still
	// present in stored credentials.
	AccountTypeLegacyEmployeeWorkspace = "employee_workspace"
	// AccountTypeAdminWorkspace is a label only; it is set from tenant consent + capabilities,
	// never from the user's delegated admin roles.
	AccountTypeAdminWorkspace = "admin_workspace"
)

// NormalizeAccountType maps stored or requested Microsoft account types to the current names
// (legacy employee_workspace becomes work_account). Unknown values are returned as "".
func NormalizeAccountType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case AccountTypePersonal:
		return AccountTypePersonal
	case AccountTypeWorkAccount, AccountTypeLegacyEmployeeWorkspace:
		return AccountTypeWorkAccount
	case AccountTypeAdminWorkspace:
		return AccountTypeAdminWorkspace
	default:
		return ""
	}
}

const (
	WorkspaceKindPersonal     = "personal"
	WorkspaceKindOrganization = "organization"
)

// MicrosoftAccountContext is the resolved account classification after OAuth (delegated token).
// AccountType is personal or work_account here; admin_workspace comes from the tenant row.
type MicrosoftAccountContext struct {
	Email           string
	AccountType     string
	WorkspaceKind   string
	TenantID        string
	TenantName      string
	IsAdmin         bool
	AdminRoles      []string
	RoleTemplateIDs []string
}

// IsMSATenant reports whether tid belongs to a Microsoft personal (consumer) account.
func IsMSATenant(tenantID string) bool {
	return strings.EqualFold(strings.TrimSpace(tenantID), MSATenantID)
}

// TenantIDFromAccessToken reads the tid claim from a JWT access token.
func TenantIDFromAccessToken(accessToken string) (string, error) {
	var claims struct {
		TID string `json:"tid"`
	}
	if err := decodeJWTClaims(accessToken, &claims); err != nil {
		return "", err
	}
	tid := strings.TrimSpace(claims.TID)
	if tid == "" {
		return "", fmt.Errorf("access token missing tid claim")
	}
	return tid, nil
}

// ResolveMicrosoftAccountContext classifies the connected Microsoft account (consumer vs work/school)
// and detects Entra Administrator roles. Work/school accounts, admins included, are work_account;
// admin roles never authorize organization backup.
func ResolveMicrosoftAccountContext(ctx context.Context, accessToken string) (*MicrosoftAccountContext, error) {
	return ResolveMicrosoftAccountContextWithIDToken(ctx, accessToken, "")
}

// ResolveMicrosoftAccountContextFromRefreshToken refreshes with account-detection scopes so the
// id_token's `wids` claim is available for admin-role detection, then classifies the account.
func ResolveMicrosoftAccountContextFromRefreshToken(ctx context.Context, refreshToken string) (*MicrosoftAccountContext, error) {
	tok, err := AuthTokenResponseForAccountDetection(refreshToken)
	if err != nil {
		return nil, err
	}
	return ResolveMicrosoftAccountContextWithIDToken(ctx, tok.AccessToken, tok.IDToken)
}

// ResolveMicrosoftAccountContextWithIDToken is ResolveMicrosoftAccountContext with an optional
// id_token from the same sign-in, used as an additional `wids` source.
func ResolveMicrosoftAccountContextWithIDToken(ctx context.Context, accessToken, idToken string) (*MicrosoftAccountContext, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}

	profile, err := graphMeProfile(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	tid, tenantName, err := tenantIDForAccountDetection(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	out := &MicrosoftAccountContext{
		Email:         profile.email(),
		TenantID:      tid,
		TenantName:    tenantName,
		AccountType:   AccountTypePersonal,
		WorkspaceKind: WorkspaceKindPersonal,
		AdminRoles:    []string{},
	}

	if IsMSATenant(tid) {
		return out, nil
	}

	out.AccountType = AccountTypeWorkAccount
	out.WorkspaceKind = WorkspaceKindOrganization
	if out.TenantName == "" {
		orgName, _ := graphOrganizationDisplayName(ctx, accessToken, tid)
		out.TenantName = orgName
	}

	roles := DetectEntraAdminRoles(ctx, accessToken, idToken)
	out.IsAdmin = roles.IsAdmin()
	out.AdminRoles = roles.Names
	if out.AdminRoles == nil {
		out.AdminRoles = []string{}
	}
	out.RoleTemplateIDs = roles.TemplateIDs
	return out, nil
}

// tenantIDForAccountDetection resolves Entra tenant id from a JWT access token or, when Microsoft
// returns an opaque token (common for consumer refresh), from Graph /organization or MSA fallback.
func tenantIDForAccountDetection(ctx context.Context, accessToken string) (tenantID, tenantName string, err error) {
	if tid, jwtErr := TenantIDFromAccessToken(accessToken); jwtErr == nil {
		if IsMSATenant(tid) {
			return tid, "", nil
		}
		name, _ := graphOrganizationDisplayName(ctx, accessToken, tid)
		return tid, name, nil
	}

	// Opaque access tokens (common for @outlook.com MSA) cannot expose tid via JWT. Only a definitive
	// /organization answer (no rows, 403 or 404) classifies the account as personal; transient or auth
	// failures are returned so callers report a detection error instead of mislabeling a work account.
	parsed, status, orgErr := graphOrganizationList(ctx, accessToken)
	if orgErr != nil {
		return "", "", fmt.Errorf("detect microsoft tenant: %w", orgErr)
	}
	switch {
	case status >= 200 && status < 300:
		if len(parsed) > 0 && strings.TrimSpace(parsed[0].ID) != "" {
			return strings.TrimSpace(parsed[0].ID), strings.TrimSpace(parsed[0].DisplayName), nil
		}
		return MSATenantID, "", nil
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return MSATenantID, "", nil
	default:
		return "", "", fmt.Errorf("detect microsoft tenant: /organization HTTP %d", status)
	}
}

type graphMeProfileRow struct {
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
	DisplayName       string `json:"displayName"`
}

func (p graphMeProfileRow) email() string {
	if e := strings.TrimSpace(p.Mail); e != "" {
		return e
	}
	return strings.TrimSpace(p.UserPrincipalName)
}

func graphMeProfile(ctx context.Context, accessToken string) (*graphMeProfileRow, error) {
	reqURL := graphBaseURL + "/me?$select=mail,userPrincipalName,displayName"
	body, status, err := graphDoJSON(ctx, accessToken, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("graph /me: HTTP %d: %s", status, truncateForErr(body))
	}
	var row graphMeProfileRow
	if err := json.Unmarshal(body, &row); err != nil {
		return nil, fmt.Errorf("parse /me: %w", err)
	}
	if row.email() == "" {
		return nil, fmt.Errorf("graph /me returned no email")
	}
	return &row, nil
}

type graphOrganizationRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

func graphOrganizationList(ctx context.Context, accessToken string) ([]graphOrganizationRow, int, error) {
	reqURL := graphBaseURL + "/organization?$select=id,displayName"
	body, status, err := graphDoJSON(ctx, accessToken, "GET", reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	if status < 200 || status >= 300 {
		return nil, status, nil
	}
	var parsed struct {
		Value []graphOrganizationRow `json:"value"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, status, fmt.Errorf("parse /organization: %w", err)
	}
	return parsed.Value, status, nil
}

func graphOrganizationDisplayName(ctx context.Context, accessToken, tenantID string) (string, error) {
	parsed, status, err := graphOrganizationList(ctx, accessToken)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("graph /organization: HTTP %d", status)
	}
	for _, org := range parsed {
		if strings.EqualFold(strings.TrimSpace(org.ID), strings.TrimSpace(tenantID)) {
			return strings.TrimSpace(org.DisplayName), nil
		}
	}
	if len(parsed) > 0 {
		return strings.TrimSpace(parsed[0].DisplayName), nil
	}
	return "", nil
}
