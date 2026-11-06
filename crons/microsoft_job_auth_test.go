package crons

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func newJobAuthTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "jobs.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&repo.GoogleBackupCredentialDB{}); err != nil {
		t.Fatal(err)
	}
	return &db.PostgresDb{
		DB:             gdb,
		CredentialRepo: repo.NewGoogleBackupCredentialRepository(gdb),
		CronJobRepo:    repo.NewCronJobRepository(gdb),
	}
}

func jobForCredential(method, name string, credID uint) *repo.CronJobListingDB {
	return &repo.CronJobListingDB{
		Name:      name,
		Method:    method,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(credID), "email": name}),
	}
}

func TestMicrosoftJobAccessToken_resolvesJobTenantAndCredential(t *testing.T) {
	store := newJobAuthTestDB(t)
	cred, err := store.CredentialRepo.CreateForUserWithTenant("u1", "admin@contoso.com", "p1", "admin_workspace", "home-tenant", "Contoso", "", "storx-grant")
	if err != nil {
		t.Fatal(err)
	}

	var got mstenant.Request
	prev := msJobResolveFn
	msJobResolveFn = func(_ context.Context, _ *db.PostgresDb, req mstenant.Request) (*mstenant.Context, error) {
		got = req
		return &mstenant.Context{Token: "app-token", TenantID: req.TenantID, Application: true}, nil
	}
	t.Cleanup(func() { msJobResolveFn = prev })

	job := jobForCredential("outlook_calendar", "ann@contoso.com", cred.ID)
	job.UserID, job.TenantID = "u1", "guest-tenant"
	auth, err := microsoftJobAccessToken(ProcessorInput{Job: job, Database: store})
	if err != nil {
		t.Fatal(err)
	}
	if !auth.Application || auth.AccessToken != "app-token" || auth.StorxToken != "storx-grant" || auth.TenantID != "guest-tenant" {
		t.Fatalf("auth = %+v", auth)
	}
	if got.UserID != "u1" || got.CredentialID != cred.ID || got.TenantID != "guest-tenant" || got.Capability != outlook.CapabilityCalendar {
		t.Fatalf("resolver called with %+v", got)
	}

	client, err := microsoftJobClient(auth, jobOutlookMailbox(job))
	if err != nil {
		t.Fatal(err)
	}
	base, err := client.MailUserBaseURL("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(base, "/me") || !strings.Contains(base, "/users/ann@contoso.com") {
		t.Fatalf("application job built %s", base)
	}
	if _, err := microsoftJobClient(auth, ""); err == nil {
		t.Fatal("application job without a mailbox must fail instead of falling back to /me")
	}

	msJobResolveFn = func(context.Context, *db.PostgresDb, mstenant.Request) (*mstenant.Context, error) {
		return nil, errors.New("consent revoked")
	}
	if _, err := microsoftJobAccessToken(ProcessorInput{Job: job, Database: store}); err == nil {
		t.Fatal("resolver failure must fail the job")
	}
}

func TestMicrosoftJobAccessToken_jobWithoutCredentialFails(t *testing.T) {
	store := newJobAuthTestDB(t)
	prev := msJobResolveFn
	msJobResolveFn = func(context.Context, *db.PostgresDb, mstenant.Request) (*mstenant.Context, error) {
		t.Fatal("resolver must not run without a credential")
		return nil, nil
	}
	t.Cleanup(func() { msJobResolveFn = prev })
	job := &repo.CronJobListingDB{Method: "outlook", StorxToken: "storx", InputData: database.NewDbJsonFromValue(map[string]interface{}{"email": "a@b.c"})}
	if _, err := microsoftJobAccessToken(ProcessorInput{Job: job, Database: store}); err == nil {
		t.Fatal("job without credential_id must fail")
	}
}
