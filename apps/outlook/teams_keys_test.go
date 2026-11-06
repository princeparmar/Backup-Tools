package outlook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const keysTestTenant = "11111111-2222-3333-4444-555555555555"

func TestTeamsKeysRoundTripIDsOnly(t *testing.T) {
	team := TeamsTeamPrefix(keysTestTenant, "team/1")
	chat := TeamsChatPrefix(keysTestTenant, "19:abc def@thread.v2")
	user := TeamsUserPrefix(keysTestTenant, "user-1")
	created := "2026-10-12T04:55:00.123Z"
	cases := []struct {
		key  string
		want TeamsObjectKey
	}{
		{TeamsTeamMetaKey(team), TeamsObjectKey{Kind: TeamsKindTeamMeta}},
		{TeamsChannelMetaKey(team, "19:ch/1$x"), TeamsObjectKey{Kind: TeamsKindChannelMeta, ChannelID: "19:ch/1$x"}},
		{TeamsChannelMessageKey(team, "19:ch/1$x", "root - 1", "msg 2", created),
			TeamsObjectKey{Kind: TeamsKindChannelMessage, ChannelID: "19:ch/1$x", RootID: "root - 1", MessageID: "msg 2"}},
		{TeamsChannelHostedKey(team, "c1", "m1", "content/1"), TeamsObjectKey{Kind: TeamsKindHosted, ChannelID: "c1", MessageID: "m1", ContentID: "content/1"}},
		{TeamsChatMessageKey(chat, "1700000000000", created), TeamsObjectKey{Kind: TeamsKindChatMessage, MessageID: "1700000000000"}},
		{TeamsUserChatPointerKey(user, "19:abc def@thread.v2"), TeamsObjectKey{Kind: TeamsKindPointer, ChatID: "19:abc def@thread.v2"}},
		{TeamsUserMeetingPointerKey(user, "pabc", created), TeamsObjectKey{Kind: TeamsKindPointer, MeetingID: "pabc"}},
		{TeamsUserCallPointerKey(user, "call/1", created), TeamsObjectKey{Kind: TeamsKindPointer, CallID: "call/1"}},
		{TeamsCallRecordKey(keysTestTenant, "call/1", created), TeamsObjectKey{Kind: TeamsKindCallRecord, CallID: "call/1"}},
		{TeamsPhoneLogKey(keysTestTenant, TeamsKeyPstn, "row1", created), TeamsObjectKey{Kind: TeamsKindPhoneLog, CallID: "row1"}},
		{TeamsMeetingOnlineMeetingKey(TeamsMeetingPrefix(keysTestTenant, "pabc")), TeamsObjectKey{Kind: TeamsKindOnlineMeeting, MeetingID: "pabc"}},
		{TeamsChannelVersionKey(team, "19:ch/1@thread", "100", `W/"etag/1"`), TeamsObjectKey{Kind: TeamsKindMessageVersion, ChannelID: "19:ch/1@thread", MessageID: "100", VersionID: `W/"etag/1"`}},
		{TeamsChatVersionKey(chat, "200", "2026-10-12T04:55:00.123Z"), TeamsObjectKey{Kind: TeamsKindMessageVersion, MessageID: "200", VersionID: "2026-10-12T04:55:00.123Z"}},
	}
	for _, c := range cases {
		got, ok := ParseTeamsObjectKey(c.key)
		if !ok {
			t.Fatalf("%s did not parse", c.key)
		}
		if got.TenantID != keysTestTenant || got.Kind != c.want.Kind || got.ChannelID != c.want.ChannelID || got.RootID != c.want.RootID ||
			got.MessageID != c.want.MessageID || got.ContentID != c.want.ContentID || got.ChatID != c.want.ChatID ||
			got.MeetingID != c.want.MeetingID || got.CallID != c.want.CallID || got.VersionID != c.want.VersionID {
			t.Fatalf("%s: got %+v want %+v", c.key, got, c.want)
		}
		if !TeamsKeyFits(c.key) {
			t.Fatalf("%s too long", c.key)
		}
	}
	if k := TeamsChannelMessageKey(team, "c", "r", "m", created); !strings.Contains(k, "/2026/10/12/") || !strings.HasSuffix(k, TeamsMessageExt) {
		t.Fatalf("message key must be dated by creation: %s", k)
	}
	if k := TeamsChatMessageKey(chat, "m", "not a time"); !strings.Contains(k, "/0000/00/00/") {
		t.Fatalf("undated message: %s", k)
	}
}

