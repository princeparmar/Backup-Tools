package crons

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func teamsSkippedKeys(r *teamsRun) map[string]bool {
	out := map[string]bool{}
	for _, s := range r.status.SkippedItems {
		out[s.Key] = true
	}
	return out
}

// A call-record window held by a refused nextLink is replayed, never requests the link, and when
// released past the hold limit records the paging failure instead of advancing silently.
func TestTeamsCallRecordsRefusedLinkReleaseIsRecorded(t *testing.T) {
	g, r, _ := teamsCallsTest(t, "call-1")
	const evil = "https://evil.example.com/v1.0/communications/callRecords?$skiptoken=x"
	g.pages["/communications/callRecords?"] = outlook.TeamsPage{Items: []json.RawMessage{callListItem("call-1")}, NextLink: evil}

	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	prevNow := teamsNowFn
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prevNow })
	r.to = clock
	r.mem.TeamsCalls = &repo.TeamsWindowState{Windows: []repo.TeamsWindow{{From: clock.Add(-10 * 24 * time.Hour), To: clock}}}
	streamKey := "call_records:stream:tenant:" + teamsTestTenant

	released := false
	for run := 1; run <= teamsReleaseReplays; run++ {
		done := r.syncCallRecords(clock.Add(time.Hour))
		if run < teamsReleaseReplays {
			if done || len(r.mem.TeamsCalls.Windows) != 1 || r.mem.TeamsCalls.HighWater != nil {
				t.Fatalf("run %d: window must stay held: %+v", run, r.mem.TeamsCalls)
			}
			if _, ok := r.mem.TeamsFailures[streamKey]; !ok {
				t.Fatalf("run %d: refused link recorded on the stream: %+v", run, r.mem.TeamsFailures)
			}
		} else {
			released = done
		}
		clock = clock.Add(12 * time.Hour)
	}
	if !released || len(r.mem.TeamsCalls.Windows) != 0 || r.mem.TeamsCalls.HighWater == nil {
		t.Fatalf("window released after %d replays over 3 days: %+v", teamsReleaseReplays, r.mem.TeamsCalls)
	}
	if !teamsSkippedKeys(r)[streamKey] {
		t.Fatalf("released window must list the refused paging as skipped: %+v gaps %+v", r.status.SkippedItems, r.status.Gaps)
	}
	if callsWith(g.calls, "evil.example.com") != 0 || len(g.unknown) != 0 {
		t.Fatalf("refused link requested: calls %v unknown %v", g.calls, g.unknown)
	}
}

func teamsReleaseRun(t *testing.T) (*teamsRun, *time.Time) {
	t.Helper()
	(&fakeGraph{strict: true}).install(t)
	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	prevNow := teamsNowFn
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prevNow })
	return newTestTeamsRun(t, newMemTeamsStore(), nil, nil), &clock
}

// endRound releases the round's stream entry with its item entries, so a refused nextLink (which
// records only the stream) is listed as skipped on release.
func TestTeamsEndRoundReleasesStreamEntry(t *testing.T) {
	r, clock := teamsReleaseRun(t)
	stream := "chat_history:stream:user:u1"
	var dirty bool
	var held int
	var since *time.Time
	for i := 0; i < teamsReleaseReplays; i++ {
		dirty = true
		r.fail(stream, repo.TeamsFailure{Phase: "chat_history", Kind: teamsFailStream, ResourceType: "user", ResourceID: "u1"},
			outlook.ErrTeamsPaging, false)
		closed, gaps := r.endRound(teamsRound{&dirty, &held, &since, stream}, func() []string { return nil })
		if i < teamsReleaseReplays-1 && closed {
			t.Fatalf("replay %d must hold", i+1)
		}
		if i == teamsReleaseReplays-1 && (!closed || !gaps) {
			t.Fatal("hold limit reached: released with gaps")
		}
		*clock = clock.Add(12 * time.Hour)
	}
	if !teamsSkippedKeys(r)[stream] || len(r.status.Gaps) != 0 {
		t.Fatalf("stream entry listed as skipped: %+v gaps %+v", r.status.SkippedItems, r.status.Gaps)
	}
}

// A round held with no failure entry at all (entries past the cap, or a stream entry cleared by a
// later listing on the same stream) is recorded as a released_round gap when it is released.
func TestTeamsEndRoundReleaseWithoutEntryRecordsGap(t *testing.T) {
	r, clock := teamsReleaseRun(t)
	stream := "shared:stream:team:team-1"
	var dirty bool
	var held int
	var since *time.Time
	start := *clock
	for i := 0; i < teamsReleaseReplays; i++ {
		dirty = true
		if closed, _ := r.endRound(teamsRound{&dirty, &held, &since, stream}, func() []string { return []string{"shared:window:team:team-1"} }); closed != (i == teamsReleaseReplays-1) {
			t.Fatalf("replay %d: closed %v", i+1, closed)
		}
		*clock = clock.Add(12 * time.Hour)
	}
	if len(r.status.Gaps) != 1 {
		t.Fatalf("one gap for the release: %+v", r.status.Gaps)
	}
	gap := r.status.Gaps[0]
	if gap.Kind != "released_round" || gap.Resource != stream || gap.From != outlook.TeamsUTC(start) || !strings.HasPrefix(gap.To, "2026-10-16") {
		t.Fatalf("gap: %+v", gap)
	}
}
