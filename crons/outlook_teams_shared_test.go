package crons

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

const otherTenant = "22222222-3333-4444-5555-666666666666"

func sharedChannelRow(id, membership, tenant string) json.RawMessage {
	return rawJSON(map[string]string{"id": id, "displayName": id, "membershipType": membership, "tenantId": tenant})
}

// sharedRoot is a root post of channel ch; expanded adds the replies array (and its nextLink).
func sharedRoot(id, ch string, expanded bool, replies []json.RawMessage, next string) json.RawMessage {
	m := map[string]interface{}{}
	_ = json.Unmarshal(teamsMsg(id, "", ch, "2026-10-02T10:00:00.000Z", nil), &m)
	if expanded {
		if replies == nil {
			replies = []json.RawMessage{}
		}
		m["replies"] = replies
		m["replies@odata.count"] = len(replies)
		if next != "" {
			m["replies@odata.nextLink"] = next
		}
	}
	return rawJSON(m)
}

func sharedReply(id, root, ch string) json.RawMessage {
	return teamsMsg(id, root, ch, "2026-10-02T11:00:00.000Z", nil)
}

func withoutQuery(u string) string { return strings.SplitN(u, "?", 2)[0] }

// sharedTeamGraph is a strict team-1 with a standard channel, a shared channel it hosts (sh-a), a
// same-tenant shared channel another team hosts (sh-in) and one owned by another tenant (sh-x).
// sh-a lists root 100 (an expanded reply plus a reply page), root 200 (no replies) and, on the
// second page (not expanded), root 300 whose replies are read on their own.
func sharedTeamGraph() (*fakeGraph, map[string]string) {
	roots := outlook.TeamsChannelRootsURL("team-1", "sh-a")
	links := map[string]string{
		"roots":      roots,
		"roots2":     withoutQuery(roots) + "?$skiptoken=p2",
		"replies100": withoutQuery(outlook.TeamsChannelRootRepliesURL("team-1", "sh-a", "100")) + "?$skiptoken=r2",
		"replies300": outlook.TeamsChannelRootRepliesURL("team-1", "sh-a", "300"),
	}
	g := &fakeGraph{strict: true,
		docs: map[string][]byte{"/teams/team-1": []byte(`{"id":"team-1"}`)},
		exact: map[string]outlook.TeamsPage{
			links["roots"]: {Items: []json.RawMessage{
				sharedRoot("100", "sh-a", true, []json.RawMessage{sharedReply("101", "100", "sh-a")}, links["replies100"]),
				sharedRoot("200", "sh-a", true, nil, ""),
			}, NextLink: links["roots2"]},
			links["roots2"]:     {Items: []json.RawMessage{sharedRoot("300", "sh-a", false, nil, "")}},
			links["replies100"]: {Items: []json.RawMessage{sharedReply("102", "100", "sh-a")}},
			links["replies300"]: {Items: []json.RawMessage{sharedReply("301", "300", "sh-a")}},
		},
		pages: map[string]outlook.TeamsPage{
			"/allChannels": {Items: []json.RawMessage{
				sharedChannelRow("ch-a", "standard", teamsTestTenant),
				sharedChannelRow("sh-a", "shared", teamsTestTenant),
				sharedChannelRow("sh-in", "shared", teamsTestTenant),
				sharedChannelRow("sh-x", "shared", otherTenant),
			}},
			"/incomingChannels": {Items: []json.RawMessage{
				sharedChannelRow("sh-in", "shared", teamsTestTenant),
				sharedChannelRow("sh-x", "shared", otherTenant),
			}},
			"getAllMessages": {Items: []json.RawMessage{
				teamsMsg("900", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
				teamsMsg("950", "", "sh-in", "2026-10-02T10:00:00.000Z", nil),
			}},
			"/members":       {},
			"/installedApps": {},
			"/tags":          {},
			"/tabs":          {},
		}}
	return g, links
}

func sharedKey(ch, root, id string) string {
	return outlook.TeamsChannelMessageKey(outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"), ch, root, id, "2026-10-01T10:00:00.000Z")
}

func sharedHostedKeys() []string {
	return []string{
		sharedKey("sh-a", "100", "100"), sharedKey("sh-a", "100", "101"), sharedKey("sh-a", "100", "102"),
		sharedKey("sh-a", "200", "200"), sharedKey("sh-a", "300", "300"), sharedKey("sh-a", "300", "301"),
	}
}

// A shared channel the team hosts in this tenant is listed per channel with every reply (expanded,
// further reply pages, and replies of posts the next page didn't expand) under its own channel
// keys. Channels another team or tenant owns are never requested for messages, and a getAllMessages
// row of an incoming channel isn't stored under this team.
func TestTeamsSharedChannelHostedListedWithReplies(t *testing.T) {
	_, advance := retainedClock(t)
	g, links := sharedTeamGraph()
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)

	status, _ := r.syncTeam(teamsTeamInfo{ID: "team-1"}, r.deadline)
	if status != teamsCompleteWithGaps {
		t.Fatalf("cross-tenant channel leaves a gap, got %s; failures %+v unknown %v", status, r.mem.TeamsFailures, g.unknown)
	}
	if len(g.unknown) != 0 || len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("unknown %v failures %+v", g.unknown, r.mem.TeamsFailures)
	}
	for _, k := range sharedHostedKeys() {
		body, ok := store.objs[k]
		if !ok {
			t.Fatalf("missing %s; have %v", k, keysOf(store.objs))
		}
		var doc map[string]json.RawMessage
		_ = json.Unmarshal(body, &doc)
		if _, embedded := doc["replies"]; embedded || !strings.Contains(string(body), `"channel_messages"`) {
			t.Fatalf("%s must be stored alone with its source: %s", k, body)
		}
	}
	if _, ok := store.objs[sharedKey("ch-a", "900", "900")]; !ok {
		t.Fatal("standard channel messages keep coming from getAllMessages")
	}
	if _, ok := store.objs[sharedKey("sh-in", "950", "950")]; ok || r.counts["incoming_channel_skipped"] != 1 {
		t.Fatalf("an incoming channel's message belongs to its host team: counts %v", r.counts)
	}
	for _, c := range g.calls {
		if strings.Contains(c, "sh-x") || strings.Contains(c, "/channels/sh-in/messages") {
			t.Fatalf("no request for a channel this team doesn't own: %s", c)
		}
	}
	if !teamsCalled(g, links["replies100"]) || !teamsCalled(g, links["replies300"]) {
		t.Fatalf("reply pages must be read: %v", g.calls)
	}

	var meta struct {
		Shared []teamsSharedChannelCoverageEntry `json:"shared_channel_message_coverage"`
	}
	_ = json.Unmarshal(store.objs[outlook.TeamsTeamMetaKey(outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))], &meta)
	got := map[string]string{}
	for _, e := range meta.Shared {
		got[e.ChannelID] = e.MessageStatus + "/" + e.CoverageReason
	}
	if got["sh-a"] != teamsSharedListed+"/getAllMessages_documents_public_and_private_only" ||
		got["sh-in"] != teamsSharedAtHostTeam+"/hosted_by_another_team" ||
		got["sh-x"] != teamsSharedNotCollected+"/app_only_access_requires_owner_tenant" {
		t.Fatalf("coverage %v", got)
	}

	g.calls = nil
	r2 := newTestTeamsRun(t, store, nil, nil)
	r2.syncTeam(teamsTeamInfo{ID: "team-1"}, r2.deadline)
	if teamsCalled(g, "/channels/sh-a/messages") {
		t.Fatalf("listed within a day: %v", g.calls)
	}

	advance(teamsSharedEvery + time.Hour)
	before := map[string]int{}
	for _, k := range sharedHostedKeys() {
		before[k] = store.puts[k]
	}
	r3 := newTestTeamsRun(t, store, nil, nil)
	r3.syncTeam(teamsTeamInfo{ID: "team-1"}, r3.deadline)
	if !teamsCalled(g, links["roots"]) {
		t.Fatal("listed again after a day")
	}
	for k, n := range before {
		if store.puts[k] != n {
			t.Fatalf("unchanged %s rewritten", k)
		}
	}
}

