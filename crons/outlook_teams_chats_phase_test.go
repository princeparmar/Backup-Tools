package crons

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func rawJSON(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func chatMsg(id, chatID, modified, sender string) json.RawMessage {
	return rawJSON(map[string]interface{}{
		"id": id, "chatId": chatID, "etag": "e" + modified, "createdDateTime": "2026-10-01T09:00:00.000Z",
		"lastModifiedDateTime": modified, "body": map[string]string{"content": "hi"},
		"from": map[string]interface{}{"user": map[string]string{"id": sender}},
	})
}

func teamsUsersPage(ids ...string) outlook.TeamsPage {
	var items []json.RawMessage
	for _, id := range ids {
		items = append(items, rawJSON(map[string]string{"id": id, "userType": "Member"}))
	}
	items = append(items, rawJSON(map[string]string{"id": "guest-1", "userType": "Guest"}))
	return outlook.TeamsPage{Items: items}
}

func TestTeamsChatsDeltaStoresOnceWithPointers(t *testing.T) {
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{
		"/users?$select":   teamsUsersPage("u1", "u2"),
		"/users/u1/chats?": {Items: []json.RawMessage{rawJSON(map[string]interface{}{"id": "chat-1", "members": []map[string]string{{"userId": "u1"}, {"userId": "u2"}}})}},
		"pinnedMessages":   {},
		"getAllMessages/delta": {Items: []json.RawMessage{chatMsg("1", "chat-1", "2026-10-02T09:00:00.000Z", "u1")},
			DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=d1"},
		"chats/getAllMessages?": {Items: []json.RawMessage{chatMsg("1", "chat-1", "2026-10-02T09:00:00.000Z", "u1")}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	if _, err := r.scopeUsers(); err != nil || r.userScope["guest-1"] || !r.userScope["u2"] {
		t.Fatalf("users in scope: %v %v", r.userScope, err)
	}
	if status, _ := r.syncUserChats("u1", r.deadline); status != teamsComplete {
		t.Fatalf("status %s", status)
	}
	chatPrefix := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	msgKey := outlook.TeamsChatMessageKey(chatPrefix, "1", "2026-10-01T09:00:00.000Z")
	if store.puts[msgKey] != 1 {
		t.Fatalf("message must be written once across delta and history, got %d", store.puts[msgKey])
	}
	for _, u := range []string{"u1", "u2"} {
		ptr := outlook.TeamsUserChatPointerKey(outlook.TeamsUserPrefix(teamsTestTenant, u), "chat-1")
		if store.meta[ptr][outlook.TeamsMetaPhysical] != chatPrefix || len(store.objs[ptr]) != 0 {
			t.Fatalf("pointer for %s: %v", u, store.meta[ptr])
		}
	}
	var st teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
	if !strings.Contains(st.ChatsDeltaLink, "deltatoken=d1") || !st.HistoryDone || st.HistoryUpper == nil {
		t.Fatalf("user state: %+v", st)
	}

	// A member removed from the chat keeps the pointer, marked left-at.
	g.pages["/users/u1/chats?"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]interface{}{"id": "chat-1", "members": []map[string]string{{"userId": "u1"}}})}}
	r.chatsSeen = map[string]bool{}
	st.ChatsListedAt = nil
	if err := r.syncUserChatList("u1", &st); err != nil {
		t.Fatal(err)
	}
	left := outlook.TeamsUserChatPointerKey(outlook.TeamsUserPrefix(teamsTestTenant, "u2"), "chat-1")
	if store.meta[left][outlook.TeamsMetaLeftAt] == "" {
		t.Fatalf("removed member's pointer must be kept with left-at: %v", store.meta[left])
	}
}

func TestTeamsChatRoundHeldReplaysFromLastDelta(t *testing.T) {
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{
		"/users/u1/chats?": {},
		"getAllMessages/delta": {Items: []json.RawMessage{chatMsg("bad", "chat-1", "2026-10-02T09:00:00.000Z", "u1")},
			DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=new"},
	}}
	g.install(t)
	store := newMemTeamsStore()
	store.putErr = func(key string, _ int) error {
		if strings.Contains(key, "/chat/") {
			return errors.New("down")
		}
		return nil
	}
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"] = true
	status, _ := r.syncUserChats("u1", r.deadline)
	if status != teamsIncomplete {
		t.Fatalf("status %s", status)
	}
	var st teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
	if st.ChatsDeltaLink != "" || st.ChatsHeld != 1 {
		t.Fatalf("held round must not move the delta link: %+v", st)
	}
	f, ok := r.mem.TeamsFailures["chats:item:chat-1:bad"]
	if !ok || f.ItemIDs["source"] != "delta" {
		t.Fatalf("item entry keyed by chat and message: %+v", r.mem.TeamsFailures)
	}
}

// Chat history follows the nextLink exactly as documented for chats getAllMessages
// (users('id')/chats/getallMessages) and finishes the history.
func TestTeamsChatHistoryFollowsDocumentedNextLink(t *testing.T) {
	next := "https://graph.microsoft.com/v1.0/users('u1')/chats/getallMessages?$top=2&$skiptoken=U2tpcFZhbHVlPTIj%3d%3d"
	g := &fakeGraph{strict: true, pages: map[string]outlook.TeamsPage{
		"/users/u1/chats?":     {},
		"getAllMessages/delta": {DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=d1"},
		"/users/u1/chats/getAllMessages?$filter": {Items: []json.RawMessage{chatMsg("h1", "chat-1", "2026-10-02T09:00:00.000Z", "u1")},
			NextLink: next},
		"$skiptoken=U2tpcFZhbHVlPTIj%3d%3d": {Items: []json.RawMessage{chatMsg("h2", "chat-1", "2026-10-02T09:05:00.000Z", "u1")}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"] = true
	if status, _ := r.syncUserChats("u1", r.deadline); status != teamsComplete {
		t.Fatalf("status %s; failures %+v; unknown %v", status, r.mem.TeamsFailures, g.unknown)
	}
	chatPrefix := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	for _, id := range []string{"h1", "h2"} {
		if _, ok := store.objs[outlook.TeamsChatMessageKey(chatPrefix, id, "2026-10-01T09:00:00.000Z")]; !ok {
			t.Fatalf("history message %s missing; have %v", id, keysOf(store.objs))
		}
	}
	var st teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
	if !st.HistoryDone || st.HistoryPageLink != "" || st.HistoryHeld != 0 {
		t.Fatalf("history must finish: %+v", st)
	}
	followed := false
	for _, c := range g.calls {
		followed = followed || c == next
	}
	if !followed || len(g.unknown) != 0 {
		t.Fatalf("nextLink must be requested unchanged: calls %v unknown %v", g.calls, g.unknown)
	}
}

// A nextLink the guard rejects holds the history round and restarts it from its start (never the
// same saved page); once the listing pages normally the round finishes and the failure clears.
func TestTeamsChatHistoryRejectedNextLinkHoldsThenRecovers(t *testing.T) {
	g := &fakeGraph{strict: true, pages: map[string]outlook.TeamsPage{
		"/users/u1/chats?":     {},
		"getAllMessages/delta": {DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=d1"},
		"/users/u1/chats/getAllMessages?$filter": {Items: []json.RawMessage{chatMsg("h1", "chat-1", "2026-10-02T09:00:00.000Z", "u1")},
			NextLink: "https://evil.example.com/v1.0/users('u1')/chats/getallMessages?$skiptoken=x"},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"] = true
	if status, _ := r.syncUserChats("u1", r.deadline); status != teamsIncomplete {
		t.Fatalf("status %s", status)
	}
	var st teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
	if st.HistoryDone || st.HistoryPageLink != "" || st.HistoryHeld != 1 || st.HistoryHeldSince == nil {
		t.Fatalf("rejected nextLink must hold the round from its start: %+v", st)
	}
	f, ok := r.mem.TeamsFailures["chat_history:stream:user:u1"]
	if !ok || f.ErrorCode != outlook.TeamsStatusPaging {
		t.Fatalf("paging failure must be recorded: %+v", r.mem.TeamsFailures)
	}
	for _, c := range g.calls {
		if strings.Contains(c, "evil.example.com") {
			t.Fatalf("a foreign nextLink must never be requested: %s", c)
		}
	}

	page := g.pages["/users/u1/chats/getAllMessages?$filter"]
	page.NextLink = ""
	g.pages["/users/u1/chats/getAllMessages?$filter"] = page
	if status, _ := r.syncUserChats("u1", r.deadline); status != teamsComplete {
		t.Fatalf("recovered status %s", status)
	}
	var after teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &after)
	if !after.HistoryDone || after.HistoryHeld != 0 || after.HistoryHeldSince != nil {
		t.Fatalf("history must finish after recovery: %+v", after)
	}
	if _, ok := r.mem.TeamsFailures["chat_history:stream:user:u1"]; ok {
		t.Fatal("the paging failure must clear once the listing pages normally")
	}
}

func TestTeamsChatDeletionMarkerPreservesStoredMessage(t *testing.T) {
	(&fakeGraph{strict: true}).install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	message := chatMsg("chat-msg", "chat-1", "2026-10-02T09:00:00Z", "u1")
	var head teamsMessageHead
	if err := json.Unmarshal(message, &head); err != nil {
		t.Fatal(err)
	}
	if _, err := r.storeChatMessage(message, head); err != nil {
		t.Fatal(err)
	}
	key := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(teamsTestTenant, "chat-1"), "chat-msg", "2026-10-01T09:00:00.000Z")
	marker := rawJSON(map[string]interface{}{"id": "chat-msg", "@removed": map[string]string{"reason": "deleted"}})
	if failureKey, _, err, _ := r.chatItem("u1", "delta", marker); err != nil || failureKey != "" {
		t.Fatalf("deletion marker: key %q, err %v", failureKey, err)
	}
	if !strings.Contains(string(store.objs[key]), "hi") || store.meta[key][outlook.TeamsMetaRemovedAt] == "" {
		t.Fatalf("deleted chat message must retain content and record removed-at: %s %v", store.objs[key], store.meta[key])
	}
}

func TestTeamsMeetNowChatIsNotStored(t *testing.T) {
	cases := []struct {
		name        string
		chatType    string
		topic       string
		wantChatKey bool
	}{
		{name: "meet now", chatType: "meeting", topic: "Meeting with Kevin", wantChatKey: false},
		{name: "scheduled meeting chat", chatType: "meeting", topic: "Standup call", wantChatKey: true},
		{name: "regular 1:1", chatType: "oneOnOne", topic: "", wantChatKey: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "chat-" + strings.ReplaceAll(tc.name, " ", "-")
			row := map[string]interface{}{"id": id, "chatType": tc.chatType, "members": []map[string]string{{"userId": "u1"}}}
			if tc.topic != "" {
				row["topic"] = tc.topic
			}
			g := &fakeGraph{pages: map[string]outlook.TeamsPage{
				"/users/u1/chats?":      {Items: []json.RawMessage{rawJSON(row)}},
				"pinnedMessages":        {},
				"getAllMessages/delta":  {DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=d1"},
				"chats/getAllMessages?": {},
			}}
			g.install(t)
			store := newMemTeamsStore()
			r := newTestTeamsRun(t, store, nil, nil)
			r.userScope["u1"] = true
			if status, _ := r.syncUserChats("u1", r.deadline); status != teamsComplete {
				t.Fatalf("status %s failures %+v", status, r.mem.TeamsFailures)
			}
			meta := outlook.TeamsChatMetaKey(outlook.TeamsChatPrefix(teamsTestTenant, id))
			_, got := store.objs[meta]
			if got != tc.wantChatKey {
				t.Fatalf("stored=%v want %v keys %v", got, tc.wantChatKey, keysOf(store.objs))
			}
		})
	}
}

func TestTeamsIsCallLogText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "plain", in: "Call Logs for Call c2a18cc6-624b-4b9f-92e4-2d9a9c038ad9", want: true},
		{name: "html", in: "<p>Call Logs for Call 7daee59e-a214-48de-974b-5a68cb750cc6</p>", want: true},
		{name: "normal chat", in: "hey, can you call me?", want: false},
		{name: "meeting ended", in: "Call ended", want: false},
		{name: "short id", in: "Call Logs for Call not-a-guid", want: false},
		{name: "topic", in: "Call logs", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := teamsIsCallLogText(tc.in); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestTeamsIsMeetNowChat(t *testing.T) {
	tests := []struct {
		name     string
		chatType string
		topic    string
		want     bool
	}{
		{name: "meet now", chatType: "meeting", topic: "Meeting with Kevin", want: true},
		{name: "scheduled", chatType: "meeting", topic: "Standup call", want: false},
		{name: "oneOnOne", chatType: "oneOnOne", topic: "Meeting with Kevin", want: false},
		{name: "empty", chatType: "meeting", topic: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := teamsIsMeetNowChat(tc.chatType, tc.topic); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestTeamsCallLogChatIsNotStored(t *testing.T) {
	callChat := rawJSON(map[string]interface{}{
		"id": "call-chat", "topic": "Call logs", "chatType": "oneOnOne", "lastUpdatedDateTime": "2026-10-28T12:13:00.000Z",
		"members": []map[string]string{{"userId": "u1", "displayName": "Me"}, {"userId": "u2", "displayName": "support"}},
	})
	humanChat := rawJSON(map[string]interface{}{"id": "chat-1", "members": []map[string]string{{"userId": "u1"}}})
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{
		"/users/u1/chats?": {Items: []json.RawMessage{callChat, humanChat}},
		"pinnedMessages":   {},
		"getAllMessages/delta": {Items: []json.RawMessage{
			chatMsg("h1", "chat-1", "2026-10-02T09:00:00.000Z", "u1"),
			rawJSON(map[string]interface{}{
				"id": "c1", "chatId": "call-chat", "etag": "e1", "createdDateTime": "2026-10-28T12:13:00.000Z",
				"lastModifiedDateTime": "2026-10-28T12:13:00.000Z",
				"body":                 map[string]string{"content": "Call Logs for Call c2a18cc6-624b-4b9f-92e4-2d9a9c038ad9"},
			}),
		}, DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/chats/getAllMessages/delta?$deltatoken=d1"},
		"chats/getAllMessages?": {Items: []json.RawMessage{chatMsg("h1", "chat-1", "2026-10-02T09:00:00.000Z", "u1")}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"] = true
	if status, _ := r.syncUserChats("u1", r.deadline); status != teamsComplete {
		t.Fatalf("status %s failures %+v", status, r.mem.TeamsFailures)
	}
	for k := range store.objs {
		if strings.Contains(k, "/chat/") && strings.Contains(k, "call-chat") {
			t.Fatalf("call log chat must not be stored under chat/: %s", k)
		}
	}
	callKey := outlook.TeamsCallRecordKey(teamsTestTenant, "call-chat", "2026-10-28T12:13:00.000Z")
	if raw, ok := store.objs[callKey]; !ok || !strings.Contains(string(raw), "support") {
		t.Fatalf("call log must land in Calls history: %s have %v", callKey, keysOf(store.objs))
	}
	humanPrefix := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	if _, ok := store.objs[outlook.TeamsChatMessageKey(humanPrefix, "h1", "2026-10-01T09:00:00.000Z")]; !ok {
		t.Fatalf("human chat must still be stored; have %v", keysOf(store.objs))
	}
}

func calendarEvent(id, ical, userRole string, start time.Time, teams bool) json.RawMessage {
	provider := "teamsForBusiness"
	if !teams {
		provider = "unknown"
	}
	return rawJSON(map[string]interface{}{
		"id": id, "iCalUId": ical, "type": "singleInstance", "changeKey": "ck-" + id, "lastModifiedDateTime": "2026-10-01T00:00:00Z",
		"isOnlineMeeting": teams, "onlineMeetingProvider": provider, "isOrganizer": userRole == "organizer",
		"onlineMeeting":  map[string]string{"joinUrl": "https://teams.microsoft.com/l/meetup-join/x"},
		"organizer":      map[string]interface{}{"emailAddress": map[string]string{"address": "org@contoso.com"}},
		"responseStatus": map[string]string{"response": "accepted"},
		"start":          map[string]string{"dateTime": start.UTC().Format("2006-01-02T15:04:05.0000000"), "timeZone": "UTC"},
		"end":            map[string]string{"dateTime": start.Add(time.Hour).UTC().Format("2006-01-02T15:04:05.0000000"), "timeZone": "UTC"},
	})
}

func TestTeamsMeetingsKeepMailboxCopiesApartAndReconcile(t *testing.T) {
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{
		"/users/u1/calendarView/delta": {Items: []json.RawMessage{
			calendarEvent("ev-1", "ical-1", "organizer", start, true),
			calendarEvent("ev-x", "ical-x", "organizer", start, false),
		}, DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/calendarView/delta?$deltatoken=c1"},
		"/users/u2/calendarView/delta": {Items: []json.RawMessage{calendarEvent("ev-2", "ical-1", "attendee", start, true)}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope = map[string]bool{"u1": true, "u2": true}
	for _, u := range []string{"u1", "u2"} {
		if status, code := r.syncUserMeetings(u, r.deadline); status != teamsComplete {
			t.Fatalf("%s: %s %s", u, status, code)
		}
	}
	m1, _, _ := outlook.TeamsMeetingID("ical-1", "u1", "ev-1", false)
	m2, _, _ := outlook.TeamsMeetingID("ical-1", "u2", "ev-2", false)
	for _, mid := range []string{m1, m2} {
		prefix := outlook.TeamsMeetingPrefix(teamsTestTenant, mid)
		if _, ok := store.objs[outlook.TeamsMeetingEventKey(prefix)]; !ok {
			t.Fatalf("event.json missing for %s", mid)
		}
		var doc map[string]interface{}
		_ = json.Unmarshal(store.objs[outlook.TeamsMeetingMetaKey(prefix)], &doc)
		if doc["transcript_content_status"] != outlook.TeamsStatusContentNotCollected || doc["identity"].(map[string]interface{})["reason"] != "case_unverified" {
			t.Fatalf("_meeting.json: %v", doc)
		}
	}
	nonTeams, _, _ := outlook.TeamsMeetingID("ical-x", "u1", "ev-x", false)
	if _, ok := store.objs[outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsTestTenant, nonTeams))]; !ok {
		t.Fatal("mailbox calendar appointments must be stored with Teams meetings")
	}
	r.writeReconcile()
	var rec struct {
		Candidates []string `json:"candidates"`
	}
	_ = json.Unmarshal(store.objs[outlook.TeamsReconcileMeetingKey(r.tenantPrefix, m1)], &rec)
	if len(rec.Candidates) != 1 || rec.Candidates[0] != m2 {
		t.Fatalf("reconcile candidates: %+v", rec)
	}
	ptr := outlook.TeamsUserMeetingPointerKey(outlook.TeamsUserPrefix(teamsTestTenant, "u1"), m1, outlook.TeamsUTC(start))
	if store.meta[ptr][outlook.TeamsMetaRole] != "organizer" {
		t.Fatalf("organizer pointer: %v", store.meta[ptr])
	}
	var st teamsUserState
	r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
	if _, ok := st.Attendance[m1]; !ok || len(st.CalendarRanges) != 1 || !st.CalendarRanges[0].Done {
		t.Fatalf("organizer meeting queued for attendance, range done: %+v", st)
	}
	if _, ok := st.Attendance[nonTeams]; ok {
		t.Fatal("plain calendar appointments must not queue meeting attendance")
	}
	if _, ok := store.objs[outlook.TeamsUserMeetingCoverageKey(outlook.TeamsUserPrefix(teamsTestTenant, "u1"))]; !ok {
		t.Fatal("calendar coverage must be written")
	}

	// @removed marks a stored event inside the range as removed, keeping it.
	g.pages["/users/u1/calendarView/delta"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]interface{}{"id": "ev-1", "@removed": map[string]string{"reason": "deleted"}})}}
	if status, _ := r.syncUserMeetings("u1", r.deadline); status != teamsComplete {
		t.Fatalf("removal run: %s", status)
	}
	evKey := outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsTestTenant, m1))
	if store.meta[evKey][outlook.TeamsMetaRemovedAt] == "" || len(store.objs[evKey]) == 0 {
		t.Fatalf("removed event keeps its object with removed-at: %v", store.meta[evKey])
	}
}

func TestTeamsCalendarStoresEveryMailboxEvent(t *testing.T) {
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	cases := []struct {
		name       string
		id, ical   string
		teams      bool
		role       string
		wantAttend bool
	}{
		{name: "teams organizer", id: "ev-t", ical: "ical-t", teams: true, role: "organizer", wantAttend: true},
		{name: "outlook appointment", id: "ev-a", ical: "ical-a", teams: false, role: "organizer", wantAttend: false},
		{name: "teams attendee", id: "ev-b", ical: "ical-b", teams: true, role: "attendee", wantAttend: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &fakeGraph{pages: map[string]outlook.TeamsPage{
				"/users/u1/calendarView/delta": {
					Items:     []json.RawMessage{calendarEvent(tc.id, tc.ical, tc.role, start, tc.teams)},
					DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/calendarView/delta?$deltatoken=c1",
				},
			}}
			g.install(t)
			store := newMemTeamsStore()
			r := newTestTeamsRun(t, store, nil, nil)
			r.userScope["u1"] = true
			if status, _ := r.syncUserMeetings("u1", r.deadline); status != teamsComplete {
				t.Fatalf("status %s failures %+v", status, r.mem.TeamsFailures)
			}
			mid, _, _ := outlook.TeamsMeetingID(tc.ical, "u1", tc.id, false)
			if _, ok := store.objs[outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))]; !ok {
				t.Fatal("event.json missing")
			}
			var st teamsUserState
			r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
			_, queued := st.Attendance[mid]
			if queued != tc.wantAttend {
				t.Fatalf("attendance queued=%v want %v: %+v", queued, tc.wantAttend, st.Attendance)
			}
		})
	}
}

func mailboxCalendar(id, name string, def bool) json.RawMessage {
	return rawJSON(map[string]interface{}{
		"id": id, "name": name, "color": "auto", "isDefaultCalendar": def,
	})
}

func withEventFields(raw json.RawMessage, extra map[string]interface{}) json.RawMessage {
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	for k, v := range extra {
		m[k] = v
	}
	return rawJSON(m)
}

func TestTeamsMailboxCalendarsBackupHolidaysAndBirthdays(t *testing.T) {
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	cases := []struct {
		name           string
		calendars      []json.RawMessage
		defaultEvents  []json.RawMessage
		named          map[string][]json.RawMessage
		docs           map[string][]byte
		wantNames      []string
		wantEventCal   map[string]string
		wantSubjects   map[string]string
		wantNamedDelta []string
	}{
		{
			name: "holidays and birthdays with the default calendar",
			calendars: []json.RawMessage{
				mailboxCalendar("cal-def", "Calendar", true),
				mailboxCalendar("cal-hol", "United States holidays", false),
				mailboxCalendar("cal-bday", "Birthdays", false),
			},
			defaultEvents: []json.RawMessage{
				withEventFields(calendarEvent("ev-standup", "ical-standup", "organizer", start, false), map[string]interface{}{"subject": "Standup"}),
			},
			named: map[string][]json.RawMessage{
				"cal-hol": {
					withEventFields(calendarEvent("ev-thanks", "ical-thanks", "organizer", start, false), map[string]interface{}{"subject": "Thanksgiving Day", "isAllDay": true}),
				},
				"cal-bday": {
					withEventFields(calendarEvent("ev-bday", "ical-bday", "organizer", start, false), map[string]interface{}{"subject": "Alice's birthday", "isAllDay": true}),
				},
			},
			wantNames: []string{"Calendar", "United States holidays", "Birthdays"},
			wantEventCal: map[string]string{
				"ev-standup": "Calendar",
				"ev-thanks":  "United States holidays",
				"ev-bday":    "Birthdays",
			},
			wantSubjects: map[string]string{
				"ev-standup": "Standup",
				"ev-thanks":  "Thanksgiving Day",
				"ev-bday":    "Alice's birthday",
			},
			wantNamedDelta: []string{"/calendars/cal-hol/calendarView/delta", "/calendars/cal-bday/calendarView/delta"},
		},
		{
			name: "occurrence without subject copies the series master",
			calendars: []json.RawMessage{
				mailboxCalendar("cal-def", "Calendar", true),
			},
			defaultEvents: []json.RawMessage{
				withEventFields(calendarEvent("ev-occ", "ical-series", "organizer", start, false), map[string]interface{}{
					"type": "occurrence", "subject": "", "seriesMasterId": "ev-master",
				}),
			},
			docs: map[string][]byte{
				"/users/u1/events/ev-master": withEventFields(calendarEvent("ev-master", "ical-series", "organizer", start, false), map[string]interface{}{
					"type": "seriesMaster", "subject": "Weekly sync",
				}),
			},
			wantNames:    []string{"Calendar"},
			wantEventCal: map[string]string{"ev-occ": "Calendar"},
			wantSubjects: map[string]string{"ev-occ": "Weekly sync"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := map[string]outlook.TeamsPage{
				"/users/u1/calendars?": {
					Items: tc.calendars,
				},
				"/users/u1/calendarView/delta": {
					Items:     tc.defaultEvents,
					DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/calendarView/delta?$deltatoken=c1",
				},
			}
			for id, items := range tc.named {
				pages["/users/u1/calendars/"+id+"/calendarView/delta"] = outlook.TeamsPage{
					Items:     items,
					DeltaLink: "https://graph.microsoft.com/v1.0/users/u1/calendars/" + id + "/calendarView/delta?$deltatoken=n1",
				}
			}
			g := &fakeGraph{pages: pages, docs: tc.docs}
			g.install(t)
			store := newMemTeamsStore()
			r := newTestTeamsRun(t, store, nil, nil)
			r.userScope["u1"] = true
			if status, code := r.syncUserMeetings("u1", r.deadline); status != teamsComplete {
				t.Fatalf("status %s %s failures %+v", status, code, r.mem.TeamsFailures)
			}
			var listed struct {
				Calendars []struct {
					Name string `json:"name"`
				} `json:"calendars"`
			}
			if err := json.Unmarshal(store.objs[outlook.TeamsUserCalendarsKey(outlook.TeamsUserPrefix(teamsTestTenant, "u1"))], &listed); err != nil {
				t.Fatalf("_calendars.json: %v", err)
			}
			gotNames := make([]string, 0, len(listed.Calendars))
			for _, c := range listed.Calendars {
				gotNames = append(gotNames, c.Name)
			}
			if strings.Join(gotNames, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("calendars %v want %v", gotNames, tc.wantNames)
			}
			var st teamsUserState
			r.loadState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &st)
			for eventID, wantCal := range tc.wantEventCal {
				mid := st.EventMids[eventID]
				if mid == "" {
					t.Fatalf("missing mid for %s: %+v", eventID, st.EventMids)
				}
				var doc map[string]interface{}
				if err := json.Unmarshal(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))], &doc); err != nil {
					t.Fatalf("_meeting.json %s: %v", eventID, err)
				}
				if doc["calendar_name"] != wantCal {
					t.Fatalf("%s calendar_name %v want %s", eventID, doc["calendar_name"], wantCal)
				}
			}
			for eventID, wantSub := range tc.wantSubjects {
				mid := st.EventMids[eventID]
				var ev struct {
					Subject string `json:"subject"`
				}
				if err := json.Unmarshal(store.objs[outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))], &ev); err != nil {
					t.Fatalf("event.json %s: %v", eventID, err)
				}
				if ev.Subject != wantSub {
					t.Fatalf("%s subject %q want %q", eventID, ev.Subject, wantSub)
				}
			}
			for _, part := range tc.wantNamedDelta {
				if !teamsCalled(g, part) {
					t.Fatalf("graph never requested %s: %v", part, g.calls)
				}
			}
		})
	}
}

