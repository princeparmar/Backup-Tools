package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// MSATenantID is the Entra tenant id for Microsoft personal (consumer) accounts.
const MSATenantID = "9188040d-6c67-4c48-b9bd-d25dd015834a"

// OrgBackupAdminRoleTemplateIDs — directory role templates that qualify for admin_workspace.
// v1 includes SharePoint Administrator so SharePoint-only admins are not blocked.
var OrgBackupAdminRoleTemplateIDs = []string{
	"62e90294-69f5-4237-9190-012177145947", // Global Administrator
	"fe930be7-5e62-47db-91fc-433a67967a1a", // User Administrator
	"9b895d92-2cd3-44c7-9d02-a6ac1d3ea011", // SharePoint Administrator
}

const (
	AccountTypePersonal           = "personal"
	AccountTypeEmployeeWorkspace  = "employee_workspace"
	AccountTypeAdminWorkspace     = "admin_workspace"
)

// MicrosoftAccountContext is the resolved account classification after OAuth.
type MicrosoftAccountContext struct {
	Email              string
	AccountType        string
	TenantID           string
	TenantName         string
	IsAdmin            bool
	SharePointEligible bool
	RoleTemplateIDs    []string
}

// MicrosoftDirectoryUserEntity is a tenant user row for admin pickers.
type MicrosoftDirectoryUserEntity struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}

// MicrosoftDirectoryUsersPage is a paginated directory user list.
type MicrosoftDirectoryUsersPage struct {
	Entities   []MicrosoftDirectoryUserEntity `json:"entities"`
	NextLink   string                         `json:"next_link,omitempty"`
	SkipToken  string                         `json:"skip_token,omitempty"`
}

// IsMSATenant reports whether tid belongs to a Microsoft personal (consumer) account.
func IsMSATenant(tenantID string) bool {
	return strings.EqualFold(strings.TrimSpace(tenantID), MSATenantID)
}

// CanPerformOrgBackup returns true when any assigned directory role template qualifies for org backup.
func CanPerformOrgBackup(roleTemplateIDs []string) bool {
	if len(roleTemplateIDs) == 0 {
		return false
	}
	allowed := make(map[string]struct{}, len(OrgBackupAdminRoleTemplateIDs))
	for _, id := range OrgBackupAdminRoleTemplateIDs {
		allowed[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	for _, id := range roleTemplateIDs {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(id))]; ok {
			return true
		}
	}
	return false
}

// TenantIDFromAccessToken reads the tid claim from a JWT access token.
func TenantIDFromAccessToken(accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode access token payload: %w", err)
	}
	var claims struct {
		TID string `json:"tid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse access token payload: %w", err)
	}
	tid := strings.TrimSpace(claims.TID)
	if tid == "" {
		return "", fmt.Errorf("access token missing tid claim")
	}
	return tid, nil
}

// ResolveMicrosoftAccountContext classifies the connected Microsoft account (MSA vs Entra, admin vs employee).
func ResolveMicrosoftAccountContext(ctx context.Context, accessToken string) (*MicrosoftAccountContext, error) {
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
		Email:       profile.email(),
		TenantID:    tid,
		TenantName:  tenantName,
		AccountType: AccountTypePersonal,
	}

	if IsMSATenant(tid) {
		return out, nil
	}

	if out.TenantName == "" {
		orgName, _ := graphOrganizationDisplayName(ctx, accessToken, tid)
		out.TenantName = orgName
	}

	roleTemplateIDs, _ := graphMyDirectoryRoleTemplateIDs(ctx, accessToken)
	out.RoleTemplateIDs = roleTemplateIDs
	if CanPerformOrgBackup(roleTemplateIDs) {
		out.AccountType = AccountTypeAdminWorkspace
		out.IsAdmin = true
		out.SharePointEligible = true
	} else {
		out.AccountType = AccountTypeEmployeeWorkspace
	}
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

	orgID, orgName, orgErr := graphPrimaryOrganization(ctx, accessToken)
	if orgErr == nil && strings.TrimSpace(orgID) != "" {
		return orgID, orgName, nil
	}
	// Opaque access tokens (common for @outlook.com MSA) cannot expose tid via JWT; /organization
	// is also unavailable for consumer accounts — classify as personal MSA tenant.
	return MSATenantID, "", nil
}

// ListDirectoryUsersPage lists tenant users with optional OData nextLink pagination.
func ListDirectoryUsersPage(ctx context.Context, accessToken, nextLink string, top int32) (*MicrosoftDirectoryUsersPage, error) {
	reqURL := strings.TrimSpace(nextLink)
	if reqURL == "" {
		if top <= 0 {
			top = 100
		}
		reqURL = fmt.Sprintf("%s/users?$top=%d&$select=id,mail,userPrincipalName,displayName,accountEnabled", graphBaseURL, top)
	}
	body, status, err := graphDoJSON(ctx, accessToken, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("list directory users: HTTP %d: %s", status, truncateForErr(body))
	}

	var parsed struct {
		Value    []graphUserRow `json:"value"`
		NextLink string         `json:"@odata.nextLink"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse directory users: %w", err)
	}

	page := &MicrosoftDirectoryUsersPage{
		NextLink: parsed.NextLink,
	}
	if page.NextLink != "" {
		page.SkipToken = odataSkipTokenFromNextLink(page.NextLink)
	}
	for _, u := range parsed.Value {
		entity := u.toEntity()
		if entity.Email == "" && entity.ID == "" {
			continue
		}
		page.Entities = append(page.Entities, entity)
	}
	return page, nil
}

