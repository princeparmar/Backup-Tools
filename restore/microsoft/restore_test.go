package microsoft

import (
	"testing"

)

func TestMicrosoftRestoreScopesForMethod_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		want   string
	}{
		{method: "outlook", want: "Mail.ReadWrite"},
		{method: "outlook_calendar", want: "Calendars.ReadWrite"},
		{method: "outlook_contacts", want: "Contacts.ReadWrite"},
		{method: "outlook_onedrive", want: "Files.ReadWrite.All"},
		{method: "outlook_sharepoint", want: "Files.ReadWrite.All"},
		{method: "outlook_teams", want: "ChannelMessage.Send"},
		{method: "outlook_groups", want: "Group.ReadWrite.All"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			got := microsoftRestoreScopesForMethod(tt.method)
			if len(got) == 0 || got[0] != tt.want {
				t.Fatalf("scopes=%v want first %q", got, tt.want)
			}
		})
	}
}

func TestMicrosoftGrantedScopes_opaqueAccessToken_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		accessToken    string
		endpointScope  string
		wantContains   string
		wantMissingLen int
	}{
		{
			name:          "personal opaque token uses endpoint scope",
			accessToken:   "EwCIBMl6BAAUCBUz0PacOpaqueNotAJWT",
			endpointScope: "openid profile email User.Read Mail.ReadWrite Mail.Read",
			wantContains:  "Mail.ReadWrite",
		},
		{
			name:          "opaque without write still missing",
			accessToken:   "EwCIBMl6BAAUCBUz0PacOpaqueNotAJWT",
			endpointScope: "openid User.Read Mail.Read",
			wantContains:  "Mail.Read",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			granted := microsoftGrantedScopes(tt.accessToken, tt.endpointScope)
			found := false
			for _, g := range granted {
				if g == tt.wantContains {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("granted=%v want contain %q", granted, tt.wantContains)
			}
			missing := microsoftMissingScopes(granted, []string{"Mail.ReadWrite"})
			if tt.wantContains == "Mail.ReadWrite" && len(missing) != 0 {
				t.Fatalf("missing=%v want none when Mail.ReadWrite granted", missing)
			}
			if tt.wantContains == "Mail.Read" && len(missing) != 1 {
				t.Fatalf("missing=%v want Mail.ReadWrite still missing", missing)
			}
		})
	}
}

func TestMicrosoftMissingScopes_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		granted  []string
		required []string
		wantLen  int
	}{
		{name: "all present", granted: []string{"Mail.ReadWrite", "User.Read"}, required: []string{"Mail.ReadWrite"}, wantLen: 0},
		{name: "missing one", granted: []string{"User.Read"}, required: []string{"Mail.ReadWrite"}, wantLen: 1},
		{name: "empty required", granted: nil, required: nil, wantLen: 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			missing := microsoftMissingScopes(tt.granted, tt.required)
			if len(missing) != tt.wantLen {
				t.Fatalf("missing=%v want len %d", missing, tt.wantLen)
			}
		})
	}
}

func TestShouldRestoreOutlookMailKey_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key  string
		want bool
	}{
		{key: "user@contoso.com/meta/2026/01/01/msgid.json", want: true},
		{key: "user@contoso.com/data/2026/01/01/msgid", want: false},
		{key: "user@contoso.com/.file_placeholder", want: false},
		{key: "", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			if got := shouldRestoreOutlookMailKey(tt.key); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
