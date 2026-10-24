package mstenant

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	homeTenant  = "11111111-1111-1111-1111-111111111111"
	guestTenant = "22222222-2222-2222-2222-222222222222"
)

func newTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "mstenant.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&repo.GoogleBackupCredentialDB{}, &repo.CronJobListingDB{}, &repo.MicrosoftTenantDB{},
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

// fakeJWT builds an unsigned token whose payload carries claims.
func fakeJWT(t *testing.T, claims map[string]string) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

// tokenStubs replaces both token minters. tokenTenant maps the requested tenant to the tid the
// minted token claims; a missing entry echoes the requested tenant.
type tokenStubs struct {
	delegatedCalls, appCalls int
	tokenTenant              map[string]string
	delegatedErr, appErr     error
}

func (s *tokenStubs) install(t *testing.T) {
	t.Helper()
	prevD, prevA := delegatedTokenFn, appOnlyTokenFn
	tidFor := func(requested string) string {
		if v, ok := s.tokenTenant[requested]; ok {
			return v
		}
		return requested
	}
	delegatedTokenFn = func(refresh, authority, scope string) (*outlook.TokenResponse, error) {
		s.delegatedCalls++
		if s.delegatedErr != nil {
			return nil, s.delegatedErr
		}
		return &outlook.TokenResponse{AccessToken: fakeJWT(t, map[string]string{"tid": tidFor(authority)}), Scope: "Mail.Read"}, nil
	}
	appOnlyTokenFn = func(ctx context.Context, tenantID string) (string, []string, error) {
		s.appCalls++
		if s.appErr != nil {
			return "", nil, s.appErr
		}
		return fakeJWT(t, map[string]string{"tid": tidFor(tenantID)}), []string{"User.Read.All", "Mail.Read"}, nil
	}
	t.Cleanup(func() { delegatedTokenFn, appOnlyTokenFn = prevD, prevA })
}

func newCredential(t *testing.T, store *db.PostgresDb, userID, oid string) *repo.GoogleBackupCredentialDB {
	t.Helper()
	cred, err := store.CredentialRepo.UpsertMicrosoftAccount(repo.MicrosoftAccountUpsert{
		UserID: userID, ExternalAccountID: oid, HomeTenantID: homeTenant, Email: oid + "@contoso.com",
		AccountType: outlook.AccountTypeWorkAccount, RefreshToken: "rt-" + oid, StorjProjectID: "p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

func link(t *testing.T, store *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, tid, backupMode string) {
	t.Helper()
	if _, err := store.MicrosoftLinkRepo.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{
		TenantID: tid, Category: repo.MicrosoftTenantCategoryHome, HomeTenantID: homeTenant,
	}); err != nil {
		t.Fatal(err)
	}
	if backupMode == "" {
		return
	}
	if _, err := Connect(store, cred, tid, backupMode); err != nil {
		t.Fatal(err)
	}
}

// grantConsent records effective consent (granted + service principal) and capabilities.
func grantConsent(t *testing.T, store *db.PostgresDb, tid string, servicePrincipal bool, caps map[string]bool) {
	t.Helper()
	if _, err := store.MicrosoftTenantRepo.GetOrCreate(tid, "Contoso"); err != nil {
		t.Fatal(err)
	}
	if err := store.MicrosoftTenantRepo.SaveConsent(tid, repo.MicrosoftConsentUpdate{Status: repo.MicrosoftConsentGranted, MarkConsented: true}); err != nil {
		t.Fatal(err)
	}
	if servicePrincipal {
		if err := store.MicrosoftTenantRepo.SaveServicePrincipal(tid, "sp-1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MicrosoftTenantRepo.SaveCapabilities(tid, repo.MicrosoftCapabilityUpdate{Capabilities: caps}); err != nil {
		t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if !IsCode(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestResolve_rejectsAnotherUsersCredential(t *testing.T) {
	store := newTestDB(t)
	(&tokenStubs{}).install(t)
	cred := newCredential(t, store, "owner", "oid-a")
	link(t, store, cred, homeTenant, repo.MicrosoftBackupModePersonal)

	_, err := Resolve(context.Background(), store, Request{UserID: "intruder", CredentialID: cred.ID})
	wantCode(t, err, CodeCredentialForbidden)

	_, err = Resolve(context.Background(), store, Request{UserID: "intruder", ExternalAccountID: "oid-a"})
	wantCode(t, err, CodeCredentialNotFound)
}

func TestResolve_rejectsForeignUnlinkedAndDisconnectedTenants(t *testing.T) {
	store := newTestDB(t)
	stubs := &tokenStubs{}
	stubs.install(t)
	mine := newCredential(t, store, "u1", "oid-a")
	other := newCredential(t, store, "u2", "oid-b")
	link(t, store, mine, homeTenant, repo.MicrosoftBackupModePersonal)
	link(t, store, other, guestTenant, repo.MicrosoftBackupModePersonal)

	// Another sign-in's link to guestTenant does not let this credential act there.
	_, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: mine.ID, TenantID: guestTenant})
	wantCode(t, err, CodeTenantNotLinked)

	link(t, store, mine, guestTenant, "")
	_, err = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: mine.ID, TenantID: guestTenant})
	wantCode(t, err, CodeTenantDisconnected)

	if err := store.MicrosoftLinkRepo.Disconnect(mine.ID, homeTenant); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: mine.ID, AllowDiscovered: true})
	wantCode(t, err, CodeTenantDisconnected)
	if stubs.delegatedCalls != 0 || stubs.appCalls != 0 {
		t.Fatal("refused requests must not mint tokens")
	}
}