// A budget stop keeps the channel and its next page; the next run resumes there.
func TestTeamsSharedChannelResumesAfterBudgetStop(t *testing.T) {
	clock, advance := retainedClock(t)
	g, links := sharedTeamGraph()
	g.install(t)
	inner := teamsPageFn
	teamsPageFn = func(ctx context.Context, tok, u string) (outlook.TeamsPage, error) {
		if strings.Contains(u, "/channels/sh-a/messages?") {
			advance(time.Hour)
		}
		return inner(ctx, tok, u)
	}
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	var ss teamsSharedState
	saves := 0

	if got := r.syncSharedChannels(&ss, "team-1", prefix, []string{"sh-a"}, clock.Add(30*time.Minute), retainedSave(&saves)); got != teamsIncomplete {
		t.Fatalf("budget stop %q", got)
	}
	if ss.Channel != "sh-a" || ss.PageLink != links["roots2"] || ss.ListedAt != nil || saves == 0 {
		t.Fatalf("page 1 stored, next page kept: %+v", ss)
	}
	if _, ok := store.objs[sharedKey("sh-a", "100", "102")]; !ok {
		t.Fatal("page 1 threads stored before the stop")
	}
	g.calls = nil
	if got := r.syncSharedChannels(&ss, "team-1", prefix, []string{"sh-a"}, clock.Add(24*time.Hour), retainedSave(&saves)); got != teamsComplete {
		t.Fatalf("resume %q", got)
	}
	if len(g.calls) == 0 || g.calls[0] != links["roots2"] {
		t.Fatalf("resume at the saved page: %v", g.calls)
	}
	for _, c := range g.calls {
		if c == links["roots"] {
			t.Fatalf("resume must not restart the channel: %v", g.calls)
		}
	}
	if _, ok := store.objs[sharedKey("sh-a", "300", "301")]; !ok || ss.ListedAt == nil || ss.Channel != "" {
		t.Fatalf("round finished: %+v", ss)
	}
}

