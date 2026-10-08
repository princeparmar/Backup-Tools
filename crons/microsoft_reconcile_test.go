package crons

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const reconcileTenant = "tenant-r"

func newReconcileTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "reconcile.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&repo.GoogleBackupCredentialDB{}, &repo.CronJobListingDB{}, &repo.TaskListingDB{}, &repo.MicrosoftTenantDB{}, &repo.AutosyncBackupPolicyDB{},
		&repo.MicrosoftAccountTenantDB{}, &repo.MicrosoftResourceDB{}, &repo.MicrosoftBackupScopeDB{}); err != nil {
		t.Fatal(err)
	}
	return &db.PostgresDb{
		DB:                    gdb,
		CronJobRepo:           repo.NewCronJobRepository(gdb),
		CredentialRepo:        repo.NewGoogleBackupCredentialRepository(gdb),
		MicrosoftTenantRepo:   repo.NewMicrosoftTenantRepository(gdb),
		MicrosoftLinkRepo:     repo.NewMicrosoftAccountTenantRepository(gdb),
		MicrosoftResourceRepo: repo.NewMicrosoftResourceRepository(gdb),
		MicrosoftScopeRepo:    repo.NewMicrosoftBackupScopeRepository(gdb),
	}
}

type reconcileStubs struct {
	resolveErr   error
	resolveCalls int
	lastRequest  mstenant.Request
	users        []outlook.DirectoryUser
	objectStates map[string]string
}

func (s *reconcileStubs) install(t *testing.T) {
	t.Helper()
	prevR, prevL, prevO := msReconcileResolveFn, msReconcileListUsersFn, msObjectStateFn
	msReconcileResolveFn = func(_ context.Context, _ *db.PostgresDb, req mstenant.Request) (*mstenant.Context, error) {
		s.resolveCalls++
		s.lastRequest = req
		if s.resolveErr != nil {
			return nil, s.resolveErr
		}
		return &mstenant.Context{Token: "app-token", TenantID: req.TenantID, Application: true}, nil
	}
	msReconcileListUsersFn = func(context.Context, string) ([]outlook.DirectoryUser, error) { return s.users, nil }
	msObjectStateFn = func(_ context.Context, _, _, id string) (string, error) {
		if st, ok := s.objectStates[id]; ok {
			return st, nil
		}
		return "", errors.New("graph unavailable")
	}
	t.Cleanup(func() { msReconcileResolveFn, msReconcileListUsersFn, msObjectStateFn = prevR, prevL, prevO })
}

