package microsoft

import (
	"strings"
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
		{key: "user@contoso.com/data/2026/01/01/msgid.json", want: false},
		{key: "user@contoso.com/data/2026/01/01/msgid", want: false},
		{key: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/Inbox/2026/01/01/a@b.c - s - C - M.outlook", want: true},
		{key: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/_folders.json", want: false},
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

func TestShouldRestoreOutlookOneDriveKey_table(t *testing.T) {
	t.Parallel()
	const prefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid"
	tests := []struct {
		key  string
		want bool
	}{
		{key: prefix + "/MY_DRIVE/F1/ITEM1$report.txt", want: true},
		{key: prefix + "/MY_DRIVE/F1/.folder__Docs", want: true},
		{key: prefix + "/BIN/ITEM2$old.txt", want: true},
		{key: prefix + "/meta/2026/01/01/ITEM3_a.txt.json", want: true},
		{key: prefix + "/data/2026/01/01/ITEM3_a.txt", want: false},
		{key: prefix + "/.file_placeholder", want: false},
		{key: "", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			if got := shouldRestoreOutlookOneDriveKey(tt.key); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRestoreOutlookCalendarAndContactsKeys(t *testing.T) {
	t.Parallel()
	const prefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid"
	for key, want := range map[string]bool{
		prefix + "/AQMkCal/AAMkEvent.json": true,
		prefix + "/AQMkCal/_calendar.json": false,
		prefix + "/AQMkCal/_index.json":    false,
		prefix + "/.file_placeholder":      false,
		prefix + "/AQMkCal/notes.txt":      false,
		"":                                 false,
	} {
		if got := shouldRestoreOutlookCalendarKey(key); got != want {
			t.Errorf("calendar %q = %v, want %v", key, got, want)
		}
	}
	for key, want := range map[string]bool{
		prefix + "/c1.json":                 true,
		prefix + "/folders/F1/c2.json":      true,
		prefix + "/folders/F1/_folder.json": false,
		prefix + "/folders/F1/_index.json":  false,
		prefix + "/_index.json":             false,
	} {
		if got := shouldRestoreOutlookContactsKey(key); got != want {
			t.Errorf("contacts %q = %v, want %v", key, got, want)
		}
	}
}

func TestContactFolderIDForDefaultFolderSkipsLookup(t *testing.T) {
	t.Parallel()
	if got := ContactFolderIDForKey(t.Context(), "", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/c1.json", nil); got != "" {
		t.Fatalf("default folder id = %q", got)
	}
}

func TestRestoreUserBase(t *testing.T) {
	t.Parallel()
	key := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid-1/MY_DRIVE/ITEM1$a.txt"
	got, err := restoreUserBase(true, key)
	if err != nil || !strings.HasSuffix(got, "/users/oid-1") {
		t.Fatalf("app-only base = %q, %v", got, err)
	}
	if got, err := restoreUserBase(false, key); err != nil || !strings.HasSuffix(got, "/me") {
		t.Fatalf("delegated base = %q, %v", got, err)
	}
	if _, err := restoreUserBase(true, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/group/g1/x"); err == nil {
		t.Fatal("group key must not resolve to a user")
	}
}
