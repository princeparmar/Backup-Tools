package restore

import (
	"testing"

)

func TestAPIServiceToMethod_microsoftAliases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		service APIService
		method  string
		wantOK  bool
	}{
		{name: "outlook", service: APIServiceOutlook, method: "outlook", wantOK: true},
		{name: "mail alias", service: APIServiceMail, method: "outlook", wantOK: true},
		{name: "outlook_calendar", service: APIServiceOutlookCalendar, method: "outlook_calendar", wantOK: true},
		{name: "outlook_contacts", service: APIServiceOutlookContacts, method: "outlook_contacts", wantOK: true},
		{name: "onedrive alias", service: APIServiceOneDrive, method: "outlook_onedrive", wantOK: true},
		{name: "sharepoint alias", service: APIServiceSharePoint, method: "outlook_sharepoint", wantOK: true},
		{name: "teams alias", service: APIServiceTeams, method: "outlook_teams", wantOK: true},
		{name: "groups alias", service: APIServiceGroups, method: "outlook_groups", wantOK: true},
		{name: "google calendar unchanged", service: APIServiceCalendar, method: "google_calendar", wantOK: true},
		{name: "google contacts unchanged", service: APIServiceContacts, method: "google_contacts", wantOK: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := APIServiceToMethod[tt.service]
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if got != tt.method {
				t.Fatalf("method=%q want %q", got, tt.method)
			}
		})
	}
}

func TestIsMicrosoftRestoreMethod_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		want   bool
	}{
		{method: "outlook", want: true},
		{method: "outlook_calendar", want: true},
		{method: "outlook_sharepoint", want: true},
		{method: "gmail", want: false},
		{method: "google_drive", want: false},
		{method: "", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			if got := IsMicrosoftRestoreMethod(tt.method); got != tt.want {
				t.Fatalf("IsMicrosoftRestoreMethod(%q)=%v want %v", tt.method, got, tt.want)
			}
		})
	}
}

func TestConfigForMethod_microsoftBuckets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method   string
		provider string
		source   string
	}{
		{method: "outlook", provider: RestoreProviderMicrosoft, source: "outlook"},
		{method: "outlook_onedrive", provider: RestoreProviderMicrosoft, source: "outlook"},
		{method: "gmail", provider: RestoreProviderGoogle, source: "google"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			cfg, ok := ConfigForMethod(tt.method)
			if !ok {
				t.Fatalf("missing config")
			}
			if cfg.Provider != tt.provider {
				t.Fatalf("provider=%q want %q", cfg.Provider, tt.provider)
			}
			if cfg.Source != tt.source {
				t.Fatalf("source=%q want %q", cfg.Source, tt.source)
			}
			if cfg.Bucket == "" {
				t.Fatalf("bucket empty")
			}
		})
	}
}

