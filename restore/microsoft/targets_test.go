package microsoft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
)

const (
	srcTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	orgTenant = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func newTargetsTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "targets.db")))
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

func targetsCred(t *testing.T, store *db.PostgresDb, email, tenant, authMode string) *repo.GoogleBackupCredentialDB {
	t.Helper()
	cred, err := store.CredentialRepo.UpsertMicrosoftAccount(repo.MicrosoftAccountUpsert{
		UserID: "u1", ExternalAccountID: "oid-" + email, HomeTenantID: tenant, Email: email,
		AccountType: outlook.AccountTypeWorkAccount, RefreshToken: "rt", StorjProjectID: "p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MicrosoftLinkRepo.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{
		TenantID: tenant, Category: repo.MicrosoftTenantCategoryHome, HomeTenantID: tenant,
	}); err != nil {
		t.Fatal(err)
	}
	mode := repo.MicrosoftBackupModePersonal
	if authMode == repo.MicrosoftAuthModeApplication {
		mode = repo.MicrosoftBackupModeOrganization
	}
	if err := store.MicrosoftLinkRepo.Connect(cred.ID, tenant, mode, authMode); err != nil {
		t.Fatal(err)
	}
	return cred
}

func targetsSourceJob(t *testing.T, store *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, method string) *repo.CronJobListingDB {
	t.Helper()
	job := &repo.CronJobListingDB{
		UserID: "u1", Name: cred.Email, Method: method, SyncType: "daily",
		Provider: repo.CredentialProviderMicrosoft, TenantID: srcTenant,
		ResourceType: repo.ResourceTypeUser, ResourceID: "oid-ann",
	}
	job.InputData = database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(cred.ID)})
	if err := store.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
		TenantID: srcTenant, ResourceType: repo.ResourceTypeUser, ExternalID: "oid-ann", Mail: cred.Email,
	}); err != nil {
		t.Fatal(err)
	}
	return job
}

func fakeJWT(claims map[string]interface{}) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func stubTargets(t *testing.T, org *OrganizationTenant, user *outlook.DirectoryUser, probeErr error) *[]string {
	t.Helper()
	probed := &[]string{}
	prevFind, prevProbe := findOrganizationTargetFn, checkRestoreTargetFn
	t.Cleanup(func() { findOrganizationTargetFn, checkRestoreTargetFn = prevFind, prevProbe })
	findOrganizationTargetFn = func(context.Context, *db.PostgresDb, string, string) (*OrganizationTenant, *outlook.DirectoryUser, error) {
		return org, user, nil
	}
	checkRestoreTargetFn = func(_ context.Context, _, userBase, method string) error {
		*probed = append(*probed, userBase+" "+method)
		return probeErr
	}
	return probed
}

