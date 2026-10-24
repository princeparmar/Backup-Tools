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

// seedSiteScope creates a credential, a job for each site and a site scope.
func seedSiteScope(t *testing.T, store *db.PostgresDb, mode string, sites ...string) (repo.MicrosoftBackupScopeDB, *repo.GoogleBackupCredentialDB) {
	t.Helper()
	scope, cred := seedScope(t, store, repo.MicrosoftScopeSelected)
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
	scope.ID = 0
	scope.ResourceType, scope.SelectionMode = repo.ResourceTypeSite, mode
	s, err := store.MicrosoftScopeRepo.Upsert(scope)
	if err != nil {
		t.Fatal(err)
	}
	return *s, cred
}

func siteJobs(t *testing.T, store *db.PostgresDb) map[string]repo.CronJobListingDB {
	t.Helper()
	rows, err := store.CronJobRepo.ListMicrosoftScopeJobs("u1", reconcileTenant, repo.ResourceTypeSite, "daily")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]repo.CronJobListingDB{}
	for _, j := range rows {
		if _, dup := out[j.ResourceID]; dup {
			t.Fatalf("duplicate job for site %s", j.ResourceID)
		}
		out[j.ResourceID] = j
	}
	return out
}

func TestReconcileSites_allScopeAddsNewSiteJobs(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{sites: []outlook.SharePointSiteSummary{{ID: siteA}, {ID: siteB}}}
	stubs.install(t)
	scope, cred := seedSiteScope(t, store, repo.MicrosoftScopeAll, siteA)
	// A custom setting on the existing job must survive.
	if err := store.CronJobRepo.UpdateCronJobByID(siteJobs(t, store)[siteA].ID, map[string]interface{}{"interval": "weekly"}); err != nil {
		t.Fatal(err)
	}

	created, err := reconcileMicrosoftScope(context.Background(), store, scope)
	if err != nil || created != 1 {
		t.Fatalf("created %d (%v)", created, err)
	}
	jobs := siteJobs(t, store)
	b, ok := jobs[siteB]
	if !ok {
		t.Fatalf("new site job missing: %v", jobs)
	}
	in := *b.InputData.Json()
	if in["site_id"] != siteB || in["drive_id"] != "b!"+siteB || in["site_name"] != "Site "+siteB || in["site_url"] != "https://contoso/"+siteB {
		t.Fatalf("new job input %v", in)
	}
	if b.Method != "outlook_sharepoint" || b.Name != "Site "+siteB || b.PolicyID != 9 || !b.Active || b.StorxToken != "storx-grant" || repo.JobCredentialID(&b) != cred.ID {
		t.Fatalf("new job %+v", b)
	}
	if jobs[siteA].Interval != "weekly" {
		t.Fatalf("existing job settings changed: %+v", jobs[siteA])
	}
	if res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeSite, siteB); res == nil {
		t.Fatal("new site resource not recorded")
	}
	if len(stubs.resolved) != 1 || stubs.resolved[0] != siteB {
		t.Fatalf("resolved %v", stubs.resolved)
	}

	// Idempotent: no duplicates and no extra Graph reads for covered sites.
	stubs.resolved = nil
	if created, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil || created != 0 {
		t.Fatalf("second run created %d (%v)", created, err)
	}
	if len(stubs.resolved) != 0 || len(siteJobs(t, store)) != 2 {
		t.Fatalf("second run resolved %v", stubs.resolved)
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
	if err != nil || created != 0 || stubs.listCalls != 0 || len(siteJobs(t, store)) != 1 {
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
	scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeAll, siteA, siteM)
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	jobs := siteJobs(t, store)
	if m, ok := jobs[siteM]; !ok || !m.Active {
		t.Fatalf("manually selected site job changed: %+v", jobs)
	}
	if len(stubs.stateCalls) != 1 || stubs.stateCalls[0] != siteM {
		t.Fatalf("only the unlisted site is read: %v", stubs.stateCalls)
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
			scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteA)
			if _, rerr := reconcileMicrosoftScope(context.Background(), store, scope); rerr != nil {
				t.Fatal(rerr)
			}
			if j := siteJobs(t, store)[siteA]; !j.Active || j.Message != "" {
				t.Fatalf("job changed on %s: %+v", name, j)
			}
		})
	}
}

func TestReconcileSites_goneSitePausesAndResumes(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &siteStubs{states: map[string]siteStateResult{siteA: {state: outlook.ObjectStateGone}}}
	stubs.install(t)
	scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteA, siteB)
	stubs.states[siteB] = siteStateResult{state: outlook.ObjectStateActive}

	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	jobs := siteJobs(t, store)
	if a := jobs[siteA]; a.Active || a.Message != msgPausedGone {
		t.Fatalf("gone site job %+v", a)
	}
	if b := jobs[siteB]; !b.Active {
		t.Fatalf("live site job paused: %+v", b)
	}

	// Pausing again is a no-op; a transient error leaves the pause alone.
	stubs.states[siteA] = siteStateResult{err: errors.New("graph sites/x: HTTP 503")}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if a := siteJobs(t, store)[siteA]; a.Active || a.Message != msgPausedGone {
		t.Fatalf("pause changed by a transient error: %+v", a)
	}

	// The site comes back (restored from the recycle bin).
	stubs.states[siteA] = siteStateResult{state: outlook.ObjectStateActive}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if a := siteJobs(t, store)[siteA]; !a.Active || a.Message == msgPausedGone {
		t.Fatalf("site job not resumed: %+v", a)
	}
}

func TestReconcileSites_userPausedJobIsNotResumed(t *testing.T) {
	store := newReconcileTestDB(t)
	(&siteStubs{states: map[string]siteStateResult{siteA: {state: outlook.ObjectStateActive}}}).install(t)
	scope, _ := seedSiteScope(t, store, repo.MicrosoftScopeSelected, siteA)
	if err := store.CronJobRepo.UpdateCronJobByID(siteJobs(t, store)[siteA].ID, map[string]interface{}{"active": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	if siteJobs(t, store)[siteA].Active {
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
	if a := siteJobs(t, store)[siteA]; !a.Active || len(stubs.stateCalls) != 0 {
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
	if err != nil || created != 0 || len(siteJobs(t, store)) != 1 {
		t.Fatalf("created %d (%v)", created, err)
	}
}
