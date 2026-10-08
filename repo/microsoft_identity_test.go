package repo

import (
	"path/filepath"
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

func newIdentityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "identity.db")))
	if err != nil {
		t.Fatal(err)
	}
	return gdb
}

func migrateIdentityModels(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := MigrateBackupIdentity(gdb.DB); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&GoogleBackupCredentialDB{}, &CronJobListingDB{}, &TaskListingDB{},
		&MicrosoftAccountTenantDB{}, &MicrosoftResourceDB{}, &MicrosoftBackupScopeDB{}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateBackupIdentity_backfillsLegacyRowsAndSwapsIndexes(t *testing.T) {
	gdb := newIdentityTestDB(t)
	for _, stmt := range []string{
		"CREATE TABLE `google_backup_credential_dbs` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`user_id` text NOT NULL DEFAULT \"\",`email` text,`storj_project_id` text,`account_type` text NOT NULL DEFAULT \"personal\",`tenant_id` text,`tenant_name` text,`refresh_token` text,`storx_token` text)",
		`CREATE UNIQUE INDEX idx_google_backup_cred_user_project_email ON google_backup_credential_dbs (user_id, storj_project_id, email)`,
		"CREATE TABLE `cron_job_listing_dbs` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`user_id` text,`name` text,`method` text,`sync_type` text,`input_data` text,`active` numeric)",
		`CREATE UNIQUE INDEX idx_name_sync_type_user ON cron_job_listing_dbs (name, method, sync_type, user_id)`,
		`INSERT INTO google_backup_credential_dbs (id, user_id, email, storj_project_id, tenant_id) VALUES
			(1, 'u1', 'A@x.com', 'p1', ''), (2, 'u1', 'admin@contoso.com', 'p1', 'T1')`,
		`INSERT INTO cron_job_listing_dbs (user_id, name, method, sync_type, input_data) VALUES
			('u1', 'a@x.com', 'gmail', 'daily', '{"credential_id":1}'),
			('u1', 'b@x.com', 'gmail', 'daily', '{"credential_id":1}'),
			('u1', 'ann@contoso.com', 'outlook', 'daily', '{"credential_id":2}'),
			('u1', 'Sales', 'outlook_teams', 'daily', '{"credential_id":2}')`,
	} {
		if err := gdb.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}

	migrateIdentityModels(t, gdb)
	// Idempotent on restart.
	migrateIdentityModels(t, gdb)

	var creds []GoogleBackupCredentialDB
	if err := gdb.Order("id").Find(&creds).Error; err != nil {
		t.Fatal(err)
	}
	if creds[0].Provider != CredentialProviderGoogle || creds[0].ExternalAccountID != "a@x.com" {
		t.Fatalf("google credential backfill = %+v", creds[0])
	}
	if creds[1].Provider != CredentialProviderMicrosoft || creds[1].HomeTenantID() != "t1" {
		t.Fatalf("microsoft credential backfill = %+v", creds[1])
	}

	var jobs []CronJobListingDB
	if err := gdb.Order("id").Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if j := jobs[0]; j.Provider != "google" || j.TenantID != "" || j.ResourceType != ResourceTypeUser || j.ResourceID != "a@x.com" {
		t.Fatalf("google job backfill = %+v", j)
	}
	if j := jobs[2]; j.Provider != "microsoft" || j.TenantID != "t1" || j.ResourceType != ResourceTypeUser {
		t.Fatalf("microsoft mailbox job backfill = %+v", j)
	}
	if j := jobs[3]; j.ResourceType != ResourceTypeTeam || j.TenantID != "t1" {
		t.Fatalf("microsoft team job backfill = %+v", j)
	}

	if gdb.Migrator().HasIndex(&CronJobListingDB{}, legacyCronJobIndex) || gdb.Migrator().HasIndex(&GoogleBackupCredentialDB{}, legacyCredentialIndex) {
		t.Fatal("legacy unique indexes must be dropped")
	}

	// Google keeps exactly the old uniqueness: same name + method + sync type is still rejected.
	dup := CronJobListingDB{UserID: "u1", Name: "a@x.com", Method: "gmail", SyncType: "daily", InputData: database.NewDbJsonFromValue(map[string]interface{}{})}
	if err := gdb.Create(&dup).Error; err == nil {
		t.Fatal("duplicate google job must violate the identity index")
	}
	other := CronJobListingDB{UserID: "u1", Name: "c@x.com", Method: "gmail", SyncType: "daily", InputData: database.NewDbJsonFromValue(map[string]interface{}{})}
	if err := gdb.Create(&other).Error; err != nil {
		t.Fatalf("new google job: %v", err)
	}
	if other.ResourceID != "c@x.com" || other.Provider != "google" {
		t.Fatalf("google job defaults = %+v", other)
	}
}

func TestCreateMicrosoftResourceJob_identityNotName(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	jobs := NewCronJobRepository(gdb)

	// Same display name in two tenants, and a team and its group sharing one ID.
	cases := []struct {
		tenant, typ, id, method string
	}{
		{"tenant-a", ResourceTypeTeam, "id-1", "outlook_teams"},
		{"tenant-b", ResourceTypeTeam, "id-1", "outlook_teams"},
		{"tenant-a", ResourceTypeGroup, "id-1", "outlook_groups"},
		{"tenant-a", ResourceTypeTeam, "id-2", "outlook_teams"},
	}
	for _, c := range cases {
		if _, err := jobs.CreateMicrosoftResourceJob("u1", "Sales", c.method, "daily", 7,
			MicrosoftJobIdentity{TenantID: c.tenant, ResourceType: c.typ, ResourceID: c.id}, nil); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if _, err := jobs.CreateMicrosoftResourceJob("u1", "Renamed", "outlook_teams", "daily", 7,
		MicrosoftJobIdentity{TenantID: "TENANT-A", ResourceType: ResourceTypeTeam, ResourceID: "id-1"}, nil); err == nil {
		t.Fatal("same tenant + resource + method must be unique")
	}
	got, err := jobs.FindMicrosoftResourceJob("u1", "tenant-b", ResourceTypeTeam, "id-1", "outlook_teams", "daily")
	if err != nil || got == nil || got.TenantID != "tenant-b" {
		t.Fatalf("find by identity = %+v, %v", got, err)
	}
	// Same email in two tenants.
	for _, tid := range []string{"tenant-a", "tenant-b"} {
		if _, err := jobs.CreateMicrosoftResourceJob("u1", "ann@contoso.com", "outlook", "daily", 7,
			MicrosoftJobIdentity{TenantID: tid, ResourceType: ResourceTypeUser, ResourceID: "oid-" + tid}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMicrosoftAccountTenantRepository_lifecycle(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	links := NewMicrosoftAccountTenantRepository(gdb)

	if _, err := links.UpsertDiscovered(1, MicrosoftTenantDiscovery{TenantID: "T-B", TenantName: "Contoso", Category: MicrosoftTenantCategoryGuest}); err != nil {
		t.Fatal(err)
	}
	if err := links.Connect(1, "t-b", MicrosoftBackupModeOrganization, MicrosoftAuthModeApplication); err != nil {
		t.Fatal(err)
	}
	// Rediscovery refreshes metadata but keeps the user's connection and modes.
	link, err := links.UpsertDiscovered(1, MicrosoftTenantDiscovery{TenantID: "t-b", TenantName: "Contoso Ltd", Category: MicrosoftTenantCategoryGuest, ObjectID: "oid-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Connected() || !link.Application() || link.BackupMode != MicrosoftBackupModeOrganization || link.TenantName != "Contoso Ltd" || link.ObjectIDValue() != "oid-b" {
		t.Fatalf("rediscovered link = %+v", link)
	}

	if err := links.SaveRoles(1, "t-b", []MicrosoftRoleAssignment{{Name: "Global Administrator", Scope: "/"}}, MicrosoftStatusUnknown, true); err != nil {
		t.Fatal(err)
	}
	link, _ = links.Get(1, "t-b")
	if link.IsAdmin {
		t.Fatal("unknown role status must never be admin")
	}

	if err := links.Disconnect(1, "t-b"); err != nil {
		t.Fatal(err)
	}
	if err := links.Connect(1, "t-b", MicrosoftBackupModeOrganization, MicrosoftAuthModeApplication); err != nil {
		t.Fatal(err)
	}
	all, _ := links.ListByCredential(1)
	if len(all) != 1 {
		t.Fatalf("reconnect must reuse the row, got %d rows", len(all))
	}
	if err := links.Connect(1, "t-unknown", MicrosoftBackupModePersonal, MicrosoftAuthModeDelegated); err == nil {
		t.Fatal("connecting an undiscovered tenant must fail")
	}
}

func TestMicrosoftResourceRepository_stateTransitions(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	res := NewMicrosoftResourceRepository(gdb)

	if _, err := res.Upsert(MicrosoftResourceDB{TenantID: "T", ResourceType: ResourceTypeUser, ExternalID: "oid-1", DisplayName: "Ann"}); err != nil {
		t.Fatal(err)
	}
	if err := res.MarkState("t", ResourceTypeUser, "oid-1", MicrosoftResourceDeleted); err != nil {
		t.Fatal(err)
	}
	got, _ := res.Get("t", ResourceTypeUser, "oid-1")
	if got.State != MicrosoftResourceDeleted || got.DeletedAt == nil {
		t.Fatalf("deleted = %+v", got)
	}
	// Restored object (same ID) seen again becomes active.
	got, err := res.Upsert(MicrosoftResourceDB{TenantID: "t", ResourceType: ResourceTypeUser, ExternalID: "oid-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != MicrosoftResourceActive || got.DeletedAt != nil || got.DisplayName != "Ann" {
		t.Fatalf("restored = %+v", got)
	}
	if err := res.AddUnavailableService("t", ResourceTypeUser, "oid-1", "onedrive"); err != nil {
		t.Fatal(err)
	}
	got, _ = res.Get("t", ResourceTypeUser, "oid-1")
	if s := got.UnavailableServices(); len(s) != 1 || s[0] != "onedrive" {
		t.Fatalf("unavailable services = %v", s)
	}
}

func TestMicrosoftBackupScopeRepository_upsertIsUnique(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	scopes := NewMicrosoftBackupScopeRepository(gdb)

	base := MicrosoftBackupScopeDB{UserID: "u1", StorjProjectID: "p1", CredentialID: 3, TenantID: "T", ResourceType: ResourceTypeTeam, SyncType: "daily", SelectionMode: MicrosoftScopeAll}
	first, err := scopes.Upsert(base)
	if err != nil {
		t.Fatal(err)
	}
	base.SelectionMode = MicrosoftScopeSelected
	second, err := scopes.Upsert(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.SelectionMode != MicrosoftScopeSelected {
		t.Fatalf("upsert = %+v then %+v", first, second)
	}
	if err := scopes.SetReconcileResult(second.ID, "tenant_disconnected"); err != nil {
		t.Fatal(err)
	}
	active, _ := scopes.ListActive()
	if len(active) != 1 || active[0].ReconcileError != "tenant_disconnected" {
		t.Fatalf("active = %+v", active)
	}
}

func TestUpsertMicrosoftAccount_keyedByHomeObjectID(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	creds := NewGoogleBackupCredentialRepository(gdb)

	a, err := creds.UpsertMicrosoftAccount(MicrosoftAccountUpsert{UserID: "u1", StorjProjectID: "p1", ExternalAccountID: "OID-A", HomeTenantID: "T1", Email: "ann@contoso.com", RefreshToken: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := creds.UpsertMicrosoftAccount(MicrosoftAccountUpsert{UserID: "u1", StorjProjectID: "p1", ExternalAccountID: "oid-b", HomeTenantID: "t2", Email: "ann@contoso.com"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("two Microsoft sign-ins must be two credentials even with the same email")
	}
	again, err := creds.UpsertMicrosoftAccount(MicrosoftAccountUpsert{UserID: "u1", ExternalAccountID: "oid-a", RefreshToken: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != a.ID || again.RefreshToken != "r2" || again.HomeTenantID() != "t1" || !again.IsMicrosoft() {
		t.Fatalf("re-sign-in = %+v", again)
	}
	if found, ok, _ := creds.FindMicrosoftByAccount("u2", "oid-a"); ok || found != nil {
		t.Fatal("another user's sign-in must not be found")
	}
}

func TestGoogleJobIdentityUnchanged(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	jobs := NewCronJobRepository(gdb)

	job, err := jobs.CreateCronJobForUser("u1", "ann@gmail.com", "gmail", "daily", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Provider != CredentialProviderGoogle || job.TenantID != "" || job.ResourceType != ResourceTypeUser || job.ResourceID != "ann@gmail.com" {
		t.Fatalf("google identity = %q %q %q %q", job.Provider, job.TenantID, job.ResourceType, job.ResourceID)
	}
	if _, err := jobs.CreateCronJobForUser("u1", "ann@gmail.com", "gmail", "daily", map[string]interface{}{}); err == nil {
		t.Fatal("google job identity must stay unique per user, mailbox, method and sync type")
	}
	// Microsoft restore lookup never returns Google jobs, even with the same login ID.
	got, err := jobs.FindMicrosoftJobsForRestore("u1", "gmail", "", "ann@gmail.com")
	if err != nil || len(got) != 0 {
		t.Fatalf("microsoft restore lookup returned google jobs: %+v, %v", got, err)
	}
}

func TestUpsertMicrosoftAccount_storesWorkAccount(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	creds := NewGoogleBackupCredentialRepository(gdb)

	work, err := creds.UpsertMicrosoftAccount(MicrosoftAccountUpsert{
		UserID: "u1", ExternalAccountID: "oid-admin", HomeTenantID: "tenant-a", Email: "admin@contoso.com", AccountType: "work_account",
	})
	if err != nil || work.AccountType != "work_account" {
		t.Fatalf("work account stored as %q (%v)", work.AccountType, err)
	}
	// A label stored wrongly by an older sign-in is corrected by the next sign-in.
	if err := gdb.Model(&GoogleBackupCredentialDB{}).Where("id = ?", work.ID).Update("account_type", "personal").Error; err != nil {
		t.Fatal(err)
	}
	again, err := creds.UpsertMicrosoftAccount(MicrosoftAccountUpsert{
		UserID: "u1", ExternalAccountID: "oid-admin", AccountType: "work_account",
	})
	if err != nil || again.AccountType != "work_account" {
		t.Fatalf("relabel = %q (%v)", again.AccountType, err)
	}
}
