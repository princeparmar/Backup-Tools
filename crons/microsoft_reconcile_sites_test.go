package crons

import (
	"context"
	"errors"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	siteA = "contoso.sharepoint.com,a,1"
	siteB = "contoso.sharepoint.com,b,2"
	siteM = "contoso.sharepoint.com,m,3"
)

type siteStateResult struct {
	state string
	err   error
}

// siteStubs fakes the tenant site list, site resolution and per-site reads.
type siteStubs struct {
	sites      []outlook.SharePointSiteSummary
	listErr    error
	listCalls  int
	resolveErr map[string]error
	resolved   []string
	states     map[string]siteStateResult
	stateCalls []string
}

func (s *siteStubs) install(t *testing.T) {
	t.Helper()
	(&reconcileStubs{}).install(t)
	prevList, prevResolve, prevState := msReconcileListSitesFn, msReconcileResolveSiteFn, msSiteStateFn
	msReconcileListSitesFn = func(_ context.Context, token string) ([]outlook.SharePointSiteSummary, error) {
		s.listCalls++
		if token != "app-token" {
			return nil, errors.New("wrong token")
		}
		return s.sites, s.listErr
	}
	msReconcileResolveSiteFn = func(_ context.Context, _, siteID, _ string) (*outlook.SharePointResolvedSite, error) {
		s.resolved = append(s.resolved, siteID)
		if err := s.resolveErr[siteID]; err != nil {
			return nil, err
		}
		return &outlook.SharePointResolvedSite{SiteID: siteID, SiteName: "Site " + siteID, SiteURL: "https://contoso/" + siteID, DriveID: "b!" + siteID}, nil
	}
	msSiteStateFn = func(_ context.Context, _, siteID string) (string, error) {
		s.stateCalls = append(s.stateCalls, siteID)
		r, ok := s.states[siteID]
		if !ok {
			return "", errors.New("graph unavailable")
		}
		return r.state, r.err
	}
	t.Cleanup(func() {
		msReconcileListSitesFn, msReconcileResolveSiteFn, msSiteStateFn = prevList, prevResolve, prevState
	})
}

// seedSiteScope creates a credential, one combined SharePoint tenant job covering the sites,
 // and a site scope. Legacy per-site jobs are still used by lifecycle tests via seedLegacySiteJobs.
func seedSiteScope(t *testing.T, store *db.PostgresDb, mode string, sites ...string) (repo.MicrosoftBackupScopeDB, *repo.GoogleBackupCredentialDB) {
	t.Helper()
	scope, cred := seedScope(t, store, repo.MicrosoftScopeSelected)
	var sels []repo.SharePointSiteSelection
	extra := map[string]interface{}{}
	for _, id := range sites {
		sel := repo.SharePointSiteSelection{
			SiteID: id, DriveID: "b!" + id, SiteName: "Site " + id, SiteURL: "https://contoso/" + id,
		}
		sels = append(sels, sel)
		extra = repo.SharePointSelectionAddSite(extra, sel)
	}
	if len(sels) > 0 {
		extra["site_id"], extra["drive_id"] = sels[0].SiteID, sels[0].DriveID
		extra["site_name"], extra["site_url"] = sels[0].SiteName, sels[0].SiteURL
	}
	job, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", sharePointTenantJobName, "outlook_sharepoint", "daily", cred.ID,
		repo.MicrosoftJobIdentity{TenantID: reconcileTenant, ResourceType: repo.ResourceTypeTenant, ResourceID: reconcileTenant},
		extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"interval": "daily", "policy_id": uint(9), "active": true, "storx_token": "storx-grant",
	}); err != nil {
		t.Fatal(err)
	}
	scope.ID = 0
	scope.ResourceType, scope.SelectionMode = repo.ResourceTypeSite, mode
	s, err := store.MicrosoftScopeRepo.Upsert(scope)
	if err != nil {
		t.Fatal(err)
	}
	return *s, cred
}

// seedLegacySiteJobs creates old per-site SharePoint jobs (for lifecycle pause/resume coverage).
func seedLegacySiteJobs(t *testing.T, store *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, sites ...string) {
	t.Helper()
	for _, id := range sites {
		job, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "Site "+id, "outlook_sharepoint", "daily", cred.ID,
			repo.MicrosoftJobIdentity{TenantID: reconcileTenant, ResourceType: repo.ResourceTypeSite, ResourceID: id},
			map[string]interface{}{"site_id": id, "drive_id": "b!" + id, "site_name": "Site " + id, "site_url": "https://contoso/" + id})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
			"interval": "daily", "policy_id": uint(9), "active": true, "storx_token": "storx-grant",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func tenantSharePointJob(t *testing.T, store *db.PostgresDb) *repo.CronJobListingDB {
	t.Helper()
	job, err := store.CronJobRepo.FindMicrosoftResourceJob("u1", reconcileTenant, repo.ResourceTypeTenant, reconcileTenant, "outlook_sharepoint", "daily")
	if err != nil || job == nil {
		t.Fatalf("tenant sharepoint job: %v", err)
	}
	return job
}

func tenantSiteIDs(t *testing.T, store *db.PostgresDb) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, s := range repo.JobSharePointSites(tenantSharePointJob(t, store)) {
		out[s.SiteID] = true
	}
	return out
}

