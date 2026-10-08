package restore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func newRestoreTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "restore.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&repo.GoogleBackupCredentialDB{}, &repo.CronJobListingDB{}, &repo.MicrosoftTenantDB{},
		&repo.MicrosoftAccountTenantDB{}, &repo.MicrosoftResourceDB{}); err != nil {
		t.Fatal(err)
	}
	return &db.PostgresDb{
		DB:                    gdb,
		CronJobRepo:           repo.NewCronJobRepository(gdb),
		CredentialRepo:        repo.NewGoogleBackupCredentialRepository(gdb),
		MicrosoftTenantRepo:   repo.NewMicrosoftTenantRepository(gdb),
		MicrosoftLinkRepo:     repo.NewMicrosoftAccountTenantRepository(gdb),
		MicrosoftResourceRepo: repo.NewMicrosoftResourceRepository(gdb),
	}
}

func restoreCred(t *testing.T, store *db.PostgresDb, oid string) *repo.GoogleBackupCredentialDB {
	t.Helper()
	cred, err := store.CredentialRepo.UpsertMicrosoftAccount(repo.MicrosoftAccountUpsert{
		UserID: "u1", ExternalAccountID: oid, HomeTenantID: tenantA, Email: oid + "@contoso.com",
		AccountType: outlook.AccountTypeWorkAccount, RefreshToken: "rt-" + oid, StorjProjectID: "p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

func connectLink(t *testing.T, store *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, tid string) {
	t.Helper()
	if _, err := store.MicrosoftLinkRepo.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{
		TenantID: tid, Category: repo.MicrosoftTenantCategoryHome, HomeTenantID: tenantA,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MicrosoftLinkRepo.Connect(cred.ID, tid, repo.MicrosoftBackupModePersonal, repo.MicrosoftAuthModeDelegated); err != nil {
		t.Fatal(err)
	}
}

func microsoftJob(t *testing.T, store *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, tid, mailbox, oid string) *repo.CronJobListingDB {
	t.Helper()
	job := &repo.CronJobListingDB{
		UserID: "u1", Name: mailbox, Method: "outlook", SyncType: "daily",
		Provider: repo.CredentialProviderMicrosoft, TenantID: tid,
		ResourceType: repo.ResourceTypeUser, ResourceID: oid,
	}
	job.InputData = database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(cred.ID)})
	if err := store.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
		TenantID: tid, ResourceType: repo.ResourceTypeUser, ExternalID: oid, Mail: mailbox,
	}); err != nil {
		t.Fatal(err)
	}
	return job
}

func wantMismatch(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrMicrosoftTenantMismatch) {
		t.Fatalf("want tenant mismatch, got %v", err)
	}
}

func TestMicrosoftTenantGuard_sameTenantPasses(t *testing.T) {
	store := newRestoreTestDB(t)
	cred := restoreCred(t, store, "oid-ann")
	connectLink(t, store, cred, tenantA)
	job := microsoftJob(t, store, cred, tenantA, "ann@contoso.com", "oid-ann")
	if err := CheckMicrosoftTenantGuard(store, tenantA, job, cred); err != nil {
		t.Fatal(err)
	}
}

func TestMicrosoftTenantGuard_jobWithoutTenant(t *testing.T) {
	store := newRestoreTestDB(t)
	cred := restoreCred(t, store, "oid-ann")
	connectLink(t, store, cred, tenantA)
	job := microsoftJob(t, store, cred, tenantA, "ann@contoso.com", "oid-ann")
	job.TenantID = ""
	wantMismatch(t, CheckMicrosoftTenantGuard(store, tenantA, job, cred))
}

func TestMicrosoftTenantGuard_resourceNotInJobTenant(t *testing.T) {
	store := newRestoreTestDB(t)
	cred := restoreCred(t, store, "oid-ann")
	connectLink(t, store, cred, tenantA)
	job := microsoftJob(t, store, cred, tenantA, "ann@contoso.com", "oid-ann")
	job.ResourceID = "oid-unknown"
	wantMismatch(t, CheckMicrosoftTenantGuard(store, tenantA, job, cred))
}