func TestMigrationReadinessOrganizationTarget(t *testing.T) {
	store := newTargetsTestDB(t)
	source := targetsCred(t, store, "ann@contoso.com", srcTenant, repo.MicrosoftAuthModeDelegated)
	admin := targetsCred(t, store, "admin@fabrikam.com", orgTenant, repo.MicrosoftAuthModeApplication)
	job := targetsSourceJob(t, store, source, "outlook")
	user := &outlook.DirectoryUser{ObjectID: "oid-bob", Mail: "bob@fabrikam.com", AccountEnabled: true}
	readOnly := fakeJWT(map[string]interface{}{"roles": []string{"Mail.Read", "User.Read.All"}})
	readWrite := fakeJWT(map[string]interface{}{"roles": []string{"Mail.ReadWrite", "User.Read.All"}})

	run := func() *restore.ReadinessResult {
		t.Helper()
		out := &restore.ReadinessResult{Service: "outlook", TargetEmail: "bob@fabrikam.com"}
		res, err := evaluateMigrationReadiness(t.Context(), store, out, "u1", "p1", "outlook", "outlook", job, "bob@fabrikam.com")
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	stubTargets(t, &OrganizationTenant{TenantID: orgTenant, CredentialID: admin.ID, Name: "Fabrikam", Token: readOnly}, user, nil)
	res := run()
	if res.Ready || res.Reason != restore.ReadinessReasonMissingPermissions ||
		len(res.MissingPermissions) != 1 || res.MissingPermissions[0].Scope != "Mail.ReadWrite" {
		t.Fatalf("read-only consent must report the missing write role: %+v", res)
	}

	probed := stubTargets(t, &OrganizationTenant{TenantID: orgTenant, CredentialID: admin.ID, Token: readWrite}, user, nil)
	res = run()
	if !res.Ready || res.CredentialID != admin.ID || res.TenantID != orgTenant {
		t.Fatalf("cross-tenant restore into an organization user must be ready in that tenant: %+v", res)
	}
	if len(*probed) != 1 || (*probed)[0] != "https://graph.microsoft.com/v1.0/users/oid-bob outlook" {
		t.Fatalf("destination mailbox not checked: %v", *probed)
	}

	stubTargets(t, &OrganizationTenant{TenantID: orgTenant, CredentialID: admin.ID, Token: readWrite}, user,
		fmt.Errorf("%w (MailboxNotEnabledForRESTAPI)", outlook.ErrRestoreTargetUnavailable))
	if res = run(); res.Ready || res.Reason != restore.ReadinessReasonTargetUnavailable {
		t.Fatalf("user without a mailbox: %+v", res)
	}
}

func TestMigrationReadinessOwnSignIn(t *testing.T) {
	store := newTargetsTestDB(t)
	source := targetsCred(t, store, "ann@contoso.com", srcTenant, repo.MicrosoftAuthModeApplication)
	target := targetsCred(t, store, "carol@outlook.com", outlook.MSATenantID, repo.MicrosoftAuthModeDelegated)
	job := targetsSourceJob(t, store, source, "outlook_calendar")
	stubTargets(t, nil, nil, nil)

	prevResolve := resolveTenantFn
	t.Cleanup(func() { resolveTenantFn = prevResolve })
	var resolved mstenant.Request
	resolveTenantFn = func(_ context.Context, _ *db.PostgresDb, req mstenant.Request) (*mstenant.Context, error) {
		resolved = req
		return &mstenant.Context{TenantID: req.TenantID, Token: "opaque", TokenScope: "Calendars.ReadWrite offline_access"}, nil
	}

	out := &restore.ReadinessResult{Service: "outlook_calendar"}
	res, err := evaluateMigrationReadiness(t.Context(), store, out, "u1", "p1", "outlook_calendar", "outlook_calendar", job, "carol@outlook.com")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready || res.CredentialID != target.ID || res.TenantID != outlook.MSATenantID {
		t.Fatalf("organization backup into a personal account writes with that account: %+v", res)
	}
	if resolved.CredentialID != target.ID || resolved.TenantID != outlook.MSATenantID {
		t.Fatalf("token resolved for %+v", resolved)
	}
}

func TestMigrationReadinessRefusals(t *testing.T) {
	store := newTargetsTestDB(t)
	source := targetsCred(t, store, "ann@contoso.com", srcTenant, repo.MicrosoftAuthModeDelegated)
	stubTargets(t, nil, nil, nil)

	for _, method := range []string{"outlook_teams", "outlook_sharepoint"} {
		job := targetsSourceJob(t, store, source, method)
		res, err := evaluateMigrationReadiness(t.Context(), store, &restore.ReadinessResult{}, "u1", "p1", method, method, job, "bob@fabrikam.com")
		if err != nil || res.Ready || res.Reason != restore.ReadinessReasonMigrationUnsupported {
			t.Fatalf("%s: %+v, %v", method, res, err)
		}
	}

	job := targetsSourceJob(t, store, source, "outlook")
	res, err := evaluateMigrationReadiness(t.Context(), store, &restore.ReadinessResult{}, "u1", "p1", "outlook", "outlook", job, "nobody@fabrikam.com")
	if err != nil || res.Ready || res.Reason != restore.ReadinessReasonNoCredential {
		t.Fatalf("destination neither connected nor in a connected organization: %+v, %v", res, err)
	}
}

func TestListOrganizationTenants(t *testing.T) {
	store := newTargetsTestDB(t)
	targetsCred(t, store, "ann@contoso.com", srcTenant, repo.MicrosoftAuthModeDelegated)
	admin := targetsCred(t, store, "admin@fabrikam.onmicrosoft.com", orgTenant, repo.MicrosoftAuthModeApplication)

	prevResolve, prevOrg, prevFind := resolveTenantFn, tenantOrganizationFn, findDirectoryUserFn
	t.Cleanup(func() { resolveTenantFn, tenantOrganizationFn, findDirectoryUserFn = prevResolve, prevOrg, prevFind })
	resolveTenantFn = func(_ context.Context, _ *db.PostgresDb, req mstenant.Request) (*mstenant.Context, error) {
		if !req.RequireApplication {
			t.Fatalf("organization tenants must use app-only tokens: %+v", req)
		}
		return &mstenant.Context{TenantID: req.TenantID, Token: "app-" + req.TenantID, Application: true}, nil
	}
	tenantOrganizationFn = func(context.Context, string) (outlook.TenantOrganization, error) {
		return outlook.TenantOrganization{Name: "Fabrikam", DefaultDomain: "fabrikam.com",
			Domains: []string{"fabrikam.onmicrosoft.com", "fabrikam.com"}}, nil
	}
	var searched []string
	findDirectoryUserFn = func(_ context.Context, token, email string) (outlook.DirectoryUser, error) {
		searched = append(searched, token+" "+email)
		if email == "bob@fabrikam.com" {
			return outlook.DirectoryUser{ObjectID: "oid-bob"}, nil
		}
		return outlook.DirectoryUser{}, outlook.ErrDirectoryUserNotFound
	}

	tenants, err := ListOrganizationTenants(t.Context(), store, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 1 || tenants[0].TenantID != orgTenant || tenants[0].CredentialID != admin.ID ||
		tenants[0].DefaultDomain != "fabrikam.com" || !tenants[0].HasDomain("FABRIKAM.onmicrosoft.com") {
		t.Fatalf("only the application-mode tenant is a destination organization: %+v", tenants)
	}

	org, user, err := FindOrganizationTarget(t.Context(), store, "u1", "bob@fabrikam.com")
	if err != nil || org == nil || user.ObjectID != "oid-bob" {
		t.Fatalf("member found: %+v %+v %v", org, user, err)
	}
	searched = nil
	if org, _, err := FindOrganizationTarget(t.Context(), store, "u1", "guest@gmail.com"); err != nil || org != nil || len(searched) != 0 {
		t.Fatalf("an address outside the tenant's domains is never searched: %+v %v %v", org, err, searched)
	}
}