// An earlier version gets its own key beside the current message, never the message key itself,
// and is not a restore item.
func TestTeamsMessageVersionKeys(t *testing.T) {
	team := TeamsTeamPrefix(keysTestTenant, "team-1")
	chat := TeamsChatPrefix(keysTestTenant, "19:abc@thread.v2")
	created := "2026-10-12T04:55:00.123Z"
	a := TeamsChannelVersionKey(team, "c1", "100", TeamsMessageVersionID("e1", "", nil))
	b := TeamsChannelVersionKey(team, "c1", "100", TeamsMessageVersionID("e2", "", nil))
	if a == b || a == TeamsChannelMessageKey(team, "c1", "100", "100", created) || !strings.HasSuffix(a, ".json") {
		t.Fatalf("versions must have distinct keys apart from the message: %s %s", a, b)
	}
	if c := TeamsChatVersionKey(chat, "200", "e1"); c == TeamsChatMessageKey(chat, "200", created) || IsTeamsRestoreKey(c) || IsTeamsRestoreKey(a) {
		t.Fatalf("a version is not the message or a restore item: %s", c)
	}
	if v := TeamsMessageVersionID("", "2026-10-12T04:55:00.123Z", []byte(`{}`)); v != "2026-10-12T04:55:00.123Z" {
		t.Fatalf("modified fallback: %s", v)
	}
	h1, h2 := TeamsMessageVersionID("", "", []byte(`{"a":1}`)), TeamsMessageVersionID("", "", []byte(`{"a":2}`))
	if h1 == h2 || h1 != TeamsMessageVersionID(" ", " ", []byte(`{"a":1}`)) || !strings.HasPrefix(h1, "sha256-") {
		t.Fatalf("hash fallback must be deterministic per body: %s %s", h1, h2)
	}
}

// The nextLink documented for getAllRetainedMessages ($skip) is accepted; another team's is not.
func TestTeamsPageGuardRetainedMessages(t *testing.T) {
	for _, c := range []struct{ first, next, other string }{
		{TeamsChannelRetainedMessagesURL("team-1"),
			"https://graph.microsoft.com/v1.0/teams/team-1/channels/getAllRetainedMessages?$skip=10",
			"https://graph.microsoft.com/v1.0/teams/team-2/channels/getAllRetainedMessages?$skip=10"},
		{TeamsUserRetainedChatMessagesURL("user-1"),
			"https://graph.microsoft.com/v1.0/users/user-1/chats/getAllRetainedMessages?$skip=10",
			"https://graph.microsoft.com/v1.0/users/user-2/chats/getAllRetainedMessages?$skip=10"},
	} {
		if err := NewTeamsPageGuard(c.first).Next(c.next); err != nil {
			t.Fatalf("%s: %v", c.next, err)
		}
		if err := NewTeamsPageGuard(c.first).Next(c.other); err == nil {
			t.Fatalf("%s must be refused", c.other)
		}
	}
}