func TestResolve_homeTenantHeaderMismatch(t *testing.T) {
	store := newTestDB(t)
	(&tokenStubs{}).install(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, repo.MicrosoftBackupModePersonal)
	_, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, HomeTenantID: guestTenant})
	wantCode(t, err, CodeTenantMismatch)
}

func TestResolve_tokenForAnotherTenantIsRejected(t *testing.T) {
	store := newTestDB(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, guestTenant, repo.MicrosoftBackupModePersonal)
	(&tokenStubs{tokenTenant: map[string]string{guestTenant: homeTenant}}).install(t)

	_, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, TenantID: guestTenant})
	wantCode(t, err, CodeTenantMismatch)

	grantConsent(t, store, guestTenant, true, map[string]bool{outlook.CapabilityListUsers: true})
	if _, err := Connect(store, cred, guestTenant, repo.MicrosoftBackupModeOrganization); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, TenantID: guestTenant})
	wantCode(t, err, CodeTenantMismatch)
}

func TestResolve_picksDelegatedOrApplicationFromLink(t *testing.T) {
	store := newTestDB(t)
	stubs := &tokenStubs{}
	stubs.install(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, repo.MicrosoftBackupModePersonal)
	grantConsent(t, store, homeTenant, true, map[string]bool{outlook.CapabilityListUsers: true, outlook.CapabilityMail: true})

	tc, err := Resolve(context.Background(), store, Request{UserID: "u1", ExternalAccountID: "oid-a", Capability: outlook.CapabilityMail})
	if err != nil {
		t.Fatal(err)
	}
	if tc.Application || stubs.delegatedCalls != 1 || stubs.appCalls != 0 || tc.TenantID != homeTenant {
		t.Fatalf("personal link must use delegated access: %+v (d=%d a=%d)", tc, stubs.delegatedCalls, stubs.appCalls)
	}
	_, err = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, RequireApplication: true})
	wantCode(t, err, CodeOrganizationModeRequired)

	if _, err := Connect(store, cred, homeTenant, repo.MicrosoftBackupModeOrganization); err != nil {
		t.Fatal(err)
	}
	tc, err = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, Capability: outlook.CapabilityMail})
	if err != nil {
		t.Fatal(err)
	}
	if !tc.Application || stubs.appCalls != 1 || stubs.delegatedCalls != 1 {
		t.Fatalf("organization link must use app-only access: %+v (d=%d a=%d)", tc, stubs.delegatedCalls, stubs.appCalls)
	}
}

func TestResolve_discoveredLinkOnlyForDelegatedPreview(t *testing.T) {
	store := newTestDB(t)
	stubs := &tokenStubs{}
	stubs.install(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, "")

	_, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID})
	wantCode(t, err, CodeTenantDisconnected)
	_, err = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, AllowDiscovered: true, RequireApplication: true})
	wantCode(t, err, CodeTenantDisconnected)

	tc, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID, AllowDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if tc.Application {
		t.Fatal("a discovered link is never application access")
	}
}

func TestResolve_consentNeedsServicePrincipalAndCapabilities(t *testing.T) {
	store := newTestDB(t)
	stubs := &tokenStubs{}
	stubs.install(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, "")
	if _, err := Connect(store, cred, homeTenant, repo.MicrosoftBackupModeOrganization); err != nil {
		t.Fatal(err)
	}
	req := Request{UserID: "u1", CredentialID: cred.ID, Capability: outlook.CapabilityMail}

	_, err := Resolve(context.Background(), store, req)
	wantCode(t, err, CodeConsentRequired)

	// Granted but no service principal: not consent.
	grantConsent(t, store, homeTenant, false, map[string]bool{outlook.CapabilityListUsers: true, outlook.CapabilityMail: true})
	_, err = Resolve(context.Background(), store, req)
	wantCode(t, err, CodeConsentRequired)

	// Service principal alone (consent not granted): not consent either.
	if err := store.MicrosoftTenantRepo.SaveConsent(homeTenant, repo.MicrosoftConsentUpdate{Status: repo.MicrosoftConsentNotRequested}); err != nil {
		t.Fatal(err)
	}
	if err := store.MicrosoftTenantRepo.SaveServicePrincipal(homeTenant, "sp-1"); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(context.Background(), store, req)
	wantCode(t, err, CodeConsentRequired)
	if stubs.appCalls != 0 {
		t.Fatal("no app-only token without effective consent")
	}

	grantConsent(t, store, homeTenant, true, map[string]bool{outlook.CapabilityListUsers: true})
	_, err = Resolve(context.Background(), store, req)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeCapabilityDenied || e.Capability != outlook.CapabilityMail {
		t.Fatalf("missing capability must be capability_denied naming it: %v", err)
	}

	grantConsent(t, store, homeTenant, true, map[string]bool{outlook.CapabilityMail: true})
	_, err = Resolve(context.Background(), store, req)
	if !errors.As(err, &e) || e.Capability != outlook.CapabilityListUsers {
		t.Fatalf("list_users is always required: %v", err)
	}
}

