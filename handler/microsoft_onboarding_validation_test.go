package handler

import (
	"net/http"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

func TestValidateMicrosoftAdminForOnboarding_selfOnly(t *testing.T) {
	err := validateMicrosoftAdminForOnboarding(
		[]string{"outlook"},
		[]string{"me@contoso.com"},
		nil, nil, nil,
		"me@contoso.com",
		outlook.AccountTypePersonal,
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("self-only personal should pass: %v", err)
	}
}

func TestValidateMicrosoftAdminForOnboarding_otherMailboxRequiresAdmin(t *testing.T) {
	err := validateMicrosoftAdminForOnboarding(
		[]string{"outlook"},
		[]string{"me@contoso.com", "other@contoso.com"},
		nil, nil, nil,
		"me@contoso.com",
		outlook.AccountTypeEmployeeWorkspace,
		"tenant-aaa",
		nil,
	)
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %v", err)
	}
}

func TestValidateMicrosoftAdminForOnboarding_sharepointRequiresAdmin(t *testing.T) {
	err := validateMicrosoftAdminForOnboarding(
		[]string{"sharepoint"},
		[]string{"admin@contoso.com"},
		[]SharePointSiteOnboardingInput{{SiteID: "site-1"}},
		nil, nil,
		"admin@contoso.com",
		outlook.AccountTypeEmployeeWorkspace,
		"tenant-aaa",
		nil,
	)
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %v", err)
	}
}

func TestValidateMicrosoftAdminForOnboarding_multiMailboxNeedsTenant(t *testing.T) {
	err := validateMicrosoftAdminForOnboarding(
		[]string{"outlook"},
		[]string{"admin@contoso.com", "user@contoso.com"},
		nil, nil, nil,
		"admin@contoso.com",
		outlook.AccountTypeAdminWorkspace,
		"",
		&repo.GoogleBackupCredentialDB{TenantID: ""},
	)
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing tenant_id, got %v", err)
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
