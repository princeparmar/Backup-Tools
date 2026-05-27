package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/utils"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// WorkspaceGmailSession is a Gmail API client plus the userId to pass to users.* calls.
// With service-account delegation, the token already represents the mailbox, so APIUser must be "me".
// With a user OAuth token for their own mailbox, APIUser may be "me" or their email.
type WorkspaceGmailSession struct {
	Client  *GmailClient
	APIUser string
}

// workspaceDelegationScopes — DWD scope URLs per Google backup product (Admin Console paste).
var workspaceDelegationScopes = map[string]string{
	"gmail":    "https://www.googleapis.com/auth/gmail.readonly",
	"drive":    "https://www.googleapis.com/auth/drive.readonly",
	"contacts": "https://www.googleapis.com/auth/contacts.readonly",
	"calendar": "https://www.googleapis.com/auth/calendar.readonly",
	"photos":   "https://www.googleapis.com/auth/photoslibrary.readonly",
}

// WorkspaceDelegationSetup contains the details an admin needs to configure DWD.
type WorkspaceDelegationSetup struct {
	ClientID         string            `json:"client_id"`
	Scopes           map[string]string `json:"scopes"`
	AdminConsolePath string            `json:"admin_console_path"`
	AdminConsoleURL  string            `json:"admin_console_url"`
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
	scopes := make(map[string]string, len(workspaceDelegationScopes))
	for k, v := range workspaceDelegationScopes {
		scopes[k] = v
	}
	return &WorkspaceDelegationSetup{
		ClientID:         clientID,
		Scopes:           scopes,
		AdminConsolePath: "Security → Access and Data Controls → API controls → Domain-wide delegation",
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

func resolveDelegationSubject(mailbox, oauthAccountEmail string) (string, error) {
	m := strings.TrimSpace(mailbox)
	o := strings.TrimSpace(oauthAccountEmail)
	if m != "" && !strings.EqualFold(m, "me") {
		return m, nil
	}
	if o != "" {
		return o, nil
	}
	return "", fmt.Errorf("delegation requires a target user email")
}

// delegationSubjectAllowed is false for consumer @gmail.com unless GMAIL_DELEGATION_ALLOW_GMAIL_COM=true.
func delegationSubjectAllowed(email string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" || !strings.Contains(e, "@") {
		return false
	}
	if strings.HasSuffix(e, "@gmail.com") || strings.HasSuffix(e, "@googlemail.com") {
		return strings.EqualFold(strings.TrimSpace(utils.GetEnvWithKey("GMAIL_DELEGATION_ALLOW_GMAIL_COM")), "true")
	}
	return true
}

// GmailJobUsesDelegationWithoutOAuth is true when backup can skip OAuth refresh and use only domain-wide delegation.
// oauthAccountEmail is the account that holds the refresh token when it differs from the mailbox (e.g. corporate admin from parent_id).
func GmailJobUsesDelegationWithoutOAuth(mailbox, oauthAccountEmail string) bool {
	if !WorkspaceServiceAccountConfigured() {
		return false
	}
	subject, err := resolveDelegationSubject(mailbox, oauthAccountEmail)
	if err != nil {
		return false
	}
	return delegationSubjectAllowed(subject)
}

// mailbox: Gmail mailbox to read — job input_data.email, or "me", or empty (treated as primary user).
func NewWorkspaceGmailSession(ctx context.Context, oauthAccessToken, oauthAccountEmail, mailbox string) (*WorkspaceGmailSession, error) {
	oauthAccountEmail = strings.TrimSpace(oauthAccountEmail)
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" {
		mailbox = "me"
	}

	// Delegation-only: no user access token (cron Workspace backup). Impersonates mailbox or OAuth holder.
	if strings.TrimSpace(oauthAccessToken) == "" && WorkspaceServiceAccountConfigured() {
		subject, err := resolveDelegationSubject(mailbox, oauthAccountEmail)
		if err != nil {
			return nil, err
		}
		if !delegationSubjectAllowed(subject) {
			return nil, fmt.Errorf("gmail backup for %q needs OAuth refresh token, or set GMAIL_DELEGATION_ALLOW_GMAIL_COM=true for @gmail.com Workspace", subject)
		}
		c, err := NewGmailClientWithServiceAccountDelegation(ctx, subject)
		if err != nil {
			return nil, fmt.Errorf("delegated gmail access failed for %q (check domain-wide delegation + scopes): %w", subject, err)
		}
		return &WorkspaceGmailSession{Client: c, APIUser: "me"}, nil
	}

	isSameUser := oauthAccountEmail != "" && strings.EqualFold(mailbox, oauthAccountEmail)
	isMe := strings.EqualFold(mailbox, "me")

	switch {
	case isSameUser || isMe:
		c, err := NewGmailClientUsingToken(oauthAccessToken)
		if err != nil {
			return nil, err
		}
		apiUser := "me"
		if isSameUser {
			apiUser = mailbox
		}
		return &WorkspaceGmailSession{Client: c, APIUser: apiUser}, nil
	default:
		c, err := NewGmailClientWithServiceAccountDelegation(ctx, mailbox)
		if err != nil {
			return nil, fmt.Errorf("delegated gmail access failed for %q (check domain-wide delegation + scopes): %w", mailbox, err)
		}
		return &WorkspaceGmailSession{Client: c, APIUser: "me"}, nil
	}
}