func legacySiteJobs(t *testing.T, store *db.PostgresDb) map[string]repo.CronJobListingDB {
	t.Helper()
	rows, err := store.CronJobRepo.ListMicrosoftScopeJobs("u1", reconcileTenant, repo.ResourceTypeSite, "daily")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]repo.CronJobListingDB{}
	for _, j := range rows {
		if j.Method != "outlook_sharepoint" {
			continue
		}
		if _, dup := out[j.ResourceID]; dup {
			t.Fatalf("duplicate job for site %s", j.ResourceID)
		}
		out[j.ResourceID] = j
	}
	return out
}

func TestReconcileSites_allScopeAddsSitesToTenantJob(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{sites: []outlook.SharePointSiteSummary{{ID: siteA}, {ID: siteB}}}
	stubs.install(t)
	scope, cred := seedSiteScope(t, store, repo.MicrosoftScopeAll, siteA)
	job := tenantSharePointJob(t, store)
	if err := store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{"interval": "weekly"}); err != nil {
		t.Fatal(err)
	}

	created, err := reconcileMicrosoftScope(context.Background(), store, scope)
	if err != nil || created != 1 {
		t.Fatalf("created %d (%v)", created, err)
	}
	ids := tenantSiteIDs(t, store)
	if !ids[siteA] || !ids[siteB] {
		t.Fatalf("tenant sites %v", ids)
	}
	job = tenantSharePointJob(t, store)
	sels := repo.JobSharePointSites(job)
	var foundB *repo.SharePointSiteSelection
	for i := range sels {
		if sels[i].SiteID == siteB {
			foundB = &sels[i]
		}
	}
	if foundB == nil || foundB.DriveID != "b!"+siteB || foundB.SiteName != "Site "+siteB {
		t.Fatalf("site B selection %+v", foundB)
	}
	if job.Interval != "weekly" || job.PolicyID != 9 || !job.Active || job.StorxToken != "storx-grant" || repo.JobCredentialID(job) != cred.ID {
		t.Fatalf("tenant job %+v", job)
	}
	if res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeSite, siteB); res == nil {
		t.Fatal("new site resource not recorded")
	}
	if len(stubs.resolved) != 1 || stubs.resolved[0] != siteB {
		t.Fatalf("resolved %v", stubs.resolved)
	}
	// Still one SharePoint job.
	tenantRows, _ := store.CronJobRepo.ListMicrosoftScopeJobs("u1", reconcileTenant, repo.ResourceTypeTenant, "daily")
	sp := 0
	for _, j := range tenantRows {
		if j.Method == "outlook_sharepoint" {
			sp++
		}
	}
	if sp != 1 {
		t.Fatalf("want 1 tenant sharepoint job, got %d", sp)
	}

	stubs.resolved = nil
	if created, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil || created != 0 {
		t.Fatalf("second run created %d (%v)", created, err)
	}
	if len(stubs.resolved) != 0 || len(tenantSiteIDs(t, store)) != 2 {
		t.Fatalf("second run resolved %v sites %v", stubs.resolved, tenantSiteIDs(t, store))
	}
}

func TestReconcileSites_selectedScopeNeverListsOrAdds(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{
		sites:  []outlook.SharePointSiteSummary{{ID: siteA}, {ID: siteB}},
		states: map[string]siteStateResult{siteA: {state: outlook.ObjectStateActive}},
	}
	stubs.install(t)
	scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteA)
	created, err := reconcileMicrosoftScope(context.Background(), store, scope)
	if err != nil || created != 0 || stubs.listCalls != 0 || len(tenantSiteIDs(t, store)) != 1 {
		t.Fatalf("created %d err %v list calls %d", created, err, stubs.listCalls)
	}
}

func TestReconcileSites_manualSiteMissingFromListIsKept(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{
		sites:  []outlook.SharePointSiteSummary{{ID: siteA}},
		states: map[string]siteStateResult{siteM: {state: outlook.ObjectStateActive}},
	}
	stubs.install(t)
	scope, cred := seedSiteScope(t, store, repo.MicrosoftScopeAll, siteA)
	// Lifecycle still watches legacy per-site jobs for gone/restored sites.
	seedLegacySiteJobs(t, store, cred, siteM)
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if m, ok := legacySiteJobs(t, store)[siteM]; !ok || !m.Active {
		t.Fatalf("manually selected site job changed: %+v", legacySiteJobs(t, store))
	}
	if !tenantSiteIDs(t, store)[siteA] {
		t.Fatal("tenant lost site A")
	}
	if len(stubs.stateCalls) != 1 || stubs.stateCalls[0] != siteM {
		t.Fatalf("only the unlisted legacy site is read: %v", stubs.stateCalls)
	}
}