func TestTeamsLegacyKeysParseAndRestore(t *testing.T) {
	team := TeamsTeamPrefix(keysTestTenant, "team-1")
	meta := TeamsIDBasedMetaKey(team, "19:ch@thread", "100", "2026-01-02T00:00:00Z")
	data := TeamsIDBasedDataKey(team, "19:ch@thread", "100", "2026-01-02T00:00:00Z")
	m, ok := ParseTeamsObjectKey(meta)
	if !ok || m.Kind != TeamsKindLegacyMeta || m.ChannelID != "19:ch@thread" || m.MessageID != "100" {
		t.Fatalf("legacy meta: %+v", m)
	}
	if d, ok := ParseTeamsObjectKey(data); !ok || d.Kind != TeamsKindLegacyData {
		t.Fatalf("legacy data: %+v", d)
	}
	if !IsTeamsRestoreKey(meta) || IsTeamsRestoreKey(data) {
		t.Fatal("old meta keys stay restorable until converted; data keys never are")
	}
	if !IsTeamsRestoreKey(TeamsChannelMessageKey(team, "c", "r", "m", "")) || IsTeamsRestoreKey(TeamsTeamMetaKey(team)) ||
		IsTeamsRestoreKey(TeamsChannelHostedKey(team, "c", "m", "x")) {
		t.Fatal("only message objects are restore items")
	}
}

func TestCompareTeamsVersion(t *testing.T) {
	stored := &TeamsVersion{Modified: "2026-10-02T10:00:00.000Z", ETag: "a"}
	cases := []struct {
		stored *TeamsVersion
		in     TeamsVersion
		want   string
	}{
		{nil, TeamsVersion{}, TeamsVersionWrite},
		{stored, TeamsVersion{Modified: "2026-10-02T10:00:01Z", ETag: "b"}, TeamsVersionWrite},
		{stored, TeamsVersion{Modified: "2026-10-01T10:00:00Z", ETag: "b"}, TeamsVersionSkip},
		{stored, TeamsVersion{Modified: "2026-10-02T10:00:00Z", ETag: "a"}, TeamsVersionSkip},
		{stored, TeamsVersion{Modified: "2026-10-02T10:00:00Z", ETag: "b"}, TeamsVersionConflict},
		{stored, TeamsVersion{Modified: "", ETag: "b"}, TeamsVersionSkip},
		{&TeamsVersion{}, TeamsVersion{Modified: "2026-10-01T10:00:00Z"}, TeamsVersionWrite},
	}
	for i, c := range cases {
		if got := CompareTeamsVersion(c.stored, c.in); got != c.want {
			t.Fatalf("case %d: got %s want %s", i, got, c.want)
		}
	}
}

func TestSanitizeTeamsErrorText(t *testing.T) {
	keep := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	in := `User bob@contoso.com cannot read "Secret plan" at https://graph.microsoft.com/v1.0/x for ` + keep +
		" or 99999999-0000-0000-0000-000000000000, call +1 (555) 123-4567 " + strings.Repeat("x", 300)
	out := SanitizeTeamsErrorText(in, keep)
	for _, leaked := range []string{"bob@", "Secret plan", "https://", "99999999-0000", "555"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("%q leaked: %s", leaked, out)
		}
	}
	if !strings.Contains(out, keep) || len([]rune(out)) > 200 {
		t.Fatalf("sanitized: %s", out)
	}
}

func TestTeamsMeetingIDSeparateUnlessShared(t *testing.T) {
	a, _, fa := TeamsMeetingID("ical-1", "user-a", "ev-a", false)
	b, _, _ := TeamsMeetingID("ical-1", "user-b", "ev-b", false)
	if a == b || !strings.HasPrefix(a, "p") || len(a) != 33 {
		t.Fatalf("separate copies must get separate folders: %s %s", a, b)
	}
	if again, _, _ := TeamsMeetingID("ical-1", "user-a", "ev-a", false); again != a {
		t.Fatal("folder id must be deterministic")
	}
	s1, _, _ := TeamsMeetingID("ical-1", "user-a", "ev-a", true)
	s2, _, _ := TeamsMeetingID("ical-1", "user-b", "ev-b", true)
	if s1 != s2 || !strings.HasPrefix(s1, "s") {
		t.Fatalf("confirmed copies share one folder: %s %s", s1, s2)
	}
	if n, _, _ := TeamsMeetingID("", "user-a", "ev-a", true); !strings.HasPrefix(n, "p") {
		t.Fatal("no iCalUId never shares a folder")
	}
	if full := TeamsMeetingFullID(a, fa); full != "p"+fa {
		t.Fatalf("full id: %s", full)
	}
}

