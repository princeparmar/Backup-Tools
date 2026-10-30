package microsoft

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func microsoftRestoreScopesForMethod(method string) []string {
	switch strings.TrimSpace(method) {
	case "outlook":
		return []string{"Mail.ReadWrite"}
	case "outlook_calendar":
		return []string{"Calendars.ReadWrite"}
	case "outlook_contacts":
		return []string{"Contacts.ReadWrite"}
	case "outlook_onedrive", "outlook_sharepoint":
		return []string{"Files.ReadWrite.All"}
	case "outlook_teams":
		return []string{"ChannelMessage.Send", "ChatMessage.Send"}
	default:
		return nil
	}
}

// microsoftTokenGrantedScopes reads scopes from a JWT access token `scp`/`roles`.
// Personal Microsoft accounts often mint opaque (non-JWT) access tokens — this returns nil
// for those; callers must also pass TokenResponse.Scope via microsoftGrantedScopes.
func microsoftTokenGrantedScopes(accessToken string) []string {
	accessToken = strings.TrimSpace(accessToken)
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims struct {
		Scp   string          `json:"scp"`
		Roles json.RawMessage `json:"roles"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	out := make([]string, 0, 8)
	for _, s := range strings.Fields(claims.Scp) {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(claims.Roles) > 0 {
		var roles []string
		if json.Unmarshal(claims.Roles, &roles) == nil {
			for _, r := range roles {
				r = strings.TrimSpace(r)
				if r != "" {
					out = append(out, r)
				}
			}
		}
	}
	return out
}

// microsoftTokenIsAppOnly reports an application (app-only) Graph token: idtyp "app", or roles
// without scp. Opaque tokens are never app-only tokens.
func microsoftTokenIsAppOnly(accessToken string) bool {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Idtyp string   `json:"idtyp"`
		Scp   string   `json:"scp"`
		Roles []string `json:"roles"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(claims.Idtyp)) {
	case "app":
		return true
	case "user":
		return false
	}
	return strings.TrimSpace(claims.Scp) == "" && len(claims.Roles) > 0
}

// CheckTeamsRestoreToken refuses a Teams message write with an app-only token, or with a delegated
// token whose scp lacks scope. Opaque tokens and tokens without scp are left to Graph.
func CheckTeamsRestoreToken(accessToken, scope string) error {
	if microsoftTokenIsAppOnly(accessToken) {
		return ErrTeamsRestoreNeedsUser
	}
	granted := microsoftTokenGrantedScopes(accessToken)
	if len(granted) == 0 || scope == "" {
		return nil
	}
	if missing := microsoftMissingScopes(granted, []string{scope}); len(missing) > 0 {
		return fmt.Errorf("%w: the signed-in user's token lacks %s", ErrTeamsRestoreMissingScope, strings.Join(missing, ", "))
	}
	return nil
}

// microsoftGrantedScopes merges JWT scp/roles with the OAuth token-endpoint `scope` string.
// Required for personal Outlook accounts whose access tokens are opaque (EwB... not eyJ...).
func microsoftGrantedScopes(accessToken, tokenEndpointScope string) []string {
	out := microsoftTokenGrantedScopes(accessToken)
	seen := make(map[string]struct{}, len(out))
	for _, s := range out {
		seen[strings.ToLower(s)] = struct{}{}
	}
	for _, s := range strings.Fields(tokenEndpointScope) {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Token endpoint may return full Graph URIs; normalize to the short permission name.
		if i := strings.LastIndex(s, "/"); i >= 0 && i+1 < len(s) {
			s = s[i+1:]
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

// microsoftRestoreGrantScopes is what the Grant Access popup should request. Always the
// delegated write scopes — application roles like Sites.ReadWrite.All cannot be added by
// a user login and caused the grant loop on every Microsoft service.
func microsoftRestoreGrantScopes(method string, appMissing []string) (grant []string, hint string) {
	grant = microsoftRestoreScopesForMethod(method)
	if strings.TrimSpace(method) == "outlook_sharepoint" {
		grant = []string{"Files.ReadWrite.All", "Sites.ReadWrite.All"}
	}
	if len(grant) == 0 {
		grant = appMissing
	}
	if len(grant) == 0 {
		return nil, "Grant Access with the Microsoft write permission for this service, then retry"
	}
	return grant, "Grant Access with " + strings.Join(grant, ", ") + ", then retry"
}

// delegatedWriteCovers is the Graph scopes that can perform the same restore write as required.
// Grant Access often returns Shared / personal / Sites variants instead of the exact name.
func delegatedWriteCovers(required string) []string {
	switch strings.ToLower(strings.TrimSpace(required)) {
	case "mail.readwrite", "mail.readwrite.shared":
		return []string{"Mail.ReadWrite", "Mail.ReadWrite.Shared"}
	case "calendars.readwrite", "calendars.readwrite.shared":
		return []string{"Calendars.ReadWrite", "Calendars.ReadWrite.Shared"}
	case "contacts.readwrite", "contacts.readwrite.shared":
		return []string{"Contacts.ReadWrite", "Contacts.ReadWrite.Shared"}
	case "files.readwrite.all", "files.readwrite", "sites.readwrite.all":
		return []string{"Files.ReadWrite.All", "Files.ReadWrite", "Sites.ReadWrite.All"}
	default:
		if required = strings.TrimSpace(required); required != "" {
			return []string{required}
		}
		return nil
	}
}

func delegatedScopeGranted(have map[string]struct{}, required string) bool {
	for _, alt := range delegatedWriteCovers(required) {
		if _, ok := have[strings.ToLower(alt)]; ok {
			return true
		}
	}
	return false
}

func grantedScopeSet(granted []string) map[string]struct{} {
	have := make(map[string]struct{}, len(granted))
	for _, g := range granted {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		have[strings.ToLower(g)] = struct{}{}
		if i := strings.LastIndex(g, "/"); i >= 0 && i+1 < len(g) {
			have[strings.ToLower(g[i+1:])] = struct{}{}
		}
	}
	return have
}

// delegatedRestoreScopesOK reports whether Grant Access put a write token on the signed-in
// account for this service (Mail, Calendar, Contacts, OneDrive, SharePoint, Teams).
func delegatedRestoreScopesOK(method string, granted, required []string) bool {
	if len(required) == 0 {
		required = microsoftRestoreScopesForMethod(method)
	}
	return len(microsoftMissingDelegatedScopes(granted, required)) == 0
}

func microsoftMissingDelegatedScopes(granted, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	have := grantedScopeSet(granted)
	var missing []string
	for _, r := range required {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !delegatedScopeGranted(have, r) {
			missing = append(missing, r)
		}
	}
	return missing
}

func microsoftMissingScopes(granted, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(granted))
	for _, g := range granted {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		have[strings.ToLower(g)] = struct{}{}
		if i := strings.LastIndex(g, "/"); i >= 0 && i+1 < len(g) {
			have[strings.ToLower(g[i+1:])] = struct{}{}
		}
	}
	var missing []string
	for _, r := range required {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, ok := have[strings.ToLower(r)]; !ok {
			missing = append(missing, r)
		}
	}
	return missing
}
