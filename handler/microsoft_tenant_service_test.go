package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

const testTenantID = "11111111-2222-3333-4444-555555555555"

func newMicrosoftTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "test.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&repo.MicrosoftTenantDB{}, &repo.GoogleBackupCredentialDB{}); err != nil {
		t.Fatal(err)
	}
	return &db.PostgresDb{
		DB:                  gdb,
		CredentialRepo:      repo.NewGoogleBackupCredentialRepository(gdb),
		MicrosoftTenantRepo: repo.NewMicrosoftTenantRepository(gdb),
	}
}

// stubMicrosoftGraph replaces Graph seams for one test.
type stubMicrosoftGraph struct {
	roles      []string
	tokenErr   error
	caps       outlook.CapabilityResult
	users      []outlook.DirectoryUser
	userRoles  map[string][]string
	samples    []string
	tokenCalls int
}

func (s *stubMicrosoftGraph) install(t *testing.T) {
	t.Helper()
	prevToken, prevInv, prevEval, prevList := msAppOnlyTokenFn, msInvalidateAppOnlyFn, msEvaluateCapabilitiesFn, msListDirectoryUsersFn
	prevRoles := msListTenantUserRolesFn
	msAppOnlyTokenFn = func(ctx context.Context, tenantID string) (string, []string, error) {
		s.tokenCalls++
		if s.tokenErr != nil {
			return "", nil, s.tokenErr
		}
		return "app-token", s.roles, nil
	}
	msInvalidateAppOnlyFn = func(string) {}
	msEvaluateCapabilitiesFn = func(ctx context.Context, in outlook.CapabilityInput) outlook.CapabilityResult {
		s.samples = in.SampleUserIDs
		return s.caps
	}
	msListDirectoryUsersFn = func(context.Context, string) ([]outlook.DirectoryUser, error) { return s.users, nil }
	msListTenantUserRolesFn = func(context.Context, string) (map[string][]string, error) { return s.userRoles, nil }
	t.Cleanup(func() {
		msListTenantUserRolesFn = prevRoles
		msAppOnlyTokenFn, msInvalidateAppOnlyFn, msEvaluateCapabilitiesFn, msListDirectoryUsersFn = prevToken, prevInv, prevEval, prevList
	})
}

func allCaps(v bool) map[string]bool {
	out := map[string]bool{}
	for _, name := range outlook.CapabilityOrder {
		out[name] = v
	}
	return out
}

func TestClassifyConsent(t *testing.T) {
	now := time.Now()
	consented := &repo.MicrosoftTenantDB{ConsentStatus: repo.MicrosoftConsentGranted, ConsentedAt: &now}
	fresh := &repo.MicrosoftTenantDB{ConsentStatus: repo.MicrosoftConsentNotRequested}
	spMissing := &outlook.AppOnlyTokenError{StatusCode: 400, Code: "unauthorized_client", Message: "AADSTS700016: Application not found in the directory"}

	cases := []struct {
		name     string
		prev     *repo.MicrosoftTenantDB
		roles    []string
		err      error
		want     string
		keepPrev bool
	}{
		{"granted", fresh, []string{"User.Read.All", "Mail.Read"}, nil, repo.MicrosoftConsentGranted, false},
		{"superset roles grant", fresh, []string{"Directory.Read.All", "Mail.ReadWrite"}, nil, repo.MicrosoftConsentGranted, false},
		{"insufficient", fresh, []string{"User.Read.All"}, nil, repo.MicrosoftConsentInsufficient, false},
		{"roles removed after consent", consented, nil, nil, repo.MicrosoftConsentRevoked, false},
		{"service principal removed", consented, nil, spMissing, repo.MicrosoftConsentRevoked, false},
		{"service principal never consented", fresh, nil, spMissing, repo.MicrosoftConsentAuthError, false},
		{"429 keeps previous", consented, nil, &outlook.AppOnlyTokenError{StatusCode: 429}, "", true},
		{"503 keeps previous", consented, nil, &outlook.AppOnlyTokenError{StatusCode: 503}, "", true},
		{"timeout keeps previous", consented, nil, context.DeadlineExceeded, "", true},
		{"other auth error", fresh, nil, &outlook.AppOnlyTokenError{StatusCode: 401, Code: "invalid_client", Message: "bad secret"}, repo.MicrosoftConsentAuthError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyConsent(tc.prev, tc.roles, tc.err)
			if got.KeepPrevious != tc.keepPrev {
				t.Fatalf("keepPrevious = %v", got.KeepPrevious)
			}
			if !tc.keepPrev && got.Status != tc.want {
				t.Fatalf("status = %q, want %q (last_error %q)", got.Status, tc.want, got.LastError)
			}
		})
	}
}

