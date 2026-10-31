package microsoft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
)

const teamsTestTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const teamsTestChannel = "19:ch@thread.tacv2"

// fakeTeamsGraph replaces the vault reads and Graph writes of the Teams restore. attempts holds
// every write, posts only the ones that succeeded.
type fakeTeamsGraph struct {
	mu       sync.Mutex
	objects  map[string]string
	fail     func(p teamsPost) error
	attempts []teamsPost
	posts    []teamsPost
}

func newFakeTeamsGraph(t *testing.T, objects map[string]string) *fakeTeamsGraph {
	t.Helper()
	g := &fakeTeamsGraph{objects: objects}
	pc, pr, pch, dl := teamsPostChannelFn, teamsPostReplyFn, teamsPostChatFn, teamsDownloadFn
	teamsPostChannelFn = func(_ context.Context, _, team, channel, body string) (string, error) {
		return g.write(teamsPost{kind: "post", team: team, channel: channel, body: body})
	}
	teamsPostReplyFn = func(_ context.Context, _, team, channel, parent, body string) error {
		_, err := g.write(teamsPost{kind: "reply", team: team, channel: channel, parent: parent, body: body})
		return err
	}
	teamsPostChatFn = func(_ context.Context, _, chat, body string) (string, error) {
		return g.write(teamsPost{kind: "chat", chat: chat, body: body})
	}
	teamsDownloadFn = func(_ context.Context, _, _, key string) ([]byte, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if v, ok := g.objects[key]; ok {
			return []byte(v), nil
		}
		return nil, fmt.Errorf("object not found: %s", key)
	}
	t.Cleanup(func() { teamsPostChannelFn, teamsPostReplyFn, teamsPostChatFn, teamsDownloadFn = pc, pr, pch, dl })
	return g
}

func (g *fakeTeamsGraph) write(p teamsPost) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attempts = append(g.attempts, p)
	if g.fail != nil {
		if err := g.fail(p); err != nil {
			return "", err
		}
	}
	g.posts = append(g.posts, p)
	return fmt.Sprintf("new-%d", len(g.posts)), nil
}

func teamsTestJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func delegatedTeamsToken(t *testing.T) string {
	return teamsTestJWT(t, map[string]interface{}{"tid": teamsTestTenant, "oid": "u1", "idtyp": "user",
		"scp": "ChannelMessage.Send ChatMessage.Send User.Read"})
}

func teamsChannelKey(root, id string) string {
	return outlook.TeamsChannelMessageKey(outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"), teamsTestChannel, root, id, "2026-10-01T10:00:00Z")
}

func teamsChatKey(chatID, id string) string {
	return outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(teamsTestTenant, chatID), id, "2026-10-01T11:00:00Z")
}

// teamsChannelMsg is a stored channel message as the backup writes it.
func teamsChannelMsg(id, extra string) string {
	return fmt.Sprintf(`{"id":%q,"messageType":"message","createdDateTime":"2026-10-01T10:00:00Z",`+
		`"from":{"user":{"displayName":"Ann"}},"body":{"content":"text %s"},`+
		`"channelIdentity":{"teamId":"team-1","channelId":%q},`+
		`"storx_backup":{"tenant_id":%q,"team_id":"team-1","channel_id":%q}%s}`, id, id, teamsTestChannel, teamsTestTenant, teamsTestChannel, extra)
}

func teamsChatMsg(id, chatID string) string {
	return fmt.Sprintf(`{"id":%q,"messageType":"message","chatId":%q,"createdDateTime":"2026-10-01T11:00:00Z",`+
		`"from":{"user":{"displayName":"Bo"}},"body":{"content":"chat %s"},"storx_backup":{"tenant_id":%q,"chat_id":%q}}`,
		id, chatID, id, teamsTestTenant, chatID)
}

