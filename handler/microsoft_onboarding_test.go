package handler

import "testing"

func TestMicrosoftOnboardingServiceToMethod(t *testing.T) {
	cases := map[string]string{
		"outlook":    "outlook",
		"mail":       "outlook",
		"calendar":   "outlook_calendar",
		"contacts":   "outlook_contacts",
		"onedrive":   "outlook_onedrive",
		"sharepoint": "outlook_sharepoint",
		"teams":      "outlook_teams",
		"groups":     "outlook_groups",
	}
	for ui, want := range cases {
		got, ok := microsoftOnboardingServiceToMethod[ui]
		if !ok || got != want {
			t.Fatalf("service %q: got %q ok=%v want %q", ui, got, ok, want)
		}
	}
	// Google map must not gain outlook aliases.
	if _, ok := onboardingServiceToMethod["outlook"]; ok {
		t.Fatal("google onboarding map must not include outlook")
	}
	if onboardingServiceToMethod["calendar"] != "google_calendar" {
		t.Fatal("google calendar mapping changed")
	}
	if _, ok := onboardingServiceToMethod["onedrive"]; ok {
		t.Fatal("google onboarding map must not include onedrive")
	}
}

func TestAllowedMicrosoftMethods(t *testing.T) {
	for _, m := range []string{"outlook", "outlook_calendar", "outlook_contacts", "outlook_onedrive", "outlook_sharepoint", "outlook_teams", "outlook_groups"} {
		if !allowedMethods[m] {
			t.Fatalf("method %q must be allowed", m)
		}
	}
}