func TestCheckConsent_grantedPersistsCapabilitiesWithLiveSamples(t *testing.T) {
	database := newMicrosoftTestDB(t)
	stub := &stubMicrosoftGraph{
		users: []outlook.DirectoryUser{
			{ObjectID: "u1", AccountEnabled: true, HasLicense: true},
			{ObjectID: "u2", AccountEnabled: true},
		},
		roles: []string{"User.Read.All", "Mail.Read"},
		caps: outlook.CapabilityResult{
			Capabilities: map[string]bool{outlook.CapabilityListUsers: true, outlook.CapabilityMail: true, outlook.CapabilityCalendar: false},
			Errors:       map[string]outlook.CapabilityError{outlook.CapabilityCalendar: {Code: outlook.CapabilityErrMissingRole, Role: "Calendars.Read"}},
		},
	}
	stub.install(t)

	tenant, err := newMicrosoftTenantService(database).CheckConsent(context.Background(), testTenantID, "Contoso", "admin@contoso.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.ConsentStatus != repo.MicrosoftConsentGranted || tenant.ConsentedBy != "admin@contoso.com" || tenant.ConsentedAt == nil {
		t.Fatalf("consent not saved: %+v", tenant)
	}
	if !tenant.Capability(outlook.CapabilityMail) || tenant.Capability(outlook.CapabilityCalendar) {
		t.Fatalf("capabilities = %v", tenant.CapabilityMap())
	}
	if e := tenant.CapabilityErrorMap()[outlook.CapabilityCalendar]; e.Code != outlook.CapabilityErrMissingRole || e.Role != "Calendars.Read" {
		t.Fatalf("capability error = %+v", e)
	}
	if tenant.CapabilityStatus != outlook.CapabilityStatusRolesOnly {
		t.Fatalf("capability_status = %q", tenant.CapabilityStatus)
	}
	if len(stub.samples) != 1 || stub.samples[0] != "u1" {
		t.Fatalf("capability samples = %v", stub.samples)
	}
}

