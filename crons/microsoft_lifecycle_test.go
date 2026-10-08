package crons

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
)

var errMailboxMissing = errors.New(`outlook mail delta http 404: {"error":{"code":"MailboxNotEnabledForRESTAPI"}}`)

func seedRunJob(t *testing.T, store *db.PostgresDb) *repo.CronJobListingDB {
	t.Helper()
	seedScope(t, store, repo.MicrosoftScopeSelected, "outlook", "outlook_onedrive")
	if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
		TenantID: reconcileTenant, ResourceType: repo.ResourceTypeUser, ExternalID: "oid-ann", Mail: "ann@contoso.com",
	}); err != nil {
		t.Fatal(err)
	}
	for _, j := range scopeJobsFor(t, store) {
		if j.Method == "outlook" {
			job := j
			return &job
		}
	}
	t.Fatal("outlook job not seeded")
	return nil
}

func TestMicrosoftRunOutcome_deletedUserPausesJobs(t *testing.T) {
	store := newReconcileTestDB(t)
	(&reconcileStubs{objectStates: map[string]string{"oid-ann": outlook.ObjectStateDeleted}}).install(t)
	job := seedRunJob(t, store)

	err := (&AutosyncManager{store: store}).microsoftRunOutcome(context.Background(), job, errMailboxMissing)
	if err == nil || !strings.Contains(err.Error(), msgPausedDeleted) {
		t.Fatalf("err = %v, want paused-deleted", err)
	}
	for _, j := range scopeJobsFor(t, store) {
		if j.Active || j.Message != msgPausedDeleted {
			t.Fatalf("job %d (%s) active=%v message=%q", j.ID, j.Method, j.Active, j.Message)
		}
	}
	res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if res.State != repo.MicrosoftResourceDeleted {
		t.Fatalf("resource state = %q", res.State)
	}
}

func TestMicrosoftRunOutcome_existingUserWithoutServiceIsSkipped(t *testing.T) {
	store := newReconcileTestDB(t)
	(&reconcileStubs{objectStates: map[string]string{"oid-ann": outlook.ObjectStateActive}}).install(t)
	job := seedRunJob(t, store)

	if err := (&AutosyncManager{store: store}).microsoftRunOutcome(context.Background(), job, errMailboxMissing); err != nil {
		t.Fatalf("missing service must skip, got %v", err)
	}
	if !ProcessorLeftWarningOutcome(job) {
		t.Fatalf("job message = %q (%s), want a warning", job.Message, job.MessageStatus)
	}
	res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if got := res.UnavailableServices(); len(got) != 1 || got[0] != "outlook" {
		t.Fatalf("unavailable services = %v", got)
	}
	for _, j := range scopeJobsFor(t, store) {
		if !j.Active {
			t.Fatalf("job %d paused for a missing service", j.ID)
		}
	}
}

func TestMicrosoftRunOutcome_otherErrorsAndLookupFailuresChangeNothing(t *testing.T) {
	store := newReconcileTestDB(t)
	stubs := &reconcileStubs{objectStates: map[string]string{}}
	stubs.install(t)
	job := seedRunJob(t, store)
	a := &AutosyncManager{store: store}

	plain := errors.New("outlook mail message http 404: item gone")
	if err := a.microsoftRunOutcome(context.Background(), job, plain); err != plain {
		t.Fatalf("unrelated error rewritten: %v", err)
	}
	if stubs.resolveCalls != 0 {
		t.Fatal("unrelated error must not trigger a lifecycle check")
	}
	if err := a.microsoftRunOutcome(context.Background(), job, errMailboxMissing); err != errMailboxMissing {
		t.Fatalf("lookup failure must keep the run error, got %v", err)
	}
	res, _ := store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if res.State != repo.MicrosoftResourceActive || len(res.UnavailableServices()) != 0 {
		t.Fatalf("resource changed: %+v", res)
	}

	google := &repo.CronJobListingDB{Provider: repo.CredentialProviderGoogle, Method: "gmail"}
	if err := a.microsoftRunOutcome(context.Background(), google, errMailboxMissing); err != errMailboxMissing {
		t.Fatalf("google job error rewritten: %v", err)
	}
}
