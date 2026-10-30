package microsoft

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
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

func TestMicrosoftRestoreGrantScopesSharePoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		want   []string
	}{
		{method: "outlook_sharepoint", want: []string{"Files.ReadWrite.All", "Sites.ReadWrite.All"}},
		{method: "outlook_onedrive", want: []string{"Files.ReadWrite.All"}},
		{method: "outlook", want: []string{"Mail.ReadWrite"}},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			got, _ := microsoftRestoreGrantScopes(tt.method, nil)
			if len(got) != len(tt.want) {
				t.Fatalf("grant=%v want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("grant=%v want %v", got, tt.want)
				}
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

func TestShouldRestoreOutlookTeamsMeetingKeys(t *testing.T) {
	t.Parallel()
	const prefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/team/team-1"
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	for key, want := range map[string]bool{
		prefix + "/meetings/_meetings.json":                         false,
		prefix + "/meetings/meet-1/_participants.json":              false,
		prefix + "/channels/channel-1/messages/msg-1.json":          true,
		prefix + "/channels/channel-1/meta/msg-1.json":              true,
		prefix + "/channels/channel-1/data/msg-1.json":              false,
		prefix + "/channels/channel-1/attachments/msg-1.json":       false,
		prefix + "/CHANNELS/ch-1/2026/10/01/100 - 101.teams":        true,
		prefix + "/CHANNELS/ch-1/hosted/101/img-1":                  false,
		tenant + "/chat/19%3Achat%40thread.v2/2026/10/01/200.teams": true,
		tenant + "/meeting/p-abc/event.json":                        false,
		tenant + "/calls/2026-10/01/call-1.json":                    false,
		tenant + "/user/u1/CHATS/.chat__19%3Achat%40thread.v2":      false,
		prefix + "/_team.json":                                      false,
		prefix + "/.file_placeholder":                               false,
		"":                                                          false,
	} {
		if got := shouldRestoreOutlookTeamsKey(key); got != want {
			t.Errorf("teams key %q = %v, want %v", key, got, want)
		}
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

func TestRestoreWriteUserBase(t *testing.T) {
	prev := findOrganizationTargetFn
	findOrganizationTargetFn = func(context.Context, *db.PostgresDb, string, string) (*OrganizationTenant, *outlook.DirectoryUser, error) {
		t.Error("destination lookup must happen when the token is minted, not per key")
		return nil, nil, nil
	}
	t.Cleanup(func() { findOrganizationTargetFn = prev })

	key := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid-1/inbox/m1.json"
	migration := &repo.RestoreJobListingDB{LoginID: "a@x.com", TargetEmail: "b@x.com", Method: "outlook"}
	tests := []struct {
		name    string
		deps    *restore.RestoreDeps
		want    string
		wantErr bool
	}{
		{name: "delegated in place", deps: &restore.RestoreDeps{}, want: "/me"},
		{name: "app in place uses key owner", deps: &restore.RestoreDeps{MicrosoftApplication: true}, want: "/users/oid-1"},
		{name: "delegated migration uses signed-in mailbox", deps: &restore.RestoreDeps{Job: migration}, want: "/me"},
		{
			name: "app migration uses cached destination",
			deps: &restore.RestoreDeps{
				MicrosoftApplication:   true,
				MicrosoftWriteUserBase: "https://graph.microsoft.com/v1.0/users/dest-oid",
				Job:                    migration,
			},
			want: "/users/dest-oid",
		},
		{
			name:    "app migration without destination fails",
			deps:    &restore.RestoreDeps{MicrosoftApplication: true, Job: migration},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := restoreWriteUserBase(tt.deps, key)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || !strings.HasSuffix(got, tt.want) {
				t.Fatalf("base=%q err=%v want suffix %q", got, err, tt.want)
			}
		})
	}
}

func TestPrepareMigrationWriteUser(t *testing.T) {
	prev := findOrganizationTargetFn
	t.Cleanup(func() { findOrganizationTargetFn = prev })
	findOrganizationTargetFn = func(context.Context, *db.PostgresDb, string, string) (*OrganizationTenant, *outlook.DirectoryUser, error) {
		return &OrganizationTenant{}, &outlook.DirectoryUser{ObjectID: "dest-oid"}, nil
	}
	inPlace := &repo.RestoreJobListingDB{LoginID: "a@x.com", Method: "outlook"}
	migration := &repo.RestoreJobListingDB{UserID: "u", LoginID: "a@x.com", TargetEmail: "b@x.com", Method: "outlook"}
	sharepoint := &repo.RestoreJobListingDB{LoginID: "a@x.com", TargetEmail: "site-1", Method: "outlook_sharepoint"}
	tests := []struct {
		name       string
		deps       *restore.RestoreDeps
		wantSuffix string
		wantErr    bool
	}{
		{name: "in place clears destination", deps: &restore.RestoreDeps{MicrosoftApplication: true, MicrosoftWriteUserBase: "stale", Job: inPlace}},
		{name: "delegated migration clears destination", deps: &restore.RestoreDeps{MicrosoftWriteUserBase: "stale", Job: migration}},
		{name: "sharepoint migration is a site not a user", deps: &restore.RestoreDeps{MicrosoftApplication: true, MicrosoftWriteUserBase: "stale", Job: sharepoint}},
		{
			name:       "app migration resolves the destination user",
			deps:       &restore.RestoreDeps{MicrosoftApplication: true, Store: &db.PostgresDb{}, Job: migration},
			wantSuffix: "/users/dest-oid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := prepareMigrationWriteUser(context.Background(), tt.deps)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantSuffix == "" && tt.deps.MicrosoftWriteUserBase != "" {
				t.Fatalf("base=%q want empty", tt.deps.MicrosoftWriteUserBase)
			}
			if tt.wantSuffix != "" && !strings.HasSuffix(tt.deps.MicrosoftWriteUserBase, tt.wantSuffix) {
				t.Fatalf("base=%q want suffix %q", tt.deps.MicrosoftWriteUserBase, tt.wantSuffix)
			}
		})
	}
}

func TestEvaluateCredentialReadinessFallsBackToDelegatedWrite(t *testing.T) {
	prevR, prevD := resolveTenantFn, delegatedRestoreTokenFn
	t.Cleanup(func() { resolveTenantFn, delegatedRestoreTokenFn = prevR, prevD })
	cases := []struct {
		name      string
		method    string
		appRoles  []string
		scp       string
		mintErr   error
		refresh   string
		wantReady bool
		wantGrant string
	}{
		{name: "mail Grant Access", method: "outlook", appRoles: []string{"Mail.Read"}, scp: "User.Read Mail.ReadWrite", refresh: "rt", wantReady: true},
		{name: "mail Shared variant", method: "outlook", appRoles: []string{"Mail.Read"}, scp: "User.Read Mail.ReadWrite.Shared", refresh: "rt", wantReady: true},
		{name: "mail still read-only", method: "outlook", appRoles: []string{"Mail.Read"}, scp: "User.Read Mail.Read", refresh: "rt", wantGrant: "Mail.ReadWrite"},
		{name: "mail no refresh", method: "outlook", appRoles: []string{"Mail.Read"}, wantGrant: "Mail.ReadWrite"},
		{name: "mail mint fails", method: "outlook", appRoles: []string{"Mail.Read"}, mintErr: errors.New("invalid_grant"), refresh: "rt", wantGrant: "Mail.ReadWrite"},
		{name: "calendar Grant Access", method: "outlook_calendar", appRoles: []string{"Calendars.Read"}, scp: "Calendars.ReadWrite", refresh: "rt", wantReady: true},
		{name: "contacts Grant Access", method: "outlook_contacts", appRoles: []string{"Contacts.Read"}, scp: "Contacts.ReadWrite", refresh: "rt", wantReady: true},
		{name: "onedrive Grant Access", method: "outlook_onedrive", appRoles: []string{"Files.Read.All"}, scp: "Files.ReadWrite.All", refresh: "rt", wantReady: true},
		{name: "onedrive personal Files.ReadWrite", method: "outlook_onedrive", appRoles: []string{"Files.Read.All"}, scp: "Files.ReadWrite", refresh: "rt", wantReady: true},
		{name: "sharepoint Files.ReadWrite.All", method: "outlook_sharepoint", appRoles: []string{"Sites.Read.All"}, scp: "Files.ReadWrite.All Sites.Read.All", refresh: "rt", wantReady: true},
		{name: "sharepoint Sites.ReadWrite.All", method: "outlook_sharepoint", appRoles: []string{"Sites.Read.All"}, scp: "Sites.ReadWrite.All", refresh: "rt", wantReady: true},
		{name: "sharepoint still read-only", method: "outlook_sharepoint", appRoles: []string{"Sites.Read.All"}, scp: "Sites.Read.All Files.Read.All", refresh: "rt", wantGrant: "Files.ReadWrite.All"},
		{name: "teams Grant Access both send scopes", method: "outlook_teams", appRoles: []string{"ChannelMessage.Read.All"}, scp: "ChannelMessage.Send ChatMessage.Send", refresh: "rt", wantReady: true},
		{name: "teams channel only still asks chat", method: "outlook_teams", appRoles: []string{"ChannelMessage.Read.All"}, scp: "ChannelMessage.Send", refresh: "rt", wantGrant: "ChatMessage.Send"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			appTok := fakeJWT(map[string]interface{}{"roles": tc.appRoles, "idtyp": "app"})
			resolveTenantFn = func(context.Context, *db.PostgresDb, mstenant.Request) (*mstenant.Context, error) {
				return &mstenant.Context{Token: appTok, Application: true, TenantID: "tenant"}, nil
			}
			delegatedRestoreTokenFn = func(string, string, string) (*outlook.TokenResponse, error) {
				if tc.mintErr != nil {
					return nil, tc.mintErr
				}
				return &outlook.TokenResponse{
					AccessToken: fakeJWT(map[string]interface{}{"scp": tc.scp}),
					Scope:       tc.scp,
				}, nil
			}
			out := &restore.ReadinessResult{Service: tc.method}
			got, err := evaluateCredentialReadiness(context.Background(), nil, out, "u", tc.method, tc.method,
				&repo.GoogleBackupCredentialDB{RefreshToken: tc.refresh}, "tenant")
			if err != nil {
				t.Fatal(err)
			}
			if got.Ready != tc.wantReady {
				t.Fatalf("ready=%v want %v reason=%s missing=%+v", got.Ready, tc.wantReady, got.Reason, got.MissingPermissions)
			}
			if tc.wantGrant == "" {
				return
			}
			found := false
			for _, p := range got.MissingPermissions {
				if p.Scope == tc.wantGrant {
					found = true
				}
			}
			if got.Ready || !found {
				t.Fatalf("grant scopes %+v want %s", got.MissingPermissions, tc.wantGrant)
			}
		})
	}
}