func TestTeamsTranscriptListingDisabledByTenantIsBlockedNotRetried(t *testing.T) {
	g := &fakeGraph{errs: map[string]error{"getAllTranscripts": &outlook.TeamsGraphError{Status: 403, Code: "Forbidden", InnerCode: "GraphAccessToTranscriptsDisabled"}}}
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	st := &teamsUserState{}
	if got := r.syncArtifactListing("u1", st, "transcripts", r.deadline, func() error { return nil }); got != teamsBlocked {
		t.Fatalf("status %s", got)
	}
	if !st.skipped("transcripts", r.now()) || st.Status["transcripts"] != outlook.TeamsStatusDisabledByTenant {
		t.Fatalf("stream must back off: %+v", st)
	}
	if r.status.Features["teams_meeting_transcripts"].CapabilityStatus != outlook.TeamsStatusDisabledByTenant {
		t.Fatalf("feature: %+v", r.status.Features)
	}
}

func TestTeamsRecordingMetadataStoredWithoutContentURL(t *testing.T) {
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{"getAllRecordings": {Items: []json.RawMessage{rawJSON(map[string]string{
		"id": "rec-1", "meetingId": "om-1", "createdDateTime": "2026-10-01T00:00:00Z", "recordingContentUrl": "https://graph/x/content",
	})}}}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	st := &teamsUserState{OnlineMeetings: map[string]string{}}
	if got := r.syncArtifactListing("u1", st, "recordings", r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("status %s", got)
	}
	mid, _, _ := outlook.TeamsMeetingID("", "u1", "onlineMeeting:om-1", false)
	raw := string(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))])
	if !strings.Contains(raw, "rec-1") || strings.Contains(raw, "recordingContentUrl") || !strings.Contains(raw, `"calendar_status":"not_found"`) {
		t.Fatalf("_meeting.json: %s", raw)
	}
}

