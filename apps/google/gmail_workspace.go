package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// WorkspaceGmailSession is a Gmail API client plus the userId to pass to users.* calls.
// With service-account delegation, the token already represents the mailbox, so APIUser must be "me".
// With the connected user's OAuth token for their own mailbox, APIUser may be "me" or their email.
type WorkspaceGmailSession struct {
	Client  *GmailClient
	APIUser string
}

// WorkspaceDelegationSetup contains the details an admin needs to configure DWD.
type WorkspaceDelegationSetup struct {
	ClientID         string   `json:"client_id"`
	RequiredScopes   []string `json:"required_scopes"`
	AdminConsolePath string   `json:"admin_console_path"`
	AdminConsoleURL  string   `json:"admin_console_url"`
}

var (
	workspaceSAOnce     sync.Once
	workspaceSAErr      error
	workspaceSAClientID string
	workspaceSATemplate *jwt.Config
)

// WorkspaceServiceAccountConfigured reports whether service-account JSON is available for delegation.
func WorkspaceServiceAccountConfigured() bool {
	_, _, err := workspaceServiceAccountConfig()
	return err == nil
}

// GetWorkspaceDelegationSetup returns service-account client ID + required scopes for UI onboarding.
func GetWorkspaceDelegationSetup() (*WorkspaceDelegationSetup, error) {
	_, clientID, err := workspaceServiceAccountConfig()
	if err != nil {
		return nil, err
	}
	return &WorkspaceDelegationSetup{
		ClientID:         clientID,
		RequiredScopes:   []string{gmail.GmailReadonlyScope},
		AdminConsolePath: "Security -> Access and Data Controls -> API controls -> Domain-wide delegation",
		AdminConsoleURL:  "https://admin.google.com/",
	}, nil
}

// loadWorkspaceServiceAccountJSON reads the Workspace service account key used for domain-wide delegation.
// The key is read from a project-root file to avoid runtime env dependency.
func loadWorkspaceServiceAccountJSON() ([]byte, error) {
	const localServiceAccountFile = "workspace-service-account.json"
	if b, err := os.ReadFile(localServiceAccountFile); err == nil {
		return b, nil
	}

	return nil, fmt.Errorf("missing %s required for delegated Gmail access", localServiceAccountFile)
}

// workspaceServiceAccountConfig returns cached service-account JWT template + client_id.
func workspaceServiceAccountConfig() (*jwt.Config, string, error) {
	workspaceSAOnce.Do(func() {
		keyJSON, err := loadWorkspaceServiceAccountJSON()
		if err != nil {
			workspaceSAErr = err
			return
		}
		var key struct {
			ClientID string `json:"client_id"`
		}
		if err := json.Unmarshal(keyJSON, &key); err != nil {
			workspaceSAErr = fmt.Errorf("parse workspace service account json: %w", err)
			return
		}
		if strings.TrimSpace(key.ClientID) == "" {
			workspaceSAErr = fmt.Errorf("workspace service account json missing client_id")
			return
		}
		cfg, err := google.JWTConfigFromJSON(keyJSON, gmail.GmailReadonlyScope)
		if err != nil {
			workspaceSAErr = fmt.Errorf("jwt config from service account json: %w", err)
			return
		}
		workspaceSAClientID = key.ClientID
		workspaceSATemplate = cfg
	})
	if workspaceSAErr != nil {
		return nil, "", workspaceSAErr
	}
	if workspaceSATemplate == nil {
		return nil, "", fmt.Errorf("workspace service account config not initialized")
	}
	return workspaceSATemplate, workspaceSAClientID, nil
}

// NewGmailClientWithServiceAccountDelegation creates a Gmail client that impersonates subjectEmail (Workspace user).
// The Admin Console must authorize this service account's client ID for domain-wide delegation with scope
// https://www.googleapis.com/auth/gmail.readonly (and the same scope string used here).
func NewGmailClientWithServiceAccountDelegation(ctx context.Context, subjectEmail string) (*GmailClient, error) {
	subjectEmail = strings.TrimSpace(subjectEmail)
	if subjectEmail == "" || strings.EqualFold(subjectEmail, "me") {
		return nil, fmt.Errorf("service account delegation requires a target user email")
	}
	templateCfg, _, err := workspaceServiceAccountConfig()
	if err != nil {
		return nil, err
	}
	cfg := &jwt.Config{
		Email:        templateCfg.Email,
		PrivateKey:   templateCfg.PrivateKey,
		PrivateKeyID: templateCfg.PrivateKeyID,
		Scopes:       append([]string(nil), templateCfg.Scopes...),
		TokenURL:     templateCfg.TokenURL,
		Subject:      subjectEmail,
		Audience:     templateCfg.Audience,
	}
	client := cfg.Client(ctx)
	client.Timeout = 30 * time.Second
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("gmail service: %w", err)
	}
	return &GmailClient{svc}, nil
}

// NewWorkspaceGmailSession picks OAuth user token vs service-account delegation so corporate mailboxes work.
// oauthAccessToken: access token from the connected account's refresh token (any time OAuth path is used).
// connectedEmail: oauth_credentials.email (admin / connected user); may be empty for legacy jobs.
// mailbox: Gmail mailbox to read — job input_data.email, or "me", or empty (treated as connected/me).
func NewWorkspaceGmailSession(ctx context.Context, oauthAccessToken, connectedEmail, mailbox string) (*WorkspaceGmailSession, error) {
	connectedEmail = strings.TrimSpace(connectedEmail)
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" {
		mailbox = "me"
	}
	isSameUser := connectedEmail != "" && strings.EqualFold(mailbox, connectedEmail)
	isMe := strings.EqualFold(mailbox, "me")

	switch {
	// Same mailbox as connected OAuth user -> use refresh-derived user token.
	case isSameUser:
		c, err := NewGmailClientUsingToken(oauthAccessToken)
		if err != nil {
			return nil, err
		}
		return &WorkspaceGmailSession{Client: c, APIUser: mailbox}, nil
	case isMe:
		c, err := NewGmailClientUsingToken(oauthAccessToken)
		if err != nil {
			return nil, err
		}
		return &WorkspaceGmailSession{Client: c, APIUser: "me"}, nil
	default:
		// Different mailbox than connected user, or legacy job without cred row -> delegation.
		c, err := NewGmailClientWithServiceAccountDelegation(ctx, mailbox)
		if err != nil {
			return nil, fmt.Errorf("delegated gmail access failed for %q (check domain-wide delegation + scopes): %w", mailbox, err)
		}
		return &WorkspaceGmailSession{Client: c, APIUser: "me"}, nil
	}
}