func TestTeamsRestoreRefusesAppOnlyTokens(t *testing.T) {
	root := teamsChannelKey("100", "100")
	g := newFakeTeamsGraph(t, map[string]string{root: teamsChannelMsg("100", "")})
	ctx := context.Background()
	appOnly := teamsTestJWT(t, map[string]interface{}{"tid": teamsTestTenant, "idtyp": "app", "roles": []string{"ChannelMessage.Read.All"}})
	rolesOnly := teamsTestJWT(t, map[string]interface{}{"tid": teamsTestTenant, "roles": []string{"Teamwork.Migrate.All"}})
	for name, tok := range map[string]string{"idtyp app": appOnly, "roles without scp": rolesOnly} {
		if err := (&TeamsRestoreSession{Token: tok}).Restore(ctx, root); !errors.Is(err, ErrTeamsRestoreNeedsUser) {
			t.Errorf("%s: session restore = %v", name, err)
		}
		if err := outlookTeamsProcessor().Setup(ctx, &restore.RestoreDeps{MicrosoftToken: tok}); !errors.Is(err, ErrTeamsRestoreNeedsUser) {
			t.Errorf("%s: scheduled processor setup = %v", name, err)
		}
		if err := CheckTeamsRestoreToken(tok, "ChannelMessage.Send"); !errors.Is(err, ErrTeamsRestoreNeedsUser) {
			t.Errorf("%s: manual token check = %v", name, err)
		}
	}
	if len(g.attempts) != 0 {
		t.Fatalf("an app-only token never writes: %+v", g.attempts)
	}

	delegated := delegatedTeamsToken(t)
	if err := outlookTeamsProcessor().Setup(ctx, &restore.RestoreDeps{MicrosoftToken: delegated}); err != nil {
		t.Fatalf("delegated setup: %v", err)
	}
	if err := CheckTeamsRestoreToken(delegated, "ChannelMessage.Send"); err != nil {
		t.Fatalf("delegated token check: %v", err)
	}
	noSend := teamsTestJWT(t, map[string]interface{}{"tid": teamsTestTenant, "idtyp": "user", "scp": "User.Read"})
	if err := CheckTeamsRestoreToken(noSend, "ChatMessage.Send"); !errors.Is(err, ErrTeamsRestoreMissingScope) {
		t.Fatalf("delegated token without the send scope: %v", err)
	}
	if err := CheckTeamsRestoreToken("EwB-opaque-token", "ChatMessage.Send"); err != nil {
		t.Fatalf("opaque tokens are left to Graph: %v", err)
	}
	o := (&TeamsRestoreSession{Token: delegated}).RestoreItem(ctx, root)
	if o.Status != TeamsRestored || o.Placement != TeamsPlacedPost || len(g.posts) != 1 || g.posts[0].team != "team-1" {
		t.Fatalf("delegated restore: %+v posts %+v", o, g.posts)
	}
}

func TestTeamsRestoreReportsThreadPlacement(t *testing.T) {
	root, reply := teamsChannelKey("100", "100"), teamsChannelKey("100", "101")
	originalOnly := teamsChannelKey("200", "201")
	orphan := teamsChannelKey("300", "301")
	chatID := "19:chat@thread.v2"
	chat := teamsChatKey(chatID, "400")
	g := newFakeTeamsGraph(t, map[string]string{
		root: teamsChannelMsg("100", ""), reply: teamsChannelMsg("101", ""),
		originalOnly: teamsChannelMsg("201", ""), orphan: teamsChannelMsg("301", ""),
		chat: teamsChatMsg("400", chatID),
	})
	g.fail = func(p teamsPost) error {
		if p.kind == "reply" && p.parent == "300" {
			return &outlook.TeamsGraphError{Status: 404, Code: "NotFound"}
		}
		return nil
	}
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t), TenantID: teamsTestTenant}
	want := map[string]string{
		root: TeamsPlacedPost, reply: TeamsPlacedRestoredRoot, originalOnly: TeamsPlacedOriginalRoot,
		orphan: TeamsPlacedRootMissing, chat: TeamsPlacedChat,
	}
	for _, key := range []string{root, reply, originalOnly, orphan, chat} {
		o := s.RestoreItem(context.Background(), key)
		if o.Status != TeamsRestored || o.Placement != want[key] || o.Ref != outlook.TeamsRestoreRef(key) {
			t.Errorf("%s: %+v, want placement %s", key, o, want[key])
		}
	}
	if g.posts[1].parent != "new-1" || g.posts[2].parent != "200" {
		t.Fatalf("replies: %+v", g.posts)
	}
	last := g.posts[3]
	if last.kind != "post" || last.body == "" {
		t.Fatalf("missing root becomes a new post: %+v", last)
	}
	if g.posts[4].chat != chatID {
		t.Fatalf("chat id comes from the object: %+v", g.posts[4])
	}
}

