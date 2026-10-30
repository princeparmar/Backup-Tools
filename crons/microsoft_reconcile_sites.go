package crons

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Seams (overridden in tests).
var (
	msReconcileListSitesFn = func(ctx context.Context, token string) ([]outlook.SharePointSiteSummary, error) {
		return outlook.ListTenantSharePointSites(ctx, token, "", 0)
	}
	msReconcileResolveSiteFn = outlook.ResolveSharePointSite
	msSiteStateFn            = outlook.SharePointSiteState
)

const sharePointTenantJobName = "Microsoft SharePoint"

// reconcileMicrosoftSiteScope checks selected SharePoint sites and, for selection_mode=all, adds
 // newly listed sites onto the combined tenant SharePoint job (never one job per site).
func reconcileMicrosoftSiteScope(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, scope repo.MicrosoftBackupScopeDB, jobs []repo.CronJobListingDB) (int, error) {
	jobs = appendSharePointTenantJobs(store, scope, jobs)
	all := scope.SelectionMode == repo.MicrosoftScopeAll
	var listed []outlook.SharePointSiteSummary
	if all {
		sites, err := msReconcileListSitesFn(ctx, tc.Token)
		if err != nil {
			return 0, fmt.Errorf("list tenant sites: %w", err)
		}
		listed = sites
	}
	applySiteLifecycle(ctx, store, tc, jobs, listed)
	if !all {
		return 0, nil
	}
	return addMicrosoftScopeSitesToTenantJob(ctx, store, tc, scope, listed, jobs)
}

// appendSharePointTenantJobs adds the connection's combined outlook_sharepoint tenant job(s) to the
 // site-scope job list (scopes are still ResourceTypeSite; the autosync job is ResourceTypeTenant).
func appendSharePointTenantJobs(store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB, jobs []repo.CronJobListingDB) []repo.CronJobListingDB {
	rows, err := store.CronJobRepo.ListMicrosoftScopeJobs(scope.UserID, scope.TenantID, repo.ResourceTypeTenant, scope.SyncType)
	if err != nil {
		return jobs
	}
	seen := map[uint]bool{}
	for _, j := range jobs {
		seen[j.ID] = true
	}
	for _, j := range rows {
		if j.Method != "outlook_sharepoint" || repo.JobCredentialID(&j) != scope.CredentialID || seen[j.ID] {
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs
}

// applySiteLifecycle pauses legacy per-site jobs when Graph answers 404 for that site, and
 // resumes jobs this check paused once the site is back. The combined tenant SharePoint job is
 // never paused here — missing sites are left in selection for the next backup warning.
func applySiteLifecycle(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, jobs []repo.CronJobListingDB, listed []outlook.SharePointSiteSummary) {
	live := make(map[string]bool, len(listed))
	for _, s := range listed {
		live[strings.TrimSpace(s.ID)] = true
	}
	legacy := make([]repo.CronJobListingDB, 0, len(jobs))
	for i := range jobs {
		if jobs[i].ResourceType == repo.ResourceTypeTenant {
			continue
		}
		legacy = append(legacy, jobs[i])
	}
	order, bySite := groupJobsByResource(legacy)
	for _, siteID := range order {
		state := outlook.ObjectStateActive
		if !live[siteID] {
			s, err := msSiteStateFn(ctx, tc.Token, siteID)
			if err != nil {
				logger.Warn(ctx, "sharepoint site lifecycle check", logger.String("site_id", siteID), logger.ErrorField(err))
				continue
			}
			state = s
		}
		ApplyMicrosoftObjectState(store, tc.TenantID, repo.ResourceTypeSite, siteID, state, bySite[siteID])
	}
}

// addMicrosoftScopeSitesToTenantJob unions every listed site into the tenant SharePoint job's
 // input_data.sites (creating that job once when only legacy site jobs exist).
func addMicrosoftScopeSitesToTenantJob(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, scope repo.MicrosoftBackupScopeDB, sites []outlook.SharePointSiteSummary, jobs []repo.CronJobListingDB) (int, error) {
	tenantJob, tmpl, err := ensureSharePointTenantJob(store, scope, jobs)
	if err != nil {
		return 0, err
	}
	_ = tmpl

	have := map[string]bool{}
	for _, sel := range repo.JobSharePointSites(tenantJob) {
		have[sel.SiteID] = true
	}
	// Legacy per-site jobs also count as covered until they merge.
	for i := range jobs {
		if jobs[i].ResourceType == repo.ResourceTypeSite && jobs[i].Method == "outlook_sharepoint" {
			if sid := strings.TrimSpace(jobs[i].ResourceID); sid != "" {
				have[sid] = true
			}
		}
	}

	added := 0
	var firstErr error
	for _, s := range sites {
		siteID := strings.TrimSpace(s.ID)
		if siteID == "" || have[siteID] {
			continue
		}
		resolved, err := msReconcileResolveSiteFn(ctx, tc.Token, siteID, "")
		if err != nil {
			if !errors.Is(err, outlook.ErrSharePointSiteNoDocumentLibrary) && firstErr == nil {
				firstErr = fmt.Errorf("resolve site %s: %w", siteID, err)
			}
			continue
		}
		if have[resolved.SiteID] {
			continue
		}
		if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: scope.TenantID, ResourceType: repo.ResourceTypeSite, ExternalID: resolved.SiteID, DisplayName: resolved.SiteName,
			Metadata: database.NewDbJsonFromValue(map[string]interface{}{"site_url": resolved.SiteURL, "drive_id": resolved.DriveID}),
		}); err != nil {
			return added, err
		}
		sel := repo.SharePointSiteSelection{
			SiteID: resolved.SiteID, DriveID: resolved.DriveID,
			SiteName: resolved.SiteName, SiteURL: resolved.SiteURL,
		}
		if err := store.CronJobRepo.UpdateJobInputData(tenantJob.ID, func(in map[string]interface{}) map[string]interface{} {
			in = repo.SharePointSelectionAddSite(in, sel)
			if sites := repo.JobSharePointSites(&repo.CronJobListingDB{InputData: database.NewDbJsonFromValue(in)}); len(sites) > 0 {
				in["site_id"], in["drive_id"] = sites[0].SiteID, sites[0].DriveID
				in["site_name"], in["site_url"] = sites[0].SiteName, sites[0].SiteURL
			}
			return in
		}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("add site %s to sharepoint job: %w", resolved.SiteID, err)
			}
			continue
		}
		have[resolved.SiteID] = true
		added++
		// Keep in-memory copy aligned for subsequent adds in this pass.
		if refreshed, gerr := store.CronJobRepo.GetCronJobByID(tenantJob.ID); gerr == nil && refreshed != nil {
			tenantJob = refreshed
		}
	}
	return added, firstErr
}

