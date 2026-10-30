package handler

import "testing"

func TestMailboxFromVaultKeys_table(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{name: "calendar event", keys: []string{"dhavalder93@gmail.com/calendar/events/cal1/2026/10/30/ev.json"}, want: "dhavalder93@gmail.com"},
		{name: "skips empty then uses next", keys: []string{"", "  ", "ann@x.com/calendar/calendars/c.json"}, want: "ann@x.com"},
		{name: "no slash", keys: []string{"not-a-key"}},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailboxFromVaultKeys(tc.keys); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

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