func TestCheckConsent_temporaryFailureKeepsPreviousState(t *testing.T) {
	database := newMicrosoftTestDB(t)
	stub := &stubMicrosoftGraph{roles: []string{"User.Read.All", "Mail.Read"}, caps: outlook.CapabilityResult{Capabilities: allCaps(true)}}
	stub.install(t)
	svc := newMicrosoftTenantService(database)
	if _, err := svc.CheckConsent(context.Background(), testTenantID, "Contoso", "a@b.c", true); err != nil {
		t.Fatal(err)
	}

	stub.tokenErr = &outlook.AppOnlyTokenError{StatusCode: 503, Message: "busy"}
	tenant, err := svc.RefreshCapabilities(context.Background(), testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.ConsentStatus != repo.MicrosoftConsentGranted || !tenant.Capability(outlook.CapabilityMail) {
		t.Fatalf("temporary failure must keep granted + capabilities: %+v %v", tenant.ConsentStatus, tenant.CapabilityMap())
	}
	if tenant.LastError == "" {
		t.Fatal("last_error should record the temporary failure")
	}
}

func TestCheckConsent_revokedClearsCapabilities(t *testing.T) {
	database := newMicrosoftTestDB(t)
	stub := &stubMicrosoftGraph{roles: []string{"User.Read.All", "Mail.Read"}, caps: outlook.CapabilityResult{Capabilities: allCaps(true)}}
	stub.install(t)
	svc := newMicrosoftTenantService(database)
	if _, err := svc.CheckConsent(context.Background(), testTenantID, "Contoso", "a@b.c", true); err != nil {
		t.Fatal(err)
	}
	stub.roles = []string{}
	tenant, err := svc.RefreshCapabilities(context.Background(), testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.ConsentStatus != repo.MicrosoftConsentRevoked {
		t.Fatalf("status = %q", tenant.ConsentStatus)
	}
	if tenant.Capability(outlook.CapabilityMail) {
		t.Fatal("revoked tenant must have no capabilities")
	}
}

func TestBuildMicrosoftWorkspaceContract_accountTypeLabel(t *testing.T) {
	granted := &repo.MicrosoftTenantDB{TenantID: testTenantID, ConsentStatus: repo.MicrosoftConsentGranted}
	granted.Capabilities = dbJSON(map[string]bool{outlook.CapabilityListUsers: true, outlook.CapabilityMail: true})
	noListUsers := &repo.MicrosoftTenantDB{TenantID: testTenantID, ConsentStatus: repo.MicrosoftConsentGranted}
	noListUsers.Capabilities = dbJSON(map[string]bool{outlook.CapabilityMail: true})
	insufficient := &repo.MicrosoftTenantDB{TenantID: testTenantID, ConsentStatus: repo.MicrosoftConsentInsufficient}
	insufficient.Capabilities = dbJSON(map[string]bool{outlook.CapabilityListUsers: true})

	employee := func() *outlook.MicrosoftAccountContext {
		return &outlook.MicrosoftAccountContext{AccountType: outlook.AccountTypeWorkAccount, TenantID: testTenantID, IsAdmin: true}
	}
	cases := []struct {
		name   string
		acct   *outlook.MicrosoftAccountContext
		tenant *repo.MicrosoftTenantDB
		token  bool
		want   string
	}{
		{"granted + list_users + token", employee(), granted, true, outlook.AccountTypeAdminWorkspace},
		{"token fails", employee(), granted, false, outlook.AccountTypeWorkAccount},
		{"no list_users", employee(), noListUsers, true, outlook.AccountTypeWorkAccount},
		{"insufficient consent", employee(), insufficient, true, outlook.AccountTypeWorkAccount},
		{"no tenant row (Entra admin only)", employee(), nil, true, outlook.AccountTypeWorkAccount},
		{"personal never admin", &outlook.MicrosoftAccountContext{AccountType: outlook.AccountTypePersonal}, granted, true, outlook.AccountTypePersonal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildMicrosoftWorkspaceContract(tc.acct, tc.tenant, tc.token)
			if got.AccountType != tc.want {
				t.Fatalf("account_type = %q, want %q", got.AccountType, tc.want)
			}
			if len(got.Capabilities) != len(outlook.CapabilityOrder) {
				t.Fatalf("contract must list every capability: %v", got.Capabilities)
			}
		})
	}

	got := buildMicrosoftWorkspaceContract(employee(), insufficient, true)
	if got.Capabilities[outlook.CapabilityListUsers] {
		t.Fatal("capabilities must read false while consent is not granted")
	}
}

func TestMicrosoftOrgAccess(t *testing.T) {
	database := newMicrosoftTestDB(t)
	stub := &stubMicrosoftGraph{
		roles: []string{"User.Read.All", "Mail.Read"},
		caps: outlook.CapabilityResult{
			Capabilities: map[string]bool{outlook.CapabilityListUsers: true, outlook.CapabilityMail: true},
			Errors:       map[string]outlook.CapabilityError{outlook.CapabilitySharePoint: {Code: outlook.CapabilityErrMissingRole, Role: "Sites.Read.All"}},
		},
	}
	stub.install(t)
	ctx := context.Background()

	var accessErr *MicrosoftOrgAccessError
	if _, _, err := MicrosoftOrgAccess(ctx, database, testTenantID, outlook.CapabilityMail); !errors.As(err, &accessErr) || accessErr.Code != "consent_not_requested" {
		t.Fatalf("missing tenant: %v", err)
	}
	if _, _, err := MicrosoftOrgAccess(ctx, database, outlook.MSATenantID, ""); !errors.As(err, &accessErr) || accessErr.Code != "personal_account" {
		t.Fatalf("MSA tenant: %v", err)
	}

	if _, err := newMicrosoftTenantService(database).CheckConsent(ctx, testTenantID, "", "", true); err != nil {
		t.Fatal(err)
	}
	token, _, err := MicrosoftOrgAccess(ctx, database, testTenantID, outlook.CapabilityMail)
	if err != nil || token != "app-token" {
		t.Fatalf("mail access: token=%q err=%v", token, err)
	}
	_, _, err = MicrosoftOrgAccess(ctx, database, testTenantID, outlook.CapabilitySharePoint)
	if !errors.As(err, &accessErr) || accessErr.Code != outlook.CapabilityErrMissingRole || accessErr.Capability != outlook.CapabilitySharePoint {
		t.Fatalf("sharepoint must be refused with missing_role: %v", err)
	}

	stub.tokenErr = &outlook.AppOnlyTokenError{StatusCode: 401, Code: "invalid_client"}
	if _, _, err := MicrosoftOrgAccess(ctx, database, testTenantID, outlook.CapabilityMail); !errors.As(err, &accessErr) || accessErr.Code != "app_token_failed" {
		t.Fatalf("token failure: %v", err)
	}
}

// Regression gate: delegated detect/domain-users must never write tenant consent or capabilities,
// and must not create a tenant row.
func TestDelegatedDetectDoesNotMutateTenant(t *testing.T) {
	database := newMicrosoftTestDB(t)
	stub := &stubMicrosoftGraph{roles: []string{"User.Read.All"}, caps: outlook.CapabilityResult{Capabilities: map[string]bool{outlook.CapabilityListUsers: true}}}
	stub.install(t)

	prevResolve := msResolveAccountFn
	msResolveAccountFn = func(ctx context.Context, refreshToken string) (*outlook.MicrosoftAccountContext, error) {
		return &outlook.MicrosoftAccountContext{
			Email: "admin@contoso.com", AccountType: outlook.AccountTypeWorkAccount,
			WorkspaceKind: outlook.WorkspaceKindOrganization, TenantID: testTenantID, IsAdmin: true,
			AdminRoles: []string{"Global Administrator"},
		}, nil
	}
	t.Cleanup(func() { msResolveAccountFn = prevResolve })

	call := func() *httptest.ResponseRecorder {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/microsoft/account/detect", nil)
		req.Header.Set("REFRESH_TOKEN", "delegated-refresh")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(middleware.DbContextKey, database)
		if err := HandleMicrosoftCorporateDomainUsers(c); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	rec := call()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if row, _ := database.MicrosoftTenantRepo.Get(testTenantID); row != nil {
		t.Fatal("delegated detect must not create a tenant row")
	}

	if _, err := newMicrosoftTenantService(database).CheckConsent(context.Background(), testTenantID, "Contoso", "a@b.c", true); err != nil {
		t.Fatal(err)
	}
	before, _ := database.MicrosoftTenantRepo.Get(testTenantID)
	tokenCallsBefore := stub.tokenCalls

	rec = call()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := database.MicrosoftTenantRepo.Get(testTenantID)
	if after.ConsentStatus != before.ConsentStatus || !after.UpdatedAt.Equal(before.UpdatedAt) ||
		after.LastProbeAt == nil || !after.LastProbeAt.Equal(*before.LastProbeAt) {
		t.Fatalf("delegated detect mutated tenant: before=%+v after=%+v", before, after)
	}
	if stub.tokenCalls-tokenCallsBefore > 1 {
		t.Fatalf("detect should at most check token usability once, got %d calls", stub.tokenCalls-tokenCallsBefore)
	}
}

func dbJSON[V any](v V) *database.DbJson[V] { return database.NewDbJsonFromValue(v) }
