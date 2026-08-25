package microsoft

import (
	"github.com/StorX2-0/Backup-Tools/restore"
	"context"
)


type outlookMailProcessor struct{}

func (p *outlookMailProcessor) Method() string { return "outlook" }
func (p *outlookMailProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook")
	return cfg
}
func (p *outlookMailProcessor) ShouldRestoreKey(key string) bool { return shouldRestoreOutlookMailKey(key) }
func (p *outlookMailProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookMailProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookMailKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookMailProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

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
	return RestoreOutlookCalendarKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookCalendarProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

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
	return RestoreOutlookContactKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookContactsProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type outlookOneDriveProcessor struct{}

func (p *outlookOneDriveProcessor) Method() string { return "outlook_onedrive" }
func (p *outlookOneDriveProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_onedrive")
	return cfg
}
func (p *outlookOneDriveProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookDriveMetaKey(key)
}
func (p *outlookOneDriveProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookOneDriveProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookOneDriveKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookOneDriveProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type outlookSharePointProcessor struct{}

func (p *outlookSharePointProcessor) Method() string { return "outlook_sharepoint" }
func (p *outlookSharePointProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_sharepoint")
	return cfg
}
func (p *outlookSharePointProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookDriveMetaKey(key)
}
func (p *outlookSharePointProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookSharePointProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookSharePointKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookSharePointProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

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
	return RequireToken(deps)
}
func (p *outlookTeamsProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookTeamsKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookTeamsProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

type outlookGroupsProcessor struct{}

func (p *outlookGroupsProcessor) Method() string { return "outlook_groups" }
func (p *outlookGroupsProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod("outlook_groups")
	return cfg
}
func (p *outlookGroupsProcessor) ShouldRestoreKey(key string) bool {
	return shouldRestoreOutlookGroupsKey(key)
}
func (p *outlookGroupsProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	return RequireToken(deps)
}
func (p *outlookGroupsProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return RestoreOutlookGroupsKey(ctx, deps.AccessGrant, deps.MicrosoftToken, objectKey)
}
func (p *outlookGroupsProcessor) Cleanup(ctx context.Context, deps *restore.RestoreDeps) error { return nil }

func init() {
	restore.RegisterProcessor(&outlookMailProcessor{})
	restore.RegisterProcessor(&outlookCalendarProcessor{})
	restore.RegisterProcessor(&outlookContactsProcessor{})
	restore.RegisterProcessor(&outlookOneDriveProcessor{})
	restore.RegisterProcessor(&outlookSharePointProcessor{})
	restore.RegisterProcessor(&outlookTeamsProcessor{})
	restore.RegisterProcessor(&outlookGroupsProcessor{})
	restore.RegisterMicrosoftAuth(MintAccessToken, RefreshAccessToken)
	restore.RegisterMicrosoftReadiness(EvaluateReadiness)
}