func TestPatchTeamsBackupKeepsBackedUpAt(t *testing.T) {
	raw, err := PatchTeamsBackup([]byte(`{"id":"1"}`), TeamsBackupBlock{TenantID: keysTestTenant, BackedUpAt: "2026-01-01T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = PatchTeamsBackup(raw, TeamsBackupBlock{TenantID: keysTestTenant, RemovedAt: "2026-02-01T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := ReadTeamsBackup(raw)
	if !ok || b.BackedUpAt != "2026-01-01T00:00:00.000Z" || b.RemovedAt == "" {
		t.Fatalf("block: %+v", b)
	}
	if _, err := PatchTeamsBackup([]byte(`[1]`), TeamsBackupBlock{}); err == nil {
		t.Fatal("non-object JSON must be refused")
	}
}

func TestTeamsPageGuard(t *testing.T) {
	first := "https://graph.microsoft.com/v1.0/teams/t1/channels/getAllMessages?$filter=x"
	g := NewTeamsPageGuard(first)
	if err := g.Next("https://graph.microsoft.com/v1.0/teams/t1/channels/getAllMessages?$skiptoken=1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"https://graph.microsoft.com/v1.0/teams/t1/channels/getAllMessages?$skiptoken=1",
		"https://evil.example.com/v1.0/teams/t1/channels/getAllMessages?$skiptoken=2",
		"https://graph.microsoft.com/v1.0/teams/t2/channels/getAllMessages?$skiptoken=3",
	} {
		if err := g.Next(bad); !errors.Is(err, ErrTeamsPaging) {
			t.Fatalf("%s: got %v", bad, err)
		}
	}
	small := NewTeamsPageGuard(first)
	small.Max = 1
	_ = small.Next(first + "&a=1")
	if err := small.Next(first + "&a=2"); !errors.Is(err, ErrTeamsPaging) {
		t.Fatal("page cap must stop the round")
	}
}

// The nextLinks below are the ones documented for channel getAllMessages and chats getAllMessages
// (Graph v1.0): OData key form and a lower-case function name.
func TestTeamsPageGuardAcceptsDocumentedNextLinks(t *testing.T) {
	cases := []struct{ first, next string }{
		{
			TeamsChannelAllMessagesURL("01fe12e0-e720-44fd-8854-28c66d1bee40", time.Time{}, time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC)),
			"https://graph.microsoft.com/v1.0/teams('01fe12e0-e720-44fd-8854-28c66d1bee40')/channels/getallMessages?$skiptoken=U2tpcFZhbHVlPTAj&$filter=lastModifiedDateTime+gt+2019-11-01T00%3a00%3a00Z",
		},
		{
			TeamsUserChatMessagesURL("0b4f1cf6-54c8-4820-bbb7-2a1f4257ade5", time.Time{}, time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC)),
			"https://graph.microsoft.com/v1.0/users('0b4f1cf6-54c8-4820-bbb7-2a1f4257ade5')/chats/getallMessages?$top=2&$skiptoken=U2tpcFZhbHVlPTIj%3d%3d",
		},
		{
			TeamsUserChatMessagesURL("0b4f1cf6-54c8-4820-bbb7-2a1f4257ade5", time.Time{}, time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC)),
			"https://graph.microsoft.com/v1.0/users('0b4f1cf6-54c8-4820-bbb7-2a1f4257ade5')/chats/getAllMessages?$skiptoken=b",
		},
		{
			TeamsUserChatDeltaURL("5ed12dd6-24f8-4777-be3d-0d234e06cefa"),
			"https://graph.microsoft.com/v1.0/users/5ed12dd6-24f8-4777-be3d-0d234e06cefa/chats/getAllMessages/delta?$skiptoken=a-5fq.y_WM",
		},
	}
	for _, c := range cases {
		g := NewTeamsPageGuard(c.first)
		if err := g.Next(c.next); err != nil {
			t.Fatalf("%s after %s: %v", c.next, c.first, err)
		}
		if err := g.Next(c.next); !errors.Is(err, ErrTeamsPaging) {
			t.Fatalf("repeated %s must stop the round, got %v", c.next, err)
		}
	}

	// The same page written in both path forms is still a repeat.
	first := TeamsChannelAllMessagesURL("team-1", time.Time{}, time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC))
	g := NewTeamsPageGuard(first)
	if err := g.Next("https://graph.microsoft.com/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=s1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Next("https://graph.microsoft.com/v1.0/teams/team-1/channels/getAllMessages?$skiptoken=s1"); !errors.Is(err, ErrTeamsPaging) {
		t.Fatalf("same page in the other form must be a repeat, got %v", err)
	}
}