func TestTeamsRestoreRepeatedKeysPostOnce(t *testing.T) {
	root := teamsChannelKey("100", "100")
	legacy := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1") + "/channels/" + teamsTestChannel + "/meta/2026/10/01/100.json"
	g := newFakeTeamsGraph(t, map[string]string{root: teamsChannelMsg("100", ""), legacy: teamsChannelMsg("100", "")})
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t)}
	ctx := context.Background()
	if err := s.Restore(ctx, root); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{root, legacy} {
		o := s.RestoreItem(ctx, key)
		if o.Status != TeamsSkipped || !strings.Contains(o.Detail, "already restored") {
			t.Fatalf("%s: %+v", key, o)
		}
		if err := s.Restore(ctx, key); err != nil {
			t.Fatalf("a message already posted in this restore counts as done: %v", err)
		}
	}
	if len(g.attempts) != 1 {
		t.Fatalf("one post for one message: %+v", g.attempts)
	}
}

func TestTeamsRestoreFailedReplyIsReportedAndRetriedSafely(t *testing.T) {
	root, replyA, replyB := teamsChannelKey("100", "100"), teamsChannelKey("100", "101"), teamsChannelKey("100", "102")
	failedRoot, itsReply := teamsChannelKey("200", "200"), teamsChannelKey("200", "201")
	g := newFakeTeamsGraph(t, map[string]string{
		root: teamsChannelMsg("100", ""), replyA: teamsChannelMsg("101", ""), replyB: teamsChannelMsg("102", ""),
		failedRoot: teamsChannelMsg("200", ""), itsReply: teamsChannelMsg("201", ""),
	})
	failA := true
	g.fail = func(p teamsPost) error {
		switch {
		case p.kind == "reply" && strings.Contains(p.body, "text 101") && failA:
			failA = false
			return &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}
		case p.kind == "post" && strings.Contains(p.body, "text 200"):
			return &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}
		}
		return nil
	}
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t)}
	ctx := context.Background()
	results := map[string]TeamsRestoreOutcome{}
	for _, key := range []string{root, replyA, replyB, failedRoot, itsReply} {
		results[key] = s.RestoreItem(ctx, key)
	}
	if results[replyA].Status != TeamsFailed || !strings.Contains(results[replyA].Detail, "403") || results[replyA].OutcomeUnknown {
		t.Fatalf("failed reply: %+v", results[replyA])
	}
	if results[root].Status != TeamsRestored || results[replyB].Placement != TeamsPlacedRestoredRoot {
		t.Fatalf("the thread's other messages: root %+v reply %+v", results[root], results[replyB])
	}
	if results[failedRoot].Status != TeamsFailed {
		t.Fatalf("failed root: %+v", results[failedRoot])
	}
	if o := results[itsReply]; o.Placement != TeamsPlacedOriginalRoot || !strings.Contains(o.Detail, "not reposted") {
		t.Fatalf("a reply of a root that failed says where it went: %+v", o)
	}
	err := s.Restore(ctx, replyA)
	if err != nil {
		t.Fatalf("retrying the failed reply: %v", err)
	}
	var rootPosts int
	for _, p := range g.posts {
		if p.kind == "post" && strings.Contains(p.body, "text 100") {
			rootPosts++
		}
	}
	last := g.posts[len(g.posts)-1]
	if rootPosts != 1 || last.parent != "new-1" || !strings.Contains(last.body, "text 101") {
		t.Fatalf("the retry posts only the reply, under the reposted root: %+v", g.posts)
	}
}

func TestTeamsRestoreUnknownOutcomeIsNotRepeated(t *testing.T) {
	root := teamsChannelKey("100", "100")
	g := newFakeTeamsGraph(t, map[string]string{root: teamsChannelMsg("100", "")})
	g.fail = func(teamsPost) error {
		return fmt.Errorf("%w: %w", outlook.ErrTeamsPostOutcomeUnknown, &outlook.TeamsGraphError{Status: 504, Code: "GatewayTimeout"})
	}
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t)}
	ctx := context.Background()
	err := s.Restore(ctx, root)
	if err == nil || restore.ErrorCodeFromErr(err) != restore.ErrorCodeOutcomeUnknown {
		t.Fatalf("unknown outcome: %v (code %q)", err, restore.ErrorCodeFromErr(err))
	}
	if !strings.Contains(err.Error(), "ref "+outlook.TeamsRestoreRef(root)) {
		t.Fatalf("the error names the ref to search for: %v", err)
	}
	o := s.RestoreItem(ctx, root)
	if o.Status != TeamsFailed || !o.OutcomeUnknown || len(g.attempts) != 1 {
		t.Fatalf("a post that may exist is never repeated in the restore: %+v attempts %d", o, len(g.attempts))
	}
}

