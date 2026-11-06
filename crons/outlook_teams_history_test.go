package crons

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

const retainedCreated = "2026-10-01T10:00:00.000Z"

// retainedClock pins the run clock; the returned func moves it.
func retainedClock(t *testing.T) (*time.Time, func(time.Duration)) {
	t.Helper()
	prev := teamsNowFn
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prev })
	return &clock, func(d time.Duration) { clock = clock.Add(d) }
}

// retainedRun is a run whose token shows both application roles getAllRetainedMessages needs.
func retainedRun(t *testing.T, store *memTeamsStore) *teamsRun {
	t.Helper()
	r := newTestTeamsRun(t, store, nil, nil)
	r.token = teamsRolesToken("ChannelMessage.Read.All", "Chat.Read.All")
	r.loadConsent()
	return r
}

// retainedChannelPages are two pages of team-1's retained messages: three versions of message 100
// (one etag each) and one version of reply 101, split by the documented $skip nextLink.
func retainedChannelPages() (first, second string, exact map[string]outlook.TeamsPage) {
	first = outlook.TeamsChannelRetainedMessagesURL("team-1")
	second = first + "?$skip=2"
	return first, second, map[string]outlook.TeamsPage{
		first: {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-01T10:00:00.000Z", nil),
			teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
		}, NextLink: second},
		second: {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-03T10:00:00.000Z", nil),
			teamsMsg("101", "100", "ch-a", "2026-10-03T11:00:00.000Z", nil),
		}},
	}
}

func retainedChannelKeys() []string {
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	return []string{
		outlook.TeamsChannelVersionKey(prefix, "ch-a", "100", "e-2026-10-01T10:00:00.000Z"),
		outlook.TeamsChannelVersionKey(prefix, "ch-a", "100", "e-2026-10-02T10:00:00.000Z"),
		outlook.TeamsChannelVersionKey(prefix, "ch-a", "100", "e-2026-10-03T10:00:00.000Z"),
		outlook.TeamsChannelVersionKey(prefix, "ch-a", "101", "e-2026-10-03T11:00:00.000Z"),
	}
}

func historyKeys(store *memTeamsStore) []string {
	var out []string
	for k := range store.objs {
		if p, ok := outlook.ParseTeamsObjectKey(k); ok && p.Kind == outlook.TeamsKindMessageVersion {
			out = append(out, k)
		}
	}
	return out
}

func retainedSave(saves *int) func() error {
	return func() error { *saves++; return nil }
}