func TestTeamsCallRecordIncompleteIsStoredAndHoldsWindow(t *testing.T) {
	g := &fakeGraph{
		pages: map[string]outlook.TeamsPage{
			"/communications/callRecords?": {Items: []json.RawMessage{rawJSON(map[string]interface{}{
				"id": "call-1", "version": 3, "lastModifiedDateTime": "2026-10-10T00:00:00Z", "startDateTime": "2026-10-09T08:00:00Z",
			})}},
			"sessions": {Items: []json.RawMessage{rawJSON(map[string]string{"id": "s1"})}},
		},
		docs: map[string][]byte{"/communications/callRecords/call-1": []byte(`{"id":"call-1","organizer_v2":{"identity":{"user":{"id":"u1"}}}}`)},
		errs: map[string]error{"participants_v2": &outlook.TeamsGraphError{Status: 500}},
	}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"] = true
	if r.syncCallRecords(r.deadline) {
		t.Fatal("an incomplete record without a verified lookup holds the window")
	}
	key := outlook.TeamsCallRecordKey(teamsTestTenant, "call-1", "2026-10-09T08:00:00Z")
	if store.meta[key][outlook.TeamsMetaComplete] != "false" {
		t.Fatalf("partial record must be stored and marked: %v", store.meta[key])
	}
	block, _ := outlook.ReadTeamsBackup(store.objs[key])
	if block.Completion["participants"] != "failed" || block.Completion["sessions"] != "complete" {
		t.Fatalf("completion: %+v", block.Completion)
	}
	ptr := outlook.TeamsUserCallPointerKey(outlook.TeamsUserPrefix(teamsTestTenant, "u1"), "call-1", "2026-10-09T08:00:00Z")
	if store.meta[ptr][outlook.TeamsMetaPhysical] != key {
		t.Fatalf("participant pointer: %v", store.meta[ptr])
	}
	ws := r.mem.TeamsCalls
	if len(ws.Windows) != 1 || ws.Windows[0].Held != 1 || ws.HighWater != nil {
		t.Fatalf("calls window: %+v", ws)
	}
	if _, ok := r.mem.TeamsFailures["call_details:item:call-1"]; !ok {
		t.Fatal("details failure keyed by call record id")
	}
}

func TestTeamsCallsGapPastRetention(t *testing.T) {
	(&fakeGraph{}).install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	old := r.now().Add(-40 * 24 * time.Hour)
	last := r.now().Add(-26 * 24 * time.Hour)
	r.mem.TeamsCalls = &repo.TeamsWindowState{HighWater: &old, LastSuccessAt: &last}
	if !r.syncCallRecords(r.deadline) {
		t.Fatal("empty listing completes")
	}
	if len(r.status.Gaps) != 1 || r.status.Gaps[0].Kind != "calls_gap" {
		t.Fatalf("gap: %+v", r.status.Gaps)
	}
	if len(r.status.Warnings) == 0 {
		t.Fatal("25 days without a calls run must warn")
	}
}

func TestTeamsPhoneLogsChunkedAndUnparsedKept(t *testing.T) {
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{"getPstnCalls": {Items: []json.RawMessage{
		rawJSON(map[string]string{"id": "row-1", "callId": "c1", "userId": "u1", "startDateTime": "2026-10-01T00:00:00Z", "callType": "out"}),
		rawJSON(map[string]string{"callId": "c2"}),
	}}}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"] = true
	if !r.syncPhoneLogs(outlook.TeamsKeyPstn, r.deadline) {
		t.Fatal("pstn should complete")
	}
	for _, u := range g.calls {
		if !strings.Contains(u, "getPstnCalls") {
			continue
		}
		from, _ := time.Parse("2006-01-02T15:04:05.000Z", between(u, "fromDateTime=", ","))
		to, _ := time.Parse("2006-01-02T15:04:05.000Z", between(u, "toDateTime=", ")"))
		if to.Sub(from) > 90*24*time.Hour {
			t.Fatalf("request over 90 days: %s", u)
		}
	}
	unparsed, rows := 0, 0
	for k := range store.objs {
		switch {
		case strings.Contains(k, "/_unparsed/"):
			unparsed++
		case strings.Contains(k, "/pstn/"):
			rows++
		}
	}
	if unparsed == 0 || rows == 0 || r.mem.TeamsPstn.HighWater == nil || len(r.mem.TeamsPstn.Windows) != 0 {
		t.Fatalf("rows %d unparsed %d state %+v", rows, unparsed, r.mem.TeamsPstn)
	}
}

