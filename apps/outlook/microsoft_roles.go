package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

// entraAdminRoleTemplates maps built-in Microsoft Entra Administrator role template IDs to display names.
// It is used to resolve `wids` claims; it does not gate organization backup (consent + application
// permissions + capabilities do).
var entraAdminRoleTemplates = map[string]string{
	"62e90394-69f5-4237-9190-012177145e10": "Global Administrator",
	"9b895d92-2cd3-44c7-9d02-a6ac1d3ea011": "Application Administrator",
	"c430b396-e693-46cc-96f3-db01bf8bb62a": "Attack Simulation Administrator",
	"58a13ea3-c632-46ae-9ee0-9c0d43cd7f3d": "Attribute Assignment Administrator",
	"8424c6f0-a189-499e-bbd0-26c1753c96d4": "Attribute Definition Administrator",
	"c4e39bd9-1100-46d3-8c65-fb160da0071f": "Authentication Administrator",
	"0526716b-113d-4c15-b2c8-68e3c22b9f80": "Authentication Policy Administrator",
	"9f06204d-73c1-4d4c-880a-6edb90606fd8": "Azure AD Joined Device Local Administrator",
	"e3973bdf-4987-49ae-837a-ba8e231c7286": "Azure DevOps Administrator",
	"7495fdc4-34c4-4d15-a289-98788ce399fd": "Azure Information Protection Administrator",
	"aaf43236-0c0d-4d5f-883a-6955382ac081": "B2C IEF Keyset Administrator",
	"3edaf663-341e-4475-9f94-5c398ef6c070": "B2C IEF Policy Administrator",
	"b0f54661-2d74-4c50-afa3-1ec803f12efe": "Billing Administrator",
	"892c5842-a9a6-463a-8041-72aa08ca3cf6": "Cloud App Security Administrator",
	"158c047a-c907-4556-b7ef-446551a6b5f7": "Cloud Application Administrator",
	"7698a772-787b-4ac8-901f-60d6b08affd2": "Cloud Device Administrator",
	"17315797-102d-40b4-93e0-432062caca18": "Compliance Administrator",
	"b1be1c3e-b65d-4f19-8427-f6fa0d97feb9": "Conditional Access Administrator",
	"38a96431-2bdf-4b4c-8b6e-5d3d8abac1a4": "Desktop Analytics Administrator",
	"8329153b-31d0-4727-b945-745eb3bc5f31": "Domain Name Administrator",
	"44367163-eba1-44c3-98af-f5787879f96a": "Dynamics 365 Administrator",
	"3f1acade-1e04-4fbc-9b69-f0302cd84aef": "Edge Administrator",
	"29232cdf-9323-42fd-ade2-1d097af3e4de": "Exchange Administrator",
	"31392ffb-586c-42d1-9346-e59415a2cc4e": "Exchange Recipient Administrator",
	"6e591065-9bad-43ed-90f3-e9424366d2f0": "External ID User Flow Administrator",
	"0f971eea-41eb-4569-a71e-57bb8a3eff1e": "External ID User Flow Attribute Administrator",
	"be2f45a1-457d-42af-a067-6ec1fa63bc45": "External Identity Provider Administrator",
	"a9ea8996-122f-4c74-9520-8edcd192826c": "Fabric Administrator",
	"ac434307-12b9-4fa1-a708-88bf58caabc1": "Global Secure Access Administrator",
	"fdd7a751-b60b-444a-984c-02652fe8fa1c": "Groups Administrator",
	"729827e3-9c14-49f7-bb1b-9608f156bbb8": "Helpdesk Administrator",
	"8ac3fc64-6eca-42ea-9e69-59f4c7b60eb2": "Hybrid Identity Administrator",
	"45d8d3c5-c802-45c6-b32a-1d70b5e1e86e": "Identity Governance Administrator",
	"eb1f4a8d-243a-41f0-9fbd-c7cdf6c5ef7c": "Insights Administrator",
	"3a2c62db-5318-420d-8d74-23affee5d9d5": "Intune Administrator",
	"74ef975b-6605-40af-a5d2-b9539d836353": "Kaizala Administrator",
	"b5a8dcf3-09d5-43a9-a639-8e29ef291470": "Knowledge Administrator",
	"4d6ac14f-3453-41d0-bef9-a3e0c569773a": "License Administrator",
	"59d46f88-662b-457b-bceb-5c3809e5908f": "Lifecycle Workflows Administrator",
	"8c8b803f-96e1-4129-9349-20738d9f9652": "Microsoft 365 Migration Administrator",
	"d37c8bed-0711-4417-ba38-b4abe66ce4c2": "Network Administrator",
	"2b745bdf-0803-4d80-aa65-822c4493daac": "Office Apps Administrator",
	"966707d0-3269-4727-9be2-8c3a10f19b9d": "Password Administrator",
	"af78dc32-cf4d-46f9-ba4e-4428526346b5": "Permissions Management Administrator",
	"11648597-926c-4cf3-9c36-bcebb0ba8dcc": "Power Platform Administrator",
	"644ef478-e28f-4e28-b9dc-3fdde9aa0b1f": "Printer Administrator",
	"7be44c8a-adaf-4e2a-84d6-ab2649e08a13": "Privileged Authentication Administrator",
	"e8611ab8-c189-46e8-94e1-60213ab1f814": "Privileged Role Administrator",
	"0964bb5e-9bdb-4d7b-ac29-58e794862a40": "Search Administrator",
	"194ae4cb-b126-40b2-bd5b-6091b380977d": "Security Administrator",
	"f023fd81-a637-4b56-95fd-791ac0226033": "Service Support Administrator",
	"f28a1f50-f6e7-4571-818b-6a12f2af6b6c": "SharePoint Administrator",
	"75941009-915a-4869-abe7-691bff18279e": "Skype for Business Administrator",
	"69091246-20e8-4a56-aa4d-066075b2a7a8": "Teams Administrator",
	"baf37b3a-610e-45da-9e62-d9d1e5e8914b": "Teams Communications Administrator",
	"3d762c5a-1b6c-493f-843e-55a3b42923d4": "Teams Devices Administrator",
	"fe930be7-5e62-47db-91fc-433a67967a1a": "User Administrator",
	"e300d9e7-4a2b-4295-9eff-f1c78b36cc98": "Virtual Visits Administrator",
	"11451d60-acb2-45eb-a7d6-43d0f0125c13": "Windows 365 Administrator",
	"32696413-001a-46ae-978c-ce0f6b3620d2": "Windows Update Deployment Administrator",
	"810a2642-a034-447f-a5e8-41982d6d4e8b": "Yammer Administrator",
}

