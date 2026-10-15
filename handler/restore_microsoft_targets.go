package handler

import (
	"net/http"
	"sort"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	msrestore "github.com/StorX2-0/Backup-Tools/restore/microsoft"
	"github.com/labstack/echo/v4"
)

// Seams (overridden in tests).
var (
	listRestoreOrgTenantsFn  = msrestore.ListOrganizationTenants
	listRestoreTenantUsersFn = outlook.ListTenantDirectoryUsers
)

// restoreWorkspaceTenant describes one Microsoft organization tab of the restore destination list.
type restoreWorkspaceTenant struct {
	Domain   string   `json:"domain"`
	TenantID string   `json:"tenant_id"`
	Name     string   `json:"name"`
	Domains  []string `json:"domains"`
}

// handleMicrosoftRestoreWorkspaces lists the organizations the user connected for organization
// backup (one tab per tenant, named by its default domain) or, with domain, the tenant's users.
// Like Google's workspace list, every user of the organization is a destination, not only the
// backed-up ones; readiness checks the chosen user has the mailbox or OneDrive.
func handleMicrosoftRestoreWorkspaces(c echo.Context, database *db.PostgresDb, userID, domain string) error {
	ctx := c.Request().Context()
	tenants, err := listRestoreOrgTenantsFn(ctx, database, userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	if domain == "" {
		workspaces := make([]string, 0, len(tenants))
		infos := make([]restoreWorkspaceTenant, 0, len(tenants))
		for _, t := range tenants {
			workspaces = append(workspaces, t.DefaultDomain)
			infos = append(infos, restoreWorkspaceTenant{Domain: t.DefaultDomain, TenantID: t.TenantID, Name: t.Name, Domains: t.Domains})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"workspaces": workspaces, "tenants": infos})
	}

	limit, offset, search, err := parseRestoreListParams(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	var tenant *msrestore.OrganizationTenant
	for i := range tenants {
		if strings.EqualFold(tenants[i].DefaultDomain, domain) {
			tenant = &tenants[i]
			break
		}
	}
	if tenant == nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "workspace not found"})
	}
	users, err := listRestoreTenantUsersFn(ctx, tenant.Token)
	if err != nil {
		logger.Warn(ctx, "restore destination list tenant users failed",
			logger.String("tenant_id", tenant.TenantID), logger.ErrorField(err))
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": "failed to list organization users from Microsoft"})
	}
	return respondRestoreWorkspaceMailboxes(c, restoreTenantUserEmails(users), limit, offset, search)
}

// restoreTenantUserEmails keeps enabled member users (guests are written in their home tenant).
func restoreTenantUserEmails(users []outlook.DirectoryUser) []string {
	out := make([]string, 0, len(users))
	seen := map[string]bool{}
	for _, u := range users {
		email := u.Email()
		key := strings.ToLower(email)
		if !u.AccountEnabled || email == "" || strings.Contains(strings.ToUpper(u.UPN), "#EXT#") || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, email)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// microsoftRestoreCredentialUsable reports whether a Microsoft sign-in can be a restore
// destination by itself: it is connected with delegated access to some tenant (organization
// connections are listed per tenant instead).
func microsoftRestoreCredentialUsable(database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB) (bool, error) {
	if !cred.IsMicrosoft() {
		return false, nil
	}
	links, err := database.MicrosoftLinkRepo.ListByCredential(cred.ID)
	if err != nil {
		return false, err
	}
	for i := range links {
		if links[i].Connected() && !links[i].Application() {
			return true, nil
		}
	}
	return false, nil
}