func TestTeamsRestoreRejectsMismatchedKeysAndObjects(t *testing.T) {
	chatID := "19:chat@thread.v2"
	otherTenant := strings.Replace(teamsChannelKey("100", "100"), teamsTestTenant, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", 1)
	tenantMismatch := teamsChannelKey("110", "110")
	idMismatch := teamsChannelKey("120", "120")
	teamMismatch := teamsChannelKey("130", "130")
	channelMismatch := teamsChannelKey("140", "140")
	chatMismatch := teamsChatKey(chatID, "400")
	g := newFakeTeamsGraph(t, map[string]string{
		otherTenant:     teamsChannelMsg("100", ""),
		tenantMismatch:  strings.Replace(teamsChannelMsg("110", ""), `"tenant_id":"`+teamsTestTenant, `"tenant_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb`, 1),
		idMismatch:      teamsChannelMsg("999", ""),
		teamMismatch:    strings.ReplaceAll(teamsChannelMsg("130", ""), `"team-1"`, `"team-2"`),
		channelMismatch: strings.ReplaceAll(teamsChannelMsg("140", ""), teamsTestChannel, "19:other@thread.tacv2"),
		chatMismatch:    teamsChatMsg("400", "19:other@thread.v2"),
	})
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t), TenantID: teamsTestTenant}
	for key, want := range map[string]string{
		otherTenant:     "not the restore's tenant",
		tenantMismatch:  "not the key's tenant",
		idMismatch:      "not the key's message",
		teamMismatch:    "not the key's team",
		channelMismatch: "not the key's channel",
		chatMismatch:    "not the key's chat",
		"not-a-key":     "not a Teams backup key",
		teamsTestTenant + "/team/team-1/CHANNELS/x/bad.teams": "not a Teams backup key",
	} {
		o := s.RestoreItem(context.Background(), key)
		if o.Status != TeamsFailed || !strings.Contains(o.Detail, want) {
			t.Errorf("%s: %+v, want %q", key, o, want)
		}
	}
	for _, key := range []string{
		outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsTestTenant, "p-abc")),
		outlook.TeamsCallRecordKey(teamsTestTenant, "call-1", "2026-10-01T10:00:00Z"),
	} {
		if o := s.RestoreItem(context.Background(), key); o.Status != TeamsUnsupported {
			t.Errorf("%s: %+v", key, o)
		}
	}
	if len(g.attempts) != 0 {
		t.Fatalf("nothing is written for a rejected key: %+v", g.attempts)
	}
}

func TestTeamsRestoreSkipsSystemAndDeletedMessages(t *testing.T) {
	system, deleted, deletedKept := teamsChannelKey("100", "100"), teamsChannelKey("110", "110"), teamsChannelKey("120", "120")
	g := newFakeTeamsGraph(t, map[string]string{
		system:      strings.Replace(teamsChannelMsg("100", ""), `"messageType":"message"`, `"messageType":"systemEventMessage"`, 1),
		deleted:     strings.Replace(teamsChannelMsg("110", `,"deletedDateTime":"2026-10-02T00:00:00Z"`), `"content":"text 110"`, `"content":""`, 1),
		deletedKept: teamsChannelMsg("120", `,"deletedDateTime":"2026-10-02T00:00:00Z"`),
	})
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t)}
	ctx := context.Background()
	if err := s.Restore(ctx, system); restore.ErrorCodeFromErr(err) != TeamsUnsupported {
		t.Fatalf("system message: %v", err)
	}
	if err := s.Restore(ctx, deleted); restore.ErrorCodeFromErr(err) != TeamsSkipped {
		t.Fatalf("deleted message without content: %v", err)
	}
	if err := s.Restore(ctx, deletedKept); err != nil {
		t.Fatalf("a deleted message whose content was kept is restored: %v", err)
	}
	if len(g.attempts) != 1 {
		t.Fatalf("writes: %+v", g.attempts)
	}
}

