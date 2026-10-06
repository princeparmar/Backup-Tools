package crons

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
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

func TestMicrosoftJobAccessToken_applicationUsesTenantTokenOnly(t *testing.T) {
	store := newJobAuthTestDB(t)
	cred, err := store.CredentialRepo.CreateForUserWithTenant("u1", "admin@contoso.com", "p1", "admin_workspace", "tenant-1", "Contoso", "", "storx-grant")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CredentialRepo.SetMicrosoftApplicationMode(context.Background(), cred.ID); err != nil {
		t.Fatal(err)
	}

	var gotTenant, gotCapability string
	prevOrg, prevRefresh := msJobOrgAccessFn, msJobRefreshToAccessFn
	msJobOrgAccessFn = func(ctx context.Context, _ *db.PostgresDb, tenantID, capability string) (string, *repo.MicrosoftTenantDB, error) {
		gotTenant, gotCapability = tenantID, capability
		return "app-token", nil, nil
	}
	msJobRefreshToAccessFn = func(string) (string, error) {
		t.Fatal("application jobs must never use a refresh token")
		return "", nil
	}
	t.Cleanup(func() { msJobOrgAccessFn, msJobRefreshToAccessFn = prevOrg, prevRefresh })

	job := jobForCredential("outlook_calendar", "ann@contoso.com", cred.ID)
	auth, err := microsoftJobAccessToken(ProcessorInput{Job: job, Database: store})
	if err != nil {
		t.Fatal(err)
	}
	if !auth.Application || auth.AccessToken != "app-token" || auth.StorxToken != "storx-grant" {
		t.Fatalf("auth = %+v", auth)
	}
	if gotTenant != "tenant-1" || gotCapability != outlook.CapabilityCalendar {
		t.Fatalf("org access called with tenant=%q capability=%q", gotTenant, gotCapability)
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

	msJobOrgAccessFn = func(context.Context, *db.PostgresDb, string, string) (string, *repo.MicrosoftTenantDB, error) {
		return "", nil, errors.New("consent revoked")
	}
	if _, err := microsoftJobAccessToken(ProcessorInput{Job: job, Database: store}); err == nil {
		t.Fatal("revoked tenant must fail the job, not fall back to delegated")
	}
}

func TestMicrosoftJobAccessToken_applicationWithoutTenantFails(t *testing.T) {
	store := newJobAuthTestDB(t)
	cred, err := store.CredentialRepo.CreateForUserWithTenant("u1", "admin@contoso.com", "p1", "admin_workspace", "", "", "rt", "storx-grant")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.CredentialRepo.SetMicrosoftApplicationMode(context.Background(), cred.ID)
	prevRefresh := msJobRefreshToAccessFn
	msJobRefreshToAccessFn = func(string) (string, error) {
		t.Fatal("must not fall back to the refresh token")
		return "", nil
	}
	t.Cleanup(func() { msJobRefreshToAccessFn = prevRefresh })

	if _, err := microsoftJobAccessToken(ProcessorInput{Job: jobForCredential("outlook", "ann@contoso.com", cred.ID), Database: store}); err == nil {
		t.Fatal("application credential without tenant_id must fail")
	}
}

func TestMicrosoftJobAccessToken_delegatedRequiresRefreshToken(t *testing.T) {
	store := newJobAuthTestDB(t)
	cred, err := store.CredentialRepo.CreateForUserWithTenant("u1", "me@contoso.com", "p1", "work_account", "tenant-1", "", "", "storx-grant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := microsoftJobAccessToken(ProcessorInput{Job: jobForCredential("outlook", "me@contoso.com", cred.ID), Database: store}); err == nil {
		t.Fatal("delegated job without refresh token must fail")
	}
}
