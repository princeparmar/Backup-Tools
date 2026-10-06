package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/utils"
)

const microsoftAuthModeDelegated = "delegated"
const microsoftAuthModeApplication = "application"

// MicrosoftAuthModeApplication is stored on credentials using app-only Graph access.
const MicrosoftAuthModeApplication = microsoftAuthModeApplication

// MicrosoftAuthModeDelegated is stored on credentials using a user's refresh token.
const MicrosoftAuthModeDelegated = microsoftAuthModeDelegated

// appOnlyTokenSkew refreshes cached tokens this long before Microsoft's expiry.
const appOnlyTokenSkew = 5 * time.Minute

// microsoftLoginBaseURL is a var so tests can stub the token endpoint.
var microsoftLoginBaseURL = "https://login.microsoftonline.com"

// AppOnlyTokenError classifies a client-credentials failure for consent status mapping.
type AppOnlyTokenError struct {
	StatusCode int
	Code       string // AAD error code, e.g. invalid_client, unauthorized_client
	Message    string
}

func (e *AppOnlyTokenError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("app-only token http %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("app-only token http %d: %s", e.StatusCode, e.Message)
}

// Temporary reports Microsoft throttling / outage; callers keep previous tenant state.
func (e *AppOnlyTokenError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

type appOnlyCacheEntry struct {
	token     string
	roles     []string
	expiresAt time.Time
}

var (
	appOnlyCacheMu sync.Mutex
	appOnlyCache   = map[string]appOnlyCacheEntry{}
)

// AppOnlyToken returns a tenant-scoped app-only Graph token using the platform
// OUTLOOK_CLIENT_ID / OUTLOOK_CLIENT_SECRET, cached per tenant until shortly before expiry.
// roles are the application permissions granted to the platform app in that tenant.
func AppOnlyToken(ctx context.Context, tenantID string) (string, []string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return "", nil, fmt.Errorf("tenant_id is required for app-only auth")
	}
	key := strings.ToLower(tenantID)

	appOnlyCacheMu.Lock()
	if e, ok := appOnlyCache[key]; ok && time.Now().Before(e.expiresAt) {
		appOnlyCacheMu.Unlock()
		return e.token, append([]string(nil), e.roles...), nil
	}
	appOnlyCacheMu.Unlock()

	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	clientSecret := utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET")
	tok, expiresIn, err := requestAppOnlyToken(ctx, tenantID, clientID, clientSecret)
	if err != nil {
		return "", nil, err
	}
	roles := RolesFromAccessToken(tok)

	ttl := time.Duration(expiresIn)*time.Second - appOnlyTokenSkew
	if ttl > 0 {
		appOnlyCacheMu.Lock()
		appOnlyCache[key] = appOnlyCacheEntry{token: tok, roles: roles, expiresAt: time.Now().Add(ttl)}
		appOnlyCacheMu.Unlock()
	}
	return tok, append([]string(nil), roles...), nil
}

// InvalidateAppOnlyToken drops the cached token (after consent changes or a 401).
func InvalidateAppOnlyToken(tenantID string) {
	appOnlyCacheMu.Lock()
	delete(appOnlyCache, strings.ToLower(strings.TrimSpace(tenantID)))
	appOnlyCacheMu.Unlock()
}

func requestAppOnlyToken(ctx context.Context, tenantID, clientID, clientSecret string) (string, int, error) {
	tenantID = strings.TrimSpace(tenantID)
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return "", 0, fmt.Errorf("tenant_id, OUTLOOK_CLIENT_ID and OUTLOOK_CLIENT_SECRET are required for app-only auth")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tokenEndpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", microsoftLoginBaseURL, url.PathEscape(tenantID))
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("scope", "https://graph.microsoft.com/.default")
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var aadErr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &aadErr)
		msg := strings.TrimSpace(aadErr.ErrorDescription)
		if msg == "" {
			msg = truncateForErr(body)
		}
		return "", 0, &AppOnlyTokenError{StatusCode: resp.StatusCode, Code: aadErr.Error, Message: msg}
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("decode app-only token: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", 0, fmt.Errorf("app-only token response missing access_token")
	}
	return strings.TrimSpace(parsed.AccessToken), parsed.ExpiresIn, nil
}

// RolesFromAccessToken returns the JWT `roles` claim (application permissions). Opaque tokens yield nil.
func RolesFromAccessToken(accessToken string) []string {
	var claims struct {
		Roles []string `json:"roles"`
	}
	if err := decodeJWTClaims(accessToken, &claims); err != nil {
		return nil
	}
	out := make([]string, 0, len(claims.Roles))
	for _, r := range claims.Roles {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// HasAllRoles reports whether granted contains every required role (case-insensitive).
func HasAllRoles(granted []string, required ...string) bool {
	return len(MissingRoles(granted, required...)) == 0
}

// MissingRoles returns required roles absent from granted (case-insensitive).
func MissingRoles(granted []string, required ...string) []string {
	have := make(map[string]struct{}, len(granted))
	for _, g := range granted {
		have[strings.ToLower(strings.TrimSpace(g))] = struct{}{}
	}
	var missing []string
	for _, r := range required {
		if _, ok := have[strings.ToLower(strings.TrimSpace(r))]; !ok {
			missing = append(missing, r)
		}
	}
	return missing
}

func decodeJWTClaims(token string, out interface{}) error {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return fmt.Errorf("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("decode access token payload: %w", err)
	}
	return json.Unmarshal(payload, out)
}
