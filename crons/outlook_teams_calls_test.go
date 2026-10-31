package crons

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	teamsCallsBase  = "https://graph.microsoft.com/v1.0/communications/callRecords"
	teamsCallsStart = "2026-10-09T08:00:00Z"
)

func callListItem(id string) json.RawMessage {
	return rawJSON(map[string]interface{}{"id": id, "version": 1, "lastModifiedDateTime": "2026-10-10T00:00:00Z", "startDateTime": teamsCallsStart})
}

func callDoc(id, organizer string) []byte {
	return []byte(`{"id":"` + id + `","organizer_v2":{"identity":{"user":{"id":"` + organizer + `"}}}}`)
}

func callParticipant(user string) json.RawMessage {
	return rawJSON(map[string]interface{}{"id": "p-" + user, "identity": map[string]interface{}{"user": map[string]string{"id": user}}})
}

func callKey(id string) string {
	return outlook.TeamsCallRecordKey(teamsTestTenant, id, teamsCallsStart)
}

func callPtr(user, id string) string {
	return outlook.TeamsUserCallPointerKey(outlook.TeamsUserPrefix(teamsTestTenant, user), id, teamsCallsStart)
}

type storedCall struct {
	Participants []json.RawMessage `json:"participants"`
	Sessions     []json.RawMessage `json:"sessions"`
}

func readCall(t *testing.T, store *memTeamsStore, id string) (storedCall, map[string]string) {
	t.Helper()
	raw, ok := store.objs[callKey(id)]
	if !ok {
		t.Fatalf("call record %s not stored", id)
	}
	var doc storedCall
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	block, _ := outlook.ReadTeamsBackup(raw)
	return doc, block.Completion
}

// teamsCallsTest is a strict fake with one listing page holding ids and an empty final list page.
func teamsCallsTest(t *testing.T, ids ...string) (*fakeGraph, *teamsRun, *memTeamsStore) {
	t.Helper()
	items := make([]json.RawMessage, 0, len(ids))
	g := &fakeGraph{strict: true, exact: map[string]outlook.TeamsPage{teamsCallsBase + "?$skiptoken=L2": {}}, docs: map[string][]byte{}}
	for _, id := range ids {
		items = append(items, callListItem(id))
		g.docs["/communications/callRecords/"+id] = callDoc(id, "u1")
		g.exact[outlook.TeamsCallParticipantsURL(id)] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u1")}}
		g.exact[outlook.TeamsCallSessionsURL(id)] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{"id": id + "-s1"})}}
	}
	g.pages = map[string]outlook.TeamsPage{"/communications/callRecords?": {Items: items, NextLink: teamsCallsBase + "?$skiptoken=L2"}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.userScope["u1"], r.userScope["u2"] = true, true
	return g, r, store
}

// Participant and session lists follow their documented @odata.nextLink pages through an empty
// final page; the record is stored once with every page and marked complete.
func TestTeamsCallRecordFollowsParticipantAndSessionPages(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1")
	pURL, sURL := outlook.TeamsCallParticipantsURL("call-1"), outlook.TeamsCallSessionsURL("call-1")
	g.exact[pURL] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u1")}, NextLink: teamsCallsBase + "/call-1/participants_v2?$skiptoken=p2"}
	g.exact[teamsCallsBase+"/call-1/participants_v2?$skiptoken=p2"] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u2")}, NextLink: teamsCallsBase + "/call-1/participants_v2?$skiptoken=p3"}
	g.exact[teamsCallsBase+"/call-1/participants_v2?$skiptoken=p3"] = outlook.TeamsPage{}
	g.exact[sURL] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{"id": "s1"})}, NextLink: teamsCallsBase + "/call-1/sessions?$expand=segments&$skiptoken=s2"}
	g.exact[teamsCallsBase+"/call-1/sessions?$expand=segments&$skiptoken=s2"] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]string{"id": "s2"})}}

	if !r.syncCallRecords(r.deadline) {
		t.Fatalf("calls must complete: %+v failures %+v", r.mem.TeamsCalls, r.mem.TeamsFailures)
	}
	doc, completion := readCall(t, store, "call-1")
	if len(doc.Participants) != 2 || len(doc.Sessions) != 2 {
		t.Fatalf("all pages stored: participants %d sessions %d", len(doc.Participants), len(doc.Sessions))
	}
	for part, v := range completion {
		if v != "complete" {
			t.Fatalf("%s: %s", part, v)
		}
	}
	if store.meta[callKey("call-1")][outlook.TeamsMetaComplete] != "true" || store.puts[callKey("call-1")] != 1 {
		t.Fatalf("one complete write: meta %v puts %d", store.meta[callKey("call-1")], store.puts[callKey("call-1")])
	}
	for _, u := range []string{"u1", "u2"} {
		if store.meta[callPtr(u, "call-1")][outlook.TeamsMetaPhysical] != callKey("call-1") {
			t.Fatalf("pointer for %s: %v", u, store.meta[callPtr(u, "call-1")])
		}
	}
	ws := r.mem.TeamsCalls
	if len(ws.Windows) != 0 || ws.HighWater == nil || len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("window closed after the empty final list page: %+v failures %+v unknown %v", ws, r.mem.TeamsFailures, g.unknown)
	}
}