func TestResolve_unavailableTenantRefusedAndJobsPaused(t *testing.T) {
	store := newTestDB(t)
	(&tokenStubs{}).install(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, repo.MicrosoftBackupModePersonal)
	job := &repo.CronJobListingDB{UserID: "u1", Name: "a@contoso.com", Method: "outlook", Provider: repo.CredentialProviderMicrosoft,
		TenantID: homeTenant, Active: true, InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(cred.ID)})}
	if err := store.DB.Create(job).Error; err != nil {
		t.Fatal(err)
	}

	MarkTenantUnavailable(store, homeTenant, "AADSTS90002")
	_, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID})
	wantCode(t, err, CodeTenantUnavailable)

	var got repo.CronJobListingDB
	if err := store.DB.First(&got, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Active || got.MessageStatus != "warning" {
		t.Fatalf("job must be paused with a warning: active=%v status=%q", got.Active, got.MessageStatus)
	}
}

// backup_mode only changes through Connect: failures and consent changes never flip it.
func TestResolve_neverChangesBackupMode(t *testing.T) {
	store := newTestDB(t)
	stubs := &tokenStubs{appErr: &outlook.AppOnlyTokenError{StatusCode: 401, Code: "invalid_client", Message: "nope"}}
	stubs.install(t)
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, "")
	grantConsent(t, store, homeTenant, true, map[string]bool{outlook.CapabilityListUsers: true})
	if _, err := Connect(store, cred, homeTenant, repo.MicrosoftBackupModeOrganization); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID}); err == nil {
		t.Fatal("app-only failure must fail the request")
	}
	_ = store.MicrosoftTenantRepo.SaveConsent(homeTenant, repo.MicrosoftConsentUpdate{Status: repo.MicrosoftConsentRevoked})
	_, _ = Resolve(context.Background(), store, Request{UserID: "u1", CredentialID: cred.ID})

	l, err := store.MicrosoftLinkRepo.Get(cred.ID, homeTenant)
	if err != nil {
		t.Fatal(err)
	}
	if l.BackupMode != repo.MicrosoftBackupModeOrganization || l.AuthMode != repo.MicrosoftAuthModeApplication || !l.Connected() {
		t.Fatalf("link mode changed automatically: %+v", l)
	}
	if stubs.delegatedCalls != 0 {
		t.Fatal("an organization link must never fall back to delegated access")
	}
}

// Token or role-read failures never make someone an admin.
func TestCheckTenantAccess_failuresNeverGrantAdmin(t *testing.T) {
	store := newTestDB(t)
	stubs := &tokenStubs{}
	stubs.install(t)
	prevMe, prevRoles := meIdentityFn, readRolesFn
	meIdentityFn = func(context.Context, string) (outlook.MeIdentity, error) { return outlook.MeIdentity{}, errors.New("boom") }
	readRolesFn = func(context.Context, string, string, string) outlook.TenantRolesResult {
		return outlook.TenantRolesResult{Status: outlook.RoleStatusUnknown,
			Roles: []outlook.TenantRole{{TemplateID: "62e90394-69f5-4237-9190-012177145e10", Name: "Global Administrator", Scope: "/"}}}
	}
	t.Cleanup(func() { meIdentityFn, readRolesFn = prevMe, prevRoles })
	cred := newCredential(t, store, "u1", "oid-a")
	link(t, store, cred, homeTenant, "")

	CheckTenantAccess(context.Background(), store, cred, homeTenant, "")
	l, _ := store.MicrosoftLinkRepo.Get(cred.ID, homeTenant)
	if l.IsAdmin || l.RoleStatus != repo.MicrosoftStatusUnknown {
		t.Fatalf("unknown role status must not be admin: %+v", l)
	}

	stubs.delegatedErr = &outlook.TokenEndpointError{StatusCode: 400, Code: "invalid_grant", Description: "AADSTS50076: MFA required"}
	CheckTenantAccess(context.Background(), store, cred, homeTenant, "")
	l, _ = store.MicrosoftLinkRepo.Get(cred.ID, homeTenant)
	if l.IsAdmin || l.TokenStatus == repo.MicrosoftStatusWorking {
		t.Fatalf("token failure must not be admin or working: %+v", l)
	}
	state := BuildAccessState(l, nil)
	if state.IsAdmin || state.CanOrganizationBackup {
		t.Fatalf("access state must not grant organization backup: %+v", state)
	}
}
