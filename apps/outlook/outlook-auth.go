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
	data.Set("client_id", os.Getenv("OUTLOOK_CLIENT_ID"))
	data.Set("client_secret", os.Getenv("OUTLOOK_CLIENT_SECRET"))
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