// A channel that refuses (permission) or is gone is skipped for the round without a failure entry
// and leaves a gap; the other channels are still listed.
func TestTeamsSharedChannelRefusedOrInaccessible(t *testing.T) {
	for _, refusal := range []error{
		&outlook.TeamsGraphError{Status: 403, Code: "Forbidden"},
		&outlook.TeamsGraphError{Status: 404, Code: "NotFound"},
	} {
		retainedClock(t)
		g, _ := sharedTeamGraph()
		g.errs = map[string]error{"/channels/sh-b/messages": refusal}
		g.install(t)
		store := newMemTeamsStore()
		r := newTestTeamsRun(t, store, nil, nil)
		var ss teamsSharedState
		got := r.syncSharedChannels(&ss, "team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"), []string{"sh-a", "sh-b"}, r.deadline, func() error { return nil })
		code := outlook.ClassifyTeamsError(refusal)
		if got != teamsCompleteWithGaps || ss.Refused["sh-b"] != code || len(r.mem.TeamsFailures) != 0 || ss.ListedAt == nil {
			t.Fatalf("%s: status %q state %+v failures %+v", code, got, ss, r.mem.TeamsFailures)
		}
		if _, ok := store.objs[sharedKey("sh-a", "300", "301")]; !ok {
			t.Fatalf("%s: other channels still listed", code)
		}
		if f := r.status.Features[teamsFeatureSharedChannels]; f.CapabilityStatus != code {
			t.Fatalf("%s: feature %+v", code, f)
		}
	}
}

// When the team's incoming channels can't be read, no shared channel is listed (its owner is
// unknown): a refusal shows host_team_unresolved without a failure entry, a transient error keeps
// one entry and the team incomplete until a later run reads them.
func TestTeamsSharedChannelHostUnresolved(t *testing.T) {
	retainedClock(t)
	g, links := sharedTeamGraph()
	g.errs = map[string]error{"/incomingChannels": &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	if status, _ := r.syncTeam(teamsTeamInfo{ID: "team-1"}, r.deadline); status != teamsCompleteWithGaps {
		t.Fatalf("refused lookup leaves a gap, got %s", status)
	}
	if teamsCalled(g, "/channels/sh-a/messages") || len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("no listing and no failure entry: calls %v failures %+v", g.calls, r.mem.TeamsFailures)
	}
	if _, ok := store.objs[sharedKey("sh-in", "950", "950")]; !ok {
		t.Fatal("unresolved: getAllMessages rows are kept as before")
	}

	g.errs = map[string]error{"/incomingChannels": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}
	r2 := newTestTeamsRun(t, store, nil, nil)
	if status, _ := r2.syncTeam(teamsTeamInfo{ID: "team-1"}, r2.deadline); status != teamsIncomplete {
		t.Fatalf("transient lookup error must be incomplete, got %s", status)
	}
	if _, ok := r2.mem.TeamsFailures[teamsSharedStreamKey("team-1")]; !ok || len(r2.mem.TeamsFailures) != 1 {
		t.Fatalf("one stream entry: %+v", r2.mem.TeamsFailures)
	}

	delete(g.errs, "/incomingChannels")
	if status, _ := r2.syncTeam(teamsTeamInfo{ID: "team-1"}, r2.deadline); status != teamsCompleteWithGaps {
		t.Fatalf("recovered status %s", status)
	}
	if len(r2.mem.TeamsFailures) != 0 || !teamsCalled(g, links["roots"]) {
		t.Fatalf("replay lists the hosted channel and clears the entry: %+v", r2.mem.TeamsFailures)
	}
}