// Every version of an edited message across pages gets its own key; the current message object
// is never rewritten, and a repeated listing writes nothing.
func TestTeamsRetainedChannelVersionsKeptApartFromCurrentMessage(t *testing.T) {
	_, advance := retainedClock(t)
	first, second, exact := retainedChannelPages()
	g := &fakeGraph{strict: true, exact: exact}
	g.install(t)
	store := newMemTeamsStore()
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	current := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "100", retainedCreated)
	currentBody := []byte(`{"id":"100","etag":"e-current","body":{"content":"latest"}}`)
	if err := store.put(context.Background(), current, currentBody, map[string]string{outlook.TeamsMetaETag: "e-current"}); err != nil {
		t.Fatal(err)
	}
	r := retainedRun(t, store)
	var rs teamsRetainedState
	saves := 0
	scope := teamsChannelRetainedScope("team-1", prefix)

	if got := r.syncRetained(&rs, scope, r.deadline, retainedSave(&saves)); got != teamsComplete {
		t.Fatalf("status %q; failures %+v; unknown %v", got, r.mem.TeamsFailures, g.unknown)
	}
	if len(g.unknown) != 0 || len(g.calls) != 2 || g.calls[0] != first || g.calls[1] != second {
		t.Fatalf("both documented pages and nothing else: calls %v unknown %v", g.calls, g.unknown)
	}
	for _, k := range retainedChannelKeys() {
		body, ok := store.objs[k]
		if !ok {
			t.Fatalf("version %s missing; have %v", k, keysOf(store.objs))
		}
		if !strings.Contains(string(body), `"getAllRetainedMessages"`) || store.meta[k][outlook.TeamsMetaETag] == "" {
			t.Fatalf("version %s must carry its source and etag: %s %v", k, body, store.meta[k])
		}
	}
	if n := len(historyKeys(store)); n != 4 {
		t.Fatalf("4 versions, got %d: %v", n, historyKeys(store))
	}
	if string(store.objs[current]) != string(currentBody) || store.puts[current] != 1 {
		t.Fatalf("current message must stay intact: %s (%d puts)", store.objs[current], store.puts[current])
	}
	if rs.PageLink != "" || rs.ListedAt == nil || rs.Held != 0 || len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("closed round: %+v failures %+v", rs, r.mem.TeamsFailures)
	}
	if f := r.status.Features[outlook.TeamsFeatureChannelHistory]; f.CapabilityStatus != outlook.TeamsStatusOK {
		t.Fatalf("feature status %+v", f)
	}

	g.calls = nil
	if got := r.syncRetained(&rs, scope, r.deadline, retainedSave(&saves)); got != "" || len(g.calls) != 0 {
		t.Fatalf("listed within a day: status %q calls %v", got, g.calls)
	}

	advance(teamsHistoryEvery + time.Hour)
	putsBefore := map[string]int{}
	for k, n := range store.puts {
		putsBefore[k] = n
	}
	r2 := retainedRun(t, store)
	if got := r2.syncRetained(&rs, scope, r2.deadline, retainedSave(&saves)); got != teamsComplete {
		t.Fatalf("second round %q", got)
	}
	for k, n := range store.puts {
		if putsBefore[k] != n {
			t.Fatalf("repeated listing rewrote %s (%d -> %d)", k, putsBefore[k], n)
		}
	}
	if r2.counts["history_skipped"] != 4 || r2.counts["history_written"] != 0 {
		t.Fatalf("counts %v", r2.counts)
	}
}

// A budget stop keeps the next page; the next run resumes there and finishes the round.
func TestTeamsRetainedResumesAfterBudgetStop(t *testing.T) {
	clock, advance := retainedClock(t)
	first, second, exact := retainedChannelPages()
	g := &fakeGraph{strict: true, exact: exact}
	g.install(t)
	inner := teamsPageFn
	teamsPageFn = func(ctx context.Context, tok, u string) (outlook.TeamsPage, error) {
		advance(time.Hour)
		return inner(ctx, tok, u)
	}
	store := newMemTeamsStore()
	r := retainedRun(t, store)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	scope := teamsChannelRetainedScope("team-1", prefix)
	var rs teamsRetainedState
	saves := 0

	if got := r.syncRetained(&rs, scope, clock.Add(30*time.Minute), retainedSave(&saves)); got != teamsIncomplete {
		t.Fatalf("budget stop must be incomplete, got %q", got)
	}
	if rs.PageLink != second || rs.ListedAt != nil || saves == 0 || len(historyKeys(store)) != 2 {
		t.Fatalf("page 1 stored and next page saved: %+v saves %d keys %v", rs, saves, historyKeys(store))
	}

	g.calls = nil
	r2 := retainedRun(t, store)
	if got := r2.syncRetained(&rs, scope, clock.Add(24*time.Hour), retainedSave(&saves)); got != teamsComplete {
		t.Fatalf("resume %q", got)
	}
	if len(g.calls) != 1 || g.calls[0] != second {
		t.Fatalf("resume must start at the saved page, not %s: %v", first, g.calls)
	}
	if len(historyKeys(store)) != 4 || rs.PageLink != "" || rs.ListedAt == nil {
		t.Fatalf("round finished: %+v keys %v", rs, historyKeys(store))
	}
}

