package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/utils"
)

// MicrosoftIdentityType is the kind of Microsoft sign-in. IsMSATenant is only used here to compute
// it; the rest of the code checks the identity type.
type MicrosoftIdentityType string

const (
	MicrosoftIdentityPersonal   MicrosoftIdentityType = "personal"
	MicrosoftIdentityWorkSchool MicrosoftIdentityType = "work_school"
)

// IdentityTypeForTenant classifies a sign-in by its home tenant.
func IdentityTypeForTenant(homeTenantID string) MicrosoftIdentityType {
	if IsMSATenant(homeTenantID) {
		return MicrosoftIdentityPersonal
	}
	return MicrosoftIdentityWorkSchool
}

// MicrosoftCloudGlobal is the only cloud supported today.
const MicrosoftCloudGlobal = "global"

// armBaseURL is a var so tests can stub Azure Resource Manager.
var armBaseURL = "https://management.azure.com"

// CloudEndpoints are the hosts for one Microsoft cloud.
type CloudEndpoints struct {
	Login string
	Graph string
	ARM   string
}

// EndpointsForCloud returns the hosts for a tenant's cloud (microsoft_tenants.cloud). Unknown or
// empty clouds resolve to global.
func EndpointsForCloud(cloud string) CloudEndpoints {
	switch strings.ToLower(strings.TrimSpace(cloud)) {
	default:
		return CloudEndpoints{Login: microsoftLoginBaseURL, Graph: graphBaseURL, ARM: armBaseURL}
	}
}

// IdentityClaims are the sign-in identity read from the id_token or access token.
type IdentityClaims struct {
	ObjectID string
	TenantID string
	Email    string
	Name     string
}

// IdentityClaimsFromTokens reads oid / tid from the id_token, falling back to the access token
// (personal accounts often return opaque access tokens).
func IdentityClaimsFromTokens(idToken, accessToken string) (IdentityClaims, error) {
	var out IdentityClaims
	for _, tok := range []string{idToken, accessToken} {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		var c struct {
			OID               string `json:"oid"`
			TID               string `json:"tid"`
			PreferredUsername string `json:"preferred_username"`
			Email             string `json:"email"`
			UPN               string `json:"upn"`
			Name              string `json:"name"`
		}
		if decodeJWTClaims(tok, &c) != nil {
			continue
		}
		if out.ObjectID == "" {
			out.ObjectID = strings.ToLower(strings.TrimSpace(c.OID))
		}
		if out.TenantID == "" {
			out.TenantID = strings.ToLower(strings.TrimSpace(c.TID))
		}
		if out.Email == "" {
			for _, e := range []string{c.Email, c.PreferredUsername, c.UPN} {
				if e = strings.TrimSpace(e); e != "" {
					out.Email = e
					break
				}
			}
		}
		if out.Name == "" {
			out.Name = strings.TrimSpace(c.Name)
		}
	}
	if out.ObjectID == "" || out.TenantID == "" {
		return out, fmt.Errorf("token has no oid/tid claims")
	}
	return out, nil
}

// Token error classes (tenant link token_status values plus tenant/app outcomes).
const (
	TokenErrorSigninRequired  = "signin_required"
	TokenErrorConsentRequired = "consent_required"
	TokenErrorTenantNotFound  = "tenant_not_found"
	TokenErrorSecretExpired   = "secret_expired"
	TokenErrorOther           = "error"
)

// TokenEndpointError is a non-2xx answer from the Microsoft token endpoint.
type TokenEndpointError struct {
	StatusCode  int
	Code        string // OAuth error, e.g. invalid_grant
	Description string
	Body        string
}

func (e *TokenEndpointError) Error() string {
	return fmt.Sprintf("error response from server: %s", e.Body)
}

var aadstsPattern = regexp.MustCompile(`AADSTS(\d+)`)

