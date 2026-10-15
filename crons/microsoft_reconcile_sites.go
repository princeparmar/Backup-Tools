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

// reconcileMicrosoftSiteScope checks the site of every job of the scope and, for
// selection_mode=all, adds jobs for listed sites that have none. Existing jobs are never changed
// except by the lifecycle check, and never removed.
func reconcileMicrosoftSiteScope(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, scope repo.MicrosoftBackupScopeDB, jobs []repo.CronJobListingDB) (int, error) {
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
	return addMicrosoftScopeSiteJobs(ctx, store, tc, scope, listed, jobs)
}

// applySiteLifecycle pauses the jobs of a site Graph answers 404 for and resumes the jobs this
// check paused once the site is back. A listed site is live; any other site is read one by one,
// and a failed read (403, 429, 5xx, network) changes nothing.
func applySiteLifecycle(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, jobs []repo.CronJobListingDB, listed []outlook.SharePointSiteSummary) {
	live := make(map[string]bool, len(listed))
	for _, s := range listed {
		live[strings.TrimSpace(s.ID)] = true
	}
	bySite := map[string][]repo.CronJobListingDB{}
	var order []string
	for _, j := range jobs {
		if _, ok := bySite[j.ResourceID]; !ok {
			order = append(order, j.ResourceID)
		}
		bySite[j.ResourceID] = append(bySite[j.ResourceID], j)
	}
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

// addMicrosoftScopeSiteJobs creates, for every listed site without one, a job per method already
// in the scope, copying the policy, interval, active state and settings of a sibling site job.
func addMicrosoftScopeSiteJobs(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, scope repo.MicrosoftBackupScopeDB, sites []outlook.SharePointSiteSummary, jobs []repo.CronJobListingDB) (int, error) {
	var methods []string
	templates := map[string][]repo.CronJobListingDB{}
	have := map[string]bool{}
	for _, j := range jobs {
		if _, ok := templates[j.Method]; !ok {
			methods = append(methods, j.Method)
		}
		templates[j.Method] = append(templates[j.Method], j)
		have[j.Method+"|"+j.ResourceID] = true
	}
	created := 0
	var firstErr error
	for _, s := range sites {
		siteID := strings.TrimSpace(s.ID)
		if siteID == "" {
			continue
		}
		var missing []string
		for _, m := range methods {
			if !have[m+"|"+siteID] {
				missing = append(missing, m)
			}
		}
		if len(missing) == 0 {
			continue
		}
		resolved, err := msReconcileResolveSiteFn(ctx, tc.Token, siteID, "")
		if err != nil {
			if !errors.Is(err, outlook.ErrSharePointSiteNoDocumentLibrary) && firstErr == nil {
				firstErr = fmt.Errorf("resolve site %s: %w", siteID, err)
			}
			continue
		}
		upserted := false
		for _, method := range missing {
			if have[method+"|"+resolved.SiteID] {
				continue
			}
			if existing, err := store.CronJobRepo.FindMicrosoftResourceJob(scope.UserID, scope.TenantID, repo.ResourceTypeSite, resolved.SiteID, method, scope.SyncType); err != nil || existing != nil {
				if err != nil && firstErr == nil {
					firstErr = err
				}
				have[method+"|"+resolved.SiteID] = existing != nil
				continue
			}
			if !upserted {
				if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
					TenantID: scope.TenantID, ResourceType: repo.ResourceTypeSite, ExternalID: resolved.SiteID, DisplayName: resolved.SiteName,
					Metadata: database.NewDbJsonFromValue(map[string]interface{}{"site_url": resolved.SiteURL, "drive_id": resolved.DriveID}),
				}); err != nil {
					return created, err
				}
				upserted = true
			}
			if err := cloneScopeSiteJob(store, scope, pickScopeTemplate(templates[method], scope.PolicyID), resolved); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("create %s job for site %s: %w", method, resolved.SiteID, err)
				}
				continue
			}
			have[method+"|"+resolved.SiteID] = true
			created++
		}
	}
	return created, firstErr
}

func cloneScopeSiteJob(store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB, tmpl repo.CronJobListingDB, site *outlook.SharePointResolvedSite) error {
	input := map[string]interface{}{}
	if tmpl.InputData != nil && tmpl.InputData.Json() != nil {
		for k, v := range *tmpl.InputData.Json() {
			input[k] = v
		}
	}
	name := strings.TrimSpace(site.SiteName)
	if name == "" {
		name = outlook.SanitizeSharePointSiteKey(site.SiteID)
	}
	input["email"] = name
	input["site_id"] = site.SiteID
	input["drive_id"] = site.DriveID
	input["site_name"] = site.SiteName
	input["site_url"] = site.SiteURL
	job, err := store.CronJobRepo.CreateMicrosoftResourceJob(scope.UserID, name, tmpl.Method, scope.SyncType, scope.CredentialID,
		repo.MicrosoftJobIdentity{TenantID: scope.TenantID, ResourceType: repo.ResourceTypeSite, ResourceID: site.SiteID}, input)
	if err != nil {
		return err
	}
	return store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"interval":    tmpl.Interval,
		"policy_id":   tmpl.PolicyID,
		"active":      tmpl.Active,
		"storx_token": tmpl.StorxToken,
	})
}
