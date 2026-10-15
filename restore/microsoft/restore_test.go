package microsoft

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
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
	got, err := restoreUserBase(true, "", key)
	if err != nil || !strings.HasSuffix(got, "/users/oid-1") {
		t.Fatalf("app-only base = %q, %v", got, err)
	}
	if got, err := restoreUserBase(true, "oid-target", key); err != nil || !strings.HasSuffix(got, "/users/oid-target") {
		t.Fatalf("app-only restore into another account = %q, %v", got, err)
	}
	if got, err := restoreUserBase(false, "", key); err != nil || !strings.HasSuffix(got, "/me") {
		t.Fatalf("delegated base = %q, %v", got, err)
	}
	if got, err := restoreUserBase(false, "oid-target", key); err != nil || !strings.HasSuffix(got, "/me") {
		t.Fatalf("delegated restore into another account writes as the signed-in target = %q, %v", got, err)
	}
	if _, err := restoreUserBase(true, "", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/group/g1/x"); err == nil {
		t.Fatal("group key must not resolve to a user")
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
		!strings.Contains(got[0].body, "Originally posted by Ann on 2026-10-01 10:00 UTC (ref "+outlook.TeamsRestoreRef(root)+")") ||
		!strings.HasSuffix(got[0].body, "hello") {
		t.Fatalf("root repost: %+v", got[0])
	}
	if got[1].kind != "reply" || got[1].parent != "new-1" {
		t.Fatalf("reply goes under the reposted root: %+v", got[1])
	}
	if got[2].kind != "reply" || got[2].parent != "300" || got[3].kind != "post" || !strings.Contains(got[3].body, "In reply to message 300") {
		t.Fatalf("missing root falls back to a new post with In reply to: %+v", got[2:4])
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
	if (&outlookTeamsProcessor{}).Setup(context.Background(), &restore.RestoreDeps{MicrosoftToken: "tok", MicrosoftApplication: true}) == nil {
		t.Fatal("processor setup refuses an organization token")
	}
}
