package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const microsoftAuthModeDelegated = "delegated"
const microsoftAuthModeApplication = "application"

// MicrosoftAuthModeApplication is stored on credentials using app-only Graph access.
const MicrosoftAuthModeApplication = microsoftAuthModeApplication

// AcquireMicrosoftAppOnlyToken obtains an app-only Graph token via client credentials.
func AcquireMicrosoftAppOnlyToken(ctx context.Context, tenantID, clientID, clientSecret string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("tenant_id, client_id, and client_secret are required for app-only auth")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenantID))
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("scope", "https://graph.microsoft.com/.default")
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("app-only token http %d: %s", resp.StatusCode, truncateForErr(body))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode app-only token: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", fmt.Errorf("app-only token response missing access_token")
	}
	return strings.TrimSpace(parsed.AccessToken), nil
}