func TestDelegatedRestoreScopesOK(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		granted  []string
		required []string
		ok       bool
	}{
		{name: "mail exact", method: "outlook", granted: []string{"Mail.ReadWrite"}, required: []string{"Mail.ReadWrite"}, ok: true},
		{name: "mail shared", method: "outlook", granted: []string{"Mail.ReadWrite.Shared"}, required: []string{"Mail.ReadWrite"}, ok: true},
		{name: "calendar shared", method: "outlook_calendar", granted: []string{"Calendars.ReadWrite.Shared"}, required: []string{"Calendars.ReadWrite"}, ok: true},
		{name: "contacts shared", method: "outlook_contacts", granted: []string{"Contacts.ReadWrite.Shared"}, required: []string{"Contacts.ReadWrite"}, ok: true},
		{name: "onedrive personal", method: "outlook_onedrive", granted: []string{"Files.ReadWrite"}, required: []string{"Files.ReadWrite.All"}, ok: true},
		{name: "sharepoint sites write", method: "outlook_sharepoint", granted: []string{"Sites.ReadWrite.All"}, required: []string{"Files.ReadWrite.All"}, ok: true},
		{name: "teams both", method: "outlook_teams", granted: []string{"ChannelMessage.Send", "ChatMessage.Send"}, required: []string{"ChannelMessage.Send", "ChatMessage.Send"}, ok: true},
		{name: "teams channel only", method: "outlook_teams", granted: []string{"ChannelMessage.Send"}, required: []string{"ChannelMessage.Send", "ChatMessage.Send"}},
		{name: "read is not write", method: "outlook", granted: []string{"Mail.Read"}, required: []string{"Mail.ReadWrite"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := delegatedRestoreScopesOK(tc.method, tc.granted, tc.required); got != tc.ok {
				t.Fatalf("ok=%v want %v", got, tc.ok)
			}
		})
	}
}

