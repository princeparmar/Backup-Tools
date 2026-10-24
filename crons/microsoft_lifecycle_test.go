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
	if !ProcessorLeftWarningOutcome(job) || !strings.Contains(job.Message, "Mail for ann@contoso.com was not backed up. Microsoft says this mailbox is inactive, stored on-premises, or not ready for backup.") {
		t.Fatalf("job message = %q (%s), want the mailbox warning", job.Message, job.MessageStatus)
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

	// A later successful run (the user got a mailbox) clears the record.
	if err := store.MicrosoftResourceRepo.AddUnavailableService(reconcileTenant, repo.ResourceTypeUser, "oid-ann", "outlook_onedrive"); err != nil {
		t.Fatal(err)
	}
	(&AutosyncManager{store: store}).microsoftRunSucceeded(job)
	res, _ = store.MicrosoftResourceRepo.Get(reconcileTenant, repo.ResourceTypeUser, "oid-ann")
	if got := res.UnavailableServices(); len(got) != 1 || got[0] != "outlook_onedrive" {
		t.Fatalf("after success, unavailable services = %v", got)
	}
}

func TestMicrosoftUnavailableMessageUsesGraphReason(t *testing.T) {
	err := errors.New(`list mail folders http 404: {"error":{"code":"MailboxNotEnabledForRESTAPI","message":"The mailbox is either inactive, soft-deleted, or is hosted on-premises."}}`)
	got := microsoftUnavailableMessage(&repo.CronJobListingDB{Method: "outlook", Name: "support@space.com"}, err)
	want := "Mail for support@space.com was not backed up. Microsoft says the mailbox is either inactive, soft-deleted, or is hosted on-premises. The next backup will try again."
	if got != want {
		t.Fatalf("message = %q", got)
	}
}

func TestMicrosoftUnavailableMessageNamesServiceAndReason(t *testing.T) {
	for method, want := range map[string]string{
		"outlook":          "Mail for ada@contoso.com was not backed up. Microsoft did not allow access to this mailbox.",
		"outlook_calendar": "Calendar for ada@contoso.com was not backed up. Microsoft did not allow access to this mailbox.",
		"outlook_contacts": "Contacts for ada@contoso.com were not backed up. Microsoft did not allow access to this mailbox.",
		"outlook_onedrive": "OneDrive for ada@contoso.com was not backed up. This account has no OneDrive.",
		"outlook_teams":    "Teams for ada@contoso.com was not backed up. This team has no Teams data.",
	} {
		resourceType := repo.ResourceTypeUser
		if method == "outlook_teams" {
			resourceType = repo.ResourceTypeTeam
		}
		got := microsoftUnavailableMessage(&repo.CronJobListingDB{
			Method: method, ResourceType: resourceType, Name: "ada@contoso.com",
		}, nil)
		if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "The next backup will try again.") {
			t.Errorf("%s: %q", method, got)
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