func TestTeamsPageGuardRejectsForeignAndMalformedLinks(t *testing.T) {
	first := TeamsChannelAllMessagesURL("team-1", time.Time{}, time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC))
	for _, bad := range []string{
		"https://evil.example.com/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=1",
		"http://graph.microsoft.com/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=2",
		"https://user@graph.microsoft.com/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=3",
		"https://graph.microsoft.com.evil.example/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=4",
		"https://graph.microsoft.com/v1.0/teams('team-2')/channels/getallMessages?$skiptoken=5",
		"https://graph.microsoft.com/v1.0/teams('TEAM-1')/channels/getallMessages?$skiptoken=6",
		"https://graph.microsoft.com/v1.0/teams('team-1')/channels/getallMessages/delta?$skiptoken=7",
		"https://graph.microsoft.com/beta/teams('team-1')/channels/getallMessages?$skiptoken=8",
		"https://graph.microsoft.com/v1.0/teams('team-1')/channels/../channels/getallMessages?$skiptoken=9",
		"https://graph.microsoft.com/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=10#frag",
		"https://graph.microsoft.com/v1.0/teams(team-1)/channels/getallMessages?$skiptoken=11",
		"https://graph.microsoft.com/v1.0/users('team-1')/chats/getallMessages?$skiptoken=12",
		"/v1.0/teams/team-1/channels/getAllMessages?$skiptoken=13",
		"teams/team-1/channels/getAllMessages?$skiptoken=14",
		"https://graph.microsoft.com/v1.0/teams/team-1/channels/get%zzAllMessages?$skiptoken=15",
		"",
	} {
		if err := NewTeamsPageGuard(first).Next(bad); !errors.Is(err, ErrTeamsPaging) {
			t.Fatalf("%q must be rejected, got %v", bad, err)
		}
	}
	if err := NewTeamsPageGuard("https://evil.example.com/v1.0/teams/team-1/channels/getAllMessages").
		Next("https://evil.example.com/v1.0/teams/team-1/channels/getAllMessages?$skiptoken=1"); !errors.Is(err, ErrTeamsPaging) {
		t.Fatalf("a round that didn't start on the Graph host must not page, got %v", err)
	}
}

func TestTeamsPageGuardKeepsLinkUnchanged(t *testing.T) {
	first := TeamsUserChatMessagesURL("u1", time.Time{}, time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC))
	next := "https://graph.microsoft.com/v1.0/users('u1')/chats/getallMessages?$top=2&$skiptoken=U2tpcA%3d%3d&$filter=a+gt+b"
	host, path, query, ok := teamsGraphLink(next)
	if !ok || host != "graph.microsoft.com" || path != "/v1.0/users/u1/chats/getallmessages" || query != "$top=2&$skiptoken=U2tpcA%3d%3d&$filter=a+gt+b" {
		t.Fatalf("canonical form: %q %q %q %v", host, path, query, ok)
	}
	if err := NewTeamsPageGuard(first).Next(next); err != nil {
		t.Fatal(err)
	}
	fn := "https://graph.microsoft.com/v1.0/users/u1/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='U1')/delta"
	if _, path, _, _ := teamsGraphLink(fn); path != "/v1.0/users/u1/onlinemeetings/getalltranscripts(meetingorganizeruserid='U1')/delta" {
		t.Fatalf("quoted parameter values must be kept as sent: %s", path)
	}
}