func TestMicrosoftTenantGuard_targetTenantDiffers(t *testing.T) {
	store := newRestoreTestDB(t)
	cred := restoreCred(t, store, "oid-ann")
	connectLink(t, store, cred, tenantA)
	connectLink(t, store, cred, tenantB)
	job := microsoftJob(t, store, cred, tenantA, "ann@contoso.com", "oid-ann")
	wantMismatch(t, CheckMicrosoftTenantGuard(store, tenantB, job, cred))
	wantMismatch(t, CheckMicrosoftTenantGuard(store, "", job, cred))
}

func TestMicrosoftTenantGuard_writeAccountNotConnectedToTenant(t *testing.T) {
	store := newRestoreTestDB(t)
	source := restoreCred(t, store, "oid-ann")
	connectLink(t, store, source, tenantA)
	job := microsoftJob(t, store, source, tenantA, "ann@contoso.com", "oid-ann")

	target := restoreCred(t, store, "oid-bob")
	connectLink(t, store, target, tenantB)
	wantMismatch(t, CheckMicrosoftTenantGuard(store, tenantA, job, target))

	if err := store.MicrosoftLinkRepo.Disconnect(source.ID, tenantA); err != nil {
		t.Fatal(err)
	}
	wantMismatch(t, CheckMicrosoftTenantGuard(store, tenantA, job, source))
	wantMismatch(t, CheckMicrosoftTenantGuard(store, tenantA, job, nil))
}

func TestFindMicrosoftRestoreJob_sameMailboxInTwoTenants(t *testing.T) {
	store := newRestoreTestDB(t)
	cred := restoreCred(t, store, "oid-ann")
	jobA := microsoftJob(t, store, cred, tenantA, "ann@contoso.com", "oid-ann")
	jobB := microsoftJob(t, store, cred, tenantB, "ann@contoso.com", "oid-ann-guest")

	if _, err := FindMicrosoftRestoreJob(store, "u1", "outlook", "", "ann@contoso.com"); !errors.Is(err, ErrMicrosoftTenantRequired) {
		t.Fatalf("want tenant required, got %v", err)
	}
	for tid, want := range map[string]uint{tenantA: jobA.ID, tenantB: jobB.ID} {
		got, err := FindMicrosoftRestoreJob(store, "u1", "outlook", tid, "ann@contoso.com")
		if err != nil || got == nil || got.ID != want {
			t.Fatalf("tenant %s: got %+v, %v; want job %d", tid, got, err, want)
		}
	}
	got, err := FindMicrosoftRestoreJob(store, "u1", "outlook", tenantA, "oid-ann")
	if err != nil || got == nil || got.ID != jobA.ID {
		t.Fatalf("lookup by resource id: got %+v, %v", got, err)
	}
	if got, err := FindMicrosoftRestoreJob(store, "u2", "outlook", tenantA, "ann@contoso.com"); err != nil || got != nil {
		t.Fatalf("another user's job must not match: %+v, %v", got, err)
	}
}

func TestRestoreKeyPrefix(t *testing.T) {
	cronJob := &repo.CronJobListingDB{TenantID: tenantA, ResourceType: repo.ResourceTypeUser, ResourceID: "oid-ann"}
	ms := &repo.RestoreJobListingDB{Method: "outlook", LoginID: "ann@contoso.com"}
	if got, want := RestoreKeyPrefix(ms, cronJob), tenantA+"/user/oid-ann/"; got != want {
		t.Fatalf("microsoft prefix = %q, want %q", got, want)
	}

	// Google keeps the login-ID layout regardless of job identity columns.
	google := &repo.RestoreJobListingDB{Method: "gmail", LoginID: "ann@gmail.com"}
	googleJob := &repo.CronJobListingDB{Provider: repo.CredentialProviderGoogle, ResourceType: repo.ResourceTypeUser, ResourceID: "ann@gmail.com"}
	if got := RestoreKeyPrefix(google, googleJob); got != "ann@gmail.com/" {
		t.Fatalf("google prefix = %q", got)
	}
	if got := RestoreKeyPrefix(google, nil); got != "ann@gmail.com/" {
		t.Fatalf("google prefix without job = %q", got)
	}
}