func TestTeamsLegacyRepliesRetryPostsOnlyMissingReplies(t *testing.T) {
	teamKey := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	key := teamKey + "/channels/ch-1/replies/msg-1.json"
	g := newFakeTeamsGraph(t, map[string]string{
		key:                     `{"parent_message_id":"p1","replies":[{"Subject":"one"},{"Subject":"two"},{"Subject":"three"}]}`,
		teamKey + "/_team.json": `{"team_id":"team-1"}`,
	})
	failTwo := true
	g.fail = func(p teamsPost) error {
		if p.body == "two" && failTwo {
			failTwo = false
			return &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}
		}
		return nil
	}
	if !shouldRestoreOutlookTeamsKey(key) {
		t.Fatal("old replies snapshots stay restorable")
	}
	s := &TeamsRestoreSession{Token: delegatedTeamsToken(t)}
	if o := s.RestoreItem(context.Background(), key); o.Status != TeamsFailed || !strings.Contains(o.Detail, "reply 2 of 3") {
		t.Fatalf("partial failure: %+v", o)
	}
	if o := s.RestoreItem(context.Background(), key); o.Status != TeamsRestored || o.Placement != TeamsPlacedLegacy {
		t.Fatalf("retry: %+v", o)
	}
	var bodies []string
	for _, p := range g.posts {
		if p.parent != "p1" || p.team != "team-1" || p.channel != "ch-1" {
			t.Fatalf("legacy reply target: %+v", p)
		}
		bodies = append(bodies, p.body)
	}
	if strings.Join(bodies, ",") != "one,two,three" {
		t.Fatalf("each reply posted once: %v", bodies)
	}
}

func TestTeamsScheduledRestoreRunsInOrderAndRetriesSafely(t *testing.T) {
	root, reply, failing := teamsChannelKey("100", "100"), teamsChannelKey("100", "101"), teamsChannelKey("100", "102")
	foreign := strings.Replace(teamsChannelKey("500", "500"), teamsTestTenant, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", 1)
	meeting := outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsTestTenant, "p-abc"))
	g := newFakeTeamsGraph(t, map[string]string{
		root: teamsChannelMsg("100", ""), reply: teamsChannelMsg("101", ""), failing: teamsChannelMsg("102", ""),
		foreign: teamsChannelMsg("500", ""),
	})
	failOnce := true
	g.fail = func(p teamsPost) error {
		if strings.Contains(p.body, "text 102") && failOnce {
			failOnce = false
			return &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}
		}
		return nil
	}
	cfg, _ := restore.ConfigForMethod("outlook_teams")
	if cfg.MaxConcurrency != 1 {
		t.Fatalf("teams restore runs one post at a time: %+v", cfg)
	}
	deps := &restore.RestoreDeps{MicrosoftToken: delegatedTeamsToken(t), Config: cfg,
		Job: &repo.RestoreJobListingDB{Method: "outlook_teams", TenantID: teamsTestTenant}}
	proc := outlookTeamsProcessor()
	if err := proc.Setup(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	rows := []repo.SyncedObject{{ObjectKey: root}, {ObjectKey: reply}, {ObjectKey: failing},
		{ObjectKey: foreign}, {ObjectKey: meeting}}
	first := restore.RunBatch(context.Background(), deps, proc, rows)
	if first.Processed != 2 || first.Failed != 2 {
		t.Fatalf("first run: %+v", first)
	}
	for _, fk := range first.FailedKeys {
		if fk.ObjectKey == foreign && !strings.Contains(fk.Reason, "not the restore's tenant") {
			t.Fatalf("foreign tenant key: %+v", fk)
		}
	}
	if g.posts[1].parent != "new-1" {
		t.Fatalf("the reply goes under the root reposted just before it: %+v", g.posts)
	}
	second := restore.RunBatch(context.Background(), deps, proc, rows)
	if second.Processed != 3 || second.Failed != 1 {
		t.Fatalf("rerun of the same batch: %+v", second)
	}
	if len(g.posts) != 3 || g.posts[2].parent != "new-1" || !strings.Contains(g.posts[2].body, "text 102") {
		t.Fatalf("the rerun posts only the failed reply: %+v", g.posts)
	}
}

func TestOrderTeamsRestoreKeysPutsRootsFirst(t *testing.T) {
	root, reply, other := teamsChannelKey("100", "100"), teamsChannelKey("100", "101"), teamsChannelKey("200", "202")
	chat := teamsChatKey("19:chat@thread.v2", "400")
	got := OrderTeamsRestoreKeys([]string{reply, chat, root, other})
	if strings.Join(got, "|") != strings.Join([]string{root, reply, chat, other}, "|") {
		t.Fatalf("order: %v", got)
	}
}