// teamsPhoneTest pins one PSTN window, [2026-09-22, 2026-10-10) requested from 2026-09-20 with
// the overlap, and returns the base of its documented nextLink (from/to, date only).
func teamsPhoneTest(t *testing.T, g *fakeGraph) (*teamsRun, *memTeamsStore, string) {
	t.Helper()
	g.strict = true
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.to = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	r.mem.TeamsPstn = &repo.TeamsWindowState{Windows: []repo.TeamsWindow{{From: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), To: r.to}}}
	return r, store, "https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2026-09-20,to=2026-10-10)"
}

func phoneRow(id string) json.RawMessage {
	return rawJSON(map[string]string{"id": id, "callId": "call-" + id, "userId": "u9", "startDateTime": "2026-10-01T00:00:00Z", "callType": "out"})
}

func phoneRows(store *memTeamsStore) int {
	n := 0
	for k := range store.objs {
		if strings.Contains(k, "/pstn/") && !strings.Contains(k, "/_unparsed/") {
			n++
		}
	}
	return n
}

func callsWith(calls []string, s string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, s) {
			n++
		}
	}
	return n
}

// A budget stop between pages keeps the next (not yet requested) page; the next run resumes from
// it, follows the documented $skip links through an empty final page and closes the window.
func TestTeamsPhoneLogsFollowContinuationPagesAndResumeAfterBudgetStop(t *testing.T) {
	g := &fakeGraph{}
	r, store, base := teamsPhoneTest(t, g)
	g.pages = map[string]outlook.TeamsPage{
		"getPstnCalls(fromDateTime=2026-09-20T00:00:00.000Z,toDateTime=2026-10-10T00:00:00.000Z)": {
			Items: []json.RawMessage{phoneRow("r1"), phoneRow("r2")}, NextLink: base + "?$skip=1000"},
		"$skip=1000": {Items: []json.RawMessage{phoneRow("r3")}, NextLink: base + "?$skip=2000"},
		"$skip=2000": {},
	}
	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	prevNow, inner := teamsNowFn, teamsPageFn
	teamsNowFn = func() time.Time { return clock }
	teamsPageFn = func(ctx context.Context, tok, u string) (outlook.TeamsPage, error) {
		clock = clock.Add(time.Hour)
		return inner(ctx, tok, u)
	}
	t.Cleanup(func() { teamsNowFn = prevNow })

	if r.syncPhoneLogs(outlook.TeamsKeyPstn, clock.Add(30*time.Minute)) {
		t.Fatal("a budget stop must leave the window open")
	}
	w := r.mem.TeamsPstn.Windows
	if len(w) != 1 || w[0].PageLink != base+"?$skip=1000" || r.mem.TeamsPstn.HighWater != nil || phoneRows(store) != 2 {
		t.Fatalf("page 1 stored, next page kept: rows %d state %+v", phoneRows(store), r.mem.TeamsPstn)
	}

	g.calls = nil
	if !r.syncPhoneLogs(outlook.TeamsKeyPstn, clock.Add(10*time.Hour)) {
		t.Fatalf("resumed window must complete: %+v", r.mem.TeamsPstn)
	}
	if len(g.calls) != 2 || !strings.HasSuffix(g.calls[0], "$skip=1000") || !strings.HasSuffix(g.calls[1], "$skip=2000") || len(g.unknown) != 0 {
		t.Fatalf("resume must start at the kept page: calls %v unknown %v", g.calls, g.unknown)
	}
	hw := r.mem.TeamsPstn.HighWater
	if len(r.mem.TeamsPstn.Windows) != 0 || hw == nil || !hw.Equal(r.to) || phoneRows(store) != 3 {
		t.Fatalf("window closed after its last page: rows %d state %+v", phoneRows(store), r.mem.TeamsPstn)
	}
}

// A failed later page keeps the rows already stored, records the stream failure and leaves the
// window open; the retry pages through again and closes it.
func TestTeamsPhoneLogsLaterPageFailureKeepsRowsAndWindowOpen(t *testing.T) {
	g := &fakeGraph{errs: map[string]error{"$skip=1000": &outlook.TeamsGraphError{Status: 503}}}
	r, store, base := teamsPhoneTest(t, g)
	g.pages = map[string]outlook.TeamsPage{
		"getPstnCalls(fromDateTime=": {Items: []json.RawMessage{phoneRow("r1"), phoneRow("r2")}, NextLink: base + "?$skip=1000"},
		"$skip=1000":                 {Items: []json.RawMessage{phoneRow("r3")}},
	}
	if r.syncPhoneLogs(outlook.TeamsKeyPstn, r.deadline) {
		t.Fatal("a failed later page must not complete the window")
	}
	streamKey := "pstn:stream:tenant:" + teamsTestTenant
	w := r.mem.TeamsPstn.Windows
	if len(w) != 1 || w[0].PageLink != "" || r.mem.TeamsPstn.HighWater != nil || phoneRows(store) != 2 {
		t.Fatalf("rows kept, window open from its start: rows %d state %+v", phoneRows(store), r.mem.TeamsPstn)
	}
	if _, ok := r.mem.TeamsFailures[streamKey]; !ok {
		t.Fatalf("stream failure must be recorded: %+v", r.mem.TeamsFailures)
	}
	if r.status.Features["teams_pstn_calls"].CapabilityStatus == outlook.TeamsStatusOK {
		t.Fatal("an interrupted listing must not report ok")
	}

	delete(g.errs, "$skip=1000")
	if !r.syncPhoneLogs(outlook.TeamsKeyPstn, r.deadline) {
		t.Fatalf("retry must complete: %+v", r.mem.TeamsPstn)
	}
	if len(r.mem.TeamsPstn.Windows) != 0 || phoneRows(store) != 3 || len(g.unknown) != 0 {
		t.Fatalf("rows %d state %+v unknown %v", phoneRows(store), r.mem.TeamsPstn, g.unknown)
	}
	if _, ok := r.mem.TeamsFailures[streamKey]; ok {
		t.Fatal("the stream failure must clear once the window pages through")
	}
}

// A repeated, foreign, other-window or other-route nextLink is never requested: the rows already
// read stay stored, the paging failure is recorded and the window is held for replay.
func TestTeamsPhoneLogsRejectRepeatedAndForeignContinuationLinks(t *testing.T) {
	const docBase = "https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2026-09-20,to=2026-10-10)"
	cases := map[string]string{
		"repeated":      docBase + "?$skip=1000",
		"foreign host":  "https://evil.example.com/v1.0/communications/callRecords/getPstnCalls(from=2026-09-20,to=2026-10-10)?$skip=2000",
		"other window":  "https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2026-09-19,to=2026-10-10)?$skip=2000",
		"other version": "https://graph.microsoft.com/beta/communications/callRecords/getPstnCalls(from=2026-09-20,to=2026-10-10)?$skip=2000",
		"other route":   "https://graph.microsoft.com/v1.0/communications/callRecords/getDirectRoutingCalls(from=2026-09-20,to=2026-10-10)?$skip=2000",
		"malformed":     "https://graph.microsoft.com/v1.0/communications/callRecords/getPstnCalls(from=2026-09-20,to)?$skip=2000",
		"fragment":      "https://graph.microsoft.com/v1.0/$metadata#communications/callRecords/getPstnCalls(from=2026-09-20,to=2026-10-10)?$skip=2000",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			g := &fakeGraph{}
			r, store, base := teamsPhoneTest(t, g)
			g.pages = map[string]outlook.TeamsPage{
				"getPstnCalls(fromDateTime=": {Items: []json.RawMessage{phoneRow("r1")}, NextLink: base + "?$skip=1000"},
				"$skip=1000":                 {Items: []json.RawMessage{phoneRow("r2")}, NextLink: bad},
			}
			if r.syncPhoneLogs(outlook.TeamsKeyPstn, r.deadline) {
				t.Fatal("a rejected nextLink must not complete the window")
			}
			if callsWith(g.calls, "$skip=1000") != 1 || callsWith(g.calls, "$skip=2000") != 0 || len(g.unknown) != 0 {
				t.Fatalf("rejected link requested: calls %v unknown %v", g.calls, g.unknown)
			}
			w := r.mem.TeamsPstn.Windows
			if len(w) != 1 || w[0].Held != 1 || w[0].PageLink != "" || r.mem.TeamsPstn.HighWater != nil || phoneRows(store) != 2 {
				t.Fatalf("rows kept, window held from its start: rows %d state %+v", phoneRows(store), r.mem.TeamsPstn)
			}
			f, ok := r.mem.TeamsFailures["pstn:stream:tenant:"+teamsTestTenant]
			if !ok || f.ErrorCode != outlook.TeamsStatusPaging {
				t.Fatalf("paging failure must be recorded: %+v", r.mem.TeamsFailures)
			}
		})
	}
}