// ensureSharePointTenantJob returns the combined SharePoint job for this scope, creating it from
 // a legacy site-job template when needed.
func ensureSharePointTenantJob(store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB, jobs []repo.CronJobListingDB) (*repo.CronJobListingDB, repo.CronJobListingDB, error) {
	var tmpl repo.CronJobListingDB
	var siteJobs []repo.CronJobListingDB
	for i := range jobs {
		j := jobs[i]
		if j.Method != "outlook_sharepoint" {
			continue
		}
		if j.ResourceType == repo.ResourceTypeTenant {
			return &j, j, nil
		}
		if j.ResourceType == repo.ResourceTypeSite {
			siteJobs = append(siteJobs, j)
		}
	}
	if existing, err := store.CronJobRepo.FindMicrosoftResourceJob(scope.UserID, scope.TenantID, repo.ResourceTypeTenant,
		strings.ToLower(strings.TrimSpace(scope.TenantID)), "outlook_sharepoint", scope.SyncType); err != nil {
		return nil, tmpl, err
	} else if existing != nil && repo.JobCredentialID(existing) == scope.CredentialID {
		return existing, *existing, nil
	}

	if len(siteJobs) > 0 {
		tmpl = pickScopeTemplate(siteJobs, scope.PolicyID)
	} else {
		tmpl = repo.CronJobListingDB{
			Method: "outlook_sharepoint", Interval: "daily", PolicyID: scope.PolicyID, Active: true,
		}
	}

	extra := scopeTemplateInput(tmpl)
	delete(extra, "site_id")
	delete(extra, "drive_id")
	delete(extra, "site_name")
	delete(extra, "site_url")
	delete(extra, "sites")
	// Seed sites from every legacy site job so the tenant job owns the selection immediately.
	for i := range siteJobs {
		for _, sel := range repo.JobSharePointSites(&siteJobs[i]) {
			extra = repo.SharePointSelectionAddSite(extra, sel)
		}
	}
	if sites := repo.JobSharePointSites(&repo.CronJobListingDB{InputData: database.NewDbJsonFromValue(extra)}); len(sites) > 0 {
		extra["site_id"], extra["drive_id"] = sites[0].SiteID, sites[0].DriveID
		extra["site_name"], extra["site_url"] = sites[0].SiteName, sites[0].SiteURL
	} else {
		// Placeholder until the first listed site is resolved and added.
		extra["sites"] = []repo.SharePointSiteSelection{}
	}

	job, err := store.CronJobRepo.CreateMicrosoftResourceJob(scope.UserID, sharePointTenantJobName, "outlook_sharepoint", scope.SyncType, scope.CredentialID,
		repo.MicrosoftJobIdentity{TenantID: scope.TenantID, ResourceType: repo.ResourceTypeTenant, ResourceID: strings.ToLower(strings.TrimSpace(scope.TenantID))},
		extra)
	if err != nil {
		return nil, tmpl, err
	}
	if err := saveScopeTemplateSchedule(store, job.ID, tmpl); err != nil {
		return nil, tmpl, err
	}
	if refreshed, gerr := store.CronJobRepo.GetCronJobByID(job.ID); gerr == nil && refreshed != nil {
		job = refreshed
	}
	return job, tmpl, nil
}
