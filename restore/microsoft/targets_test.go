package microsoft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

	prevDel := delegatedRestoreTokenFn
	t.Cleanup(func() { delegatedRestoreTokenFn = prevDel })
	delegatedRestoreTokenFn = func(string, string, string) (*outlook.TokenResponse, error) {
		return nil, errors.New("no delegated write")
	}
	stubTargets(t, &OrganizationTenant{TenantID: orgTenant, CredentialID: admin.ID, Name: "Fabrikam", Token: readOnly}, user, nil)
	res := run()
	if res.Ready || res.Reason != restore.ReadinessReasonMissingPermissions ||
		len(res.MissingPermissions) != 1 || res.MissingPermissions[0].Scope != "Mail.ReadWrite" {
		t.Fatalf("read-only consent must report the missing write role: %+v", res)
	}

	delegatedRestoreTokenFn = func(string, string, string) (*outlook.TokenResponse, error) {
		return &outlook.TokenResponse{
			AccessToken: fakeJWT(map[string]interface{}{"scp": "Mail.ReadWrite"}),
			Scope:       "Mail.ReadWrite",
		}, nil
	}
	probedGrant := stubTargets(t, &OrganizationTenant{TenantID: orgTenant, CredentialID: admin.ID, Name: "Fabrikam", Token: readOnly}, user, nil)
	res = run()
	if !res.Ready || len(*probedGrant) != 1 {
		t.Fatalf("Grant Access mail write should restore into the org mailbox: %+v probed=%v", res, *probedGrant)
	}
	delegatedRestoreTokenFn = func(string, string, string) (*outlook.TokenResponse, error) {
		return nil, errors.New("no delegated write")
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

	teamsJob := targetsSourceJob(t, store, source, "outlook_teams")
	res, err := evaluateMigrationReadiness(t.Context(), store, &restore.ReadinessResult{}, "u1", "p1", "outlook_teams", "outlook_teams", teamsJob, "bob@fabrikam.com")
	if err != nil || res.Ready || res.Reason != restore.ReadinessReasonMigrationUnsupported {
		t.Fatalf("teams: %+v, %v", res, err)
	}

	job := targetsSourceJob(t, store, source, "outlook")
	res, err = evaluateMigrationReadiness(t.Context(), store, &restore.ReadinessResult{}, "u1", "p1", "outlook", "outlook", job, "nobody@fabrikam.com")
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

func TestSharePointSiteReadiness(t *testing.T) {
	store := newTargetsTestDB(t)
	admin := targetsCred(t, store, "admin@contoso.com", srcTenant, repo.MicrosoftAuthModeApplication)
	job := targetsSourceJob(t, store, admin, "outlook_sharepoint")
	const siteID = "contoso.sharepoint.com,11111111-1111-1111-1111-111111111111,22222222-2222-2222-2222-222222222222"

	prevResolve, prevOrg, prevSite := resolveTenantFn, tenantOrganizationFn, checkSharePointSiteFn
	t.Cleanup(func() {
		resolveTenantFn, tenantOrganizationFn, checkSharePointSiteFn = prevResolve, prevOrg, prevSite
	})
	token := fakeJWT(map[string]interface{}{"roles": []string{"Sites.ReadWrite.All"}})
	resolveTenantFn = func(_ context.Context, _ *db.PostgresDb, req mstenant.Request) (*mstenant.Context, error) {
		return &mstenant.Context{TenantID: req.TenantID, Token: token, Application: true}, nil
	}
	tenantOrganizationFn = func(context.Context, string) (outlook.TenantOrganization, error) {
		return outlook.TenantOrganization{Name: "Contoso", DefaultDomain: "contoso.com", Domains: []string{"contoso.com"}}, nil
	}
	var probed string
	checkSharePointSiteFn = func(_ context.Context, gotToken, site string) (string, error) {
		if gotToken != token {
			t.Fatalf("token %q", gotToken)
		}
		probed = site
		return "drive-default", nil
	}

	run := func(target string) *restore.ReadinessResult {
		t.Helper()
		out := &restore.ReadinessResult{Service: "outlook_sharepoint"}
		res, err := evaluateMigrationReadiness(t.Context(), store, out, "u1", "p1", "outlook_sharepoint", "outlook_sharepoint", job, target)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := run(siteID)
	if !res.Ready || !res.Migration || res.CredentialID != admin.ID || res.TenantID != srcTenant || res.TargetEmail != siteID || probed != siteID {
		t.Fatalf("ready site: %+v probed=%q", res, probed)
	}
	res = run("contoso.sharepoint.com_11111111-1111-1111-1111-111111111111_22222222-2222-2222-2222-222222222222")
	if !res.Ready || probed != siteID || res.TargetEmail != siteID {
		t.Fatalf("sanitized site must probe Graph id: %+v probed=%q", res, probed)
	}

	checkSharePointSiteFn = func(context.Context, string, string) (string, error) {
		return "", outlook.ErrSharePointSiteNoDocumentLibrary
	}
	res = run(siteID)
	if res.Ready || res.Reason != restore.ReadinessReasonTargetUnavailable || res.Message != "The destination site has no document library" {
		t.Fatalf("site without a library: %+v", res)
	}

	checkSharePointSiteFn = func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("sharepoint default drive http 404: missing")
	}
	res = run(siteID)
	if res.Ready || res.Reason != restore.ReadinessReasonTargetUnavailable || !strings.Contains(res.Message, "not found") {
		t.Fatalf("unknown site: %+v", res)
	}

	resolveTenantFn = func(_ context.Context, _ *db.PostgresDb, req mstenant.Request) (*mstenant.Context, error) {
		return &mstenant.Context{
			TenantID: req.TenantID, Application: true,
			Token: fakeJWT(map[string]interface{}{"roles": []string{"Sites.Read.All"}}),
		}, nil
	}
	prevDel := delegatedRestoreTokenFn
	t.Cleanup(func() { delegatedRestoreTokenFn = prevDel })
	delegatedRestoreTokenFn = func(string, string, string) (*outlook.TokenResponse, error) {
		return nil, errors.New("no delegated write")
	}
	res = run(siteID)
	if res.Ready || res.Reason != restore.ReadinessReasonMissingPermissions {
		t.Fatalf("missing write role: %+v", res)
	}
	gotScopes := map[string]bool{}
	for _, p := range res.MissingPermissions {
		gotScopes[p.Scope] = true
	}
	if !gotScopes["Files.ReadWrite.All"] || !gotScopes["Sites.ReadWrite.All"] {
		t.Fatalf("grant scopes %+v", res.MissingPermissions)
	}

	delTok := fakeJWT(map[string]interface{}{"scp": "Files.ReadWrite.All Sites.Read.All"})
	delegatedRestoreTokenFn = func(string, string, string) (*outlook.TokenResponse, error) {
		return &outlook.TokenResponse{AccessToken: delTok, Scope: "Files.ReadWrite.All Sites.Read.All"}, nil
	}
	var wroteWith string
	checkSharePointSiteFn = func(_ context.Context, gotToken, site string) (string, error) {
		wroteWith = gotToken
		if site != siteID {
			t.Fatalf("site %q", site)
		}
		return "drive-default", nil
	}
	res = run(siteID)
	if !res.Ready || wroteWith != delTok {
		t.Fatalf("Grant Access Files.ReadWrite.All should restore the site: ready=%v token=%q", res.Ready, wroteWith)
	}

	res = run("someone@contoso.com")
	if res.Ready || res.Reason != restore.ReadinessReasonNoCredential {
		t.Fatalf("an email is not a SharePoint site: %+v", res)
	}
}