// ClassifyTokenError maps a token failure to a class. MFA / Conditional Access / not-a-member need a
// sign-in to that tenant; consent errors need admin or user consent.
func ClassifyTokenError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	var te *TokenEndpointError
	if errors.As(err, &te) {
		text = te.Code + " " + te.Description + " " + te.Body
	}
	var ae *AppOnlyTokenError
	if errors.As(err, &ae) {
		text = ae.Code + " " + ae.Message
	}
	m := aadstsPattern.FindStringSubmatch(text)
	if len(m) == 2 {
		switch m[1] {
		case "7000222", "7000215":
			return TokenErrorSecretExpired
		case "90002", "900023":
			return TokenErrorTenantNotFound
		case "65001", "65004", "650051", "650052", "50105", "90094", "90008":
			return TokenErrorConsentRequired
		case "50076", "50079", "50158", "53000", "53001", "53003", "50020", "90072", "50034", "50053", "50057", "50173", "700082", "70043", "50132", "54005":
			return TokenErrorSigninRequired
		}
	}
	if strings.Contains(text, "invalid_grant") || strings.Contains(text, "interaction_required") {
		return TokenErrorSigninRequired
	}
	return TokenErrorOther
}

// IsClientSecretExpired reports AADSTS7000222 (the platform app secret expired).
func IsClientSecretExpired(err error) bool {
	return ClassifyTokenError(err) == TokenErrorSecretExpired
}

// AuthTokenForTenant mints a delegated Graph token for one tenant by redeeming the refresh token at
// the tenant authority (/{tenantID}). /common always returns the home-tenant token.
func AuthTokenForTenant(refreshToken, tenantID string) (*TokenResponse, error) {
	return AuthTokenForTenantScope(refreshToken, tenantID, "")
}

// AuthTokenForTenantScope is AuthTokenForTenant for a specific scope (e.g. ARM). Empty scope keeps
// the scopes of the original grant.
func AuthTokenForTenantScope(refreshToken, tenantID, scope string) (*TokenResponse, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required for a tenant-scoped token")
	}
	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", microsoftLoginBaseURL, url.PathEscape(tenantID))
	return refreshTokenAt(endpoint, refreshToken, scope)
}

// ClientSecretExpiry returns the configured platform app secret expiry (OUTLOOK_CLIENT_SECRET_EXPIRES_AT,
// RFC3339 or YYYY-MM-DD). ok is false when it is not configured.
func ClientSecretExpiry() (time.Time, bool, error) {
	raw := strings.TrimSpace(utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET_EXPIRES_AT"))
	if raw == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("OUTLOOK_CLIENT_SECRET_EXPIRES_AT %q is not RFC3339 or YYYY-MM-DD", raw)
}

// ClientSecretExpiryWarning returns a warning when the secret expires within 30 days (or already did).
func ClientSecretExpiryWarning(now time.Time) string {
	expires, ok, err := ClientSecretExpiry()
	if err != nil {
		return err.Error()
	}
	if !ok {
		return ""
	}
	left := expires.Sub(now)
	switch {
	case left <= 0:
		return fmt.Sprintf("OUTLOOK_CLIENT_SECRET expired on %s; organization backups will fail until it is rotated", expires.Format("2006-01-02"))
	case left <= 30*24*time.Hour:
		return fmt.Sprintf("OUTLOOK_CLIENT_SECRET expires on %s (%d days); rotate it before then", expires.Format("2006-01-02"), int(left.Hours()/24))
	}
	return ""
}

func readTokenResponse(resp *http.Response) (*TokenResponse, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		var aad struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &aad)
		return nil, &TokenEndpointError{StatusCode: resp.StatusCode, Code: aad.Error, Description: aad.ErrorDescription, Body: string(body)}
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("error parsing response: %v", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("received empty access token")
	}
	return &tok, nil
}

func refreshTokenAt(endpoint, refreshToken, scope string) (*TokenResponse, error) {
	refreshToken = strings.Trim(strings.TrimSpace(refreshToken), `"'`)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	// Access tokens are JWTs (three base64 segments); refresh tokens are opaque and usually not JWTs.
	if strings.Count(refreshToken, ".") == 2 && strings.HasPrefix(refreshToken, "eyJ") {
		return nil, fmt.Errorf("refresh token looks like an access token (JWT); store the OAuth refresh_token from the token response, not access_token")
	}
	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	clientSecret := utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET")
	if clientID == "" {
		return nil, fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("OUTLOOK_CLIENT_SECRET environment variable is not set")
	}
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")
	if scope != "" {
		data.Set("scope", scope)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()
	return readTokenResponse(resp)
}