// A repeated participant link (as in the Graph example), a sessions link in the documented
// $metadata# form and a segments nextLink are never requested; what was read is stored, the
// parts stay incomplete and the window is held for replay.
func TestTeamsCallRecordUnsupportedContinuationsStayIncomplete(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1", "call-2")
	pURL := outlook.TeamsCallParticipantsURL("call-1")
	g.exact[pURL] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u1")}, NextLink: pURL}
	g.exact[outlook.TeamsCallSessionsURL("call-1")] = outlook.TeamsPage{
		Items:    []json.RawMessage{rawJSON(map[string]string{"id": "s1"})},
		NextLink: "https://graph.microsoft.com/v1.0/$metadata#communications/callRecords('call-1')/sessions?$expand=segments&$skiptoken=abc",
	}
	g.exact[outlook.TeamsCallSessionsURL("call-2")] = outlook.TeamsPage{Items: []json.RawMessage{rawJSON(map[string]interface{}{
		"id": "s1", "segments": []map[string]string{{"id": "seg-1"}},
		"segments@odata.nextLink": "https://graph.microsoft.com/v1.0/$metadata#communications/callRecords('call-2')/sessions('s1')/segments?$skiptoken=x",
	})}}

	for run := 1; run <= 2; run++ {
		g.calls = nil
		if r.syncCallRecords(r.deadline) {
			t.Fatalf("run %d: incomplete records must hold the window", run)
		}
		for _, c := range g.calls {
			if strings.Contains(c, "$metadata") || strings.Contains(c, "skiptoken=abc") || strings.Contains(c, "/segments") {
				t.Fatalf("run %d: unsupported link requested: %s", run, c)
			}
		}
		if n := callsWith(g.calls, pURL); n != 1 {
			t.Fatalf("run %d: repeated participant link requested again (%d calls)", run, n)
		}
		if ws := r.mem.TeamsCalls; len(ws.Windows) != 1 || ws.Windows[0].Held != run || ws.HighWater != nil {
			t.Fatalf("run %d: window held: %+v", run, ws)
		}
	}
	doc, completion := readCall(t, store, "call-1")
	if completion["participants"] != "incomplete" || completion["sessions"] != "incomplete" || completion["segments"] != "incomplete" {
		t.Fatalf("call-1 completion: %+v", completion)
	}
	if len(doc.Participants) != 1 || len(doc.Sessions) != 1 {
		t.Fatalf("pages read before the refused link are kept: %+v", doc)
	}
	doc2, completion2 := readCall(t, store, "call-2")
	if completion2["participants"] != "complete" || completion2["sessions"] != "complete" || completion2["segments"] != "incomplete" {
		t.Fatalf("call-2 completion: %+v", completion2)
	}
	if len(doc2.Sessions) != 1 || !strings.Contains(string(doc2.Sessions[0]), "seg-1") {
		t.Fatalf("expanded segments kept: %+v", doc2)
	}
	for _, id := range []string{"call-1", "call-2"} {
		if store.meta[callKey(id)][outlook.TeamsMetaComplete] != "false" {
			t.Fatalf("%s must be marked incomplete: %v", id, store.meta[callKey(id)])
		}
		if _, ok := r.mem.TeamsFailures["call_details:item:"+id]; !ok {
			t.Fatalf("%s details failure kept for replay: %+v", id, r.mem.TeamsFailures)
		}
	}
	if len(g.unknown) != 0 {
		t.Fatalf("unknown requests: %v", g.unknown)
	}
}

