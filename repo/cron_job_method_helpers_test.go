package repo

import "testing"

func TestAutosyncMethodHelpers_Google(t *testing.T) {
	for _, m := range GoogleAutosyncMethods {
		if !IsGoogleAutosyncMethod(m) {
			t.Fatalf("expected google method %q", m)
		}
		if IsMicrosoftAutosyncMethod(m) {
			t.Fatalf("google method %q must not be microsoft", m)
		}
	}
	if IsGoogleAutosyncMethod("outlook") {
		t.Fatal("outlook must not be google")
	}
}

func TestAutosyncMethodHelpers_Microsoft(t *testing.T) {
	for _, m := range MicrosoftAutosyncMethods {
		if !IsMicrosoftAutosyncMethod(m) {
			t.Fatalf("expected microsoft method %q", m)
		}
		if IsGoogleAutosyncMethod(m) {
			t.Fatalf("microsoft method %q must not be google", m)
		}
		if !IsSharedCredentialAutosyncMethod(m) {
			t.Fatalf("expected shared credential method %q", m)
		}
	}
}

func TestAutosyncMethodHelpers_AllAutosyncMethods(t *testing.T) {
	all := AllAutosyncMethods()
	if len(all) != len(GoogleAutosyncMethods)+len(MicrosoftAutosyncMethods) {
		t.Fatalf("want %d methods, got %d", len(GoogleAutosyncMethods)+len(MicrosoftAutosyncMethods), len(all))
	}
	for _, m := range all {
		if !IsSharedCredentialAutosyncMethod(m) {
			t.Fatalf("expected shared credential method %q", m)
		}
	}
}

func TestAutosyncMethodHelpers_FilterJobs(t *testing.T) {
	jobs := []CronJobListingDB{
		{Method: "gmail"},
		{Method: "outlook"},
		{Method: "outlook_calendar"},
		{Method: "outlook_onedrive"},
		{Method: "outlook_sharepoint"},
		{Method: "outlook_teams"},
		{Method: "outlook_groups"},
		{Method: "google_drive"},
	}
	ms := FilterJobsByMethods(jobs, IsMicrosoftAutosyncMethod)
	if len(ms) != 6 {
		t.Fatalf("want 6 microsoft jobs, got %d", len(ms))
	}
	g := FilterJobsByMethods(jobs, IsGoogleAutosyncMethod)
	if len(g) != 2 {
		t.Fatalf("want 2 google jobs, got %d", len(g))
	}
}
