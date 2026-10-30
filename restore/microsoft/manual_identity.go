package microsoft

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

// Manual restore identity copy. These strings are shown on the restore response and in the
// activity log when the signed-in Microsoft account cannot write Teams or SharePoint.
const (
	ManualRestorePersonalAccount    = "Sign in with a work or school account from the same organization that was backed up. A personal Microsoft account cannot restore Teams or SharePoint."
	ManualRestoreOtherOrganization  = "This backup belongs to another Microsoft organization. Sign in with a work account from the organization that was backed up."
	ManualRestoreWrongSelectedOrg   = "The signed-in Microsoft account is from a different organization than the one selected."
	ManualRestoreTeamsNoAccess      = "This work account cannot post in that team or chat. Sign in as someone who is a member of the original team or chat."
	ManualRestoreSharePointNoAccess = "This work account cannot write to that SharePoint site. Sign in as someone who has permission to the original site."
)

// ManualRestoreIdentityIssue reports why a signed-in token cannot restore these tenant-scoped
// keys. msg is empty when the tenant matches (including a different employee in the same org).
func ManualRestoreIdentityIssue(tokenTenant, headerTenant string, keyTenants []string) (msg string, code int) {
	tokenTenant = strings.ToLower(strings.TrimSpace(tokenTenant))
	headerTenant = strings.ToLower(strings.TrimSpace(headerTenant))
	if tokenTenant == "" {
		return "cannot determine the access token's tenant; send MICROSOFT_TENANT_ID", http.StatusBadRequest
	}
	if outlook.IsMSATenant(tokenTenant) {
		for _, keyTenant := range keyTenants {
			if keyTenant != "" && !strings.EqualFold(keyTenant, tokenTenant) {
				return ManualRestorePersonalAccount, http.StatusForbidden
			}
		}
		if headerTenant != "" && headerTenant != tokenTenant {
			return ManualRestorePersonalAccount, http.StatusForbidden
		}
	}
	if headerTenant != "" && headerTenant != tokenTenant {
		return ManualRestoreWrongSelectedOrg, http.StatusForbidden
	}
	for _, keyTenant := range keyTenants {
		if keyTenant != "" && !strings.EqualFold(keyTenant, tokenTenant) {
			return ManualRestoreOtherOrganization, http.StatusForbidden
		}
	}
	return "", 0
}

// WrapWorkspaceWriteError turns a Graph 403/404 into the Teams or SharePoint no-access message
// so the activity log does not show a raw Graph dump.
func WrapWorkspaceWriteError(kind string, err error) error {
	if err == nil || !isWorkspaceAccessDenied(err) {
		return err
	}
	msg := ManualRestoreTeamsNoAccess
	if strings.EqualFold(strings.TrimSpace(kind), "sharepoint") {
		msg = ManualRestoreSharePointNoAccess
	}
	return fmt.Errorf("%s: %w", msg, err)
}

func isWorkspaceAccessDenied(err error) bool {
	var ge *outlook.TeamsGraphError
	if errors.As(err, &ge) {
		return ge.Status == http.StatusForbidden || ge.Status == http.StatusNotFound
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "http 403") || strings.Contains(s, "http 404") ||
		strings.Contains(s, "accessdenied") || strings.Contains(s, "access denied")
}
