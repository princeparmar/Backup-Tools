package google

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	people "google.golang.org/api/people/v1"
)

// GetWorkspaceRestoreDelegationSetup returns client ID + all restore write scopes for Admin Console.
func GetWorkspaceRestoreDelegationSetup() (*WorkspaceDelegationSetup, error) {
	_, clientID, err := workspaceServiceAccountConfig()
	if err != nil {
		return nil, err
	}
	return &WorkspaceDelegationSetup{
		ClientID:         clientID,
		Scopes:           RestoreDWDScopesMap(),
		AdminConsolePath: "Security → Access and Data Controls → API controls → Domain-wide delegation",
		AdminConsoleURL:  "https://admin.google.com/",
	}, nil
}

func jwtConfigForRestoreDelegation(subjectEmail string, scopes ...string) (*jwt.Config, error) {
	subjectEmail = strings.TrimSpace(subjectEmail)
	if subjectEmail == "" || strings.EqualFold(subjectEmail, "me") {
		return nil, fmt.Errorf("service account delegation requires a target user email")
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("at least one scope is required for restore delegation")
	}
	keyJSON, err := loadWorkspaceServiceAccountJSON()
	if err != nil {
		return nil, err
	}
	cfg, err := google.JWTConfigFromJSON(keyJSON, scopes...)
	if err != nil {
		return nil, fmt.Errorf("jwt config for restore: %w", err)
	}
	cfg.Subject = subjectEmail
	return cfg, nil
}

func jwtHTTPClientForRestoreDelegation(ctx context.Context, subjectEmail string, scopes ...string) (*http.Client, error) {
	cfg, err := jwtConfigForRestoreDelegation(subjectEmail, scopes...)
	if err != nil {
		return nil, err
	}
	client := cfg.Client(ctx)
	client.Timeout = 30 * time.Second
	return client, nil
}

// probeRestoreDWDToken verifies Admin Console DWD by minting an access token for the restore write scope.
// Do not call read APIs here — scopes like gmail.insert do not cover users.getProfile / drive.about.
func probeRestoreDWDToken(ctx context.Context, subjectEmail, service string) error {
	scope := RestoreDWDScopeForService(service)
	if scope == "" {
		return fmt.Errorf("unsupported service for DWD restore probe: %s", service)
	}
	cfg, err := jwtConfigForRestoreDelegation(subjectEmail, scope)
	if err != nil {
		return err
	}
	_, err = cfg.TokenSource(ctx).Token()
	return err
}

// NewGmailClientWithServiceAccountDelegationForRestore impersonates subject with gmail.insert scope.
func NewGmailClientWithServiceAccountDelegationForRestore(ctx context.Context, subjectEmail string) (*GmailClient, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, RestoreDWDScopeForService("gmail"))
	if err != nil {
		return nil, err
	}
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("gmail restore service: %w", err)
	}
	return &GmailClient{svc}, nil
}

// GetDriveServiceForRestoreDWD impersonates subject with drive.file scope.
func GetDriveServiceForRestoreDWD(ctx context.Context, subjectEmail string) (*drive.Service, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, RestoreDWDScopeForService("drive"))
	if err != nil {
		return nil, err
	}
	return drive.NewService(ctx, option.WithHTTPClient(client))
}

// NewCalendarServiceForRestoreDWD impersonates subject with calendar.events scope.
func NewCalendarServiceForRestoreDWD(ctx context.Context, subjectEmail string) (*calendar.Service, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, RestoreDWDScopeForService("calendar"))
	if err != nil {
		return nil, err
	}
	return calendar.NewService(ctx, option.WithHTTPClient(client))
}

// NewPeopleServiceForRestoreDWD impersonates subject with contacts write scope.
func NewPeopleServiceForRestoreDWD(ctx context.Context, subjectEmail string) (*people.Service, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, RestoreDWDScopeForService("contacts"))
	if err != nil {
		return nil, err
	}
	return people.NewService(ctx, option.WithHTTPClient(client))
}

// NewGPhotosClientForRestoreDWD impersonates subject with photoslibrary.appendonly scope.
func NewGPhotosClientForRestoreDWD(ctx context.Context, subjectEmail string) (*GPotosClient, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, RestoreDWDScopeForService("photos"))
	if err != nil {
		return nil, err
	}
	return newGPotosClientFromHTTPClient(client)
}

// ProbeDWDRestore checks that the Workspace admin authorized the restore write scope for this service
// (token mint via domain-wide delegation). Write-only scopes cannot call read probe APIs.
func ProbeDWDRestore(ctx context.Context, service, loginID string) error {
	service = strings.ToLower(strings.TrimSpace(service))
	switch service {
	case "gmail", "drive", "calendar", "contacts", "photos":
		return probeRestoreDWDToken(ctx, loginID, service)
	default:
		return fmt.Errorf("unsupported service for DWD restore probe: %s", service)
	}
}

// ProbeDWDGmailRestore is an alias for gmail DWD probe (backward compatible).
func ProbeDWDGmailRestore(ctx context.Context, loginID string) error {
	return ProbeDWDRestore(ctx, "gmail", loginID)
}
