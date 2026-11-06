package crons

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

func qaMsg(id, replyTo, organizer, onlineMeetingID string, upvotes int, modified string) json.RawMessage {
	return rawJSON(map[string]interface{}{
		"@odata.type": "#microsoft.graph.engagementConversationQuestionMessage", "id": id, "replyToId": replyTo,
		"createdDateTime": "2025-01-01T09:00:00Z", "lastModifiedDateTime": modified,
		"body": map[string]string{"content": "question " + id, "contentType": "html"},
		"from": map[string]interface{}{"user": map[string]string{"id": "asker"}},
		"conversation": map[string]interface{}{
			"id": "conv-" + organizer, "starterId": "q1", "moderationState": "published", "upvoteCount": upvotes,
			"onlineMeetingId": onlineMeetingID, "organizer": map[string]interface{}{"user": map[string]string{"id": organizer}},
		},
	})
}

func enableTeamsQA(t *testing.T) {
	t.Helper()
	prev := teamsQAEnabled
	teamsQAEnabled = true
	t.Cleanup(func() { teamsQAEnabled = prev })
}

func TestTeamsMeetingQAOffMakesNoCalls(t *testing.T) {
	g := &fakeGraph{}
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	if !r.syncMeetingQA(r.deadline) || len(g.calls) != 0 {
		t.Fatalf("q&a is off: %v", g.calls)
	}
}

func qaReactionCalls(calls []string) int {
	n := 0
	for _, u := range calls {
		if strings.HasSuffix(u, "/reactions") {
			n++
		}
	}
	return n
}

