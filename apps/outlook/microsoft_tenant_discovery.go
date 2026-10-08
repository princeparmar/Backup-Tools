package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ARMDiscoveryScope is the delegated Azure Service Management permission used for tenant discovery.
const ARMDiscoveryScope = "https://management.azure.com/user_impersonation offline_access"

// Discovery outcomes. A failed discovery never means "this account has one tenant".
const (
	DiscoveryComplete          = "complete"
	DiscoveryUnavailable       = "unavailable"
	DiscoveryPermissionMissing = "permission_missing"
)

// DiscoveredTenant is one tenant ARM exposes to the signed-in identity ("accessible", not "member").
type DiscoveredTenant struct {
	TenantID      string
	DisplayName   string
	DefaultDomain string
	// TenantCategory is ARM's category: Home, ProjectedBy or ManagedBy.
	TenantCategory string
}

// DiscoveryResult is the tenant list plus how complete it is. Error explains a non-complete status.
type DiscoveryResult struct {
	Tenants []DiscoveredTenant
	Status  string
	Error   string
}

// Seams (overridden in tests).
var armTokenFn = AuthTokenForTenantScope

// DiscoverTenants lists the tenants the sign-in can reach via ARM GET /tenants. The ARM token is
// minted at the home tenant authority with user_impersonation. Failures return an empty list with
// status unavailable / permission_missing.
func DiscoverTenants(ctx context.Context, refreshToken, homeTenantID string) DiscoveryResult {
	authority := strings.TrimSpace(homeTenantID)
	if authority == "" {
		authority = "organizations"
	}
	tok, err := armTokenFn(refreshToken, authority, ARMDiscoveryScope)
	if err != nil {
		status := DiscoveryUnavailable
		if ClassifyTokenError(err) == TokenErrorConsentRequired {
			status = DiscoveryPermissionMissing
		}
		return DiscoveryResult{Status: status, Error: err.Error()}
	}
	reqURL := EndpointsForCloud(MicrosoftCloudGlobal).ARM + "/tenants?api-version=2022-12-01"
	var tenants []DiscoveredTenant
	for reqURL != "" {
		body, status, err := graphDoJSON(ctx, tok.AccessToken, http.MethodGet, reqURL, nil)
		if err != nil {
			return DiscoveryResult{Status: DiscoveryUnavailable, Error: err.Error()}
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return DiscoveryResult{Status: DiscoveryPermissionMissing, Error: fmt.Sprintf("ARM /tenants: HTTP %d", status)}
		}
		if status < 200 || status >= 300 {
			return DiscoveryResult{Status: DiscoveryUnavailable, Error: fmt.Sprintf("ARM /tenants: HTTP %d: %s", status, truncateForErr(body))}
		}
		var parsed struct {
			Value []struct {
				TenantID       string `json:"tenantId"`
				DisplayName    string `json:"displayName"`
				DefaultDomain  string `json:"defaultDomain"`
				TenantCategory string `json:"tenantCategory"`
			} `json:"value"`
			NextLink string `json:"nextLink"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return DiscoveryResult{Status: DiscoveryUnavailable, Error: err.Error()}
		}
		for _, t := range parsed.Value {
			if id := strings.ToLower(strings.TrimSpace(t.TenantID)); id != "" {
				tenants = append(tenants, DiscoveredTenant{
					TenantID: id, DisplayName: strings.TrimSpace(t.DisplayName),
					DefaultDomain: strings.TrimSpace(t.DefaultDomain), TenantCategory: strings.TrimSpace(t.TenantCategory),
				})
			}
		}
		reqURL = parsed.NextLink
	}
	return DiscoveryResult{Tenants: tenants, Status: DiscoveryComplete}
}

// TenantRole is one directory role of a user in a tenant. Scope is "/" (tenant-wide) or
// "/administrativeUnits/{id}".
type TenantRole struct {
	TemplateID string
	Name       string
	Scope      string
}

// Role read statuses.
const (
	RoleStatusKnown   = "known"
	RoleStatusUnknown = "unknown"
)

// TenantRolesResult is the outcome of reading a user's roles in one tenant.
type TenantRolesResult struct {
	Roles  []TenantRole
	Status string
	Source string // role_assignments, member_of, wids
}

// IsTenantWideAdmin reports a tenant-wide administrator role. Scoped (administrative unit) roles do
// not count toward organization backup.
func (r TenantRolesResult) IsTenantWideAdmin() bool {
	if r.Status != RoleStatusKnown {
		return false
	}
	for _, role := range r.Roles {
		if role.Scope != "/" {
			continue
		}
		if _, ok := entraAdminRoleTemplates[strings.ToLower(role.TemplateID)]; ok || isAdministratorRoleName(role.Name) {
			return true
		}
	}
	return false
}

// ReadTenantRoles reads the user's directory roles in the token's tenant. Order: role assignments
// (authoritative, with scope), then /me/memberOf directory roles (tenant-wide), then the `wids`
// hint. Only role assignments or memberOf make the status known; wids alone stays unknown.
func ReadTenantRoles(ctx context.Context, accessToken, idToken, objectID string) TenantRolesResult {
	if objectID = strings.TrimSpace(objectID); objectID != "" {
		if roles, err := roleAssignmentsForPrincipal(ctx, accessToken, objectID); err == nil {
			return TenantRolesResult{Roles: roles, Status: RoleStatusKnown, Source: "role_assignments"}
		}
	}
	if rows, err := memberOfDirectoryRolesStrict(ctx, accessToken); err == nil {
		roles := make([]TenantRole, 0, len(rows))
		for _, r := range rows {
			roles = append(roles, TenantRole{TemplateID: strings.ToLower(strings.TrimSpace(r.RoleTemplateID)), Name: strings.TrimSpace(r.DisplayName), Scope: "/"})
		}
		return TenantRolesResult{Roles: sortRoles(roles), Status: RoleStatusKnown, Source: "member_of"}
	}
	var hinted []TenantRole
	seen := map[string]struct{}{}
	for _, tok := range []string{accessToken, idToken} {
		for _, id := range widsFromToken(tok) {
			id = strings.ToLower(id)
			name, ok := entraAdminRoleTemplates[id]
			if _, dup := seen[id]; !ok || dup {
				continue
			}
			seen[id] = struct{}{}
			hinted = append(hinted, TenantRole{TemplateID: id, Name: name, Scope: "/"})
		}
	}
	return TenantRolesResult{Roles: sortRoles(hinted), Status: RoleStatusUnknown, Source: "wids"}
}

func sortRoles(roles []TenantRole) []TenantRole {
	sort.Slice(roles, func(i, j int) bool {
		if roles[i].Name != roles[j].Name {
			return roles[i].Name < roles[j].Name
		}
		return roles[i].Scope < roles[j].Scope
	})
	if roles == nil {
		return []TenantRole{}
	}
	return roles
}

func roleAssignmentsForPrincipal(ctx context.Context, accessToken, objectID string) ([]TenantRole, error) {
	filter := url.QueryEscape(fmt.Sprintf("principalId eq '%s'", objectID))
	reqURL := graphBaseURL + "/roleManagement/directory/roleAssignments?$filter=" + filter +
		"&$expand=roleDefinition($select=displayName,templateId)"
	rows, err := graphListAll[struct {
		DirectoryScopeID string `json:"directoryScopeId"`
		RoleDefinition   struct {
			DisplayName string `json:"displayName"`
			TemplateID  string `json:"templateId"`
		} `json:"roleDefinition"`
	}](ctx, accessToken, reqURL, 0)
	if err != nil {
		return nil, err
	}
	roles := make([]TenantRole, 0, len(rows))
	for _, r := range rows {
		scope := strings.TrimSpace(r.DirectoryScopeID)
		if scope == "" {
			scope = "/"
		}
		roles = append(roles, TenantRole{
			TemplateID: strings.ToLower(strings.TrimSpace(r.RoleDefinition.TemplateID)),
			Name:       strings.TrimSpace(r.RoleDefinition.DisplayName),
			Scope:      scope,
		})
	}
	return sortRoles(roles), nil
}

// memberOfDirectoryRolesStrict is memberOfDirectoryRoles but treats 403 as "could not read".
func memberOfDirectoryRolesStrict(ctx context.Context, accessToken string) ([]directoryRoleRow, error) {
	reqURL := graphBaseURL + "/me/memberOf/microsoft.graph.directoryRole?$select=roleTemplateId,displayName"
	return graphListAll[directoryRoleRow](ctx, accessToken, reqURL, 0)
}

// MeIdentity is the signed-in person's object in the token's tenant.
type MeIdentity struct {
	ObjectID string
	UserType string // Member or Guest
	Email    string
}

// GraphMeIdentity reads /me in the token's tenant.
func GraphMeIdentity(ctx context.Context, accessToken string) (MeIdentity, error) {
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, graphBaseURL+"/me?$select=id,userType,mail,userPrincipalName", nil)
	if err != nil {
		return MeIdentity{}, err
	}
	if status < 200 || status >= 300 {
		return MeIdentity{}, fmt.Errorf("graph /me: HTTP %d: %s", status, truncateForErr(body))
	}
	var row struct {
		ID       string `json:"id"`
		UserType string `json:"userType"`
		Mail     string `json:"mail"`
		UPN      string `json:"userPrincipalName"`
	}
	if err := json.Unmarshal(body, &row); err != nil {
		return MeIdentity{}, err
	}
	email := strings.TrimSpace(row.Mail)
	if email == "" {
		email = strings.TrimSpace(row.UPN)
	}
	return MeIdentity{ObjectID: strings.ToLower(strings.TrimSpace(row.ID)), UserType: strings.TrimSpace(row.UserType), Email: email}, nil
}

// ServicePrincipalForApp returns the object ID of the app's service principal in the token's tenant.
// found=false means the app has no enterprise application there (consent was never granted or was removed).
func ServicePrincipalForApp(ctx context.Context, appOnlyToken, clientID string) (id string, found bool, err error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return "", false, fmt.Errorf("client id is required")
	}
	reqURL := graphBaseURL + "/servicePrincipals(appId='" + url.PathEscape(clientID) + "')?$select=id"
	body, status, err := graphDoJSON(ctx, appOnlyToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, err
	}
	if status == http.StatusNotFound {
		return "", false, nil
	}
	if status < 200 || status >= 300 {
		return "", false, fmt.Errorf("graph servicePrincipals: HTTP %d: %s", status, truncateForErr(body))
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(parsed.ID), strings.TrimSpace(parsed.ID) != "", nil
}
