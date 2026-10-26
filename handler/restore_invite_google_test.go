package handler

import "testing"

func TestGoogleRestoreService(t *testing.T) {
	for _, service := range []string{"gmail", "google_drive", "google_photos", "google_calendar", "google_contacts"} {
		if !googleRestoreService(service) {
			t.Fatalf("%s should resolve the invited member's Google access token", service)
		}
	}
	for _, service := range []string{"outlook", "onedrive", "outlook_contacts", "outlook_calendar", "outlook_sharepoint", "outlook_teams"} {
		if googleRestoreService(service) {
			t.Fatalf("%s must not look up a Google access token", service)
		}
	}
}