// The continuation links below are the ones documented for getAllTranscripts/getAllRecordings
// delta (unquoted user key, skipToken on both nextLink and deltaLink), getPstnCalls (from/to with
// dates in its nextLink) and getDirectRoutingCalls (Graph v1.0).
func TestTeamsPageGuardAcceptsDocumentedCallAndTranscriptLinks(t *testing.T) {
	const u = "ba321e0d-79ee-478d-8e28-85a19507f456"
	from, to := time.Date(2019, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2019, 12, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct{ first, next, delta string }{
		{
			TeamsAllTranscriptsDeltaURL(u),
			"https://graph.microsoft.com/v1.0/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=MSMjMCMjMjAyNC0wMi0yN1QwNDozNTozNi45MTk2ODY1WjsxMDA%3d",
			"https://graph.microsoft.com/v1.0/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=MSMjMCMjMjAyNC0wMi0yN1QwNDozNTozNi45MTk2ODY1WjsxMDA%3d2",
		},
		{
			TeamsAllRecordingsDeltaURL(u),
			"https://graph.microsoft.com/v1.0/users('" + u + "')/onlineMeetings/getAllRecordings(meetingOrganizerUserId='" + u + "')/delta?skipToken=a",
			"https://graph.microsoft.com/v1.0/users/" + u + "/onlineMeetings/getallrecordings(meetingorganizeruserid='" + u + "')/delta?deltaToken=b",
		},
		{
			TeamsPhoneLogsURL(TeamsKeyPstn, from, to),
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01)?$skip=1000",
			"",
		},
		{
			TeamsPhoneLogsURL(TeamsKeyPstn, from, to),
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(fromDateTime=2019-11-01T00:00:00.000Z, toDateTime=2019-12-01T00:00:00Z)?$skip=1000",
			"",
		},
		{
			TeamsPhoneLogsURL(TeamsKeyDirectRouting, from, to),
			"https://graph.microsoft.com/v1.0/communications/callRecords/getDirectRoutingCalls(fromDateTime=2019-11-01,toDateTime=2019-12-01)?$skip=1000",
			"",
		},
	}
	for _, c := range cases {
		g := NewTeamsPageGuard(c.first)
		if err := g.Next(c.next); err != nil {
			t.Fatalf("%s after %s: %v", c.next, c.first, err)
		}
		if err := g.Next(c.next); !errors.Is(err, ErrTeamsPaging) {
			t.Fatalf("repeated %s must stop the round, got %v", c.next, err)
		}
		if c.delta != "" {
			if err := g.Delta(c.delta); err != nil {
				t.Fatalf("deltaLink %s: %v", c.delta, err)
			}
		}
	}

	// Later pages of the same window differ only in $skip.
	g := NewTeamsPageGuard(TeamsPhoneLogsURL(TeamsKeyPstn, from, to))
	for _, skip := range []string{"1000", "2000", "3000"} {
		if err := g.Next("https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01)?$skip=" + skip); err != nil {
			t.Fatalf("$skip=%s: %v", skip, err)
		}
	}
}