func TestEvaluateCredentialReadinessRequiresWriteRole(t *testing.T) {
	prev := resolveTenantFn
	t.Cleanup(func() { resolveTenantFn = prev })
	tests := []struct {
		name        string
		method      string
		roles       []string
		application bool
		scope       string
		wantReady   bool
	}{
		{name: "app mail read is not enough", method: "outlook", roles: []string{"Mail.Read"}, application: true},
		{name: "app mail readwrite", method: "outlook", roles: []string{"Mail.ReadWrite"}, application: true, wantReady: true},
		{name: "app sharepoint files role is not site write", method: "outlook_sharepoint", roles: []string{"Files.ReadWrite.All"}, application: true},
		{name: "app sharepoint sites write", method: "outlook_sharepoint", roles: []string{"Sites.ReadWrite.All"}, application: true, wantReady: true},
		{name: "teams stays delegated", method: "outlook_teams", roles: []string{"ChannelMessage.Send"}, application: true},
		{name: "personal opaque write scope", method: "outlook", scope: "Mail.ReadWrite", wantReady: true},
		{name: "personal opaque read scope", method: "outlook", scope: "Mail.Read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := "EwCIBMl6BAAUCBUz0PacOpaqueNotAJWT"
			if tt.application {
				token = fakeJWT(map[string]interface{}{"roles": tt.roles, "idtyp": "app"})
			}
			resolveTenantFn = func(context.Context, *db.PostgresDb, mstenant.Request) (*mstenant.Context, error) {
				return &mstenant.Context{Token: token, TokenScope: tt.scope, Application: tt.application}, nil
			}
			out := &restore.ReadinessResult{Service: "outlook"}
			got, err := evaluateCredentialReadiness(context.Background(), nil, out, "u", "outlook", tt.method, &repo.GoogleBackupCredentialDB{}, "tenant")
			if err != nil {
				t.Fatal(err)
			}
			if got.Ready != tt.wantReady {
				t.Fatalf("ready=%v want %v reason=%s message=%s", got.Ready, tt.wantReady, got.Reason, got.Message)
			}
			if !tt.wantReady && got.Reason != restore.ReadinessReasonMissingPermissions {
				t.Fatalf("reason=%s", got.Reason)
			}
		})
	}
}