// seedScope creates one credential, a sibling job per method for ann, and a scope.
func seedScope(t *testing.T, store *db.PostgresDb, mode string, methods ...string) (repo.MicrosoftBackupScopeDB, *repo.GoogleBackupCredentialDB) {
	t.Helper()
	cred, err := store.CredentialRepo.UpsertMicrosoftAccount(repo.MicrosoftAccountUpsert{
		UserID: "u1", ExternalAccountID: "oid-admin", HomeTenantID: reconcileTenant, Email: "admin@contoso.com", StorjProjectID: "p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &repo.AutosyncBackupPolicyDB{UserID: "u1", Name: "org", Interval: "daily", On: "00:00"}
	policy.ID = 9
	if err := store.DB.Create(policy).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.MicrosoftLinkRepo.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{TenantID: reconcileTenant, Category: repo.MicrosoftTenantCategoryHome}); err != nil {
		t.Fatal(err)
	}
	if err := store.MicrosoftLinkRepo.Connect(cred.ID, reconcileTenant, repo.MicrosoftBackupModeOrganization, repo.MicrosoftAuthModeApplication); err != nil {
		t.Fatal(err)
	}
	for _, m := range methods {
		job, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "ann@contoso.com", m, "daily", cred.ID,
			repo.MicrosoftJobIdentity{TenantID: reconcileTenant, ResourceType: repo.ResourceTypeUser, ResourceID: "oid-ann"},
			map[string]interface{}{"org_unit_path": "/Sales"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
			"interval": "daily", "policy_id": uint(9), "active": true, "storx_token": "storx-grant",
		}); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := store.MicrosoftScopeRepo.Upsert(repo.MicrosoftBackupScopeDB{
		UserID: "u1", StorjProjectID: "p1", CredentialID: cred.ID, TenantID: reconcileTenant,
		ResourceType: repo.ResourceTypeUser, SyncType: "daily", SelectionMode: mode, PolicyID: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	return *scope, cred
}

func scopeJobsFor(t *testing.T, store *db.PostgresDb) []repo.CronJobListingDB {
	t.Helper()
	jobs, err := store.CronJobRepo.ListMicrosoftScopeJobs("u1", reconcileTenant, repo.ResourceTypeUser, "daily")
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

var (
	ann = outlook.DirectoryUser{ObjectID: "oid-ann", Mail: "ann@contoso.com", Department: "Sales", AccountEnabled: true}
	bob = outlook.DirectoryUser{ObjectID: "oid-bob", Mail: "bob@contoso.com", Department: "Sales", AccountEnabled: true}
	// cat has the same mailbox name as a user in another tenant; identity is the object ID.
	cat      = outlook.DirectoryUser{ObjectID: "oid-cat", UPN: "cat@contoso.com", AccountEnabled: true}
	disabled = outlook.DirectoryUser{ObjectID: "oid-off", Mail: "off@contoso.com", AccountEnabled: false}
)

func TestReconcile_allScopeClonesSiblingsForNewUsers(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &reconcileStubs{users: []outlook.DirectoryUser{ann, bob, cat, disabled}}
	stubs.install(t)
	scope, cred := seedScope(t, store, repo.MicrosoftScopeAll, "outlook", "outlook_onedrive")

	created, err := reconcileMicrosoftScope(context.Background(), store, scope)
	if err != nil {
		t.Fatal(err)
	}
	if created != 4 {
		t.Fatalf("created = %d, want 4 (bob and cat × 2 methods)", created)
	}
	if !stubs.lastRequest.RequireApplication || stubs.lastRequest.CredentialID != cred.ID || stubs.lastRequest.TenantID != reconcileTenant {
		t.Fatalf("resolver request = %+v", stubs.lastRequest)
	}
	byKey := map[string]repo.CronJobListingDB{}
	for _, j := range scopeJobsFor(t, store) {
		byKey[j.Method+"|"+j.ResourceID] = j
	}
	bobMail, ok := byKey["outlook|oid-bob"]
	if !ok {
		t.Fatalf("bob mail job missing: %v", byKey)
	}
	if bobMail.PolicyID != 9 || bobMail.Interval != "daily" || !bobMail.Active || bobMail.StorxToken != "storx-grant" ||
		bobMail.Name != "bob@contoso.com" || repo.JobCredentialID(&bobMail) != cred.ID {
		t.Fatalf("clone must copy policy/interval/active/token: %+v", bobMail)
	}
	if path := (*bobMail.InputData.Json())["org_unit_path"]; path != "/Sales" {
		t.Fatalf("org unit = %v", path)
	}
	if _, ok := byKey["outlook_onedrive|oid-cat"]; !ok {
		t.Fatal("cat onedrive job missing")
	}
	if _, ok := byKey["outlook|oid-off"]; ok {
		t.Fatal("disabled users must not get new jobs")
	}
	if res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-bob"); res == nil {
		t.Fatal("new user resource must be recorded")
	}

	// Idempotent.
	if created, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil || created != 0 {
		t.Fatalf("second run created %d (%v)", created, err)
	}
}

func TestReconcile_selectedScopeNeverAddsJobs(t *testing.T) {
	store := newReconcileTestDB(t)
	(&reconcileStubs{users: []outlook.DirectoryUser{ann, bob}}).install(t)
	scope, _ := seedScope(t, store, repo.MicrosoftScopeSelected, "outlook")

	created, err := reconcileMicrosoftScope(context.Background(), store, scope)
	if err != nil || created != 0 {
		t.Fatalf("selected scope created %d (%v)", created, err)
	}
	if n := len(scopeJobsFor(t, store)); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
}

func TestReconcile_resolverFailureRecordsErrorAndCreatesNothing(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &reconcileStubs{users: []outlook.DirectoryUser{ann, bob},
		resolveErr: &mstenant.Error{Code: mstenant.CodeTenantDisconnected, Message: "tenant is disconnected"}}
	stubs.install(t)
	seedScope(t, store, repo.MicrosoftScopeAll, "outlook")

	(&AutosyncManager{store: store}).reconcileMicrosoftScopes(context.Background())
	scopes, _ := store.MicrosoftScopeRepo.ListActive()
	if len(scopes) != 1 || scopes[0].ReconcileError != "tenant is disconnected" || scopes[0].LastReconciledAt == nil {
		t.Fatalf("scope = %+v", scopes)
	}
	if n := len(scopeJobsFor(t, store)); n != 1 {
		t.Fatalf("no jobs may be created when the tenant cannot be resolved, got %d", n)
	}

	stubs.resolveErr = nil
	(&AutosyncManager{store: store}).reconcileMicrosoftScopes(context.Background())
	scopes, _ = store.MicrosoftScopeRepo.ListActive()
	if scopes[0].ReconcileError != "" {
		t.Fatalf("success must clear reconcile_error: %q", scopes[0].ReconcileError)
	}
}

func TestReconcile_lifecyclePausesDeletedAndResumesRestored(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &reconcileStubs{users: []outlook.DirectoryUser{}, objectStates: map[string]string{"oid-ann": outlook.ObjectStateDeleted}}
	stubs.install(t)
	scope, _ := seedScope(t, store, repo.MicrosoftScopeSelected, "outlook", "outlook_calendar")
	if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{TenantID: reconcileTenant, ResourceType: repo.ResourceTypeUser, ExternalID: "oid-ann"}); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	for _, j := range scopeJobsFor(t, store) {
		if j.Active || j.MessageStatus != "warning" || j.Message != msgPausedDeleted {
			t.Fatalf("deleted user's jobs must be paused with a warning: %+v", j)
		}
	}
	res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if res.State != repo.MicrosoftResourceDeleted {
		t.Fatalf("state = %q", res.State)
	}

	// Restored with the same object ID.
	stubs.users = []outlook.DirectoryUser{ann}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	for _, j := range scopeJobsFor(t, store) {
		if !j.Active {
			t.Fatalf("restored user's jobs must resume: %+v", j)
		}
	}
	res, _ = store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if res.State != repo.MicrosoftResourceActive {
		t.Fatalf("state = %q", res.State)
	}

	// Disabled: marked, jobs untouched.
	stubs.users = []outlook.DirectoryUser{{ObjectID: "oid-ann", Mail: "ann@contoso.com", AccountEnabled: false}}
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	res, _ = store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if res.State != repo.MicrosoftResourceDisabled {
		t.Fatalf("state = %q", res.State)
	}
	for _, j := range scopeJobsFor(t, store) {
		if !j.Active {
			t.Fatal("a disabled user's jobs are not paused by the lifecycle check")
		}
	}
}

func TestReconcile_lifecycleLookupFailureChangesNothing(t *testing.T) {
	store := newReconcileTestDB(t)
	(&reconcileStubs{users: []outlook.DirectoryUser{}}).install(t)
	scope, _ := seedScope(t, store, repo.MicrosoftScopeSelected, "outlook")
	if _, err := reconcileMicrosoftScope(context.Background(), store, scope); err != nil {
		t.Fatal(err)
	}
	for _, j := range scopeJobsFor(t, store) {
		if !j.Active {
			t.Fatal("an unknown lifecycle state must not pause jobs")
		}
	}
}
