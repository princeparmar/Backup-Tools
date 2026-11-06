package outlook

import (
	"fmt"
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
