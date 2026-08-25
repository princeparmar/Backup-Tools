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

// defaultScopes are delegated Microsoft Graph permissions for backup connect / cron (read).
// Restore write scopes are separate — UI must OAuth with RestoreScopes then POST /microsoft-auth
// (all Microsoft Graph restore: mail, calendar, contacts, OneDrive, SharePoint, Teams, Groups).
// (same pattern as Google: backup scopes ≠ restore; POST /google-auth before restore).
// Auth uses the authorization-code + refresh-token (delegated) flow, not app-only.
// Files.Read.All is required for OneDrive and SharePoint document libraries.
// Sites.Read.All is required to list/resolve SharePoint sites (outlook_sharepoint).
// Existing users must reconnect (and often grant admin consent) after scope changes.
var defaultScopes = []string{
	"offline_access",
	"Mail.Read",
	"Mail.Read.Shared",
	"Calendars.Read",
	"Contacts.Read",
	"Files.Read.All",
	"Sites.Read.All",
	"Team.ReadBasic.All",
	"Channel.ReadBasic.All",
	"ChannelMessage.Read.All",
	"Group.Read.All",
	"Group-Conversation.Read.All",
	"openid",
	"profile",
	"email",
	"User.Read",
	"RoleManagement.Read.Directory",
}

// restoreScopes are write permissions for select-and-restore only.
// UI builds a separate Microsoft authorize URL with these scopes, exchanges the Graph
// access token via POST /microsoft-auth, then calls satellite-to-* with that JWT.
var restoreScopes = []string{
	"offline_access",
	"openid",
	"profile",
	"email",
	"User.Read",
	"Mail.ReadWrite",
	"Calendars.ReadWrite",
	"Contacts.ReadWrite",
	"Files.ReadWrite.All",
	"ChannelMessage.Send",
	"Group.ReadWrite.All",
}

// RestoreScopes returns Graph scopes for the restore OAuth consent screen.
func RestoreScopes() []string {
	out := make([]string, len(restoreScopes))
	copy(out, restoreScopes)
	return out
}

// RestoreScopesString returns space-separated restore scopes for authorize URL.
func RestoreScopesString() string {
	return strings.Join(restoreScopes, " ")
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

// BuildRestoreAuthURL builds a Microsoft authorize URL with restore write scopes only.
// UI should use this (or MicrosoftRestoreScopes on Satellite) before POST /microsoft-auth — not backup connect scopes.
func BuildRestoreAuthURL(ctx context.Context, redirectURI string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	if redirectURI == "" {
		redirectURI = utils.GetEnvWithKey("OUTLOOK_REDIRECT_URI")
	}
	if clientID == "" {
		return "", fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if redirectURI == "" {
		return "", fmt.Errorf("redirect URI is required for restore OAuth")
	}
	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_mode", "query")
	params.Set("scope", RestoreScopesString())
	params.Set("prompt", "consent")
	return authURL + "?" + params.Encode(), nil
}

// AuthTokenUsingRefreshToken mints a Graph access token from a refresh token.
func AuthTokenUsingRefreshToken(refreshToken string) (string, error) {
	tok, err := AuthTokenResponseUsingRefreshToken(refreshToken)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// AuthTokenResponseUsingRefreshToken refreshes tokens and returns the full Microsoft token
// response. Personal Microsoft accounts often return opaque (non-JWT) access tokens; granted
// scopes are then only available on TokenResponse.Scope — not in a JWT `scp` claim.
func AuthTokenResponseUsingRefreshToken(refreshToken string) (*TokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	// Common Satellite/proxy artifacts that make AAD return AADSTS9002313.
	refreshToken = strings.Trim(refreshToken, `"'`)

	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	// Access tokens are JWTs (three base64 segments); refresh tokens are opaque and usually not JWTs.
	if strings.Count(refreshToken, ".") == 2 && strings.HasPrefix(refreshToken, "eyJ") {
		return nil, fmt.Errorf("refresh token looks like an access token (JWT); store the OAuth refresh_token from the token response, not access_token")
	}

	// Prepare the form data
	data := url.Values{}
	clientID := utils.GetEnvWithKey("OUTLOOK_CLIENT_ID")
	clientSecret := utils.GetEnvWithKey("OUTLOOK_CLIENT_SECRET")

	if clientID == "" {
		return nil, fmt.Errorf("OUTLOOK_CLIENT_ID environment variable is not set")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("OUTLOOK_CLIENT_SECRET environment variable is not set")
	}

	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

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

	return &tokenResponse, nil
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