// A participant page failing mid-list stores the partial record as failed; the replay reads every
// page, completes the record, adds the missing pointer and closes the window.
func TestTeamsCallRecordInterruptedDetailsReplay(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1")
	pURL := outlook.TeamsCallParticipantsURL("call-1")
	g.exact[pURL] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u1")}, NextLink: teamsCallsBase + "/call-1/participants_v2?$skiptoken=p2"}
	g.exact[teamsCallsBase+"/call-1/participants_v2?$skiptoken=p2"] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u2")}}
	g.errs = map[string]error{"skiptoken=p2": &outlook.TeamsGraphError{Status: 503}}

	if r.syncCallRecords(r.deadline) {
		t.Fatal("a failed participant page must hold the window")
	}
	if _, completion := readCall(t, store, "call-1"); completion["participants"] != "failed" {
		t.Fatalf("completion: %+v", completion)
	}
	if _, ok := store.meta[callPtr("u2", "call-1")]; ok {
		t.Fatal("no pointer for a participant that wasn't read")
	}

	delete(g.errs, "skiptoken=p2")
	if !r.syncCallRecords(r.deadline) {
		t.Fatalf("replay must complete: %+v failures %+v", r.mem.TeamsCalls, r.mem.TeamsFailures)
	}
	doc, completion := readCall(t, store, "call-1")
	if completion["participants"] != "complete" || len(doc.Participants) != 2 || store.meta[callKey("call-1")][outlook.TeamsMetaComplete] != "true" {
		t.Fatalf("replayed record: %+v %+v", completion, doc)
	}
	if store.meta[callPtr("u2", "call-1")][outlook.TeamsMetaPhysical] != callKey("call-1") {
		t.Fatal("replay adds the missing pointer")
	}
	if ws := r.mem.TeamsCalls; len(ws.Windows) != 0 || ws.HighWater == nil || len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("closed: %+v failures %+v unknown %v", ws, r.mem.TeamsFailures, g.unknown)
	}
}

// A budget stop after the first listing page keeps the next page link; the next run resumes from
// it without fetching the stored record again.
func TestTeamsCallRecordsListResumesAfterBudgetStop(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1", "call-2")
	g.pages["/communications/callRecords?"] = outlook.TeamsPage{Items: []json.RawMessage{callListItem("call-1")}, NextLink: teamsCallsBase + "?$skiptoken=L1"}
	g.exact[teamsCallsBase+"?$skiptoken=L1"] = outlook.TeamsPage{Items: []json.RawMessage{callListItem("call-2")}, NextLink: teamsCallsBase + "?$skiptoken=L2"}

	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	r.to = clock
	// Inside retention, so the resume isn't trimmed to the moving 30-day edge.
	r.mem.TeamsCalls = &repo.TeamsWindowState{Windows: []repo.TeamsWindow{{From: clock.Add(-10 * 24 * time.Hour), To: clock}}}
	prevNow, inner := teamsNowFn, teamsPageFn
	teamsNowFn = func() time.Time { return clock }
	teamsPageFn = func(ctx context.Context, tok, u string) (outlook.TeamsPage, error) {
		clock = clock.Add(time.Hour)
		return inner(ctx, tok, u)
	}
	t.Cleanup(func() { teamsNowFn = prevNow })

	// Listing page 1 plus call-1's participants and sessions take three hours.
	if r.syncCallRecords(clock.Add(150 * time.Minute)) {
		t.Fatal("a budget stop leaves the window open")
	}
	ws := r.mem.TeamsCalls
	if len(ws.Windows) != 1 || ws.Windows[0].PageLink != teamsCallsBase+"?$skiptoken=L1" || ws.HighWater != nil {
		t.Fatalf("next page kept: %+v", ws)
	}
	if _, ok := store.objs[callKey("call-1")]; !ok {
		t.Fatal("page 1 record stored before the stop")
	}
	if _, ok := store.objs[callKey("call-2")]; ok {
		t.Fatal("page 2 not read yet")
	}

	g.calls = nil
	if !r.syncCallRecords(clock.Add(24 * time.Hour)) {
		t.Fatalf("resume must complete: %+v failures %+v", r.mem.TeamsCalls, r.mem.TeamsFailures)
	}
	if !strings.HasSuffix(g.calls[0], "$skiptoken=L1") || callsWith(g.calls, "callRecords/call-1") != 0 {
		t.Fatalf("resume starts at the kept page and doesn't refetch call-1: %v", g.calls)
	}
	if _, ok := store.objs[callKey("call-2")]; !ok || store.puts[callKey("call-1")] != 1 {
		t.Fatalf("call-2 stored, call-1 written once (%d)", store.puts[callKey("call-1")])
	}
	if ws := r.mem.TeamsCalls; len(ws.Windows) != 0 || ws.HighWater == nil || len(g.unknown) != 0 {
		t.Fatalf("closed: %+v unknown %v", ws, g.unknown)
	}
}

