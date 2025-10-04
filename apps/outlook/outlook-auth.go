package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

const (
	tokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
)

func AuthTokenUsingRefreshToken(refreshToken string) (string, error) {
	start := time.Now()

	if refreshToken == "" {
		prometheus.RecordError("outlook_auth_refresh_token_empty", "outlook")
		return "", fmt.Errorf("refresh token is empty")
	}

	// Prepare the form data
	data := url.Values{}
	clientID := os.Getenv("OUTLOOK_CLIENT_ID")
	clientSecret := os.Getenv("OUTLOOK_CLIENT_SECRET")

	if clientID == "" {
		prometheus.RecordError("outlook_auth_client_id_missing", "outlook")
		return "", fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if clientSecret == "" {
		prometheus.RecordError("outlook_auth_client_secret_missing", "outlook")
		return "", fmt.Errorf("OUTLOOK_CLIENT_SECRET environment variable is not set")
	}

	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	// Create the request
	req, err := http.NewRequestWithContext(context.Background(), "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		prometheus.RecordError("outlook_auth_request_creation_failed", "outlook")
		return "", fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		prometheus.RecordError("outlook_auth_request_send_failed", "outlook")
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Read the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		prometheus.RecordError("outlook_auth_response_read_failed", "outlook")
		return "", fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		prometheus.RecordError("outlook_auth_server_error", "outlook")
		prometheus.RecordCounter("outlook_auth_http_errors_total", 1, "component", "outlook", "status_code", fmt.Sprintf("%d", resp.StatusCode))
		return "", fmt.Errorf("error response from server: %s", string(body))
	}

	// Parse the response
	var tokenResponse TokenResponse
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		prometheus.RecordError("outlook_auth_json_parse_failed", "outlook")
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	if tokenResponse.AccessToken == "" {
		prometheus.RecordError("outlook_auth_empty_access_token", "outlook")
		return "", fmt.Errorf("received empty access token")
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_auth_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_auth_total", 1, "component", "outlook", "status", "success")
	prometheus.RecordCounter("outlook_auth_token_refreshed_total", 1, "component", "outlook")

	return tokenResponse.AccessToken, nil
}
