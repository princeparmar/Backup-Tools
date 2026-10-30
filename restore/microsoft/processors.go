package microsoft

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
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
	if outlook.IsSharePointListItemKey(objectKey) || outlook.IsSharePointPageKey(objectKey) {
		return restoreSharePointWorkspaceKey(ctx, deps, objectKey)
	}
	if _, ok := outlook.ParseOneDriveObjectKey(objectKey); ok {
		return restoreLibraryKey(ctx, deps, satellite.ReserveBucket_OutlookSharePoint, "outlook_sharepoint", objectKey)
	}
	return RestoreOutlookSharePointKey(ctx, deps, objectKey)
}

func restoreSharePointWorkspaceKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	if deps == nil {
		return fmt.Errorf("restore deps are required")
	}
	get := func(ctx context.Context, key string) ([]byte, error) {
		return satellite.DownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_OutlookSharePoint, key)
	}
	destSite := sharePointWriteSite(deps, objectKey)
	if outlook.IsSharePointListItemKey(objectKey) {
		_, err := outlook.RestoreSharePointListItemFromVault(ctx, deps.MicrosoftToken, destSite, objectKey, get)
		return sharePointWorkspaceWriteErr(err)
	}
	_, err := outlook.RestoreSharePointPageFromVault(ctx, deps.MicrosoftToken, destSite, objectKey, get)
	return sharePointWorkspaceWriteErr(err)
}

// sharePointWriteSite is the Graph site lists/pages restore into. Migration uses the dest site;
// in-place uses the site id encoded in the vault key (unsanitized).
func sharePointWriteSite(deps *restore.RestoreDeps, objectKey string) string {
	if site := outlook.GraphSharePointSiteID(sharePointDestinationSite(deps)); site != "" {
		return site
	}
	if site := outlook.GraphSharePointSiteIDFromKey(objectKey); site != "" {
		return site
	}
	if deps != nil && deps.CronJob != nil {
		if site := outlook.GraphSharePointSiteID(repo.JobSharePointSiteID(deps.CronJob)); site != "" {
			return site
		}
		for _, sel := range repo.JobSharePointSites(deps.CronJob) {
			prefix := outlook.ResourceKeyPrefix(deps.CronJob.TenantID, repo.ResourceTypeSite, sel.SiteID)
			if prefix != "" && (objectKey == prefix || strings.HasPrefix(objectKey, prefix+"/")) {
				return outlook.GraphSharePointSiteID(sel.SiteID)
			}
		}
	}
	return ""
}

// sharePointWorkspaceWriteErr treats permission and "already there" Graph writes as restored so
// restore-all of library files is not Partial Completed with Dead items.
func sharePointWorkspaceWriteErr(err error) error {
	if err == nil || errors.Is(err, errObjectSkipped) {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "http 403") || strings.Contains(msg, "http 409") ||
		strings.Contains(msg, "access denied") {
		return nil
	}
	return err
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