// EntraAdminRoles is the result of administrator-role detection for the signed-in user.
type EntraAdminRoles struct {
	TemplateIDs []string
	Names       []string
}

// IsAdmin reports whether any Entra Administrator role was detected.
func (r EntraAdminRoles) IsAdmin() bool { return len(r.Names) > 0 }

// DetectEntraAdminRoles collects every Entra Administrator role for the token's user from the
// `wids` claim of the access token and id_token, and /me/memberOf directory roles (fallback).
// The id_token carries `wids` only when the app manifest sets groupMembershipClaims to
// "DirectoryRole" or "All". Source errors are logged and do not fail detection.
func DetectEntraAdminRoles(ctx context.Context, accessToken, idToken string) EntraAdminRoles {
	set := map[string]string{} // lower(template id) or "name:<name>" -> display name

	accessWIDs, idWIDs := widsFromToken(accessToken), widsFromToken(idToken)
	for _, wids := range [][]string{accessWIDs, idWIDs} {
		for _, id := range wids {
			if name, ok := entraAdminRoleTemplates[strings.ToLower(id)]; ok {
				set[strings.ToLower(id)] = name
			}
		}
	}

	roles, err := memberOfDirectoryRoles(ctx, accessToken)
	if err != nil {
		logger.Warn(ctx, "microsoft role detection: memberOf directory roles failed", logger.ErrorField(err))
	}
	logger.Info(ctx, "microsoft role detection sources",
		logger.Bool("access_token_jwt", isJWT(accessToken)),
		logger.Any("access_token_wids", accessWIDs),
		logger.Bool("id_token_present", strings.TrimSpace(idToken) != ""),
		logger.Any("id_token_wids", idWIDs),
		logger.Any("member_of_roles", roles))
	for _, r := range roles {
		tid := strings.ToLower(strings.TrimSpace(r.RoleTemplateID))
		if name, ok := entraAdminRoleTemplates[tid]; ok {
			set[tid] = name
			continue
		}
		if isAdministratorRoleName(r.DisplayName) {
			if tid == "" {
				tid = "name:" + strings.ToLower(r.DisplayName)
			}
			set[tid] = strings.TrimSpace(r.DisplayName)
		}
	}

	return buildEntraAdminRoles(set)
}