// A transient error on a later page keeps one stream entry; the replay restarts the channel,
// rewrites nothing already stored and clears the entry.
func TestTeamsSharedChannelTransientErrorReplays(t *testing.T) {
	retainedClock(t)
	g, _ := sharedTeamGraph()
	g.errs = map[string]error{"$skiptoken=p2": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	var ss teamsSharedState
	if got := r.syncSharedChannels(&ss, "team-1", prefix, []string{"sh-a"}, r.deadline, func() error { return nil }); got != teamsIncomplete {
		t.Fatalf("status %q", got)
	}
	if len(r.mem.TeamsFailures) != 1 || r.mem.TeamsFailures[teamsSharedStreamKey("team-1")].Kind != teamsFailStream || ss.Channel != "sh-a" {
		t.Fatalf("one stream entry, channel kept: %+v %+v", r.mem.TeamsFailures, ss)
	}
	before := store.puts[sharedKey("sh-a", "100", "101")]
	delete(g.errs, "$skiptoken=p2")
	if got := r.syncSharedChannels(&ss, "team-1", prefix, []string{"sh-a"}, r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("replay %q", got)
	}
	if len(r.mem.TeamsFailures) != 0 || store.puts[sharedKey("sh-a", "100", "101")] != before {
		t.Fatalf("replay clears the entry without rewrites: %+v", r.mem.TeamsFailures)
	}
	if _, ok := store.objs[sharedKey("sh-a", "300", "301")]; !ok {
		t.Fatal("replay stores page 2")
	}
}

// A thread whose replies can't be read holds the round with one entry for the team; the replay
// stores them and closes the round.
func TestTeamsSharedChannelReplyFailureHoldsRound(t *testing.T) {
	retainedClock(t)
	g, links := sharedTeamGraph()
	g.errs = map[string]error{"/messages/300/replies": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	var ss teamsSharedState
	if got := r.syncSharedChannels(&ss, "team-1", prefix, []string{"sh-a"}, r.deadline, func() error { return nil }); got != teamsIncomplete {
		t.Fatalf("status %q", got)
	}
	if len(r.mem.TeamsFailures) != 1 || r.mem.TeamsFailures["shared:window:team:team-1"].Kind != teamsFailWindow || ss.Held != 1 || ss.ListedAt != nil {
		t.Fatalf("held with one entry: %+v %+v", r.mem.TeamsFailures, ss)
	}
	delete(g.errs, "/messages/300/replies")
	g.calls = nil
	if got := r.syncSharedChannels(&ss, "team-1", prefix, []string{"sh-a"}, r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("replay %q", got)
	}
	if len(r.mem.TeamsFailures) != 0 || ss.Held != 0 || ss.ListedAt == nil || !teamsCalled(g, links["replies300"]) {
		t.Fatalf("replay closes the round: %+v %+v", r.mem.TeamsFailures, ss)
	}
	if _, ok := store.objs[sharedKey("sh-a", "300", "301")]; !ok {
		t.Fatal("reply stored on replay")
	}
}

// A listed message naming another channel, team or thread is refused and nothing is written.
func TestTeamsSharedChannelOwnership(t *testing.T) {
	retainedClock(t)
	(&fakeGraph{strict: true}).install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	for name, raw := range map[string]json.RawMessage{
		"other channel": teamsMsg("101", "100", "sh-b", "2026-10-02T10:00:00.000Z", nil),
		"other team": teamsMsg("101", "100", "sh-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{
			"channelIdentity": map[string]string{"teamId": "team-2", "channelId": "sh-a"}}),
		"other thread": teamsMsg("101", "999", "sh-a", "2026-10-02T10:00:00.000Z", nil),
		"no channel":   teamsMsg("101", "100", "sh-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{"channelIdentity": nil}),
	} {
		if err := r.storeSharedMessage("team-1", prefix, "sh-a", "100", raw); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	if len(store.puts) != 0 {
		t.Fatalf("nothing written: %v", store.puts)
	}
}

// A retained version of a channel another team hosts is not stored under this team.
func TestTeamsRetainedSkipsIncomingSharedChannel(t *testing.T) {
	retainedClock(t)
	(&fakeGraph{strict: true}).install(t)
	store := newMemTeamsStore()
	r := retainedRun(t, store)
	r.sharedIncoming = map[string]map[string]bool{"team-1": {"sh-in": true}}
	scope := teamsChannelRetainedScope("team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
	if err := r.storeRetainedVersion(scope, teamsMsg("950", "", "sh-in", "2026-10-02T10:00:00.000Z", nil)); err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 0 || r.counts["history_out_of_scope"] != 1 {
		t.Fatalf("incoming channel version skipped: puts %v counts %v", store.puts, r.counts)
	}
}

// Channel config (members, tabs) of a channel owned by another tenant is never requested.
func TestTeamsOwnTenantChannels(t *testing.T) {
	in := []teamsChannelInfo{{ID: "a", TenantID: teamsTestTenant}, {ID: "b", TenantID: strings.ToUpper(teamsTestTenant)},
		{ID: "c"}, {ID: "x", TenantID: otherTenant}}
	out := teamsOwnTenantChannels(in, teamsTestTenant)
	if len(out) != 3 || out[0].ID != "a" || out[1].ID != "b" || out[2].ID != "c" {
		t.Fatalf("got %+v", out)
	}
}