// A refused record write keeps the records already stored; the replay stores the missing record
// and skips the stored one.
func TestTeamsCallRecordStorageFailureKeepsStoredRecords(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1", "call-2")
	store.putErr = func(key string, _ int) error {
		if key == callKey("call-2") {
			return errors.New("write refused")
		}
		return nil
	}
	if r.syncCallRecords(r.deadline) {
		t.Fatal("a refused write must hold the window")
	}
	if store.meta[callKey("call-1")][outlook.TeamsMetaComplete] != "true" {
		t.Fatal("call-1 stored")
	}
	if _, ok := store.objs[callKey("call-2")]; ok {
		t.Fatal("call-2 refused")
	}
	if _, ok := r.mem.TeamsFailures["call_records:item:call-2"]; !ok {
		t.Fatalf("item failure: %+v", r.mem.TeamsFailures)
	}

	store.putErr = nil
	g.calls = nil
	if !r.syncCallRecords(r.deadline) {
		t.Fatalf("replay must complete: %+v failures %+v", r.mem.TeamsCalls, r.mem.TeamsFailures)
	}
	if callsWith(g.calls, "callRecords/call-1") != 0 || store.puts[callKey("call-1")] != 1 {
		t.Fatalf("stored call-1 is skipped on replay: %v", g.calls)
	}
	if store.meta[callKey("call-2")][outlook.TeamsMetaComplete] != "true" || len(r.mem.TeamsFailures) != 0 || len(g.unknown) != 0 {
		t.Fatalf("call-2 stored on replay: failures %+v unknown %v", r.mem.TeamsFailures, g.unknown)
	}
}

// A refused pointer write leaves the record unwritten, so the replay (which skips complete stored
// records) still writes the pointer.
func TestTeamsCallRecordPointerFailureIsRetried(t *testing.T) {
	_, r, store := teamsCallsTest(t, "call-1")
	ptr := callPtr("u1", "call-1")
	store.putErr = func(key string, _ int) error {
		if key == ptr {
			return errors.New("write refused")
		}
		return nil
	}
	if r.syncCallRecords(r.deadline) {
		t.Fatal("a refused pointer must hold the window")
	}
	if _, ok := store.objs[callKey("call-1")]; ok {
		t.Fatal("record not written before its pointers")
	}
	store.putErr = nil
	if !r.syncCallRecords(r.deadline) {
		t.Fatalf("replay must complete: %+v", r.mem.TeamsFailures)
	}
	if store.meta[ptr][outlook.TeamsMetaPhysical] != callKey("call-1") || store.meta[callKey("call-1")][outlook.TeamsMetaComplete] != "true" {
		t.Fatalf("pointer %v record %v", store.meta[ptr], store.meta[callKey("call-1")])
	}
}