func buildEntraAdminRoles(set map[string]string) EntraAdminRoles {
	out := EntraAdminRoles{}
	names := map[string]struct{}{}
	for key, name := range set {
		if !strings.HasPrefix(key, "name:") {
			out.TemplateIDs = append(out.TemplateIDs, key)
		}
		if _, dup := names[name]; !dup {
			names[name] = struct{}{}
			out.Names = append(out.Names, name)
		}
	}
	sort.Strings(out.TemplateIDs)
	sort.Strings(out.Names)
	return out
}

func isAdministratorRoleName(name string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(name)), "administrator")
}

func isJWT(token string) bool {
	var claims map[string]interface{}
	return decodeJWTClaims(token, &claims) == nil
}

func widsFromToken(token string) []string {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	var claims struct {
		WIDs []string `json:"wids"`
	}
	if err := decodeJWTClaims(token, &claims); err != nil {
		return nil
	}
	return claims.WIDs
}

type directoryRoleRow struct {
	RoleTemplateID string `json:"roleTemplateId"`
	DisplayName    string `json:"displayName"`
}

// ErrDirectoryRolesForbidden means the app lacks RoleManagement.Read.Directory (or
// Directory.Read.All) to read tenant directory role members.
var ErrDirectoryRolesForbidden = errors.New("listing directory roles requires RoleManagement.Read.Directory")

type tenantDirectoryRoleRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// ListTenantUserRoles maps user object ID to the names of the Entra directory roles they hold
// (app-only token), as reported by Graph. Holding any directory role is the Microsoft equivalent
// of Google's isAdmin || isDelegatedAdmin.
func ListTenantUserRoles(ctx context.Context, accessToken string) (map[string][]string, error) {
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, graphBaseURL+"/directoryRoles?$select=id,displayName", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		return nil, ErrDirectoryRolesForbidden
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("graph directory roles: HTTP %d: %s", status, truncateForErr(body))
	}
	var roles struct {
		Value []tenantDirectoryRoleRow `json:"value"`
	}
	if err := json.Unmarshal(body, &roles); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range roles.Value {
		name := strings.TrimSpace(r.DisplayName)
		reqURL := graphBaseURL + "/directoryRoles/" + url.PathEscape(r.ID) + "/members?$select=id"
		members, err := graphListAll[struct {
			Type string `json:"@odata.type"`
			ID   string `json:"id"`
		}](ctx, accessToken, reqURL, 0)
		if err != nil {
			return nil, fmt.Errorf("directory role %s members: %w", name, err)
		}
		for _, m := range members {
			id := strings.TrimSpace(m.ID)
			if id == "" || (m.Type != "" && m.Type != "#microsoft.graph.user") {
				continue
			}
			out[id] = append(out[id], name)
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, nil
}

func memberOfDirectoryRoles(ctx context.Context, accessToken string) ([]directoryRoleRow, error) {
	reqURL := graphBaseURL + "/me/memberOf/microsoft.graph.directoryRole?$select=roleTemplateId,displayName"
	var out []directoryRoleRow
	for reqURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
		if err != nil {
			return out, err
		}
		if status == http.StatusForbidden {
			return out, nil
		}
		if status < 200 || status >= 300 {
			return out, fmt.Errorf("graph directory roles: HTTP %d: %s", status, truncateForErr(body))
		}
		var parsed struct {
			Value    []directoryRoleRow `json:"value"`
			NextLink string             `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return out, err
		}
		out = append(out, parsed.Value...)
		reqURL = parsed.NextLink
	}
	return out, nil
}