func TestTeamsMeetingQAStoredUnderMeetingWithReactions(t *testing.T) {
	enableTeamsQA(t)
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{
		"/users?$select": teamsUsersPage("u1"),
		"getAllOnlineMeetingMessages": {Items: []json.RawMessage{
			qaMsg("q1", "", "u1", "om-1", 2, "2025-01-01T09:00:00Z"),
			qaMsg("a1", "q1", "u1", "om-1", 2, "2025-01-01T09:05:00Z"),
			qaMsg("x1", "", "outsider", "om-9", 0, "2025-01-01T09:00:00Z"),
		}},
		"/reactions": {Items: []json.RawMessage{rawJSON(map[string]interface{}{"id": "r1", "reactionType": "like", "reactionBy": map[string]interface{}{"user": map[string]string{"id": "u2"}}})}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	if !r.syncMeetingQA(r.deadline) {
		t.Fatal("q&a listing should complete")
	}
	mid, _, _ := outlook.TeamsMeetingID("", "u1", "onlineMeeting:om-1", false)
	prefix := outlook.TeamsMeetingPrefix(teamsTestTenant, mid)
	q1 := outlook.TeamsMeetingQAKey(prefix, "conv-u1", "q1")
	a1 := outlook.TeamsMeetingQAKey(prefix, "conv-u1", "a1")
	var doc struct {
		Reactions []map[string]interface{} `json:"reactions"`
	}
	if err := json.Unmarshal(store.objs[q1], &doc); err != nil || len(doc.Reactions) != 1 {
		t.Fatalf("question with reactions: %s", store.objs[q1])
	}
	if block, ok := outlook.ReadTeamsBackup(store.objs[a1]); !ok || block.Source != "getAllOnlineMeetingMessages" || block.ReplyToID != "q1" {
		t.Fatalf("answer backup block: %+v", block)
	}
	if store.meta[a1][outlook.TeamsMetaReplyToID] != "q1" || store.meta[q1][outlook.TeamsMetaReactions] == teamsQAReactionsPending {
		t.Fatalf("metadata: %v %v", store.meta[q1], store.meta[a1])
	}
	meta := string(store.objs[outlook.TeamsMeetingMetaKey(prefix)])
	if !strings.Contains(meta, `"qa_status":"ok"`) || !strings.Contains(meta, `"calendar_status":"not_found"`) || !strings.Contains(meta, `"online_meeting_id":"om-1"`) {
		t.Fatalf("_meeting.json: %s", meta)
	}
	for k := range store.objs {
		if strings.Contains(k, "conv-outsider") {
			t.Fatalf("q&a of an organizer outside the scope must be skipped: %s", k)
		}
	}
	if k, ok := outlook.ParseTeamsObjectKey(q1); !ok || k.Kind != outlook.TeamsKindMeetingQA || k.RootID != "conv-u1" || k.MessageID != "q1" || outlook.IsTeamsRestoreKey(q1) {
		t.Fatalf("parsed q&a key: %+v", k)
	}
	if r.status.Features[outlook.TeamsFeatureQA].CapabilityStatus != outlook.TeamsStatusOK {
		t.Fatalf("feature: %+v", r.status.Features)
	}

	// Within a day the listing doesn't run again.
	g.calls = nil
	if !r.syncMeetingQA(r.deadline) || len(g.calls) != 0 {
		t.Fatalf("daily listing ran again: %v", g.calls)
	}

	// A day later: an upvote change of the conversation rewrites its old messages, keeping their
	// stored reactions without asking for them again.
	var st teamsQAState
	r.loadState(outlook.TeamsQAStateKey(r.tenantPrefix), &st)
	old := r.now().Add(-25 * time.Hour)
	st.DoneAt = &old
	if err := r.saveState(outlook.TeamsQAStateKey(r.tenantPrefix), &st); err != nil {
		t.Fatal(err)
	}
	g.pages["getAllOnlineMeetingMessages"] = outlook.TeamsPage{Items: []json.RawMessage{
		qaMsg("q1", "", "u1", "om-1", 5, "2025-01-01T09:00:00Z"),
		qaMsg("a1", "q1", "u1", "om-1", 5, "2025-01-01T09:05:00Z"),
	}}
	g.calls = nil
	putsQ, putsA := store.puts[q1], store.puts[a1]
	if !r.syncMeetingQA(r.deadline) {
		t.Fatal("second listing should complete")
	}
	if store.puts[q1] != putsQ+1 || store.puts[a1] != putsA+1 {
		t.Fatalf("conversation change rewrites both messages of the conversation: q %d->%d a %d->%d", putsQ, store.puts[q1], putsA, store.puts[a1])
	}
	if qaReactionCalls(g.calls) != 0 {
		t.Fatalf("old unchanged messages keep their stored reactions: %v", g.calls)
	}
	if err := json.Unmarshal(store.objs[q1], &doc); err != nil || len(doc.Reactions) != 1 || store.meta[q1][outlook.TeamsMetaETag] != "published|5" {
		t.Fatalf("rewritten question: %s %v", store.objs[q1], store.meta[q1])
	}
}

func TestTeamsMeetingQAUsesOrganizerCalendarFolder(t *testing.T) {
	enableTeamsQA(t)
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{
		"/users?$select":              teamsUsersPage("u1"),
		"getAllOnlineMeetingMessages": {Items: []json.RawMessage{qaMsg("q1", "", "u1", "om-1", 0, "2025-01-01T09:00:00Z")}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	if err := r.saveState(outlook.TeamsUserStateKey(r.tenantPrefix, "u1"), &teamsUserState{OnlineMeetings: map[string]string{"om-1": "pcalendar"}}); err != nil {
		t.Fatal(err)
	}
	if !r.syncMeetingQA(r.deadline) {
		t.Fatal("q&a listing should complete")
	}
	key := outlook.TeamsMeetingQAKey(outlook.TeamsMeetingPrefix(teamsTestTenant, "pcalendar"), "conv-u1", "q1")
	if len(store.objs[key]) == 0 {
		t.Fatalf("q&a must go to the calendar meeting's folder; objects: %v", store.puts)
	}
}

func TestTeamsMeetingQAForbiddenIsBlockedNotFailed(t *testing.T) {
	enableTeamsQA(t)
	g := &fakeGraph{
		pages: map[string]outlook.TeamsPage{"/users?$select": teamsUsersPage("u1")},
		errs:  map[string]error{"getAllOnlineMeetingMessages": &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}},
	}
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	if !r.syncMeetingQA(r.deadline) {
		t.Fatal("a forbidden listing is blocked, not left unfinished")
	}
	var st teamsQAState
	r.loadState(outlook.TeamsQAStateKey(r.tenantPrefix), &st)
	if !st.skipped("qa", r.now()) || st.DoneAt != nil {
		t.Fatalf("listing must back off: %+v", st)
	}
	if got := r.status.Features[outlook.TeamsFeatureQA].CapabilityStatus; got == outlook.TeamsStatusOK || got == "" {
		t.Fatalf("feature: %+v", r.status.Features)
	}
}
