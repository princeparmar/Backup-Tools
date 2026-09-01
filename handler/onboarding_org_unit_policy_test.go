package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestNormalizedPolicyScope(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", OnboardingPolicyScopeAll},
		{"all", OnboardingPolicyScopeAll},
		{"org_unit", OnboardingPolicyScopeOrgUnit},
		{"OU", OnboardingPolicyScopeOrgUnit},
		{"groups", OnboardingPolicyScopeOrgUnit},
	}
	for _, tc := range cases {
		req := &GoogleBackupOnboardingRequest{PolicyScope: tc.in}
		if got := req.normalizedPolicyScope(); got != tc.want {
			t.Fatalf("scope %q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestOnboardingPolicyBaseNameOverridesDefault(t *testing.T) {
	cred := &repo.GoogleBackupCredentialDB{AccountType: "admin_workspace", Email: "admin@co.com"}
	if got := onboardingPolicyBaseName(cred, nil); got != corporateDefaultPolicyName {
		t.Fatalf("nil req: got %q", got)
	}
	if got := onboardingPolicyBaseName(cred, &GoogleBackupOnboardingRequest{}); got != corporateDefaultPolicyName {
		t.Fatalf("empty name: got %q", got)
	}
	if got := onboardingPolicyBaseName(cred, &GoogleBackupOnboardingRequest{PolicyName: "Nightly all"}); got != "Nightly all" {
		t.Fatalf("ui name: got %q", got)
	}
}

func TestDefaultOrgUnitPolicyName(t *testing.T) {
	if got := defaultOrgUnitPolicyName("/"); got != "OU Root defaults" {
		t.Fatalf("root: got %q", got)
	}
	if got := defaultOrgUnitPolicyName("/SAles"); got != "OU SAles defaults" {
		t.Fatalf("sales: got %q", got)
	}
}

func TestValidateOrgUnitOnboardingSchedules(t *testing.T) {
	req := &GoogleBackupOnboardingRequest{
		AccountType: "admin_workspace",
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
			"b@co.com": "/HR",
		},
		OrgUnitSchedules: map[string]OrgUnitScheduleInput{
			"/SAles": {Interval: "daily", On: "12am", PolicyName: "Sales", Services: []string{"gmail"}},
			"/HR":    {Interval: "weekly", On: "Monday", Services: []string{"gmail"}},
		},
	}
	if err := validateOrgUnitOnboardingSchedules(req, []string{"a@co.com", "b@co.com"}); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}

	reqMissing := &GoogleBackupOnboardingRequest{
		AccountType: "admin_workspace",
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
		},
	}
	if err := validateOrgUnitOnboardingSchedules(reqMissing, []string{"a@co.com"}); err == nil {
		t.Fatal("expected missing schedule error")
	}

	reqFallback := &GoogleBackupOnboardingRequest{
		AccountType: "admin_workspace",
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		Interval:    "daily",
		On:          "12am",
		Services:    []string{"gmail"},
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
		},
	}
	if err := validateOrgUnitOnboardingSchedules(reqFallback, []string{"a@co.com"}); err != nil {
		t.Fatalf("fallback should work: %v", err)
	}

	reqPerOUServices := &GoogleBackupOnboardingRequest{
		AccountType: "admin_workspace",
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
			"b@co.com": "/HR",
		},
		OrgUnitSchedules: map[string]OrgUnitScheduleInput{
			"/SAles": {Interval: "daily", On: "12am", Services: []string{"gmail", "drive"}},
			"/HR":    {Interval: "weekly", On: "Monday", Services: []string{"gmail"}},
		},
	}
	if err := validateOrgUnitOnboardingSchedules(reqPerOUServices, []string{"a@co.com", "b@co.com"}); err != nil {
		t.Fatalf("per-OU services should work: %v", err)
	}

	reqMissingServices := &GoogleBackupOnboardingRequest{
		AccountType: "admin_workspace",
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
		},
		OrgUnitSchedules: map[string]OrgUnitScheduleInput{
			"/SAles": {Interval: "daily", On: "12am"},
		},
	}
	if err := validateOrgUnitOnboardingSchedules(reqMissingServices, []string{"a@co.com"}); err == nil {
		t.Fatal("expected missing services error")
	}
}

func TestServicesForOnboardingOrgUnit(t *testing.T) {
	req := &GoogleBackupOnboardingRequest{
		Services: []string{"gmail", "drive"},
		OrgUnitSchedules: map[string]OrgUnitScheduleInput{
			"/SAles": {Services: []string{"gmail", "contacts"}},
		},
	}
	got, err := servicesForOnboardingOrgUnit(req, "/SAles")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "gmail" || got[1] != "contacts" {
		t.Fatalf("override: %#v", got)
	}
	got, err = servicesForOnboardingOrgUnit(req, "/HR")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "gmail" || got[1] != "drive" {
		t.Fatalf("fallback: %#v", got)
	}
}

func TestEmailsForOnboardingOrgUnit(t *testing.T) {
	req := &GoogleBackupOnboardingRequest{
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
			"b@co.com": "/SAles",
			"c@co.com": "/HR",
		},
	}
	got := emailsForOnboardingOrgUnit(req, []string{"a@co.com", "b@co.com", "c@co.com"}, "/SAles")
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
}

func TestLookupOrgUnitScheduleInputNormalizesPath(t *testing.T) {
	req := &GoogleBackupOnboardingRequest{
		OrgUnitSchedules: map[string]OrgUnitScheduleInput{
			"SAles": {Interval: "daily", On: "12am"},
		},
	}
	input, ok := lookupOrgUnitScheduleInput(req, "/SAles")
	if !ok {
		t.Fatal("expected match after normalize")
	}
	if input.Interval != "daily" {
		t.Fatalf("interval=%q", input.Interval)
	}
	if google.NormalizeOrgUnitPath("SAles") != "/SAles" {
		t.Fatal("normalize sanity")
	}
}

func TestOnboardingNeedsScheduleInBodyOrgUnit(t *testing.T) {
	req := &GoogleBackupOnboardingRequest{PolicyScope: OnboardingPolicyScopeOrgUnit}
	if onboardingNeedsScheduleInBody(true, req) {
		t.Fatal("org_unit scope should not require top-level schedule body")
	}
}

func TestUniqueOnboardingOrgUnitPaths(t *testing.T) {
	req := &GoogleBackupOnboardingRequest{
		PolicyScope: OnboardingPolicyScopeOrgUnit,
		EmailOrgUnits: map[string]string{
			"a@co.com": "/SAles",
			"b@co.com": "/SAles",
			"c@co.com": "/HR",
		},
	}
	got := uniqueOnboardingOrgUnitPaths(req, []string{"a@co.com", "b@co.com", "c@co.com"})
	if len(got) != 2 || got[0] != "/HR" || got[1] != "/SAles" {
		t.Fatalf("got %#v", got)
	}
}