// Transcript metadata pages through the documented delta links (unquoted users(id) key), keeps
// what it read when a later page fails, stores only a validated deltaLink and never requests a
// rejected or legacy foreign one. Transcript content stays not backed up.
func TestTeamsTranscriptListingPagesDeltaLinksAndResumes(t *testing.T) {
	const u = "ba321e0d-79ee-478d-8e28-85a19507f456"
	link := func(token string) string {
		return "https://graph.microsoft.com/v1.0/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=" + token
	}
	transcript := func(id, om string) json.RawMessage {
		return rawJSON(map[string]string{"id": id, "meetingId": om, "createdDateTime": "2026-10-01T00:00:00Z",
			"transcriptContentUrl": "https://graph.microsoft.com/v1.0/x/content"})
	}
	g := &fakeGraph{strict: true,
		pages: map[string]outlook.TeamsPage{
			"/users/" + u + "/onlineMeetings/": {Items: []json.RawMessage{transcript("tr-1", "om-1")}, NextLink: link("p2")},
			"skipToken=p2":                     {Items: []json.RawMessage{transcript("tr-2", "om-2")}, NextLink: link("p3")},
			"skipToken=p3":                     {DeltaLink: link("d1")},
		},
		errs: map[string]error{"skipToken=p3": &outlook.TeamsGraphError{Status: 503}},
	}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	st := &teamsUserState{OnlineMeetings: map[string]string{}}
	sync := func() string {
		return r.syncArtifactListing(u, st, "transcripts", r.deadline, func() error { return nil })
	}
	meta := func(om string) string {
		mid, _, _ := outlook.TeamsMeetingID("", u, "onlineMeeting:"+om, false)
		return string(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))])
	}
	streamKey := "transcripts_meta:stream:user:" + u

	if got := sync(); got == teamsComplete {
		t.Fatal("a failed later page must not complete the listing")
	}
	if !strings.Contains(meta("om-1"), "tr-1") || !strings.Contains(meta("om-2"), "tr-2") {
		t.Fatal("pages read before the failure must stay stored")
	}
	if st.Transcripts.DeltaLink != "" || st.Transcripts.PageLink != "" {
		t.Fatalf("no deltaLink before the last page: %+v", st.Transcripts)
	}
	if _, ok := r.mem.TeamsFailures[streamKey]; !ok {
		t.Fatal("stream failure must be recorded")
	}

	delete(g.errs, "skipToken=p3")
	g.calls = nil
	if got := sync(); got != teamsComplete {
		t.Fatalf("retry status %s", got)
	}
	if len(g.calls) != 3 || !strings.HasSuffix(g.calls[1], "skipToken=p2") || !strings.HasSuffix(g.calls[2], "skipToken=p3") {
		t.Fatalf("documented nextLinks must be followed unchanged: %v", g.calls)
	}
	if st.Transcripts.DeltaLink != link("d1") {
		t.Fatalf("deltaLink after the empty final page: %+v", st.Transcripts)
	}
	if _, ok := r.mem.TeamsFailures[streamKey]; ok {
		t.Fatal("stream failure must clear")
	}
	if m := meta("om-1"); strings.Contains(m, "transcriptContentUrl") || !strings.Contains(m, `"transcript_content_status":"`+outlook.TeamsStatusContentNotCollected+`"`) {
		t.Fatalf("transcript content must not be stored or reported: %s", m)
	}

	// The next round starts from the deltaLink; a foreign deltaLink is recorded, not stored.
	evil := "https://evil.example.com/v1.0/users(" + u + ")/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + u + "')/delta?skipToken=d2"
	g.pages["skipToken=d1"] = outlook.TeamsPage{Items: []json.RawMessage{transcript("tr-3", "om-3")}, DeltaLink: evil}
	g.calls = nil
	if got := sync(); got != teamsComplete {
		t.Fatalf("delta round status %s", got)
	}
	if len(g.calls) != 1 || g.calls[0] != link("d1") || !strings.Contains(meta("om-3"), "tr-3") {
		t.Fatalf("delta round: calls %v", g.calls)
	}
	f, ok := r.mem.TeamsFailures[streamKey]
	if st.Transcripts.DeltaLink != link("d1") || !ok || f.ErrorCode != outlook.TeamsStatusPaging {
		t.Fatalf("rejected deltaLink keeps the previous one and is recorded: %+v %+v", st.Transcripts, r.mem.TeamsFailures)
	}

	g.pages["skipToken=d1"] = outlook.TeamsPage{DeltaLink: link("d2")}
	if got := sync(); got != teamsComplete || st.Transcripts.DeltaLink != link("d2") {
		t.Fatalf("valid deltaLink must be stored: %s %+v", got, st.Transcripts)
	}
	if _, ok := r.mem.TeamsFailures[streamKey]; ok {
		t.Fatal("stream failure must clear")
	}

	// A stored deltaLink that doesn't continue this listing is dropped for a full listing.
	st.Transcripts.DeltaLink = evil
	g.calls = nil
	g.pages["/users/"+u+"/onlineMeetings/"] = outlook.TeamsPage{DeltaLink: link("d3")}
	if got := sync(); got != teamsComplete || st.Transcripts.DeltaLink != link("d3") {
		t.Fatalf("legacy deltaLink: %s %+v", got, st.Transcripts)
	}
	if callsWith(g.calls, "evil.example.com") != 0 || len(g.unknown) != 0 {
		t.Fatalf("foreign links must never be requested: calls %v unknown %v", g.calls, g.unknown)
	}
}

func between(s, a, b string) string {
	_, rest, _ := strings.Cut(s, a)
	v, _, _ := strings.Cut(rest, b)
	return v
}

func TestTeamsFileContractNeedsOwnerDriveAndExactKey(t *testing.T) {
	g := &fakeGraph{docs: map[string][]byte{
		"/shares/":        []byte(`{"id":"item-1","name":"a.docx","eTag":"et","parentReference":{"driveId":"d1","driveType":"business"}}`),
		"/drives/d1":      []byte(`{"id":"d1","owner":{"user":{"id":"ou"}}}`),
		"/users/ou/drive": []byte(`{"id":"d1"}`),
	}}
	g.install(t)
	dbs := newTeamsTestDB(t)
	owner, err := dbs.CronJobRepo.CreateMicrosoftResourceJob("u1", "ou@contoso.com", "outlook_onedrive", "daily", 1,
		repo.MicrosoftJobIdentity{TenantID: teamsTestTenant, ResourceType: repo.ResourceTypeUser, ResourceID: "ou"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs.DB.Model(owner).Updates(map[string]interface{}{"active": true, "storx_token": "grant"}).Error; err != nil {
		t.Fatal(err)
	}
	ownerPrefix := outlook.ResourceKeyPrefix(teamsTestTenant, repo.ResourceTypeUser, "ou")
	fileKey := outlook.OneDriveFileKey(ownerPrefix, outlook.OneDriveSectionMyDrive, nil, "item-1", "a.docx")
	statMeta := map[string]string{outlook.OneDriveMetaItemID: "item-1", outlook.OneDriveMetaETag: "bk"}
	prevK, prevS := teamsOwnerKeysFn, teamsOwnerStatFn
	teamsOwnerKeysFn = func(context.Context, ProcessorInput, string, string, string, string, string) (map[string]bool, error) {
		return map[string]bool{fileKey: true, ownerPrefix + "/.file_placeholder": true}, nil
	}
	teamsOwnerStatFn = func(context.Context, string, string, string) (map[string]string, error) { return statMeta, nil }
	t.Cleanup(func() { teamsOwnerKeysFn, teamsOwnerStatFn = prevK, prevS })

	r := newTestTeamsRun(t, newMemTeamsStore(), dbs, nil)
	head := teamsMessageHead{ID: "m1"}
	head.Attachments = append(head.Attachments, struct {
		ID          string          `json:"id"`
		ContentType string          `json:"contentType"`
		ContentURL  string          `json:"contentUrl"`
		Name        string          `json:"name"`
		Content     json.RawMessage `json:"content"`
	}{ID: "a", ContentType: "reference", ContentURL: "https://contoso-my.sharepoint.com/a.docx"})
	refs := r.messageFiles(head, "obj-key")
	if len(refs) != 1 || refs[0].BackupStatus != outlook.TeamsFileBackedUp || refs[0].BackupKey != fileKey || refs[0].OwnerJobID != owner.ID || refs[0].BackupETag != "bk" {
		t.Fatalf("verified file: %+v", refs)
	}

	statMeta = map[string]string{outlook.OneDriveMetaItemID: "other"}
	r.files = nil
	if refs := r.messageFiles(head, "obj-key"); refs[0].BackupStatus != outlook.TeamsFileKeyUnverified || refs[0].BackupKey != "" {
		t.Fatalf("metadata mismatch: %+v", refs[0])
	}

	g.docs["/users/ou/drive"] = []byte(`{"id":"d2"}`)
	r.files = nil
	if refs := r.messageFiles(head, "obj-key"); refs[0].BackupStatus != outlook.TeamsFileDriveNotBackedUp {
		t.Fatalf("drive mismatch: %+v", refs[0])
	}

	if err := dbs.DB.Model(owner).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	r.files = nil
	if refs := r.messageFiles(head, "obj-key"); refs[0].BackupStatus != outlook.TeamsFileOwnerNotBackedUp {
		t.Fatalf("no active owner job: %+v", refs[0])
	}
	r.saveFiles()
	var cov teamsFilesDoc
	if !r.loadState(outlook.TeamsFilesCoverageKey(r.tenantPrefix), &cov) || len(cov.MissingOwners) != 1 || cov.MissingOwners[0].ID != "ou" {
		t.Fatalf("files coverage: %+v", cov)
	}
}

func TestTeamsSharePointFileMatchesSiteJobByDriveAndLibrary(t *testing.T) {
	const site = "contoso.sharepoint.com,92ca94f9-db0d-49b1-9ad1-5da1838f0fa3,55c3a1a8-dfaa-4250-b32d-b46fef7eaffc"
	share := func(item, drive, siteID string) []byte {
		return []byte(`{"id":"` + item + `","name":"a.docx","eTag":"et","parentReference":{"driveId":"` + drive + `","driveType":"documentLibrary","siteId":"` + siteID + `"}}`)
	}
	g := &fakeGraph{docs: map[string][]byte{"/shares/": share("item-1", "d-default", "92ca94f9-db0d-49b1-9ad1-5da1838f0fa3")}}
	g.install(t)
	dbs := newTeamsTestDB(t)
	owner, err := dbs.CronJobRepo.CreateMicrosoftResourceJob("u1", "Site", "outlook_sharepoint", "daily", 1,
		repo.MicrosoftJobIdentity{TenantID: teamsTestTenant, ResourceType: repo.ResourceTypeSite, ResourceID: site},
		map[string]interface{}{"site_id": site, "drive_id": "d-default"})
	if err != nil {
		t.Fatal(err)
	}
	mem := repo.TaskMemory{SharePointLibraries: map[string]repo.SharePointLibraryState{
		"d-default": {Default: true}, "d-lib": {Name: "Docs"}, "d-gone": {RemovedAt: "2026-10-01T00:00:00Z"},
	}}
	if err := dbs.DB.Model(owner).Updates(map[string]interface{}{"active": true, "storx_token": "grant", "task_memory": mem}).Error; err != nil {
		t.Fatal(err)
	}
	sitePrefix := outlook.ResourceKeyPrefix(teamsTestTenant, repo.ResourceTypeSite, site)
	libPrefix := outlook.SharePointLibraryPrefix(sitePrefix, "d-lib")
	defaultKey := outlook.OneDriveFileKey(sitePrefix, outlook.OneDriveSectionMyDrive, nil, "item-1", "a.docx")
	libKey := outlook.OneDriveFileKey(libPrefix, outlook.OneDriveSectionMyDrive, nil, "item-2", "b.docx")
	statMeta := map[string]string{outlook.OneDriveMetaItemID: "item-1", outlook.OneDriveMetaDriveID: "d-default"}
	prevK, prevS := teamsOwnerKeysFn, teamsOwnerStatFn
	teamsOwnerKeysFn = func(context.Context, ProcessorInput, string, string, string, string, string) (map[string]bool, error) {
		return map[string]bool{defaultKey: true, libKey: true}, nil
	}
	teamsOwnerStatFn = func(context.Context, string, string, string) (map[string]string, error) { return statMeta, nil }
	t.Cleanup(func() { teamsOwnerKeysFn, teamsOwnerStatFn = prevK, prevS })

	r := newTestTeamsRun(t, newMemTeamsStore(), dbs, nil)
	head := teamsMessageHead{ID: "m1"}
	head.Attachments = append(head.Attachments, struct {
		ID          string          `json:"id"`
		ContentType string          `json:"contentType"`
		ContentURL  string          `json:"contentUrl"`
		Name        string          `json:"name"`
		Content     json.RawMessage `json:"content"`
	}{ID: "a", ContentType: "reference", ContentURL: "https://contoso.sharepoint.com/sites/x/a.docx"})
	check := func(name, want string) outlook.TeamsFileRef {
		t.Helper()
		r.files = nil
		refs := r.messageFiles(head, "obj-key")
		if len(refs) != 1 || refs[0].BackupStatus != want {
			t.Fatalf("%s: want %s, got %+v", name, want, refs)
		}
		return refs[0]
	}

	// Graph gives the bare site collection id; the job is keyed by the composite id.
	if ref := check("default library", outlook.TeamsFileBackedUp); ref.BackupKey != defaultKey || ref.OwnerJobID != owner.ID || ref.Owner != "sharepoint:"+site {
		t.Fatalf("default library: %+v", ref)
	}

	g.docs["/shares/"] = share("item-2", "d-lib", "92ca94f9-db0d-49b1-9ad1-5da1838f0fa3")
	statMeta = map[string]string{outlook.OneDriveMetaItemID: "item-2", outlook.OneDriveMetaDriveID: "d-lib"}
	if ref := check("other library", outlook.TeamsFileBackedUp); ref.BackupKey != libKey {
		t.Fatalf("other library: %+v", ref)
	}

	// The same item id in the default library is not the library's file.
	g.docs["/shares/"] = share("item-1", "d-lib", site)
	check("item of another library", outlook.TeamsFileNotBackedUp)

	g.docs["/shares/"] = share("item-3", "d-gone", "92ca94f9-db0d-49b1-9ad1-5da1838f0fa3")
	if ref := check("removed library", outlook.TeamsFileDriveNotBackedUp); ref.Owner != "sharepoint:"+site {
		t.Fatalf("removed library owner: %+v", ref)
	}

	g.docs["/shares/"] = share("item-4", "d-other", "0b468b65-b1fd-4eae-adc5-5e40e20aef01")
	check("site without a job", outlook.TeamsFileOwnerNotBackedUp)
	r.saveFiles()
	var cov teamsFilesDoc
	if !r.loadState(outlook.TeamsFilesCoverageKey(r.tenantPrefix), &cov) || len(cov.MissingOwners) != 1 || cov.MissingOwners[0].ID != "0b468b65-b1fd-4eae-adc5-5e40e20aef01" {
		t.Fatalf("files coverage: %+v", cov)
	}
}

func TestSharePointSiteGUID(t *testing.T) {
	for in, want := range map[string]string{
		"92CA94F9-db0d":                   "92ca94f9-db0d",
		"host.sharepoint.com,ABC-1,web-2": "abc-1",
		"host.sharepoint.com,ABC-1":       "",
		"":                                "",
	} {
		if got := sharePointSiteGUID(in); got != want {
			t.Errorf("sharePointSiteGUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTeamsFeatureWithoutRoleIsNotConsentedAndNeverCalled(t *testing.T) {
	g := &fakeGraph{}
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	claims, _ := json.Marshal(map[string]interface{}{"tid": teamsTestTenant, "roles": []string{"User.Read.All", "CallRecords.Read.All"}})
	r.token = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	r.loadConsent()
	if !r.runChatsPhase(r.deadline) || !r.runTeamsPhase(r.deadline) || !r.runMeetingsPhase(r.deadline) {
		t.Fatal("a phase without its role is skipped, not failed")
	}
	if len(g.calls) != 0 {
		t.Fatalf("no Graph call without the role: %v", g.calls)
	}
	for _, f := range []string{outlook.TeamsFeatureChats, outlook.TeamsFeatureChannelMessages, outlook.TeamsFeatureCalendar} {
		if got := r.status.Features[f]; got.CapabilityStatus != outlook.TeamsStatusNotConsented || got.CoverageStatus != "unavailable" {
			t.Fatalf("%s: %+v", f, got)
		}
	}
	if !r.consented(outlook.TeamsFeatureCallRecords) {
		t.Fatal("CallRecords.Read.All consents Teams call records")
	}
	for _, f := range []string{outlook.TeamsFeaturePSTN, outlook.TeamsFeatureDirectRouting} {
		if r.consented(f) {
			t.Fatalf("%s: CallRecords.Read.All does not cover PSTN/Direct Routing", f)
		}
	}
	if outlook.TeamsFeaturesNotConsented(outlook.TokenRoles("opaque")) != nil {
		t.Fatal("a token without roles must not block features")
	}
}

const teamsTestJoin = "https://teams.microsoft.com/l/meetup-join/19%3ameeting_"

// teamsAttendanceState queues ended organizer meetings for attendance: mid -> online meeting id
// ("" when it still has to be looked up by join URL).
func teamsAttendanceState(end time.Time, meetings map[string]string) *teamsUserState {
	st := &teamsUserState{OnlineMeetings: map[string]string{}, Attendance: map[string]teamsPendingAttendance{}}
	for mid, omid := range meetings {
		st.Attendance[mid] = teamsPendingAttendance{JoinURL: teamsTestJoin + mid, End: end, OnlineMeetingID: omid}
	}
	return st
}

func teamsAttendanceLookupURL(mid string) string {
	return outlook.TeamsOnlineMeetingByJoinURL("u1", teamsTestJoin+mid)
}

func teamsAttendanceListKey(omid string) string {
	return "/users/u1/onlineMeetings/" + omid + "/attendanceReports"
}

// teamsServeAttendance answers the lookup, report listing and report of each meeting.
func teamsServeAttendance(g *fakeGraph, meetings map[string]string) {
	if g.pages == nil {
		g.pages = map[string]outlook.TeamsPage{}
	}
	if g.docs == nil {
		g.docs = map[string][]byte{}
	}
	for mid, omid := range meetings {
		g.pages[teamsAttendanceLookupURL(mid)] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{"id": omid})}}
		g.pages[teamsAttendanceListKey(omid)] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{"id": "rep-" + omid})}}
		g.docs["/attendanceReports/rep-"+omid+"?$expand=attendanceRecords"] = rawJSON(map[string]interface{}{"id": "rep-" + omid, "attendanceRecords": []interface{}{}})
	}
}

