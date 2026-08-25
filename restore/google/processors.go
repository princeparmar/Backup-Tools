package googlestore

import (
	"context"
	"fmt"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/restore"
)

func requireGoogleToken(deps *restore.RestoreDeps) error {
	if strings.TrimSpace(deps.GoogleToken) == "" {
		return fmt.Errorf("google access token missing")
	}
	return nil
}

type gmailProcessor struct{}

func (g *gmailProcessor) Method() string { return "gmail" }
func (g *gmailProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("gmail")
	return cfg
}
func (g *gmailProcessor) ShouldRestoreKey(key string) bool { return !restore.ShouldSkipObjectKey(key) }
func (g *gmailProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if deps.AuthMode == restore.RestoreAuthModeDWD {
		client, err := google.NewGmailClientWithServiceAccountDelegationForRestore(ctx, WriteEmail(deps))
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
func (g *gmailProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreGmailKey(ctx, deps.AccessGrant, deps.GmailClient, objectKey)
}
func (g *gmailProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type driveProcessor struct{}

func (d *driveProcessor) Method() string { return "google_drive" }
func (d *driveProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("google_drive")
	return cfg
}
func (d *driveProcessor) ShouldRestoreKey(key string) bool {
	if restore.ShouldSkipObjectKey(key) {
		return false
	}
	// Cron split layout: restore from meta only (data pulled via data_object_key).
	if google.IsDriveIDBasedDataKey(key) {
		return false
	}
	return true
}
func (d *driveProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if deps.AuthMode == restore.RestoreAuthModeDWD {
		srv, err := google.GetDriveServiceForRestoreDWD(ctx, WriteEmail(deps))
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
func (d *driveProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreDriveKey(ctx, deps.AccessGrant, deps.DriveService, WriteEmail(deps), objectKey)
}
func (d *driveProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type photosProcessor struct{}

func (p *photosProcessor) Method() string { return "google_photos" }
func (p *photosProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("google_photos")
	return cfg
}
func (p *photosProcessor) ShouldRestoreKey(key string) bool { return !restore.ShouldSkipObjectKey(key) }
func (p *photosProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if deps.AuthMode == restore.RestoreAuthModeDWD {
		client, err := google.NewGPhotosClientForRestoreDWD(ctx, WriteEmail(deps))
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
func (p *photosProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestorePhotosKey(ctx, deps, objectKey)
}
func (p *photosProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type calendarProcessor struct{}

func (c *calendarProcessor) Method() string { return "google_calendar" }
func (c *calendarProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("google_calendar")
	return cfg
}
func (c *calendarProcessor) ShouldRestoreKey(key string) bool {
	return google.IsCalendarEventRestoreObjectKey(key)
}
func (c *calendarProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if deps.AuthMode == restore.RestoreAuthModeDWD {
		svc, err := google.NewCalendarServiceForRestoreDWD(ctx, WriteEmail(deps))
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
func (c *calendarProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreCalendarKey(ctx, deps.AccessGrant, deps.CalendarService, objectKey)
}
func (c *calendarProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type contactsProcessor struct{}

func (c *contactsProcessor) Method() string { return "google_contacts" }
func (c *contactsProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("google_contacts")
	return cfg
}
func (c *contactsProcessor) ShouldRestoreKey(key string) bool {
	return google.IsContactsRestoreObjectKey(key)
}
func (c *contactsProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if deps.AuthMode == restore.RestoreAuthModeDWD {
		svc, err := google.NewPeopleServiceForRestoreDWD(ctx, WriteEmail(deps))
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
func (c *contactsProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreContactKey(ctx, deps.AccessGrant, deps.PeopleService, objectKey)
}
func (c *contactsProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

func init() {
	restore.RegisterProcessor(&gmailProcessor{})
	restore.RegisterProcessor(&driveProcessor{})
	restore.RegisterProcessor(&photosProcessor{})
	restore.RegisterProcessor(&calendarProcessor{})
	restore.RegisterProcessor(&contactsProcessor{})
	restore.RegisterGoogleAuth(MintAccessToken, RefreshAccessToken)
}
