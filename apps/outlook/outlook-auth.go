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
	IDToken      string `json:"id_token"`
}

const (
	tokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	authURL  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
)

var tokenEndpointURL = tokenURL

// defaultScopes are the minimal delegated Microsoft Graph permissions for personal backup connect / cron.
// Organization backups (other mailboxes, Teams, Groups, SharePoint, directory) use the platform
// app-only token with admin consent instead of delegated scopes.
// Restore write scopes are separate — UI must OAuth with RestoreScopes then POST /microsoft-auth.
var defaultScopes = []string{
	"openid",
	"profile",
	"email",
	"offline_access",
	"User.Read",
	"Mail.Read",
	"Calendars.Read",
	"Contacts.Read",
	"Files.Read",
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
	// Groups restore is hidden for now; Group.ReadWrite.All is only used by it.
	// "Group.ReadWrite.All",
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
	return refreshTokenResponse(refreshToken, "")
}

// accountDetectionScopes is a subset of the sign-in grant. Requesting openid makes Microsoft
// return an id_token, whose `wids` claim carries the user's Entra directory roles.
var accountDetectionScopes = "openid profile email offline_access User.Read"

// AuthTokenResponseForAccountDetection refreshes with scopes that return an id_token alongside a
// User.Read access token, for account classification and admin-role detection.
func AuthTokenResponseForAccountDetection(refreshToken string) (*TokenResponse, error) {
	return refreshTokenResponse(refreshToken, accountDetectionScopes)
}

func refreshTokenResponse(refreshToken, scope string) (*TokenResponse, error) {
	return refreshTokenAt(tokenEndpointURL, refreshToken, scope)
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