func teamsAttendanceStored(store *memTeamsStore, mid, omid string) bool {
	_, ok := store.objs[outlook.TeamsMeetingAttendanceKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid), "rep-"+omid)]
	return ok
}

func teamsDelegatedToken(scp string) string {
	claims, _ := json.Marshal(map[string]interface{}{"tid": teamsTestTenant, "scp": scp})
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(claims) + ".sig"
}

// Each attendance request runs only with its own permission: without OnlineMeetingArtifact.Read.All
// (app role or delegated scope) nothing is requested; without OnlineMeetings.Read.All the reports
// of meetings already resolved are collected and the others wait. No failure entry is added.
func TestTeamsAttendanceSkipsRequestsWithoutTheirPermission(t *testing.T) {
	g := &fakeGraph{strict: true}
	teamsServeAttendance(g, map[string]string{"mid-b": "om-b"})
	delete(g.pages, teamsAttendanceLookupURL("mid-b"))
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": "", "mid-b": "om-b"})

	for _, tok := range []string{
		teamsRolesToken("User.Read.All", "Calendars.Read", "OnlineMeetings.Read.All"),
		teamsDelegatedToken("User.Read Calendars.Read OnlineMeetings.Read"),
	} {
		r.token = tok
		r.loadConsent()
		if !r.syncAttendance("u1", st, r.deadline) || len(g.calls) != 0 || len(r.mem.TeamsFailures) != 0 {
			t.Fatalf("no request without the reports permission: %v %+v", g.calls, r.mem.TeamsFailures)
		}
		if got := r.status.Features[outlook.TeamsFeatureAttendance]; got.CapabilityStatus != outlook.TeamsStatusNotConsented {
			t.Fatalf("attendance feature: %+v", got)
		}
	}

	r.token = teamsRolesToken("OnlineMeetingArtifact.Read.All")
	r.loadConsent()
	if !r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("attendance must complete")
	}
	if !teamsAttendanceStored(store, "mid-b", "om-b") || len(g.calls) != 2 || teamsCalled(g, "/onlineMeetings?") {
		t.Fatalf("only the resolved meeting's reports may be requested: %v", g.calls)
	}
	if st.Attendance["mid-a"].CheckedAt != nil || st.Attendance["mid-b"].CheckedAt == nil {
		t.Fatalf("unresolved meeting must stay pending: %+v", st.Attendance)
	}
	if got := r.status.Features[outlook.TeamsFeatureAttendance+"_lookup"]; got.CapabilityStatus != outlook.TeamsStatusNotConsented || got.Code != "missing permission OnlineMeetings.Read.All" {
		t.Fatalf("lookup feature: %+v", got)
	}
	if len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("failures %+v unknown %v", r.mem.TeamsFailures, g.unknown)
	}
}

// A 403 (here Graph's documented answer without an application access policy) holds for all the
// organizer's meetings: one request is refused, the request is backed off for the organizer for a
// day without any failure entry, and after the back-off a later run retries and recovers.
func TestTeamsAttendanceAccessDenialBacksOffTheOrganizerAndRecovers(t *testing.T) {
	clock := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	prevNow := teamsNowFn
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prevNow })
	forbidden := &outlook.TeamsGraphError{Status: 403, Code: "Forbidden", Message: "No application access policy found for this app"}
	g := &fakeGraph{strict: true, errs: map[string]error{"/users/u1/onlineMeetings?": forbidden}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.token = teamsRolesToken("OnlineMeetings.Read.All", "OnlineMeetingArtifact.Read.All")
	r.loadConsent()
	st := teamsAttendanceState(clock.Add(-2*time.Hour), map[string]string{"mid-a": "", "mid-b": "", "mid-c": ""})
	sync := func() bool { return r.syncAttendance("u1", st, clock.Add(time.Hour)) }

	if !sync() || len(g.calls) != 1 || len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("one refused lookup, no failures: %v %+v", g.calls, r.mem.TeamsFailures)
	}
	if st.Status["attendance_lookup"] != outlook.TeamsStatusMissingPermission ||
		r.status.Features[outlook.TeamsFeatureAttendance+"_lookup"].CapabilityStatus != outlook.TeamsStatusMissingPermission {
		t.Fatalf("lookup must be backed off and reported: %+v %+v", st.Status, r.status.Features)
	}
	if !sync() || len(g.calls) != 1 {
		t.Fatalf("no request during the back-off: %v", g.calls)
	}

	st.Attendance["mid-d"] = teamsPendingAttendance{JoinURL: teamsTestJoin + "mid-d", End: clock.Add(-2 * time.Hour), OnlineMeetingID: "om-d"}
	st.Attendance["mid-e"] = teamsPendingAttendance{JoinURL: teamsTestJoin + "mid-e", End: clock.Add(-2 * time.Hour), OnlineMeetingID: "om-e"}
	g.errs["/attendanceReports"] = forbidden
	if !sync() || len(g.calls) != 2 || len(r.mem.TeamsFailures) != 0 || st.Status["attendance"] != outlook.TeamsStatusMissingPermission {
		t.Fatalf("one refused report listing, no failures: %v %+v %+v", g.calls, r.mem.TeamsFailures, st.Status)
	}
	if !sync() || len(g.calls) != 2 {
		t.Fatalf("no request during the back-off: %v", g.calls)
	}

	clock = clock.Add(25 * time.Hour)
	g.errs = nil
	all := map[string]string{"mid-a": "om-a", "mid-b": "om-b", "mid-c": "om-c", "mid-d": "om-d", "mid-e": "om-e"}
	teamsServeAttendance(g, all)
	if !sync() {
		t.Fatal("recovery run must complete")
	}
	for mid, omid := range all {
		if !teamsAttendanceStored(store, mid, omid) || st.Attendance[mid].CheckedAt == nil {
			t.Fatalf("%s must be collected after the back-off: %v", mid, keysOf(store.objs))
		}
	}
	if len(st.SkipUntil) != 0 || len(st.Status) != 0 || len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("back-off must clear: %+v %+v %+v %v", st.SkipUntil, st.Status, r.mem.TeamsFailures, g.unknown)
	}
	if r.status.Features[outlook.TeamsFeatureAttendance].CapabilityStatus != outlook.TeamsStatusOK {
		t.Fatalf("attendance feature: %+v", r.status.Features[outlook.TeamsFeatureAttendance])
	}
}