// A token without the application role, or a delegated token, never requests the listing and
// reports the feature not consented without a failure entry.
func TestTeamsRetainedNeedsApplicationRole(t *testing.T) {
	retainedClock(t)
	for name, token := range map[string]string{
		"delegated":    teamsDelegatedToken("ChannelMessage.Read.All Chat.Read"),
		"missing role": teamsRolesToken("Team.ReadBasic.All"),
		"opaque":       "tok",
	} {
		g := &fakeGraph{strict: true}
		g.install(t)
		r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
		r.token = token
		r.loadConsent()
		var team, user teamsRetainedState
		prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
		a := r.syncRetained(&team, teamsChannelRetainedScope("team-1", prefix), r.deadline, func() error { return nil })
		b := r.syncRetained(&user, teamsChatRetainedScope("u1"), r.deadline, func() error { return nil })
		if a != "" || b != "" || len(g.calls) != 0 || len(r.mem.TeamsFailures) != 0 {
			t.Fatalf("%s: statuses %q %q calls %v failures %+v", name, a, b, g.calls, r.mem.TeamsFailures)
		}
		for _, f := range []string{outlook.TeamsFeatureChannelHistory, outlook.TeamsFeatureChatHistory} {
			if s := r.status.Features[f]; s.CapabilityStatus != outlook.TeamsStatusNotConsented || s.Code == "" {
				t.Fatalf("%s: %s must be not_consented with a reason: %+v", name, f, s)
			}
		}
	}
}

