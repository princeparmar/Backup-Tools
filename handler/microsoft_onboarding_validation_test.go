package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

func delegatedReq(services ...string) *MicrosoftBackupOnboardingRequest {
	return &MicrosoftBackupOnboardingRequest{
		Services: services, MicrosoftEmail: "me@contoso.com", RefreshToken: "rt", ProjectID: "p", Interval: "daily",
	}
}

func TestValidateMicrosoftDelegatedOnboarding(t *testing.T) {
	if he := validateMicrosoftDelegatedOnboarding(delegatedReq("outlook", "onedrive"), []string{"outlook", "onedrive"}, []string{"me@contoso.com"}); he != nil {
		t.Fatalf("self mailbox should pass: %v", he)
	}

	cases := map[string]func() (*MicrosoftBackupOnboardingRequest, []string, []string){
		"other mailbox": func() (*MicrosoftBackupOnboardingRequest, []string, []string) {
			return delegatedReq("outlook"), []string{"outlook"}, []string{"me@contoso.com", "other@contoso.com"}
		},
		"sharepoint": func() (*MicrosoftBackupOnboardingRequest, []string, []string) {
			return delegatedReq("sharepoint"), []string{"sharepoint"}, []string{"me@contoso.com"}
		},
		"all users": func() (*MicrosoftBackupOnboardingRequest, []string, []string) {
			r := delegatedReq("outlook")
			r.AllUsers = true
			return r, []string{"outlook"}, []string{"me@contoso.com"}
		},
		"shared mailbox": func() (*MicrosoftBackupOnboardingRequest, []string, []string) {
			r := delegatedReq("outlook")
			r.SharedMailboxes = []string{"shared@contoso.com"}
			return r, []string{"outlook"}, []string{"me@contoso.com"}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			req, svcs, emails := mk()
			he := validateMicrosoftDelegatedOnboarding(req, svcs, emails)
			if he == nil || he.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %v", he)
			}
		})
	}
}

func TestMicrosoftOnboardingRequestValidate_authModes(t *testing.T) {
	app := &MicrosoftBackupOnboardingRequest{
		Services: []string{"outlook"}, MicrosoftEmail: "admin@contoso.com", ProjectID: "p", Interval: "daily",
		BackupMode: microsoftBackupModeOrganization,
	}
	if err := app.validate("u"); err != nil {
		t.Fatalf("application mode must not require refresh_token: %v", err)
	}
	if !app.applicationMode() {
		t.Fatal("backup_mode=organization implies application auth")
	}

	self := delegatedReq("outlook")
	self.RefreshToken = ""
	if err := self.validate("u"); err == nil {
		t.Fatal("delegated mode requires refresh_token")
	}

	conflict := delegatedReq("outlook")
	conflict.AuthMode = outlook.MicrosoftAuthModeDelegated
	conflict.BackupMode = microsoftBackupModeOrganization
	if err := conflict.validate("u"); err == nil {
		t.Fatal("conflicting auth_mode/backup_mode must fail")
	}
}

func TestAuthorizeMicrosoftApplicationOnboarding(t *testing.T) {
	database := newMicrosoftTestDB(t)
	stub := &stubMicrosoftGraph{
		roles: []string{"User.Read.All", "Mail.Read"},
		caps: outlook.CapabilityResult{
			Capabilities: map[string]bool{outlook.CapabilityListUsers: true, outlook.CapabilityMail: true},
			Errors: map[string]outlook.CapabilityError{
				outlook.CapabilitySharePoint: {Code: outlook.CapabilityErrMissingRole, Role: "Sites.Read.All"},
			},
		},
	}
	stub.install(t)
	ctx := context.Background()
	req := &MicrosoftBackupOnboardingRequest{AllUsers: true}

	var accessErr *MicrosoftOrgAccessError
	_, err := authorizeMicrosoftApplicationOnboarding(ctx, database, req, testTenantID, []string{"outlook"})
	if !errors.As(err, &accessErr) || accessErr.HTTPStatus != http.StatusForbidden || accessErr.Code != "consent_not_requested" {
		t.Fatalf("no consent: %v", err)
	}

	if _, err := newMicrosoftTenantService(database).CheckConsent(ctx, testTenantID, "Contoso", "a@b.c", true); err != nil {
		t.Fatal(err)
	}

	_, err = authorizeMicrosoftApplicationOnboarding(ctx, database, req, testTenantID, []string{"sharepoint"})
	if !errors.As(err, &accessErr) || accessErr.Code != outlook.CapabilityErrMissingRole || accessErr.Capability != outlook.CapabilitySharePoint {
		t.Fatalf("sharepoint must be refused naming the missing role: %v", err)
	}

	stub.users = []outlook.DirectoryUser{
		{ObjectID: "u1", UPN: "ann@contoso.com", Department: "Sales", AccountEnabled: true},
		{ObjectID: "u2", UPN: "bob@contoso.com", AccountEnabled: false},
	}
	got, err := authorizeMicrosoftApplicationOnboarding(ctx, database, req, testTenantID, []string{"outlook"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Emails) != 1 || got.Emails[0] != "ann@contoso.com" || got.OrgUnits["ann@contoso.com"] != "/Sales" {
		t.Fatalf("all_users must expand enabled directory users with org units: %+v", got)
	}
	if got.AccessToken != "app-token" {
		t.Fatalf("token = %q", got.AccessToken)
	}

	_, err = authorizeMicrosoftApplicationOnboarding(ctx, database,
		&MicrosoftBackupOnboardingRequest{Emails: []string{"stranger@other.com"}}, testTenantID, []string{"outlook"})
	if !errors.As(err, &accessErr) || accessErr.Code != "unknown_users" {
		t.Fatalf("unknown mailbox must be rejected: %v", err)
	}
	shared, err := authorizeMicrosoftApplicationOnboarding(ctx, database,
		&MicrosoftBackupOnboardingRequest{SharedMailboxes: []string{"BOB@contoso.com"}}, testTenantID, []string{"outlook"})
	if err != nil || len(shared.Emails) != 1 || shared.ObjectIDs["bob@contoso.com"] != "u2" {
		t.Fatalf("disabled shared mailbox must be allowed: %+v %v", shared, err)
	}

	// is_admin / account_type never authorize: same tenant, revoked consent, admin label irrelevant.
	stub.roles = nil
	if _, err := newMicrosoftTenantService(database).RefreshCapabilities(ctx, testTenantID); err != nil {
		t.Fatal(err)
	}
	_, err = authorizeMicrosoftApplicationOnboarding(ctx, database, req, testTenantID, []string{"outlook"})
	if !errors.As(err, &accessErr) || accessErr.Code != "consent_revoked" {
		t.Fatalf("revoked consent must block org backup: %v", err)
	}
}

func TestNormalizeCredentialAccountTypeForMicrosoft(t *testing.T) {
	if normalizeCredentialAccountTypeForMicrosoft(" admin_workspace ") != "admin_workspace" {
		t.Fatal("expected normalized admin_workspace")
	}
	if normalizeCredentialAccountTypeForMicrosoft("bogus") != "" {
		t.Fatal("bogus type should be empty")
	}
}
