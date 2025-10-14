package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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
)

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

func AuthTokenUsingCode(code string) (string, error) {
	if code == "" {
		return "", fmt.Errorf("code is empty")
	}

	// Prepare the form data
	data := url.Values{}
	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	clientSecret := utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET")
	redirectURI := utils.GetEnvWithKey("OUTLOOK_REDIRECT_URI")

	if clientID == "" {
		return "", fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if clientSecret == "" {
		return "", fmt.Errorf("OUTLOOK_CLIENT_SECRET environment variable is not set")
	}
	if redirectURI == "" {
		return "", fmt.Errorf("OUTLOOK_REDIRECT_URI environment variable is not set")
	}

	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")

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