type teamsPost struct{ kind, team, channel, parent, chat, body string }

func fakeTeamsPosts(t *testing.T, replyErr map[string]error) *[]teamsPost {
	t.Helper()
	var posts []teamsPost
	pc, pr, pch := teamsPostChannelFn, teamsPostReplyFn, teamsPostChatFn
	n := 0
	teamsPostChannelFn = func(_ context.Context, _, team, channel, body string) (string, error) {
		n++
		posts = append(posts, teamsPost{kind: "post", team: team, channel: channel, body: body})
		return fmt.Sprintf("new-%d", n), nil
	}
	teamsPostReplyFn = func(_ context.Context, _, team, channel, parent, body string) error {
		posts = append(posts, teamsPost{kind: "reply", team: team, channel: channel, parent: parent, body: body})
		return replyErr[parent]
	}
	teamsPostChatFn = func(_ context.Context, _, chat, body string) (string, error) {
		posts = append(posts, teamsPost{kind: "chat", chat: chat, body: body})
		return "c1", nil
	}
	t.Cleanup(func() { teamsPostChannelFn, teamsPostReplyFn, teamsPostChatFn = pc, pr, pch })
	return &posts
}

func TestTeamsRestoreSessionRepostsWithRefAndThreads(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	teamPrefix := outlook.TeamsTeamPrefix(tenant, "team-1")
	root := outlook.TeamsChannelMessageKey(teamPrefix, "19:ch@thread.tacv2", "100", "100", "2026-10-01T10:00:00Z")
	reply := outlook.TeamsChannelMessageKey(teamPrefix, "19:ch@thread.tacv2", "100", "101", "2026-10-01T10:05:00Z")
	orphan := outlook.TeamsChannelMessageKey(teamPrefix, "19:ch@thread.tacv2", "300", "301", "2026-10-02T10:05:00Z")
	chat := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(tenant, "19:chat@thread.v2"), "200", "2026-10-01T11:00:00Z")
	msg := `{"createdDateTime":"2026-10-01T10:00:00Z","chatId":"19:chat@thread.v2","from":{"user":{"displayName":"Ann"}},"body":{"content":"hello"}}`
	posts := fakeTeamsPosts(t, map[string]error{"300": &outlook.TeamsGraphError{Status: 404, Code: "NotFound"}})
	s := &TeamsRestoreSession{Token: "tok", Download: func(context.Context, string) ([]byte, error) { return []byte(msg), nil }}
	for _, k := range []string{root, reply, orphan, chat} {
		if err := s.Restore(context.Background(), k); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	got := *posts
	if len(got) != 5 {
		t.Fatalf("posts: %+v", got)
	}
	if got[0].kind != "post" || got[0].team != "team-1" || got[0].channel != "19:ch@thread.tacv2" ||
		got[0].body != "hello" {
		t.Fatalf("root repost: %+v", got[0])
	}
	if got[1].kind != "reply" || got[1].parent != "new-1" {
		t.Fatalf("reply goes under the reposted root: %+v", got[1])
	}
	if got[2].kind != "reply" || got[2].parent != "300" || got[3].kind != "post" || got[3].body != "hello" {
		t.Fatalf("missing root falls back to a new post: %+v", got[2:4])
	}
	if got[4].kind != "chat" || got[4].chat != "19:chat@thread.v2" {
		t.Fatalf("chat restore: %+v", got[4])
	}
}

func TestTeamsRestoreSessionRefusesOrgTokenAndDownloadOnly(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	posts := fakeTeamsPosts(t, nil)
	key := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(tenant, "19:chat@thread.v2"), "200", "2026-10-01T11:00:00Z")
	app := &TeamsRestoreSession{Token: "tok", Application: true}
	if err := app.Restore(context.Background(), key); !errors.Is(err, ErrTeamsRestoreNeedsUser) {
		t.Fatalf("organization token: %v", err)
	}
	s := &TeamsRestoreSession{Token: "tok", Download: func(context.Context, string) ([]byte, error) { return []byte(`{}`), nil }}
	for _, k := range []string{
		outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(tenant, "p-abc")),
		outlook.TeamsCallRecordKey(tenant, "call-1", "2026-10-01T10:00:00Z"),
	} {
		if err := s.Restore(context.Background(), k); err == nil || !strings.Contains(err.Error(), "download only") {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if len(*posts) != 0 {
		t.Fatalf("nothing posted: %+v", *posts)
	}
	if outlookTeamsProcessor().Setup(context.Background(), &restore.RestoreDeps{MicrosoftToken: "tok", MicrosoftApplication: true}) == nil {
		t.Fatal("processor setup refuses an organization token")
	}
}