// A report refused with 403 stops the organizer's attendance like a refused listing: no failure
// entry for the report, no further report requested.
func TestTeamsAttendanceRefusedReportAddsNoFailure(t *testing.T) {
	g := &fakeGraph{strict: true, errs: map[string]error{"/attendanceReports/rep-": &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}}}
	teamsServeAttendance(g, map[string]string{"mid-a": "om-a", "mid-b": "om-b"})
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": "om-a", "mid-b": "om-b"})
	if !r.syncAttendance("u1", st, r.deadline) || len(g.calls) != 2 || len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("listing and one refused report, no failures: %v %+v", g.calls, r.mem.TeamsFailures)
	}
	if st.Status["attendance"] != outlook.TeamsStatusMissingPermission || st.Attendance["mid-a"].CheckedAt != nil {
		t.Fatalf("organizer must be backed off with the meeting pending: %+v %+v", st.Status, st.Attendance)
	}
}

// Throttling, server errors and 404s (not documented as a capability answer on these endpoints)
// are item failures: the meeting stays pending, nothing is backed off, and the next run collects
// the reports and clears the failures.
func TestTeamsAttendanceTransientErrorsStayOnTheRetryPath(t *testing.T) {
	g := &fakeGraph{strict: true, errs: map[string]error{
		teamsAttendanceLookupURL("mid-a"): &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"},
		teamsAttendanceListKey("om-b"):    &outlook.TeamsGraphError{Status: 404, Code: "NotFound"},
		"/attendanceReports/rep-om-c?":    &outlook.TeamsGraphError{Status: 500, Code: "InternalServerError"},
		teamsAttendanceListKey("om-d"):    &outlook.TeamsGraphError{Status: 429, Code: "TooManyRequests"},
	}}
	teamsServeAttendance(g, map[string]string{"mid-c": "om-c"})
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": "", "mid-b": "om-b", "mid-c": "om-c", "mid-d": "om-d"})
	if r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("failed requests must leave attendance incomplete")
	}
	want := map[string]int{"attendance:item:u1:mid-a": 503, "attendance:item:u1:mid-b": 404, "attendance:item:u1:om-c:rep-om-c": 500, "attendance:item:u1:mid-d": 429}
	if len(r.mem.TeamsFailures) != len(want) {
		t.Fatalf("failures: %+v", r.mem.TeamsFailures)
	}
	for k, status := range want {
		if f, ok := r.mem.TeamsFailures[k]; !ok || f.HTTPStatus != status || f.Phase != "attendance" {
			t.Fatalf("%s: %+v", k, r.mem.TeamsFailures)
		}
	}
	if len(st.SkipUntil) != 0 {
		t.Fatalf("transient errors must not back off the organizer: %+v", st.SkipUntil)
	}
	for _, mid := range []string{"mid-a", "mid-b", "mid-c", "mid-d"} {
		if st.Attendance[mid].CheckedAt != nil {
			t.Fatalf("%s must stay pending: %+v", mid, st.Attendance[mid])
		}
	}

	g.errs = nil
	teamsServeAttendance(g, map[string]string{"mid-a": "om-a", "mid-b": "om-b", "mid-d": "om-d"})
	if !r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("retry run must complete")
	}
	for mid, omid := range map[string]string{"mid-a": "om-a", "mid-b": "om-b", "mid-c": "om-c", "mid-d": "om-d"} {
		if !teamsAttendanceStored(store, mid, omid) || st.Attendance[mid].CheckedAt == nil {
			t.Fatalf("%s must be collected on the retry: %v", mid, keysOf(store.objs))
		}
	}
	if len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("failures must clear: %+v %v", r.mem.TeamsFailures, g.unknown)
	}
}

// teamsOnlineMeetingJSON is an onlineMeeting as the join-URL lookup returns it.
func teamsOnlineMeetingJSON(omid, organizer, subject string, chatInfo map[string]string) json.RawMessage {
	m := map[string]interface{}{
		"id": omid, "subject": subject, "joinWebUrl": "https://teams.microsoft.com/l/meetup-join/x",
		"startDateTime": "2026-10-12T08:00:00Z", "endDateTime": "2026-10-12T09:00:00Z",
		"participants": map[string]interface{}{
			"organizer": map[string]interface{}{"upn": "org@contoso.com", "role": "presenter",
				"identity": map[string]interface{}{"user": map[string]string{"id": organizer, "tenantId": teamsTestTenant}}},
			"attendees": []interface{}{},
		},
		"joinMeetingIdSettings": map[string]interface{}{"isPasscodeRequired": true, "joinMeetingId": "1234567890", "passcode": "s3cret-pass"},
		"joinInformation":       map[string]string{"content": "Meeting ID 1234567890 Passcode s3cret-pass", "contentType": "html"},
	}
	if chatInfo != nil {
		m["chatInfo"] = chatInfo
	}
	return rawJSON(m)
}

func teamsMeetingDoc(t *testing.T, store *memTeamsStore, mid string) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	if err := json.Unmarshal(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))], &doc); err != nil {
		t.Fatalf("_meeting.json of %s: %v", mid, err)
	}
	return doc
}

func teamsOnlineMeetingKey(mid string) string {
	return outlook.TeamsMeetingOnlineMeetingKey(outlook.TeamsMeetingPrefix(teamsTestTenant, mid))
}

// The onlineMeeting returned by the attendance lookup is stored in the organizer's meeting folder
// without passcode or invite text; a meeting chat is linked by chatInfo.threadId, a channel
// meeting is not; an object owned by another organizer is not stored. Attendance and the
// transcript/recording statuses already in _meeting.json are kept.
func TestTeamsOnlineMeetingStoredWithOwnershipAndChatLink(t *testing.T) {
	const chatID = "19:meeting_NDRiZjRi@thread.v2"
	g := &fakeGraph{strict: true}
	teamsServeAttendance(g, map[string]string{"mid-a": "om-a", "mid-b": "om-b", "mid-c": "om-c"})
	g.pages[teamsAttendanceLookupURL("mid-a")] = outlook.TeamsPage{Items: []json.RawMessage{
		teamsOnlineMeetingJSON("om-a", "u1", "Weekly sync", map[string]string{"threadId": chatID, "messageId": "0"})}}
	g.pages[teamsAttendanceLookupURL("mid-b")] = outlook.TeamsPage{Items: []json.RawMessage{
		teamsOnlineMeetingJSON("om-b", "u1", "Channel meeting", map[string]string{"threadId": "19:chan@thread.skype", "messageId": "153367081"})}}
	g.pages[teamsAttendanceLookupURL("mid-c")] = outlook.TeamsPage{Items: []json.RawMessage{
		teamsOnlineMeetingJSON("om-c", "u2", "Someone else's", nil)}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefixA := outlook.TeamsMeetingPrefix(teamsTestTenant, "mid-a")
	if err := r.updateMeetingMeta(prefixA, func(d map[string]interface{}) {
		d["mid"], d["transcript_metadata_status"], d["transcripts"] = "mid-a", outlook.TeamsStatusOK, []interface{}{map[string]string{"id": "tr-1"}}
		teamsMeetingDefaults(d)
	}); err != nil {
		t.Fatal(err)
	}
	st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": "", "mid-b": "", "mid-c": ""})
	if !r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("attendance must complete")
	}
	if len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("failures %+v unknown %v", r.mem.TeamsFailures, g.unknown)
	}

	raw := string(store.objs[teamsOnlineMeetingKey("mid-a")])
	if !strings.Contains(raw, "Weekly sync") || !strings.Contains(raw, "1234567890") || !strings.Contains(raw, chatID) {
		t.Fatalf("online_meeting.json must keep the meeting metadata: %s", raw)
	}
	if strings.Contains(raw, "s3cret-pass") || strings.Contains(raw, "joinInformation") {
		t.Fatalf("passcode and invite text must not be stored: %s", raw)
	}
	if block, ok := outlook.ReadTeamsBackup(store.objs[teamsOnlineMeetingKey("mid-a")]); !ok || block.TenantID != teamsTestTenant || block.Source != "onlineMeetings" {
		t.Fatalf("backup block: %+v", block)
	}
	if k, ok := outlook.ParseTeamsObjectKey(teamsOnlineMeetingKey("mid-a")); !ok || k.Kind != outlook.TeamsKindOnlineMeeting || k.MeetingID != "mid-a" || outlook.IsTeamsRestoreKey(teamsOnlineMeetingKey("mid-a")) {
		t.Fatalf("key: %+v", k)
	}
	a := teamsMeetingDoc(t, store, "mid-a")
	if a["online_meeting_status"] != outlook.TeamsStatusOK || a["online_meeting_id"] != "om-a" || a["chat_link_status"] != outlook.TeamsStatusOK ||
		a["chat_id"] != chatID || a["chat_prefix"] != outlook.TeamsChatPrefix(teamsTestTenant, chatID) {
		t.Fatalf("meeting chat link: %v", a)
	}
	if a["attendance_status"] != outlook.TeamsStatusOK || a["transcript_metadata_status"] != outlook.TeamsStatusOK ||
		a["transcript_content_status"] != outlook.TeamsStatusContentNotCollected || len(a["transcripts"].([]interface{})) != 1 {
		t.Fatalf("existing statuses must be kept: %v", a)
	}

	b := teamsMeetingDoc(t, store, "mid-b")
	if _, ok := store.objs[teamsOnlineMeetingKey("mid-b")]; !ok || b["chat_link_status"] != "channel_meeting" || b["chat_id"] != nil {
		t.Fatalf("a channel meeting's thread is not a chat: %v", b)
	}

	c := teamsMeetingDoc(t, store, "mid-c")
	if _, ok := store.objs[teamsOnlineMeetingKey("mid-c")]; ok || c["online_meeting_status"] != "organizer_mismatch" || c["chat_id"] != nil {
		t.Fatalf("another organizer's meeting must not be stored: %v", c)
	}
	for mid, omid := range map[string]string{"mid-a": "om-a", "mid-b": "om-b", "mid-c": "om-c"} {
		if !teamsAttendanceStored(store, mid, omid) || st.Attendance[mid].CheckedAt == nil {
			t.Fatalf("attendance of %s must be kept", mid)
		}
	}
}

