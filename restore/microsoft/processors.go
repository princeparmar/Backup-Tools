package microsoft

import (
	"context"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type outlookMailProcessor struct{}

func (p *outlookMailProcessor) Method() string { return "outlook" }
func (p *outlookMailProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook")
	return cfg
}
func (p *outlookMailProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookMailKey(key)
}
func (p *outlookMailProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookMailProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookMailKey(ctx, deps, objectKey)
}
func (p *outlookMailProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error {
	return nil
}

type outlookCalendarProcessor struct{}

func (p *outlookCalendarProcessor) Method() string { return "outlook_calendar" }
func (p *outlookCalendarProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_calendar")
	return cfg
}
func (p *outlookCalendarProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookCalendarKey(key)
}
func (p *outlookCalendarProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookCalendarProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookCalendarKey(ctx, deps, objectKey)
}
func (p *outlookCalendarProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error {
	return nil
}

type outlookContactsProcessor struct{}

func (p *outlookContactsProcessor) Method() string { return "outlook_contacts" }
func (p *outlookContactsProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_contacts")
	return cfg
}
func (p *outlookContactsProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookContactsKey(key)
}
func (p *outlookContactsProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookContactsProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookContactKey(ctx, deps, objectKey)
}
func (p *outlookContactsProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error {
	return nil
}

type outlookOneDriveProcessor struct{}

func (p *outlookOneDriveProcessor) Method() string { return "outlook_onedrive" }
func (p *outlookOneDriveProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_onedrive")
	return cfg
}
func (p *outlookOneDriveProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookOneDriveKey(key)
}
func (p *outlookOneDriveProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookOneDriveProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookOneDriveKey(ctx, deps, objectKey)
}
func (p *outlookOneDriveProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error {
	return nil
}

type outlookSharePointProcessor struct{}

func (p *outlookSharePointProcessor) Method() string { return "outlook_sharepoint" }
func (p *outlookSharePointProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_sharepoint")
	return cfg
}
func (p *outlookSharePointProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookSharePointKey(key)
}
func (p *outlookSharePointProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookSharePointProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	if _, ok := outlook.ParseOneDriveObjectKey(objectKey); ok {
		return restoreLibraryKey(ctx, deps, satellite.ReserveBucket_OutlookSharePoint, "outlook_sharepoint", objectKey)
	}
	return RestoreOutlookSharePointKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookSharePointProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error {
	return nil
}

type outlookTeamsProcessor struct{}

func (p *outlookTeamsProcessor) Method() string { return "outlook_teams" }
func (p *outlookTeamsProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_teams")
	return cfg
}
func (p *outlookTeamsProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookTeamsKey(key)
}
func (p *outlookTeamsProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if err := RequireToken(deps); err != nil {
		return err
	}
	if deps.MicrosoftApplication || microsoftTokenIsAppOnly(deps.MicrosoftToken) {
		return ErrTeamsRestoreNeedsUser
	}
	return nil
}
func (p *outlookTeamsProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	s := &TeamsRestoreSession{AccessGrant: deps.AccessGrant, Token: deps.MicrosoftToken,
		Application: deps.MicrosoftApplication, TenantID: teamsRestoreTenant(deps), State: &deps.ContainerIDs}
	return s.Restore(ctx, objectKey)
}

// teamsRestoreTenant is the tenant of the restore job, else of its backup job.
func teamsRestoreTenant(deps *restore.RestoreDeps) string {
	if deps.Job != nil && strings.TrimSpace(deps.Job.TenantID) != "" {
		return strings.TrimSpace(deps.Job.TenantID)
	}
	if deps.CronJob != nil {
		return strings.TrimSpace(deps.CronJob.TenantID)
	}
	return ""
}
func (p *outlookTeamsProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error {
	return nil
}

func init() {
	restore.RegisterProcessor(&outlookMailProcessor{})
	restore.RegisterProcessor(&outlookCalendarProcessor{})
	restore.RegisterProcessor(&outlookContactsProcessor{})
	restore.RegisterProcessor(&outlookOneDriveProcessor{})
	restore.RegisterProcessor(&outlookSharePointProcessor{})
	restore.RegisterProcessor(&outlookTeamsProcessor{})
	restore.RegisterMicrosoftAuth(MintAccessToken, RefreshAccessToken)
	restore.RegisterMicrosoftReadiness(EvaluateReadiness)
}