// A refusal for the whole listing (permission, paid API, not found) backs it off without a
// failure entry and without requests until the back-off ends; then the listing runs again.
func TestTeamsRetainedPermanentRefusalBacksOff(t *testing.T) {
	cases := []struct {
		err     error
		status  string
		backoff time.Duration
	}{
		{&outlook.TeamsGraphError{Status: 403, Code: "Forbidden", Message: "Missing role permissions"}, outlook.TeamsStatusMissingPermission, teamsPermanentBackoff},
		{&outlook.TeamsGraphError{Status: 402, Code: "PaymentRequired"}, outlook.TeamsStatusPaidAPI, teamsPaidAPIBackoff},
		{&outlook.TeamsGraphError{Status: 404, Code: "NotFound"}, outlook.TeamsStatusNotFound, teamsPermanentBackoff},
	}
	for _, c := range cases {
		clock, advance := retainedClock(t)
		_, _, exact := retainedChannelPages()
		g := &fakeGraph{strict: true, exact: exact, errs: map[string]error{"getAllRetainedMessages": c.err}}
		g.install(t)
		store := newMemTeamsStore()
		r := retainedRun(t, store)
		scope := teamsChannelRetainedScope("team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
		var rs teamsRetainedState

		if got := r.syncRetained(&rs, scope, r.deadline, func() error { return nil }); got != "" {
			t.Fatalf("%s: refusal must not mark the team incomplete, got %q", c.status, got)
		}
		if len(r.mem.TeamsFailures) != 0 || rs.SkipUntil == nil || !rs.SkipUntil.Equal(clock.Add(c.backoff)) || rs.Status != c.status {
			t.Fatalf("%s: backed off without failure entry: %+v failures %+v", c.status, rs, r.mem.TeamsFailures)
		}
		if f := r.status.Features[outlook.TeamsFeatureChannelHistory]; f.CapabilityStatus != c.status {
			t.Fatalf("%s: feature %+v", c.status, f)
		}

		g.calls = nil
		advance(c.backoff - time.Hour)
		if got := r.syncRetained(&rs, scope, clock.Add(time.Hour), func() error { return nil }); got != "" || len(g.calls) != 0 {
			t.Fatalf("%s: no request during the back-off: %q %v", c.status, got, g.calls)
		}

		advance(2 * time.Hour)
		delete(g.errs, "getAllRetainedMessages")
		if got := r.syncRetained(&rs, scope, clock.Add(time.Hour), func() error { return nil }); got != teamsComplete {
			t.Fatalf("%s: listing after the back-off %q", c.status, got)
		}
		if rs.SkipUntil != nil || rs.Status != "" || len(historyKeys(store)) != 4 || len(g.unknown) != 0 {
			t.Fatalf("%s: recovered %+v keys %v unknown %v", c.status, rs, historyKeys(store), g.unknown)
		}
	}
}

// A transient error on the second page keeps one stream failure and restarts the listing; the
// replay stores the rest, rewrites nothing from page 1 and clears the failure.
func TestTeamsRetainedTransientErrorReplays(t *testing.T) {
	retainedClock(t)
	_, second, exact := retainedChannelPages()
	g := &fakeGraph{strict: true, exact: exact, errs: map[string]error{"$skip=2": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}}
	g.install(t)
	store := newMemTeamsStore()
	r := retainedRun(t, store)
	scope := teamsChannelRetainedScope("team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
	var rs teamsRetainedState

	if got := r.syncRetained(&rs, scope, r.deadline, func() error { return nil }); got != teamsIncomplete {
		t.Fatalf("transient error must be incomplete, got %q", got)
	}
	streamKey := "history:stream:team:team-1"
	if len(r.mem.TeamsFailures) != 1 || r.mem.TeamsFailures[streamKey].Phase != "history" {
		t.Fatalf("one stream failure: %+v", r.mem.TeamsFailures)
	}
	if rs.SkipUntil != nil || rs.ListedAt != nil || len(historyKeys(store)) != 2 {
		t.Fatalf("no back-off, page 1 kept: %+v keys %v", rs, historyKeys(store))
	}
	if outlook.TeamsVerified(outlook.TeamsCheckReplayNextLink) != (rs.PageLink == second) {
		t.Fatalf("page link after failure: %q", rs.PageLink)
	}

	delete(g.errs, "$skip=2")
	page1 := map[string]int{}
	for _, k := range historyKeys(store) {
		page1[k] = store.puts[k]
	}
	if got := r.syncRetained(&rs, scope, r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("replay %q", got)
	}
	if len(r.mem.TeamsFailures) != 0 || len(historyKeys(store)) != 4 {
		t.Fatalf("replay clears the failure and stores the rest: %+v keys %v", r.mem.TeamsFailures, historyKeys(store))
	}
	for k, n := range page1 {
		if store.puts[k] != n {
			t.Fatalf("replay rewrote %s", k)
		}
	}
}

// Versions that can't be written hold the round with a single failure entry for the listing
// (however many fail); the replay writes them, closes the round and clears the entry.
func TestTeamsRetainedWriteFailureHoldsRoundThenReplays(t *testing.T) {
	retainedClock(t)
	_, _, exact := retainedChannelPages()
	g := &fakeGraph{strict: true, exact: exact}
	g.install(t)
	store := newMemTeamsStore()
	refuse := true
	store.putErr = func(key string, _ int) error {
		if refuse && strings.Contains(key, "/history/100/") {
			return errors.New("write refused")
		}
		return nil
	}
	r := retainedRun(t, store)
	scope := teamsChannelRetainedScope("team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
	var rs teamsRetainedState

	if got := r.syncRetained(&rs, scope, r.deadline, func() error { return nil }); got != teamsIncomplete {
		t.Fatalf("held round must be incomplete, got %q", got)
	}
	windowKey := "history:window:team:team-1"
	if len(r.mem.TeamsFailures) != 1 || r.mem.TeamsFailures[windowKey].Kind != teamsFailWindow {
		t.Fatalf("three failed versions, one entry: %+v", r.mem.TeamsFailures)
	}
	if rs.Held != 1 || rs.HeldSince == nil || rs.ListedAt != nil || len(historyKeys(store)) != 1 {
		t.Fatalf("round held, reply stored: %+v keys %v", rs, historyKeys(store))
	}

	refuse = false
	if got := r.syncRetained(&rs, scope, r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("replay %q", got)
	}
	if len(r.mem.TeamsFailures) != 0 || rs.Held != 0 || rs.ListedAt == nil || len(historyKeys(store)) != 4 {
		t.Fatalf("replay closes the round: %+v failures %+v keys %v", rs, r.mem.TeamsFailures, historyKeys(store))
	}
}

// Channel versions of another team or an unselected channel are not stored.
func TestTeamsRetainedChannelScope(t *testing.T) {
	retainedClock(t)
	other := teamsMsg("300", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{
		"channelIdentity": map[string]string{"teamId": "team-2", "channelId": "ch-a"}})
	g := &fakeGraph{strict: true, exact: map[string]outlook.TeamsPage{
		outlook.TeamsChannelRetainedMessagesURL("team-1"): {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
			teamsMsg("200", "", "ch-b", "2026-10-02T10:00:00.000Z", nil),
			other,
		}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := retainedRun(t, store)
	r.sel.ChannelIDs["team-1"] = map[string]bool{"ch-a": true}
	var rs teamsRetainedState
	if got := r.syncRetained(&rs, teamsChannelRetainedScope("team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")),
		r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("status %q", got)
	}
	keys := historyKeys(store)
	if len(keys) != 1 || keys[0] != retainedChannelKeys()[1] || r.counts["history_out_of_scope"] != 2 {
		t.Fatalf("only the selected channel of this team: %v counts %v", keys, r.counts)
	}
}

// The user's chats round stores retained chat versions beside the chat's current messages.
func TestTeamsRetainedChatVersionsInUserChats(t *testing.T) {
	retainedClock(t)
	retained := outlook.TeamsUserRetainedChatMessagesURL("u1")
	g := &fakeGraph{strict: true,
		exact: map[string]outlook.TeamsPage{
			retained: {Items: []json.RawMessage{
				chatMsg("m1", "chat-1", "2026-10-01T09:00:00.000Z", "u1"),
				chatMsg("m1", "chat-1", "2026-10-01T09:30:00.000Z", "u1"),
			}, NextLink: retained + "?$skip=2"},
			retained + "?$skip=2": {Items: []json.RawMessage{chatMsg("m9", "chat-2", "2026-10-01T09:45:00.000Z", "u1")}},
		},
		pages: map[string]outlook.TeamsPage{
			"/users/u1/chats?": {},
			"getAllMessages/delta": {Items: []json.RawMessage{chatMsg("m1", "chat-1", "2026-10-01T10:00:00.000Z", "u1")},
				DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=d1"},
			"/users/u1/chats/getAllMessages?$filter": {},
		}}
	g.install(t)
	store := newMemTeamsStore()
	r := retainedRun(t, store)
	r.userScope["u1"] = true
	if status, _ := r.syncUserChats("u1", r.deadline); status != teamsComplete {
		t.Fatalf("status %s; failures %+v; unknown %v", status, r.mem.TeamsFailures, g.unknown)
	}
	chat1 := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	current := outlook.TeamsChatMessageKey(chat1, "m1", "2026-10-01T09:00:00.000Z")
	if !strings.Contains(string(store.objs[current]), "2026-10-01T10:00:00.000Z") {
		t.Fatalf("current message keeps the latest version: %s", store.objs[current])
	}
	want := []string{
		outlook.TeamsChatVersionKey(chat1, "m1", "e2026-10-01T09:00:00.000Z"),
		outlook.TeamsChatVersionKey(chat1, "m1", "e2026-10-01T09:30:00.000Z"),
		outlook.TeamsChatVersionKey(outlook.TeamsChatPrefix(teamsTestTenant, "chat-2"), "m9", "e2026-10-01T09:45:00.000Z"),
	}
	for _, k := range want {
		if _, ok := store.objs[k]; !ok {
			t.Fatalf("version %s missing; have %v", k, keysOf(store.objs))
		}
	}
	var st teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
	if st.Retained.ListedAt == nil || st.Retained.PageLink != "" || len(g.unknown) != 0 {
		t.Fatalf("retained state saved with the user: %+v unknown %v", st.Retained, g.unknown)
	}
	if _, ok := r.mem.TeamsFailures["history:stream:user:u1"]; ok {
		t.Fatalf("no history failure: %+v", r.mem.TeamsFailures)
	}
}
