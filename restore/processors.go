package restore

import (
	"context"
	"fmt"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
)

// Processor restores one object key for a service.
type Processor interface {
	Method() string
	Config() ServiceConfig
	ShouldRestoreKey(objectKey string) bool
	Setup(ctx context.Context, deps *RestoreDeps) error
	RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error
	Cleanup(ctx context.Context, deps *RestoreDeps) error
}

// Registry maps cron method names to processors.
var Registry = map[string]Processor{
	"gmail":           &gmailProcessor{},
	"google_drive":    &driveProcessor{},
	"google_photos":   &photosProcessor{},
	"google_calendar": &calendarProcessor{},
	"google_contacts": &contactsProcessor{},
}

func ProcessorForMethod(method string) (Processor, error) {
	p, ok := Registry[method]
	if !ok {
		return nil, fmt.Errorf("unsupported restore method: %s", method)
	}
	return p, nil
}

func requireGoogleToken(deps *RestoreDeps) error {
	if strings.TrimSpace(deps.GoogleToken) == "" {
		return fmt.Errorf("google access token missing")
	}
	return nil
}

// baseProcessor helpers
type gmailProcessor struct{}

func (g *gmailProcessor) Method() string { return "gmail" }
func (g *gmailProcessor) Config() ServiceConfig {
	cfg, _ := ConfigForMethod("gmail")
	return cfg
}
func (g *gmailProcessor) ShouldRestoreKey(key string) bool { return !ShouldSkipObjectKey(key) }
func (g *gmailProcessor) Setup(ctx context.Context, deps *RestoreDeps) error {
	if deps.AuthMode == RestoreAuthModeDWD {
		client, err := google.NewGmailClientWithServiceAccountDelegationForRestore(ctx, deps.GoogleWriteEmail())
		if err != nil {
			return err
		}
		deps.GmailClient = client
		return nil
	}
	if err := requireGoogleToken(deps); err != nil {
		return err
	}
	client, err := google.NewGmailClientForRestore(deps.GoogleToken)
	if err != nil {
		return err
	}
	deps.GmailClient = client
	return nil
}
func (g *gmailProcessor) RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error {
	return RestoreGmailKey(ctx, deps.AccessGrant, deps.GmailClient, objectKey)
}
func (g *gmailProcessor) Cleanup(ctx context.Context, deps *RestoreDeps) error { return nil }

type driveProcessor struct{}

func (d *driveProcessor) Method() string { return "google_drive" }
func (d *driveProcessor) Config() ServiceConfig {
	cfg, _ := ConfigForMethod("google_drive")
	return cfg
}
func (d *driveProcessor) ShouldRestoreKey(key string) bool {
	if ShouldSkipObjectKey(key) {
		return false
	}
	// Cron split layout: restore from meta only (data pulled via data_object_key).
	if google.IsDriveIDBasedDataKey(key) {
		return false
	}
	return true
}
func (d *driveProcessor) Setup(ctx context.Context, deps *RestoreDeps) error {
	if deps.AuthMode == RestoreAuthModeDWD {
		srv, err := google.GetDriveServiceForRestoreDWD(ctx, deps.GoogleWriteEmail())
		if err != nil {
			return err
		}
		deps.DriveService = srv
		return nil
	}
	if err := requireGoogleToken(deps); err != nil {
		return err
	}
	srv, err := google.GetDriveServiceUsingToken(deps.GoogleToken)
	if err != nil {
		return err
	}
	deps.DriveService = srv
	return nil
}
func (d *driveProcessor) RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error {
	return RestoreDriveKey(ctx, deps.AccessGrant, deps.DriveService, deps.GoogleWriteEmail(), objectKey)
}
func (d *driveProcessor) Cleanup(ctx context.Context, deps *RestoreDeps) error { return nil }

type photosProcessor struct{}

func (p *photosProcessor) Method() string { return "google_photos" }
func (p *photosProcessor) Config() ServiceConfig {
	cfg, _ := ConfigForMethod("google_photos")
	return cfg
}
func (p *photosProcessor) ShouldRestoreKey(key string) bool { return !ShouldSkipObjectKey(key) }
func (p *photosProcessor) Setup(ctx context.Context, deps *RestoreDeps) error {
	if deps.AuthMode == RestoreAuthModeDWD {
		client, err := google.NewGPhotosClientForRestoreDWD(ctx, deps.GoogleWriteEmail())
		if err != nil {
			return err
		}
		deps.PhotosClient = client
		return nil
	}
	if err := requireGoogleToken(deps); err != nil {
		return err
	}
	client, err := google.NewGPhotosClientForRestore(deps.GoogleToken)
	if err != nil {
		return err
	}
	deps.PhotosClient = client
	return nil
}
func (p *photosProcessor) RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error {
	return RestorePhotosKey(ctx, deps, objectKey)
}
func (p *photosProcessor) Cleanup(ctx context.Context, deps *RestoreDeps) error { return nil }

type calendarProcessor struct{}

func (c *calendarProcessor) Method() string { return "google_calendar" }
func (c *calendarProcessor) Config() ServiceConfig {
	cfg, _ := ConfigForMethod("google_calendar")
	return cfg
}
func (c *calendarProcessor) ShouldRestoreKey(key string) bool {
	return google.IsCalendarEventRestoreObjectKey(key)
}
func (c *calendarProcessor) Setup(ctx context.Context, deps *RestoreDeps) error {
	if deps.AuthMode == RestoreAuthModeDWD {
		svc, err := google.NewCalendarServiceForRestoreDWD(ctx, deps.GoogleWriteEmail())
		if err != nil {
			return err
		}
		deps.CalendarService = svc
		return nil
	}
	if err := requireGoogleToken(deps); err != nil {
		return err
	}
	svc, err := google.NewCalendarServiceForRestore(ctx, deps.GoogleToken)
	if err != nil {
		return err
	}
	deps.CalendarService = svc
	return nil
}
func (c *calendarProcessor) RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error {
	return RestoreCalendarKey(ctx, deps.AccessGrant, deps.CalendarService, objectKey)
}
func (c *calendarProcessor) Cleanup(ctx context.Context, deps *RestoreDeps) error { return nil }

type contactsProcessor struct{}

func (c *contactsProcessor) Method() string { return "google_contacts" }
func (c *contactsProcessor) Config() ServiceConfig {
	cfg, _ := ConfigForMethod("google_contacts")
	return cfg
}
func (c *contactsProcessor) ShouldRestoreKey(key string) bool {
	return google.IsContactsRestoreObjectKey(key)
}
func (c *contactsProcessor) Setup(ctx context.Context, deps *RestoreDeps) error {
	if deps.AuthMode == RestoreAuthModeDWD {
		svc, err := google.NewPeopleServiceForRestoreDWD(ctx, deps.GoogleWriteEmail())
		if err != nil {
			return err
		}
		deps.PeopleService = svc
		return nil
	}
	if err := requireGoogleToken(deps); err != nil {
		return err
	}
	svc, err := google.NewPeopleServiceForRestore(ctx, deps.GoogleToken)
	if err != nil {
		return err
	}
	deps.PeopleService = svc
	return nil
}
func (c *contactsProcessor) RestoreKey(ctx context.Context, deps *RestoreDeps, objectKey string) error {
	return RestoreContactKey(ctx, deps.AccessGrant, deps.PeopleService, objectKey)
}
func (c *contactsProcessor) Cleanup(ctx context.Context, deps *RestoreDeps) error { return nil }