func TestReconcileSites_transientErrorsNeverPause(t *testing.T) {
	for name, err := range map[string]error{
		"403": errors.New("graph sites/x: HTTP 403: accessDenied"),
		"429": errors.New("graph sites/x: HTTP 429: tooManyRequests"),
		"503": errors.New("graph sites/x: HTTP 503: serviceNotAvailable"),
	} {
		t.Run(name, func(t *testing.T) {
			store := newReconcileTestDB(t)
			(&siteStubs{states: map[string]siteStateResult{siteA: {err: err}}}).install(t)
			scope, cred := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteB)
			seedLegacySiteJobs(t, store, cred, siteA)
			if _, rerr := reconcileMicrosoftScope(context.Background(), store, scope); rerr != nil {
				t.Fatal(rerr)
			}
			if j := legacySiteJobs(t, store)[siteA]; !j.Active || j.Message != "" {
				t.Fatalf("job changed on %s: %+v", name, j)
			}
		})
	}
}

func TestReconcileSites_goneSitePausesAndResumesLegacyJob(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{states: map[string]siteStateResult{siteA: {state: outlook.ObjectStateGone}}}
	stubs.install(t)
	scope, cred := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteM)
	seedLegacySiteJobs(t, store, cred, siteA, siteB)
	stubs.states[siteB] = siteStateResult{state: outlook.ObjectStateActive}
	stubs.states[siteM] = siteStateResult{state: outlook.ObjectStateActive}

	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	jobs := legacySiteJobs(t, store)
	if a := jobs[siteA]; a.Active || a.Message != msgPausedGone {
		t.Fatalf("gone site job %+v", a)
	}
	if b := jobs[siteB]; !b.Active {
		t.Fatalf("live site job paused: %+v", b)
	}
	// Combined tenant job stays active.
	if tj := tenantSharePointJob(t, store); !tj.Active {
		t.Fatalf("tenant job paused: %+v", tj)
	}

	stubs.states[siteA] = siteStateResult{err: errors.New("graph sites/x: HTTP 503")}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if a := legacySiteJobs(t, store)[siteA]; a.Active || a.Message != msgPausedGone {
		t.Fatalf("pause changed by a transient error: %+v", a)
	}

	stubs.states[siteA] = siteStateResult{state: outlook.ObjectStateActive}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if a := legacySiteJobs(t, store)[siteA]; !a.Active || a.Message == msgPausedGone {
		t.Fatalf("site job not resumed: %+v", a)
	}
}

func TestReconcileSites_userPausedJobIsNotResumed(t *testing.T) {
	store := newReconcileTestDB(t)
	(&siteStubs{states: map[string]siteStateResult{siteA: {state: outlook.ObjectStateActive}}}).install(t)
	scope, cred := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteB)
	seedLegacySiteJobs(t, store, cred, siteA)
	if err := store.CronJobRepo.UpdateCronJobByID(legacySiteJobs(t, store)[siteA].ID, map[string]interface{}{"active": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if legacySiteJobs(t, store)[siteA].Active {
		t.Fatal("a job the user turned off was resumed")
	}
}

func TestReconcileSites_listFailureChangesNothing(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{listErr: errors.New("graph list http 503"), states: map[string]siteStateResult{siteA: {state: outlook.ObjectStateGone}}}
	stubs.install(t)
	scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeAll, siteA)
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err == nil {
		t.Fatal("a failed site list must be reported")
	}
	if a := tenantSharePointJob(t, store); !a.Active || len(stubs.stateCalls) != 0 {
		t.Fatalf("job changed after a failed list: %+v (reads %v)", a, stubs.stateCalls)
	}
}

func TestReconcileSites_siteWithoutLibraryIsSkipped(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{
		sites:      []outlook.SharePointSiteSummary{{ID: siteA}, {ID: siteB}},
		resolveErr: map[string]error{siteB: outlook.ErrSharePointSiteNoDocumentLibrary},
	}
	stubs.install(t)
	scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeAll, siteA)
	created, err := reconcileMicrosoftScope(context.Background(), store, scope)
	if err != nil || created != 0 || len(tenantSiteIDs(t, store)) != 1 {
		t.Fatalf("created %d (%v) sites %v", created, err, tenantSiteIDs(t, store))
	}
}

func TestReconcileSites_legacyOnlyCreatesTenantJob(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{sites: []outlook.SharePointSiteSummary{{ID: siteA}, {ID: siteB}}}
	stubs.install(t)
	scope, cred := seedScope(t, store, repo.MicrosoftScopeAll)
	seedLegacySiteJobs(t, store, cred, siteA)
	scope.ID = 0
	scope.ResourceType, scope.SelectionMode = repo.ResourceTypeSite, repo.MicrosoftScopeAll
	s, err := store.MicrosoftScopeRepo.Upsert(scope)
	if err != nil {
		t.Fatal(err)
	}
	created, err := reconcileMicrosoftScope(context.Background(), store, *s)
	if err != nil || created != 1 {
		t.Fatalf("created %d (%v)", created, err)
	}
	job := tenantSharePointJob(t, store)
	if job.Name != sharePointTenantJobName {
		t.Fatalf("name %q", job.Name)
	}
	ids := map[string]bool{}
	for _, sel := range repo.JobSharePointSites(job) {
		ids[sel.SiteID] = true
	}
	if !ids[siteA] || !ids[siteB] {
		t.Fatalf("sites %v", ids)
	}
}
