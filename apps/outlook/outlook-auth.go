package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
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
	authURL  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
)

var defaultScopes = []string{
	"offline_access",
	"Mail.ReadWrite",
	"openid",
	"profile",
	"email",
	"User.Read",
}

// BuildAuthURL builds the Microsoft OAuth authorization URL
func BuildAuthURL(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	redirectURI := utils.GetEnvWithKey("OUTLOOK_REDIRECT_URI")

	logger.Info(ctx, "Building Microsoft OAuth authorization URL",
		logger.String("base_auth_url", authURL),
		logger.String("redirect_uri", redirectURI),
		logger.String("scopes", strings.Join(defaultScopes, " ")),
	)

	if clientID == "" {
		logger.Error(ctx, "OUTLOOK_CLIENT_ID environment variable is not set")
		return "", fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if redirectURI == "" {
		logger.Error(ctx, "OUTLOOK_REDIRECT_URI environment variable is not set")
		return "", fmt.Errorf("OUTLOOK_REDIRECT_URI environment variable is not set")
	}

	scope := strings.Join(defaultScopes, " ")

	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_mode", "query")
	params.Set("scope", scope)

	finalURL := authURL + "?" + params.Encode()

	logger.Info(ctx, "Microsoft OAuth authorization URL built successfully",
		logger.String("final_url", finalURL),
		logger.String("expected_base", "https://login.microsoftonline.com"),
		logger.Bool("url_starts_with_expected", strings.HasPrefix(finalURL, "https://login.microsoftonline.com")),
	)

	return finalURL, nil
}

func AuthTokenUsingRefreshToken(refreshToken string) (string, error) {

	if refreshToken == "" {
		return "", fmt.Errorf("refresh token is empty")
	}

	// Prepare the form data
	data := url.Values{}
	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	clientSecret := utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET")

	if clientID == "" {
		return "", fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if clientSecret == "" {
		return "", fmt.Errorf("OUTLOOK_CLIENT_SECRET environment variable is not set")
	}

	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	// Create the request
	req, err := http.NewRequestWithContext(context.Background(), "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Read the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error response from server: %s", string(body))
	}

	// Parse the response
	var tokenResponse TokenResponse
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return "", fmt.Errorf("error parsing response: %v", err)
	}

	if tokenResponse.AccessToken == "" {
		return "", fmt.Errorf("received empty access token")
	}

	return tokenResponse.AccessToken, nil
}

func AuthTokenUsingCode(code string) (*TokenResponse, error) {
	if code == "" {
		return nil, fmt.Errorf("code is empty")
	}

	// Prepare the form data
	data := url.Values{}
	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	clientSecret := utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET")
	redirectURI := utils.GetEnvWithKey("OUTLOOK_REDIRECT_URI")

	if clientID == "" {
		return nil, fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("OUTLOOK_CLIENT_SECRET environment variable is not set")
	}
	if redirectURI == "" {
		return nil, fmt.Errorf("OUTLOOK_REDIRECT_URI environment variable is not set")
	}

	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")

	// Create the request
	req, err := http.NewRequestWithContext(context.Background(), "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Read the response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error response from server: %s", string(body))
	}

	// Parse the response
	var tokenResponse TokenResponse
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return nil, fmt.Errorf("error parsing response: %v", err)
	}

	if tokenResponse.AccessToken == "" {
		return nil, fmt.Errorf("received empty access token")
	}

	if tokenResponse.RefreshToken == "" {
		return nil, fmt.Errorf("received empty refresh token")
	}

	return &tokenResponse, nil
}
