package google

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	photoslibrary "github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"golang.org/x/oauth2/google"
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

func jwtHTTPClientForRestoreDelegation(ctx context.Context, subjectEmail string, scopes ...string) (*http.Client, error) {
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
	client := cfg.Client(ctx)
	client.Timeout = 30 * time.Second
	return client, nil
}

// NewGmailClientWithServiceAccountDelegationForRestore impersonates subject with mail.google.com scope.
func NewGmailClientWithServiceAccountDelegationForRestore(ctx context.Context, subjectEmail string) (*GmailClient, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, gmail.MailGoogleComScope)
	if err != nil {
		return nil, err
	}
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("gmail restore service: %w", err)
	}
	return &GmailClient{svc}, nil
}

// GetDriveServiceForRestoreDWD impersonates subject with drive full scope.
func GetDriveServiceForRestoreDWD(ctx context.Context, subjectEmail string) (*drive.Service, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, drive.DriveScope)
	if err != nil {
		return nil, err
	}
	return drive.NewService(ctx, option.WithHTTPClient(client))
}

// NewCalendarServiceForRestoreDWD impersonates subject with calendar write scope.
func NewCalendarServiceForRestoreDWD(ctx context.Context, subjectEmail string) (*calendar.Service, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, calendar.CalendarScope)
	if err != nil {
		return nil, err
	}
	return calendar.NewService(ctx, option.WithHTTPClient(client))
}

// NewPeopleServiceForRestoreDWD impersonates subject with contacts write scope.
func NewPeopleServiceForRestoreDWD(ctx context.Context, subjectEmail string) (*people.Service, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, contactsScope)
	if err != nil {
		return nil, err
	}
	return people.NewService(ctx, option.WithHTTPClient(client))
}

// NewGPhotosClientForRestoreDWD impersonates subject with photoslibrary write scope.
func NewGPhotosClientForRestoreDWD(ctx context.Context, subjectEmail string) (*GPotosClient, error) {
	client, err := jwtHTTPClientForRestoreDelegation(ctx, subjectEmail, photoslibrary.PhotoslibraryScope)
	if err != nil {
		return nil, err
	}
	return newGPotosClientFromHTTPClient(client)
}

// ProbeDWDRestore checks impersonation + restore scope for a service via a lightweight API call.
func ProbeDWDRestore(ctx context.Context, service, loginID string) error {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "gmail":
		return probeDWDGmail(ctx, loginID)
	case "drive":
		return probeDWDDrive(ctx, loginID)
	case "calendar":
		return probeDWDCalendar(ctx, loginID)
	case "contacts":
		return probeDWDContacts(ctx, loginID)
	case "photos":
		return probeDWDPhotos(ctx, loginID)
	default:
		return fmt.Errorf("unsupported service for DWD restore probe: %s", service)
	}
}

// ProbeDWDGmailRestore is an alias for gmail DWD probe (backward compatible).
func ProbeDWDGmailRestore(ctx context.Context, loginID string) error {
	return probeDWDGmail(ctx, loginID)
}

func probeDWDGmail(ctx context.Context, loginID string) error {
	client, err := NewGmailClientWithServiceAccountDelegationForRestore(ctx, loginID)
	if err != nil {
		return err
	}
	_, err = client.Users.GetProfile("me").Do()
	return err
}

func probeDWDDrive(ctx context.Context, loginID string) error {
	srv, err := GetDriveServiceForRestoreDWD(ctx, loginID)
	if err != nil {
		return err
	}
	_, err = srv.About.Get().Fields("user").Do()
	return err
}

func probeDWDCalendar(ctx context.Context, loginID string) error {
	srv, err := NewCalendarServiceForRestoreDWD(ctx, loginID)
	if err != nil {
		return err
	}
	_, err = srv.CalendarList.List().MaxResults(1).Do()
	return err
}

func probeDWDContacts(ctx context.Context, loginID string) error {
	srv, err := NewPeopleServiceForRestoreDWD(ctx, loginID)
	if err != nil {
		return err
	}
	_, err = srv.People.Connections.List("people/me").PageSize(1).PersonFields("names").Do()
	return err
}

func probeDWDPhotos(ctx context.Context, loginID string) error {
	client, err := NewGPhotosClientForRestoreDWD(ctx, loginID)
	if err != nil {
		return err
	}
	if client.Service == nil {
		return fmt.Errorf("photos service unavailable")
	}
	_, err = client.Service.Albums.List().PageSize(1).Do()
	return err
}
