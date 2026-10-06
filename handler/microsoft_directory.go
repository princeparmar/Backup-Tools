package handler

import (
	"context"
	"sort"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

// msListDirectoryUsersFn lists tenant users live from Graph (overridden in tests). Organization
// users are never stored, matching Google Workspace domain users.
var (
	msListDirectoryUsersFn  = outlook.ListTenantDirectoryUsers
	msListTenantUserRolesFn = outlook.ListTenantUserRoles
)

// microsoftTenantUserRoles returns directory role names per user object ID. On failure (e.g. the
// app lacks RoleManagement.Read.Directory) roles are empty and the error message is returned so
// responses can report it instead of silently marking everyone non-admin.
func microsoftTenantUserRoles(ctx context.Context, token string) (map[string][]string, string) {
	roles, err := msListTenantUserRolesFn(ctx, token)
	if err != nil {
		logger.Warn(ctx, "microsoft directory: user roles unavailable", logger.ErrorField(err))
		return nil, err.Error()
	}
	return roles, ""
}

// microsoftDirectoryFilter filters a live directory listing.
type microsoftDirectoryFilter struct {
	Search      string
	Department  string
	OrgUnitPath string
	EnabledOnly bool
}

// filterMicrosoftDirectoryUsers applies f and sorts by display name, then email.
func filterMicrosoftDirectoryUsers(users []outlook.DirectoryUser, f microsoftDirectoryFilter) []outlook.DirectoryUser {
	search := strings.ToLower(strings.TrimSpace(f.Search))
	department := strings.TrimSpace(f.Department)
	orgUnit := ""
	if strings.TrimSpace(f.OrgUnitPath) != "" {
		orgUnit = normalizeOrgUnitPath(f.OrgUnitPath)
	}
	out := make([]outlook.DirectoryUser, 0, len(users))
	for _, u := range users {
		if f.EnabledOnly && !u.AccountEnabled {
			continue
		}
		if department != "" && !strings.EqualFold(u.Department, department) {
			continue
		}
		if orgUnit != "" && !strings.EqualFold(u.OrgUnitPath(), orgUnit) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(u.DisplayName), search) &&
			!strings.Contains(strings.ToLower(u.Mail), search) && !strings.Contains(strings.ToLower(u.UPN), search) {
			continue
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].DisplayName), strings.ToLower(out[j].DisplayName)
		if a != b {
			return a < b
		}
		return strings.ToLower(out[i].Email()) < strings.ToLower(out[j].Email())
	})
	return out
}

func microsoftDirectoryUserViews(users []outlook.DirectoryUser, userRoles map[string][]string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		roles := userRoles[u.ObjectID]
		if roles == nil {
			roles = []string{}
		}
		out = append(out, map[string]interface{}{
			"id":              u.ObjectID,
			"email":           u.Email(),
			"upn":             u.UPN,
			"display_name":    u.DisplayName,
			"department":      u.Department,
			"job_title":       u.JobTitle,
			"office_location": u.OfficeLocation,
			"enabled":         u.AccountEnabled,
			"has_license":     u.HasLicense,
			"org_unit_path":   u.OrgUnitPath(),
			"roles":           roles,
			"is_admin":        len(roles) > 0,
		})
	}
	return out
}

// microsoftOrgUnitViews counts enabled users per department-derived org unit.
func microsoftOrgUnitViews(users []outlook.DirectoryUser) []map[string]interface{} {
	counts := map[string]int{}
	for _, u := range users {
		if u.AccountEnabled {
			counts[u.OrgUnitPath()]++
		}
	}
	paths := make([]string, 0, len(counts))
	for p := range counts {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]map[string]interface{}, 0, len(paths))
	for _, p := range paths {
		name := strings.TrimPrefix(p, "/")
		if name == "" {
			name = "Root"
		}
		out = append(out, map[string]interface{}{"org_unit_path": p, "name": name, "user_count": counts[p]})
	}
	return out
}

// microsoftCapabilitySampleUsers picks licensed, enabled users for per-user capability probes.
func microsoftCapabilitySampleUsers(users []outlook.DirectoryUser, limit int) []string {
	out := make([]string, 0, limit)
	for _, u := range users {
		if len(out) >= limit {
			break
		}
		if u.AccountEnabled && u.HasLicense {
			out = append(out, u.ObjectID)
		}
	}
	return out
}

// normalizeOrgUnitPath trims and ensures a single leading slash.
func normalizeOrgUnitPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "/"
	}
	return "/" + p
}