func TestTeamsPageGuardRejectsForeignCallAndTranscriptLinks(t *testing.T) {
	const u = "ba321e0d-79ee-478d-8e28-85a19507f456"
	const other = "11111111-2222-3333-4444-555555555555"
	from, to := time.Date(2019, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2019, 12, 1, 0, 0, 0, 0, time.UTC)
	pstn := TeamsPhoneLogsURL(TeamsKeyPstn, from, to)
	transcripts := TeamsAllTranscriptsDeltaURL(u)
	cases := map[string][]string{
		pstn: {
			"https://evil.example.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01)?$skip=1000",
			"https://graph.microsoft.com/beta/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getDirectRoutingCalls(from=2019-11-01,to=2019-12-01)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-10-01,to=2019-12-01)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-02)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(fromDateTime=2019-11-01)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,from=2019-11-01,to=2019-12-01)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01,x=1)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from='2019-11-01,to=2019-12-01)?$skip=1000",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01)?$skip=1000#frag",
			"https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2019-11-01,to=2019-12-01)/../getPstnCalls(from=2019-11-01,to=2019-12-01)?$skip=1000",
		},
		transcripts: {
			"https://graph.microsoft.com/v1.0/users(" + other + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=a",
			"https://graph.microsoft.com/v1.0/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + other + "')/delta?skipToken=a",
			"https://graph.microsoft.com/v1.0/users(" + u + ")/onlineMeetings/getAllRecordings(meetingOrganizerUserId='" + u + "')/delta?skipToken=a",
			"https://graph.microsoft.com/v1.0/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')?skipToken=a",
			"https://graph.microsoft.com/beta/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=a",
			"https://graph.microsoft.com/v1.0/users(not-a-guid)/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=a",
			"https://graph.microsoft.com/v1.0/$metadata#users('" + u + "')/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=a",
		},
	}
	for first, bad := range cases {
		for _, link := range bad {
			if err := NewTeamsPageGuard(first).Next(link); !errors.Is(err, ErrTeamsPaging) {
				t.Fatalf("nextLink %q must be rejected, got %v", link, err)
			}
			if err := NewTeamsPageGuard(first).Delta(link); !errors.Is(err, ErrTeamsPaging) {
				t.Fatalf("deltaLink %q must be rejected, got %v", link, err)
			}
		}
	}
	// The documented sessions nextLink is a $metadata fragment and can't be followed.
	sessions := TeamsCallSessionsURL("e523d2ed-2966-4b6b-925b-754a88034cc5")
	if err := NewTeamsPageGuard(sessions).Next("https://graph.microsoft.com/v1.0/$metadata#communications/callRecords('e523d2ed-2966-4b6b-925b-754a88034cc5')/sessions?$skiptoken=abc"); !errors.Is(err, ErrTeamsPaging) {
		t.Fatalf("fragment sessions link must be rejected, got %v", err)
	}
	if err := NewTeamsPageGuard("https://evil.example.com" + strings.TrimPrefix(transcripts, "https://graph.microsoft.com")).Delta(transcripts); !errors.Is(err, ErrTeamsPaging) {
		t.Fatalf("a round off the Graph host must not accept a deltaLink, got %v", err)
	}
}

func TestClassifyTeamsError(t *testing.T) {
	cases := map[string]error{
		TeamsStatusDisabledByTenant:  &TeamsGraphError{Status: 403, Code: "Forbidden", InnerCode: "GraphAccessToTranscriptsDisabled"},
		TeamsStatusMissingPermission: &TeamsGraphError{Status: 403, Code: "Authorization_RequestDenied"},
		TeamsStatusPaidAPI:           &TeamsGraphError{Status: 402},
		TeamsStatusResync:            &TeamsGraphError{Status: 400, Code: "syncStateNotFound"},
		TeamsStatusThrottled:         &TeamsGraphError{Status: 429},
		TeamsStatusUnauthorized:      &TeamsGraphError{Status: 401},
		TeamsStatusPaging:            ErrTeamsPaging,
	}
	for want, err := range cases {
		if got := ClassifyTeamsError(err); got != want {
			t.Fatalf("%v: got %s want %s", err, got, want)
		}
	}
	if !IsTeamsContentURL("https://graph.microsoft.com/v1.0/users/u/onlineMeetings/m/recordings/r/content") ||
		IsTeamsContentURL(TeamsAllRecordingsDeltaURL("u")) {
		t.Fatal("content URL detection")
	}
}
