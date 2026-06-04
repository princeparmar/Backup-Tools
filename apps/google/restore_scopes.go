package google

import (
	"strings"

	"github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
)

// Restore account types (from Satellite onboarding).
const (
	AccountTypePersonal          = "personal"
	AccountTypeEmployeeWorkspace = "employee_workspace"
	AccountTypeAdminWorkspace    = "admin_workspace"
)

// NormalizeAccountType returns a known account type or personal as default.
func NormalizeAccountType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case AccountTypeEmployeeWorkspace:
		return AccountTypeEmployeeWorkspace
	case AccountTypeAdminWorkspace:
		return AccountTypeAdminWorkspace
	default:
		return AccountTypePersonal
	}
}

// restoreDWDScopesByService — Admin Console DWD scope URLs for restore (write) per product.
var restoreDWDScopesByService = map[string]string{
	"gmail":    gmail.MailGoogleComScope,
	"drive":    drive.DriveScope,
	"calendar": calendar.CalendarScope,
	"contacts": contactsScope,
	"photos":   photoslibrary.PhotoslibraryScope,
}

// RestoreDWDScopesMap returns all restore DWD scopes keyed by API service name (for Admin Console UI).
func RestoreDWDScopesMap() map[string]string {
	out := make(map[string]string, len(restoreDWDScopesByService))
	for k, v := range restoreDWDScopesByService {
		out[k] = v
	}
	return out
}

// AllRestoreDWDScopeURLs returns every restore DWD scope URL (admin pastes all in one delegation row).
func AllRestoreDWDScopeURLs() []string {
	return []string{
		gmail.MailGoogleComScope,
		drive.DriveScope,
		calendar.CalendarScope,
		contactsScope,
		photoslibrary.PhotoslibraryScope,
	}
}

// RestoreOAuthScopesForService returns required OAuth scopes for restore-all by API service name.
func RestoreOAuthScopesForService(service string) []string {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "gmail":
		return []string{gmail.MailGoogleComScope}
	case "drive":
		return []string{drive.DriveScope}
	case "photos":
		return []string{photoslibrary.PhotoslibraryScope}
	case "calendar":
		return []string{calendar.CalendarScope}
	case "contacts":
		return []string{contactsScope}
	default:
		return nil
	}
}

// RestoreDWDScopeForService returns the Admin Console DWD scope URL for restore for one service.
func RestoreDWDScopeForService(service string) string {
	if s, ok := restoreDWDScopesByService[strings.ToLower(strings.TrimSpace(service))]; ok {
		return s
	}
	return ""
}

// ScopeSet builds a normalized set from a space-separated scope string (tokeninfo).
func ScopeSet(scopeString string) map[string]bool {
	out := make(map[string]bool)
	for _, s := range strings.Fields(strings.TrimSpace(scopeString)) {
		if s != "" {
			out[s] = true
		}
	}
	return out
}

// TokenInfoMissingScopes returns required scopes not present in granted set.
func TokenInfoMissingScopes(grantedScopeString string, required []string) []string {
	granted := ScopeSet(grantedScopeString)
	var missing []string
	for _, req := range required {
		if req == "" {
			continue
		}
		if !granted[req] {
			missing = append(missing, req)
		}
	}
	return missing
}