// DirectoryUsersToEntities converts DomainUser rows to directory user entities.
func DirectoryUsersToEntities(users []DomainUser) []MicrosoftDirectoryUserEntity {
	out := make([]MicrosoftDirectoryUserEntity, 0, len(users))
	for _, u := range users {
		email := strings.TrimSpace(u.Mail)
		if email == "" {
			email = strings.TrimSpace(u.UserPrincipalName)
		}
		out = append(out, MicrosoftDirectoryUserEntity{
			ID:          strings.TrimSpace(u.ID),
			Email:       email,
			DisplayName: strings.TrimSpace(u.DisplayName),
			Enabled:     u.AccountEnabled,
		})
	}
	return out
}

type graphUserRow struct {
	ID                string `json:"id"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
	DisplayName       string `json:"displayName"`
	AccountEnabled    bool   `json:"accountEnabled"`
}

func (u graphUserRow) toEntity() MicrosoftDirectoryUserEntity {
	email := strings.TrimSpace(u.Mail)
	if email == "" {
		email = strings.TrimSpace(u.UserPrincipalName)
	}
	return MicrosoftDirectoryUserEntity{
		ID:          strings.TrimSpace(u.ID),
		Email:       email,
		DisplayName: strings.TrimSpace(u.DisplayName),
		Enabled:     u.AccountEnabled,
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

func graphPrimaryOrganization(ctx context.Context, accessToken string) (id, displayName string, err error) {
	parsed, status, err := graphOrganizationList(ctx, accessToken)
	if err != nil {
		return "", "", err
	}
	if status < 200 || status >= 300 {
		return "", "", nil
	}
	if len(parsed) == 0 {
		return "", "", nil
	}
	row := parsed[0]
	return strings.TrimSpace(row.ID), strings.TrimSpace(row.DisplayName), nil
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

func graphMyDirectoryRoleTemplateIDs(ctx context.Context, accessToken string) ([]string, error) {
	reqURL := graphBaseURL + "/me/memberOf/microsoft.graph.directoryRole?$select=roleTemplateId"
	body, status, err := graphDoJSON(ctx, accessToken, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("graph directory roles: HTTP %d", status)
	}
	var parsed struct {
		Value []struct {
			RoleTemplateID string `json:"roleTemplateId"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Value))
	for _, row := range parsed.Value {
		if id := strings.TrimSpace(row.RoleTemplateID); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

func odataSkipTokenFromNextLink(nextLink string) string {
	u, err := url.Parse(nextLink)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("$skiptoken"))
}