// A repeated lookup of an unchanged meeting rewrites nothing; a changed meeting is rewritten once.
func TestTeamsOnlineMeetingRepeatedLookupWritesOnlyChanges(t *testing.T) {
	g := &fakeGraph{strict: true}
	teamsServeAttendance(g, map[string]string{"mid-a": "om-a"})
	lookup := teamsAttendanceLookupURL("mid-a")
	g.pages[lookup] = outlook.TeamsPage{Items: []json.RawMessage{teamsOnlineMeetingJSON("om-a", "u1", "Weekly sync", nil)}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": ""})
	again := func() {
		p := st.Attendance["mid-a"]
		p.OnlineMeetingID, p.CheckedAt = "", nil
		st.Attendance["mid-a"] = p
		if !r.syncAttendance("u1", st, r.deadline) {
			t.Fatal("attendance must complete")
		}
	}
	again()
	key := teamsOnlineMeetingKey("mid-a")
	if store.puts[key] != 1 {
		t.Fatalf("first lookup writes once, puts %d", store.puts[key])
	}

	calls := len(g.calls)
	if !r.syncAttendance("u1", st, r.deadline) || len(g.calls) != calls {
		t.Fatalf("a resolved, checked meeting is not looked up again: %v", g.calls[calls:])
	}
	again()
	if store.puts[key] != 1 || !teamsCalled(g, "/onlineMeetings?") {
		t.Fatalf("unchanged meeting must not be rewritten, puts %d", store.puts[key])
	}

	g.pages[lookup] = outlook.TeamsPage{Items: []json.RawMessage{teamsOnlineMeetingJSON("om-a", "u1", "Renamed sync", nil)}}
	again()
	if store.puts[key] != 2 || !strings.Contains(string(store.objs[key]), "Renamed sync") {
		t.Fatalf("changed meeting must be rewritten once, puts %d", store.puts[key])
	}
	if doc := teamsMeetingDoc(t, store, "mid-a"); doc["chat_link_status"] != "not_provided" || doc["chat_id"] != nil {
		t.Fatalf("no chatInfo, no link: %v", doc)
	}
}

// A lookup that finds no meeting, or is refused, stores no object and records the status in
// _meeting.json without a failure entry.
func TestTeamsOnlineMeetingMissingOrInaccessible(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		g := &fakeGraph{strict: true, pages: map[string]outlook.TeamsPage{teamsAttendanceLookupURL("mid-a"): {}}}
		g.install(t)
		store := newMemTeamsStore()
		r := newTestTeamsRun(t, store, nil, nil)
		st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": ""})
		if !r.syncAttendance("u1", st, r.deadline) {
			t.Fatal("attendance must complete")
		}
		doc := teamsMeetingDoc(t, store, "mid-a")
		if _, ok := store.objs[teamsOnlineMeetingKey("mid-a")]; ok || doc["online_meeting_status"] != "not_found" || doc["attendance_status"] != "not_found" {
			t.Fatalf("not found: %v", doc)
		}
		if len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
			t.Fatalf("failures %+v unknown %v", r.mem.TeamsFailures, g.unknown)
		}
	})
	t.Run("refused", func(t *testing.T) {
		g := &fakeGraph{strict: true, errs: map[string]error{"/users/u1/onlineMeetings?": &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}}}
		g.install(t)
		store := newMemTeamsStore()
		r := newTestTeamsRun(t, store, nil, nil)
		st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": ""})
		if !r.syncAttendance("u1", st, r.deadline) {
			t.Fatal("a refusal backs off, it doesn't fail")
		}
		doc := teamsMeetingDoc(t, store, "mid-a")
		if _, ok := store.objs[teamsOnlineMeetingKey("mid-a")]; ok || doc["online_meeting_status"] != outlook.TeamsStatusMissingPermission {
			t.Fatalf("refused: %v", doc)
		}
		if len(r.mem.TeamsFailures) != 0 || st.Status["attendance_lookup"] != outlook.TeamsStatusMissingPermission {
			t.Fatalf("failures %+v status %+v", r.mem.TeamsFailures, st.Status)
		}
	})
}

// A failed lookup or a failed write of online_meeting.json leaves the meeting unresolved with an
// item failure; the next run looks it up again, stores it and collects the attendance.
func TestTeamsOnlineMeetingFailuresAreRetried(t *testing.T) {
	g := &fakeGraph{strict: true, errs: map[string]error{teamsAttendanceLookupURL("mid-a"): &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}}
	teamsServeAttendance(g, map[string]string{"mid-a": "om-a"})
	g.pages[teamsAttendanceLookupURL("mid-a")] = outlook.TeamsPage{Items: []json.RawMessage{
		teamsOnlineMeetingJSON("om-a", "u1", "Weekly sync", map[string]string{"threadId": "19:meeting_x@thread.v2", "messageId": "0"})}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	st := teamsAttendanceState(r.now().Add(-2*time.Hour), map[string]string{"mid-a": ""})
	failKey := "attendance:item:u1:mid-a"

	if r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("a failed lookup must leave attendance incomplete")
	}
	if _, ok := r.mem.TeamsFailures[failKey]; !ok || st.Attendance["mid-a"].OnlineMeetingID != "" {
		t.Fatalf("lookup failure: %+v %+v", r.mem.TeamsFailures, st.Attendance)
	}

	delete(g.errs, teamsAttendanceLookupURL("mid-a"))
	store.putErr = func(key string, _ int) error {
		if strings.HasSuffix(key, "/online_meeting.json") {
			return errors.New("write refused")
		}
		return nil
	}
	g.calls = nil
	if r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("a failed write must leave attendance incomplete")
	}
	f, ok := r.mem.TeamsFailures[failKey]
	if !ok || f.ItemIDs["source"] != "online_meeting" || st.Attendance["mid-a"].OnlineMeetingID != "" || st.Attendance["mid-a"].CheckedAt != nil {
		t.Fatalf("write failure keeps the meeting unresolved: %+v %+v", r.mem.TeamsFailures, st.Attendance)
	}
	if teamsCalled(g, "/attendanceReports") || teamsAttendanceStored(store, "mid-a", "om-a") {
		t.Fatalf("attendance waits for the meeting: %v", g.calls)
	}
	if _, ok := store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, "mid-a"))]; ok {
		if doc := teamsMeetingDoc(t, store, "mid-a"); doc["online_meeting_status"] == outlook.TeamsStatusOK {
			t.Fatalf("an unwritten meeting must not be reported ok: %v", doc)
		}
	}

	store.putErr = nil
	if !r.syncAttendance("u1", st, r.deadline) {
		t.Fatal("retry must complete")
	}
	if _, ok := store.objs[teamsOnlineMeetingKey("mid-a")]; !ok || !teamsAttendanceStored(store, "mid-a", "om-a") || len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("retry: failures %+v unknown %v keys %v", r.mem.TeamsFailures, g.unknown, keysOf(store.objs))
	}
	if doc := teamsMeetingDoc(t, store, "mid-a"); doc["online_meeting_status"] != outlook.TeamsStatusOK || doc["chat_id"] != "19:meeting_x@thread.v2" {
		t.Fatalf("retry: %v", doc)
	}
}

func TestTeamsRecordingArtifactCoverageStates(t *testing.T) {
	shareBody := []byte(`{"id":"item-1","name":"meeting.mp4","eTag":"et","parentReference":{"driveId":"d1","driveType":"business"}}`)
	g := &fakeGraph{
		pages: map[string]outlook.TeamsPage{},
		docs: map[string][]byte{
			"/shares/":        shareBody,
			"/drives/d1":      []byte(`{"id":"d1","owner":{"user":{"id":"ou"}}}`),
			"/users/ou/drive": []byte(`{"id":"d1"}`),
		},
	}
	g.install(t)
	dbs := newTeamsTestDB(t)
	owner, err := dbs.CronJobRepo.CreateMicrosoftResourceJob("u1", "OD", "outlook_onedrive", "daily", 1,
		repo.MicrosoftJobIdentity{TenantID: teamsTestTenant, ResourceType: repo.ResourceTypeUser, ResourceID: "ou"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs.DB.Model(owner).Updates(map[string]interface{}{"active": true, "storx_token": "grant"}).Error; err != nil {
		t.Fatal(err)
	}
	ownerPrefix := outlook.ResourceKeyPrefix(teamsTestTenant, repo.ResourceTypeUser, "ou")
	fileKey := outlook.OneDriveFileKey(ownerPrefix, outlook.OneDriveSectionMyDrive, nil, "item-1", "meeting.mp4")
	prevK, prevS := teamsOwnerKeysFn, teamsOwnerStatFn
	teamsOwnerKeysFn = func(context.Context, ProcessorInput, string, string, string, string, string) (map[string]bool, error) {
		return map[string]bool{fileKey: true}, nil
	}
	teamsOwnerStatFn = func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{outlook.OneDriveMetaItemID: "item-1", outlook.OneDriveMetaETag: "bk"}, nil
	}
	t.Cleanup(func() { teamsOwnerKeysFn, teamsOwnerStatFn = prevK, prevS })

	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, dbs, nil)
	st := &teamsUserState{OnlineMeetings: map[string]string{}}

	// Metadata only (Graph content URL) → unresolved, never backed_up.
	g.pages["getAllRecordings"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{
		"id": "rec-meta", "meetingId": "om-meta", "recordingContentUrl": "https://graph.microsoft.com/v1.0/users/u/onlineMeetings/m/recordings/r/content",
	})}}
	if got := r.syncArtifactListing("u1", st, "recordings", r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("meta-only status %s", got)
	}
	midMeta, _, _ := outlook.TeamsMeetingID("", "u1", "onlineMeeting:om-meta", false)
	metaRaw := string(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, midMeta))])
	if !strings.Contains(metaRaw, `"backup_status":"`+outlook.TeamsFileUnresolved+`"`) || strings.Contains(metaRaw, `"backup_status":"`+outlook.TeamsFileBackedUp+`"`) {
		t.Fatalf("unresolvable recording must stay unresolved: %s", metaRaw)
	}

	// Resolvable webUrl but OD/SP not backed up yet.
	g.pages["getAllRecordings"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{
		"id": "rec-pending", "meetingId": "om-pending", "webUrl": "https://contoso-my.sharepoint.com/personal/ou/Documents/pending.mp4",
	})}}
	g.docs["/shares/"] = []byte(`{"id":"item-pending","name":"pending.mp4","eTag":"et","parentReference":{"driveId":"d1","driveType":"business"}}`)
	teamsOwnerKeysFn = func(context.Context, ProcessorInput, string, string, string, string, string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}
	r.files = nil
	if got := r.syncArtifactListing("u1", st, "recordings", r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("pending status %s", got)
	}
	midPending, _, _ := outlook.TeamsMeetingID("", "u1", "onlineMeeting:om-pending", false)
	pendingRaw := string(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, midPending))])
	if !strings.Contains(pendingRaw, `"backup_status":"`+outlook.TeamsFileNotBackedUp+`"`) &&
		!strings.Contains(pendingRaw, `"backup_status":"`+outlook.TeamsFileKeyUnverified+`"`) {
		t.Fatalf("resolved but missing OD/SP backup: %s", pendingRaw)
	}

	// Resolvable and backed up by OneDrive.
	g.pages["getAllRecordings"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{
		"id": "rec-ok", "meetingId": "om-ok", "webUrl": "https://contoso-my.sharepoint.com/personal/ou/Documents/meeting.mp4",
	})}}
	g.docs["/shares/"] = shareBody
	teamsOwnerKeysFn = func(context.Context, ProcessorInput, string, string, string, string, string) (map[string]bool, error) {
		return map[string]bool{fileKey: true}, nil
	}
	r.files = nil
	if got := r.syncArtifactListing("u1", st, "recordings", r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("backed-up status %s", got)
	}
	midOK, _, _ := outlook.TeamsMeetingID("", "u1", "onlineMeeting:om-ok", false)
	okRaw := string(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, midOK))])
	if !strings.Contains(okRaw, `"backup_status":"`+outlook.TeamsFileBackedUp+`"`) || !strings.Contains(okRaw, fileKey) {
		t.Fatalf("backed up recording: %s", okRaw)
	}

	// @removed keeps entry with removed_at.
	g.pages["getAllRecordings"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]interface{}{
		"id": "rec-ok", "meetingId": "om-ok", "@removed": map[string]string{"reason": "deleted"},
	})}}
	r.files = nil
	if got := r.syncArtifactListing("u1", st, "recordings", r.deadline, func() error { return nil }); got != teamsComplete {
		t.Fatalf("removed status %s", got)
	}
	removedRaw := string(store.objs[outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(teamsTestTenant, midOK))])
	if !strings.Contains(removedRaw, `"removed_at"`) || !strings.Contains(removedRaw, "rec-ok") {
		t.Fatalf("removed artifact must keep body + removed_at: %s", removedRaw)
	}
}
