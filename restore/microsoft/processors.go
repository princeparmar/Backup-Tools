package microsoft

import (
	"context"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// serviceProcessor is one Microsoft restore service. Setup requires a Graph token unless a service sets its own.
type serviceProcessor struct {
	method  string
	match   func(string) bool
	restore func(context.Context, *restore.RestoreDeps, string) error
	setup   func(context.Context, *restore.RestoreDeps) error
}

func (p *serviceProcessor) Method() string { return p.method }
func (p *serviceProcessor) Config() restore.ServiceConfig {
	cfg, _ := restore.ConfigForMethod(p.method)
	return cfg
}
func (p *serviceProcessor) ShouldRestoreKey(key string) bool { return p.match(key) }
func (p *serviceProcessor) Setup(ctx context.Context, deps *restore.RestoreDeps) error {
	if p.setup != nil {
		return p.setup(ctx, deps)
	}
	return RequireToken(deps)
}
func (p *serviceProcessor) RestoreKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	return p.restore(ctx, deps, objectKey)
}
func (p *serviceProcessor) Cleanup(context.Context, *restore.RestoreDeps) error { return nil }

func restoreSharePointKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	if _, ok := outlook.ParseOneDriveObjectKey(objectKey); ok {
		return restoreLibraryKey(ctx, deps, satellite.ReserveBucket_OutlookSharePoint, "outlook_sharepoint", objectKey)
	}
	return RestoreOutlookSharePointKey(ctx, deps, objectKey)
}

func setupTeamsRestore(ctx context.Context, deps *restore.RestoreDeps) error {
	if err := RequireToken(deps); err != nil {
		return err
	}
	if deps.MicrosoftApplication || microsoftTokenIsAppOnly(deps.MicrosoftToken) {
		return ErrTeamsRestoreNeedsUser
	}
	return nil
}

func restoreTeamsKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	s := &TeamsRestoreSession{
		AccessGrant: deps.AccessGrant, Token: deps.MicrosoftToken,
		Application: deps.MicrosoftApplication, TenantID: teamsRestoreTenant(deps), State: &deps.ContainerIDs,
	}
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

func outlookSharePointProcessor() *serviceProcessor {
	return &serviceProcessor{method: "outlook_sharepoint", match: shouldRestoreOutlookSharePointKey, restore: restoreSharePointKey}
}

func outlookTeamsProcessor() *serviceProcessor {
	return &serviceProcessor{method: "outlook_teams", match: shouldRestoreOutlookTeamsKey, restore: restoreTeamsKey, setup: setupTeamsRestore}
}

func init() {
	for _, p := range []*serviceProcessor{
		{method: "outlook", match: shouldRestoreOutlookMailKey, restore: RestoreOutlookMailKey},
		{method: "outlook_calendar", match: shouldRestoreOutlookCalendarKey, restore: RestoreOutlookCalendarKey},
		{method: "outlook_contacts", match: shouldRestoreOutlookContactsKey, restore: RestoreOutlookContactKey},
		{method: "outlook_onedrive", match: shouldRestoreOutlookOneDriveKey, restore: RestoreOutlookOneDriveKey},
		outlookSharePointProcessor(),
		outlookTeamsProcessor(),
	} {
		restore.RegisterProcessor(p)
	}
	restore.RegisterMicrosoftAuth(MintAccessToken, RefreshAccessToken)
	restore.RegisterMicrosoftReadiness(EvaluateReadiness)
}