// With a user selection, a record whose participant list stopped early isn't dropped as out of
// scope: it's held and stored once every page is read.
func TestTeamsCallRecordScopeWaitsForAllParticipants(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1")
	r.sel.UserIDs = map[string]bool{"u2": true}
	r.userScope = map[string]bool{"u2": true}
	pURL := outlook.TeamsCallParticipantsURL("call-1")
	g.exact[pURL] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u1")}, NextLink: teamsCallsBase + "/call-1/participants_v2?$skiptoken=p2"}
	g.exact[teamsCallsBase+"/call-1/participants_v2?$skiptoken=p2"] = outlook.TeamsPage{Items: []json.RawMessage{callParticipant("u2")}}
	g.errs = map[string]error{"skiptoken=p2": &outlook.TeamsGraphError{Status: 503}}

	if r.syncCallRecords(r.deadline) {
		t.Fatal("unknown scope must hold the window")
	}
	if _, ok := store.objs[callKey("call-1")]; ok {
		t.Fatal("nothing stored while scope is unknown")
	}
	if _, ok := r.mem.TeamsFailures["call_details:item:call-1"]; !ok {
		t.Fatalf("held for replay: %+v", r.mem.TeamsFailures)
	}

	delete(g.errs, "skiptoken=p2")
	if !r.syncCallRecords(r.deadline) {
		t.Fatalf("replay must complete: %+v", r.mem.TeamsFailures)
	}
	if store.meta[callPtr("u2", "call-1")][outlook.TeamsMetaPhysical] != callKey("call-1") {
		t.Fatal("selected user's pointer written once the page is read")
	}
	if _, ok := store.meta[callPtr("u1", "call-1")]; ok {
		t.Fatal("no pointer for an unselected user")
	}
}

// The record's own participants list (Graph's documented fallback for an incomplete
// participants_v2) also places the record in a selected user's scope.
func TestTeamsCallRecordScopeUsesRecordParticipants(t *testing.T) {
	g, r, store := teamsCallsTest(t, "call-1")
	r.sel.UserIDs = map[string]bool{"u2": true}
	r.userScope = map[string]bool{"u2": true}
	g.docs["/communications/callRecords/call-1"] = []byte(`{"id":"call-1","organizer_v2":{"identity":{"user":{"id":"u1"}}},"participants":[{"user":{"id":"u2"}}]}`)

	if !r.syncCallRecords(r.deadline) {
		t.Fatalf("calls must complete: %+v", r.mem.TeamsFailures)
	}
	if store.meta[callPtr("u2", "call-1")][outlook.TeamsMetaPhysical] != callKey("call-1") {
		t.Fatal("record participants put the call in u2's scope")
	}
}

// A refused PSTN pointer write is retried on replay even though the row itself is already stored.
func TestTeamsPhonePointerFailureIsRetried(t *testing.T) {
	g := &fakeGraph{}
	r, store, _ := teamsPhoneTest(t, g)
	g.pages = map[string]outlook.TeamsPage{"getPstnCalls(fromDateTime=": {Items: []json.RawMessage{phoneRow("r1")}}}
	r.userScope["u9"] = true
	rowKey := teamsPhoneRowKey("r1", "call-r1", "u9", "2026-10-01T00:00:00Z", "out")
	row := outlook.TeamsPhoneLogKey(teamsTestTenant, outlook.TeamsKeyPstn, rowKey, "2026-10-01T00:00:00Z")
	ptr := outlook.TeamsUserCallPointerKey(outlook.TeamsUserPrefix(teamsTestTenant, "u9"), rowKey, "2026-10-01T00:00:00Z")
	store.putErr = func(key string, _ int) error {
		if key == ptr {
			return errors.New("write refused")
		}
		return nil
	}
	if r.syncPhoneLogs(outlook.TeamsKeyPstn, r.deadline) {
		t.Fatal("a refused pointer must hold the window")
	}
	if _, ok := store.objs[row]; !ok {
		t.Fatal("row stored")
	}
	store.putErr = nil
	if !r.syncPhoneLogs(outlook.TeamsKeyPstn, r.deadline) {
		t.Fatalf("replay must complete: %+v", r.mem.TeamsFailures)
	}
	if store.meta[ptr][outlook.TeamsMetaPhysical] != row || store.puts[row] != 1 || len(g.unknown) != 0 {
		t.Fatalf("pointer %v row puts %d unknown %v", store.meta[ptr], store.puts[row], g.unknown)
	}
}
