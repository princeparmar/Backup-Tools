package crons

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const pimTestPrefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/u1"

type memPIMStore struct {
	objects map[string][]byte
	puts    []string
	failPut func(key string) error
}

func (s *memPIMStore) get(_ context.Context, key string) ([]byte, error) {
	b, ok := s.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func (s *memPIMStore) put(_ context.Context, key string, data []byte) error {
	if s.failPut != nil {
		if err := s.failPut(key); err != nil {
			return err
		}
	}
	s.objects[key] = append([]byte(nil), data...)
	s.puts = append(s.puts, key)
	return nil
}

// stubPIMGraph serves list pages by URL; each URL maps to its pages in order via nextLink.
type stubPIMGraph struct {
	pages map[string][][]outlook.PIMItem
	fail  map[string]error
}

func (g *stubPIMGraph) list(_ context.Context, _ string, pageURL string) ([]outlook.PIMItem, string, error) {
	base, page := pageURL, 0
	if i := strings.Index(pageURL, "#page="); i >= 0 {
		base = pageURL[:i]
		page = int(pageURL[i+len("#page=")] - '0')
	}
	if err := g.fail[base]; err != nil {
		return nil, "", err
	}
	pages := g.pages[base]
	if page >= len(pages) {
		return nil, "", nil
	}
	next := ""
	if page+1 < len(pages) {
		next = base + "#page=" + string(rune('0'+page+1))
	}
	return pages[page], next, nil
}

func pimItem(id, changeKey string) outlook.PIMItem {
	raw, _ := json.Marshal(map[string]string{"id": id, "changeKey": changeKey, "subject": "s-" + id})
	return outlook.PIMItem{ID: id, ChangeKey: changeKey, Raw: raw}
}

func withPIMGraph(t *testing.T, g *stubPIMGraph) {
	t.Helper()
	prev := pimListPageFn
	pimListPageFn = g.list
	t.Cleanup(func() { pimListPageFn = prev })
}

func newTestPIMRun(store *memPIMStore) *pimRun {
	synced := map[string]bool{}
	for k := range store.objects {
		synced[k] = true
	}
	return &pimRun{
		store: store, accessToken: "tok", method: "outlook_calendar", synced: synced,
		heartbeat: func() error { return nil },
		now:       func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) },
	}
}

func readPIMIndex(t *testing.T, store *memPIMStore, key string) outlook.PIMIndex {
	t.Helper()
	var idx outlook.PIMIndex
	if err := json.Unmarshal(store.objects[key], &idx); err != nil {
		t.Fatalf("index %s: %v", key, err)
	}
	return idx
}

func TestPIMSyncFirstRunUploadsAllAcrossPages(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{
		"list": {{pimItem("e1", "k1")}, {pimItem("e2", "k1")}},
	}})
	store := &memPIMStore{objects: map[string][]byte{}}
	run := newTestPIMRun(store)

	if err := run.syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 2 {
		t.Fatalf("uploaded = %d", run.uploaded)
	}
	if !strings.Contains(string(store.objects[dir+"e1.json"]), `"subject":"s-e1"`) {
		t.Fatalf("e1 body = %s", store.objects[dir+"e1.json"])
	}
	idx := readPIMIndex(t, store, dir+outlook.PIMIndexName)
	if idx.Items["e2"].Key != dir+"e2.json" || idx.Items["e2"].ChangeKey != "k1" {
		t.Fatalf("index = %+v", idx)
	}
}

func TestPIMSyncSkipsUnchangedAndUploadsChanged(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	g := &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{pimItem("e1", "k1"), pimItem("e2", "k1")}}}}
	withPIMGraph(t, g)
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}

	g.pages["list"] = [][]outlook.PIMItem{{pimItem("e1", "k1"), pimItem("e2", "k2")}}
	store.puts = nil
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if run.unchanged != 1 || run.uploaded != 1 {
		t.Fatalf("unchanged=%d uploaded=%d", run.unchanged, run.uploaded)
	}
	if strings.Join(store.puts, ",") != dir+"e2.json,"+dir+outlook.PIMIndexName {
		t.Fatalf("puts = %v", store.puts)
	}
}

func TestPIMSyncNoChangesWritesNothing(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{pimItem("e1", "k1")}}}})
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	store.puts = nil
	if err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 0 {
		t.Fatalf("puts = %v", store.puts)
	}
}

func TestPIMSyncMarksRemovedAndKeepsBackup(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	g := &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{pimItem("e1", "k1"), pimItem("e2", "k1")}}}}
	withPIMGraph(t, g)
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}

	g.pages["list"] = [][]outlook.PIMItem{{pimItem("e1", "k1")}}
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if run.removed != 1 {
		t.Fatalf("removed = %d", run.removed)
	}
	if _, kept := store.objects[dir+"e2.json"]; !kept {
		t.Fatal("removed item's backup was deleted")
	}
	if got := readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["e2"].RemovedAt; got != "2026-10-07T00:00:00Z" {
		t.Fatalf("removed_at = %q", got)
	}

	g.pages["list"] = [][]outlook.PIMItem{{pimItem("e1", "k1"), pimItem("e2", "k1")}}
	run = newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 1 || readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["e2"].RemovedAt != "" {
		t.Fatalf("reappearing item not backed up again: uploaded=%d", run.uploaded)
	}
}

func TestPIMSyncMigratesLegacyObjects(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{pimItem("e1", "k1")}}}})
	store := &memPIMStore{objects: map[string][]byte{dir + "e1.json": []byte(`{"id":"e1","subject":"old flat"}`)}}
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 1 || !strings.Contains(string(store.objects[dir+"e1.json"]), `"changeKey":"k1"`) {
		t.Fatalf("legacy object not replaced: %s", store.objects[dir+"e1.json"])
	}
}

func TestPIMSyncStorageFullAbortsAndKeepsProgress(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{pimItem("e1", "k1"), pimItem("e2", "k1")}}}})
	full := false
	store := &memPIMStore{objects: map[string][]byte{}, failPut: func(key string) error {
		if strings.HasSuffix(key, "e2.json") {
			full = true
		}
		if full && !strings.HasSuffix(key, outlook.PIMIndexName) {
			return &quota.ErrStorageQuota{Method: "outlook_calendar"}
		}
		return nil
	}}
	err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir})
	if !quota.IsStorageQuota(err) {
		t.Fatalf("err = %v, want storage quota", err)
	}
	idx := readPIMIndex(t, store, dir+outlook.PIMIndexName)
	if _, ok := idx.Items["e1"]; !ok {
		t.Fatal("index of items backed up before the limit was not saved")
	}
	if _, ok := idx.Items["e2"]; ok {
		t.Fatal("index claims an item that was not uploaded")
	}
}

func TestSyncOutlookCalendarsStopsAtStorageFull(t *testing.T) {
	userBase := "https://g/users/u1"
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{
		outlook.PIMEventsURL(userBase, "a"): {{pimItem("e1", "k1")}},
		outlook.PIMEventsURL(userBase, "b"): {{pimItem("e2", "k1")}},
	}})
	withCalendars(t, outlook.FlatCalendar{ID: "a", Name: "A", IsDefault: true}, outlook.FlatCalendar{ID: "b", Name: "B"})
	dirB := outlook.PIMCalendarDir(pimTestPrefix, "b")
	store := &memPIMStore{objects: map[string][]byte{dirB + outlook.PIMCalendarMetaName: []byte(`{"id":"b","name":"B"}`)},
		failPut: func(key string) error {
			if strings.HasSuffix(key, "e1.json") {
				return &quota.ErrStorageQuota{Method: "outlook_calendar"}
			}
			return nil
		}}
	err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix)
	if !quota.IsStorageQuota(err) {
		t.Fatalf("err = %v, want storage quota", err)
	}
	if _, ok := store.objects[dirB+"e2.json"]; ok {
		t.Fatal("backup went on after storage was full")
	}
}

func seriesItem(id, changeKey string) outlook.PIMItem {
	it := pimItem(id, changeKey)
	it.SeriesMaster = true
	return it
}

// seriesGraph stubs the series reads of a calendar and records their URLs.
type seriesGraph struct {
	occurrenceURLs, instanceURLs   []string
	failOccurrences, failInstances bool
}

func withSeries(t *testing.T) *seriesGraph {
	t.Helper()
	g := &seriesGraph{}
	prevSeries, prevInstances := pimSeriesFn, pimInstancesFn
	pimSeriesFn = func(_ context.Context, _, seriesURL string, raw []byte) ([]byte, error) {
		g.occurrenceURLs = append(g.occurrenceURLs, seriesURL)
		if g.failOccurrences {
			return nil, errors.New("http 503")
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["cancelledOccurrences"] = []string{"OID.s1.2026-10-07"}
		return json.Marshal(m)
	}
	pimInstancesFn = func(_ context.Context, _, instancesURL string) ([][2]string, error) {
		g.instanceURLs = append(g.instanceURLs, instancesURL)
		if g.failInstances {
			return nil, errors.New("http 503")
		}
		return [][2]string{{"2026-10-08T03:30:00Z", "2026-10-08T04:00:00Z"}}, nil
	}
	t.Cleanup(func() { pimSeriesFn, pimInstancesFn = prevSeries, prevInstances })
	return g
}

const pimTestUserBase = "https://g/users/u1"

func calendarCollection(dir string) pimCollection {
	return pimCollection{listURL: "list", dir: dir, userBase: pimTestUserBase, calendarID: "cal1"}
}

func eventItem(id, changeKey string) outlook.PIMItem {
	raw, _ := json.Marshal(map[string]any{"id": id, "changeKey": changeKey, "subject": "Review", "isAllDay": false,
		"start":    map[string]string{"dateTime": "2026-10-05T03:30:00.0000000", "timeZone": "UTC"},
		"end":      map[string]string{"dateTime": "2026-10-05T04:00:00.0000000", "timeZone": "UTC"},
		"location": map[string]string{"displayName": "Room 1"}})
	return outlook.PIMItem{ID: id, ChangeKey: changeKey, Raw: raw}
}

func TestPIMSyncStoresSeriesWithOccurrences(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{seriesItem("s1", "k1"), pimItem("e1", "k1")}}}})
	g := withSeries(t)
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.occurrenceURLs, ",") != outlook.PIMSeriesURL(pimTestUserBase, "cal1", "s1") {
		t.Fatalf("series reads = %v", g.occurrenceURLs)
	}
	if !strings.Contains(string(store.objects[dir+"s1.json"]), "cancelledOccurrences") {
		t.Fatalf("series stored without occurrences: %s", store.objects[dir+"s1.json"])
	}
	s1 := readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["s1"]
	if !s1.Occurrences || s1.Summary == nil || len(s1.Summary.Instances) != 1 ||
		s1.Summary.From != "2025-10-07T00:00:00Z" || s1.Summary.Until != "2028-04-07T00:00:00Z" {
		t.Fatalf("index entry = %+v summary = %+v", s1, s1.Summary)
	}
	if len(g.instanceURLs) != 1 || !strings.Contains(g.instanceURLs[0], "/calendars/cal1/events/s1/instances?") {
		t.Fatalf("instance reads = %v", g.instanceURLs)
	}

	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	if run.unchanged != 2 || len(g.occurrenceURLs) != 1 || len(g.instanceURLs) != 1 {
		t.Fatalf("unchanged series read again: unchanged=%d reads=%v %v", run.unchanged, g.occurrenceURLs, g.instanceURLs)
	}
}

func TestPIMSyncBacksUpOccurrencesOfSeriesStoredBefore(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{seriesItem("s1", "k1")}}}})
	withSeries(t)
	index, _ := json.Marshal(outlook.PIMIndex{Items: map[string]outlook.PIMIndexEntry{"s1": {Key: dir + "s1.json", ChangeKey: "k1"}}})
	store := &memPIMStore{objects: map[string][]byte{dir + "s1.json": []byte(`{"id":"s1"}`), dir + outlook.PIMIndexName: index}}
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 1 || !strings.Contains(string(store.objects[dir+"s1.json"]), "cancelledOccurrences") {
		t.Fatalf("series stored before occurrence backup not updated: %s", store.objects[dir+"s1.json"])
	}
}

func TestPIMSyncSeriesReadFailureRetriedNextRun(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{seriesItem("s1", "k1")}}}})
	g := withSeries(t)
	g.failOccurrences, g.failInstances = true, true
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	s1 := readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["s1"]
	if _, ok := store.objects[dir+"s1.json"]; !ok || s1.Occurrences || s1.Summary == nil || s1.Summary.Instances != nil {
		t.Fatal("series must be stored without occurrences and marked for a new read")
	}
	g.failOccurrences, g.failInstances = false, false
	if err := newTestPIMRun(store).syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	s1 = readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["s1"]
	if !s1.Occurrences || len(s1.Summary.Instances) != 1 {
		t.Fatal("occurrences not read on the next run")
	}
}

func TestPIMSyncContactsNeverReadSeriesOrSummaries(t *testing.T) {
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{seriesItem("c1", "k1")}}}})
	g := withSeries(t)
	store := &memPIMStore{objects: map[string][]byte{}}
	dir := pimTestPrefix + "/"
	if err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if len(g.occurrenceURLs)+len(g.instanceURLs) != 0 || readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["c1"].Summary != nil {
		t.Fatalf("contacts treated as calendar events: %+v", g)
	}
}

func TestPIMSyncCalendarIndexHasEventSummaries(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{eventItem("e1", "k1")}}}})
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	idx := readPIMIndex(t, store, dir+outlook.PIMIndexName)
	s := idx.Items["e1"].Summary
	if idx.V != outlook.PIMIndexVersion || s == nil || s.Subject != "Review" || s.Start != "2026-10-05T03:30:00Z" ||
		s.End != "2026-10-05T04:00:00Z" || s.Location != "Room 1" || s.Instances != nil {
		t.Fatalf("index = %+v summary = %+v", idx, s)
	}
}

func TestPIMSyncAddsSummariesToOldIndexWithoutUploading(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{eventItem("e1", "k1")}}}})
	index, _ := json.Marshal(outlook.PIMIndex{Items: map[string]outlook.PIMIndexEntry{"e1": {Key: dir + "e1.json", ChangeKey: "k1"}}})
	store := &memPIMStore{objects: map[string][]byte{dir + "e1.json": []byte(`{"id":"e1"}`), dir + outlook.PIMIndexName: index}}
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 0 || strings.Join(store.puts, ",") != dir+outlook.PIMIndexName {
		t.Fatalf("uploaded=%d puts=%v", run.uploaded, store.puts)
	}
	if readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["e1"].Summary == nil {
		t.Fatal("old index entry got no summary")
	}
}

func TestPIMSyncRefreshesSeriesOccurrencesBeforeWindowEnds(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{seriesItem("s1", "k1")}}}})
	g := withSeries(t)
	entry := outlook.PIMIndexEntry{Key: dir + "s1.json", ChangeKey: "k1", Occurrences: true,
		Summary: &outlook.EventSummary{Until: "2027-01-15T00:00:00Z"}}
	index, _ := json.Marshal(outlook.PIMIndex{Items: map[string]outlook.PIMIndexEntry{"s1": entry}})
	store := &memPIMStore{objects: map[string][]byte{dir + "s1.json": []byte(`{"id":"s1"}`), dir + outlook.PIMIndexName: index}}
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), calendarCollection(dir)); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 0 || len(g.occurrenceURLs) != 0 || len(g.instanceURLs) != 1 {
		t.Fatalf("uploaded=%d reads=%v %v", run.uploaded, g.occurrenceURLs, g.instanceURLs)
	}
	if got := readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["s1"].Summary.Until; got != "2028-04-07T00:00:00Z" {
		t.Fatalf("until = %q", got)
	}
}

func withCalendars(t *testing.T, calendars ...outlook.FlatCalendar) {
	t.Helper()
	prev := pimCalendarsFn
	pimCalendarsFn = func(context.Context, string, string) ([]outlook.FlatCalendar, error) { return calendars, nil }
	t.Cleanup(func() { pimCalendarsFn = prev })
	withCalendarGroupCals(t) // default: no extra calendarGroup entries
	withGroupCalendars(t)    // default: no M365 Groups calendars unless a test overrides
}

func withCalendarGroupCals(t *testing.T, calendars ...outlook.FlatCalendar) {
	t.Helper()
	prev := pimCalendarGroupsCalsFn
	pimCalendarGroupsCalsFn = func(context.Context, string, string) ([]outlook.FlatCalendar, error) { return calendars, nil }
	t.Cleanup(func() { pimCalendarGroupsCalsFn = prev })
}

func withGroupCalendars(t *testing.T, calendars ...outlook.FlatCalendar) {
	t.Helper()
	prev := pimGroupCalendarsFn
	pimGroupCalendarsFn = func(context.Context, string, string) ([]outlook.FlatCalendar, error) { return calendars, nil }
	t.Cleanup(func() { pimGroupCalendarsFn = prev })
}

func readCalendarMeta(t *testing.T, store *memPIMStore, calendarID string) outlook.FlatCalendar {
	t.Helper()
	var cal outlook.FlatCalendar
	if err := json.Unmarshal(store.objects[outlook.PIMCalendarDir(pimTestPrefix, calendarID)+outlook.PIMCalendarMetaName], &cal); err != nil {
		t.Fatalf("calendar %s meta: %v", calendarID, err)
	}
	return cal
}

func TestSyncOutlookCalendarsMarksDeletedCalendar(t *testing.T) {
	userBase := "https://g/users/u1"
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{
		outlook.PIMEventsURL(userBase, "main"): {{pimItem("e1", "k1")}},
		outlook.PIMEventsURL(userBase, "trip"): {{pimItem("e2", "k1")}},
	}})
	main := outlook.FlatCalendar{ID: "main", Name: "Calendar", IsDefault: true}
	withCalendars(t, main, outlook.FlatCalendar{ID: "trip", Name: "Trip"})
	store := &memPIMStore{objects: map[string][]byte{}}
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err != nil {
		t.Fatal(err)
	}

	withCalendars(t, main)
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	trip := readCalendarMeta(t, store, "trip")
	if trip.RemovedAt != "2026-10-07T00:00:00Z" || trip.Name != "Trip" || trip.ID != "trip" {
		t.Fatalf("deleted calendar meta = %+v", trip)
	}
	tripDir := outlook.PIMCalendarDir(pimTestPrefix, "trip")
	if _, kept := store.objects[tripDir+"e2.json"]; !kept {
		t.Fatal("events of the deleted calendar were deleted")
	}
	if readPIMIndex(t, store, tripDir+outlook.PIMIndexName).Items["e2"].RemovedAt == "" {
		t.Fatal("events of the deleted calendar not marked removed")
	}
	if readCalendarMeta(t, store, "main").RemovedAt != "" {
		t.Fatal("listed calendar marked removed")
	}

	withCalendars(t, main, outlook.FlatCalendar{ID: "trip", Name: "Trip"})
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	if readCalendarMeta(t, store, "trip").RemovedAt != "" {
		t.Fatal("calendar listed again still marked removed")
	}
}

func TestSyncOutlookCalendarsDoesNotMarkOnUntrustedListing(t *testing.T) {
	userBase := "https://g/users/u1"
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{
		outlook.PIMEventsURL(userBase, "other"): {{pimItem("e1", "k1")}},
	}})
	tripMeta := outlook.PIMCalendarDir(pimTestPrefix, "trip") + outlook.PIMCalendarMetaName
	store := &memPIMStore{objects: map[string][]byte{tripMeta: []byte(`{"id":"trip","name":"Trip"}`)}}

	withCalendars(t, outlook.FlatCalendar{ID: "other", Name: "Other"})
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	prev := pimCalendarsFn
	pimCalendarsFn = func(context.Context, string, string) ([]outlook.FlatCalendar, error) {
		return nil, errors.New("http 503")
	}
	t.Cleanup(func() { pimCalendarsFn = prev })
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err == nil {
		t.Fatal("failed calendar listing must fail the job")
	}
	if readCalendarMeta(t, store, "trip").RemovedAt != "" {
		t.Fatal("calendar marked removed without a complete listing")
	}
}

func TestPIMSyncItemFailureRetriedNextRun(t *testing.T) {
	dir := outlook.PIMCalendarDir(pimTestPrefix, "cal1")
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{"list": {{pimItem("e1", "k1")}}}})
	store := &memPIMStore{objects: map[string][]byte{}, failPut: func(key string) error {
		if strings.HasSuffix(key, "e1.json") {
			return errors.New("network")
		}
		return nil
	}}
	if err := newTestPIMRun(store).syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	store.failPut = nil
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), pimCollection{listURL: "list", dir: dir}); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 1 {
		t.Fatalf("failed item not retried: uploaded=%d", run.uploaded)
	}
}

func withContactsDelta(t *testing.T, pages map[string]*outlook.ContactsDeltaPage) {
	t.Helper()
	prev := pimContactsDeltaFn
	pimContactsDeltaFn = func(_ context.Context, _, reqURL string) (*outlook.ContactsDeltaPage, error) {
		id := "default"
		if i := strings.Index(reqURL, "/contactFolders/"); i >= 0 {
			rest := reqURL[i+len("/contactFolders/"):]
			if j := strings.Index(rest, "/"); j > 0 {
				id = rest[:j]
			}
		}
		page := pages[id]
		if page == nil {
			return &outlook.ContactsDeltaPage{DeltaLink: "delta-" + id}, nil
		}
		out := *page
		if out.DeltaLink == "" {
			out.DeltaLink = "delta-" + id
		}
		return &out, nil
	}
	t.Cleanup(func() { pimContactsDeltaFn = prev })
}

func stubContactFullFetch(t *testing.T) {
	t.Helper()
	prev := pimContactFetchRawFn
	pimContactFetchRawFn = func(_ context.Context, _, _, _, contactID string) ([]byte, error) {
		raw, _ := json.Marshal(map[string]any{
			"id": contactID, "displayName": "n-" + contactID, "changeKey": "k1",
			"spouseName": "so", "companyName": "co", "jobTitle": "dev",
			"categories": []string{"test"}, "imAddresses": []string{"im"},
			"businessHomePage": "https://example.com", "personalNotes": "note",
		})
		return raw, nil
	}
	t.Cleanup(func() { pimContactFetchRawFn = prev })
}

func newTestContactsRun(store *memPIMStore) *pimRun {
	run := newTestPIMRun(store)
	run.method = "outlook_contacts"
	run.fetchPhotos = false
	run.tm = &repo.TaskMemory{OutlookContactsLayout: outlook.OutlookContactsLayoutFull}
	return run
}

func TestSyncOutlookContactsCoversFolders(t *testing.T) {
	stubContactFullFetch(t)
	withContactsDelta(t, map[string]*outlook.ContactsDeltaPage{
		"default": {Items: []outlook.PIMItem{pimItem("c1", "k1")}},
		"F1":      {Items: []outlook.PIMItem{pimItem("c2", "k1")}},
	})
	prev := pimContactFoldersFn
	pimContactFoldersFn = func(context.Context, string, string) ([]outlook.PIMContactFolder, error) {
		return []outlook.PIMContactFolder{{ID: "F1", DisplayName: "Work"}}, nil
	}
	t.Cleanup(func() { pimContactFoldersFn = prev })

	store := &memPIMStore{objects: map[string][]byte{}}
	run := newTestContactsRun(store)
	if err := syncOutlookContacts(context.Background(), run, "https://g/me", pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		pimTestPrefix + "/c1.json",
		pimTestPrefix + "/" + outlook.PIMIndexName,
		pimTestPrefix + "/folders/F1/c2.json",
		pimTestPrefix + "/folders/F1/" + outlook.PIMFolderMetaName,
		pimTestPrefix + "/folders/F1/" + outlook.PIMIndexName,
	} {
		if _, ok := store.objects[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if !strings.Contains(string(store.objects[pimTestPrefix+"/folders/F1/"+outlook.PIMFolderMetaName]), `"displayName":"Work"`) {
		t.Fatalf("folder meta = %s", store.objects[pimTestPrefix+"/folders/F1/"+outlook.PIMFolderMetaName])
	}
	if run.tm.OutlookContactFolderDeltas[""] == "" || run.tm.OutlookContactFolderDeltas["F1"] == "" {
		t.Fatalf("delta links not saved: %+v", run.tm.OutlookContactFolderDeltas)
	}
}

func TestSyncOutlookContactsFailsWhenFoldersUnavailable(t *testing.T) {
	prev := pimContactFoldersFn
	pimContactFoldersFn = func(context.Context, string, string) ([]outlook.PIMContactFolder, error) {
		return nil, errors.New("http 503")
	}
	t.Cleanup(func() { pimContactFoldersFn = prev })

	if err := syncOutlookContacts(context.Background(), newTestContactsRun(&memPIMStore{objects: map[string][]byte{}}), "https://g/me", pimTestPrefix); err == nil {
		t.Fatal("folder listing failure must fail the job")
	}
}

func TestSyncOutlookContactsSkipsSparseDeltaWhenFullFetchFails(t *testing.T) {
	prev := pimContactFetchRawFn
	pimContactFetchRawFn = func(context.Context, string, string, string, string) ([]byte, error) {
		return nil, errors.New("graph down")
	}
	t.Cleanup(func() { pimContactFetchRawFn = prev })
	withContactsDelta(t, map[string]*outlook.ContactsDeltaPage{
		"default": {Items: []outlook.PIMItem{pimItem("c1", "k1")}},
	})
	prevFolders := pimContactFoldersFn
	pimContactFoldersFn = func(context.Context, string, string) ([]outlook.PIMContactFolder, error) {
		return nil, nil
	}
	t.Cleanup(func() { pimContactFoldersFn = prevFolders })

	store := &memPIMStore{objects: map[string][]byte{}}
	run := newTestContactsRun(store)
	if err := syncOutlookContacts(context.Background(), run, "https://g/me", pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.objects[pimTestPrefix+"/c1.json"]; ok {
		t.Fatal("must not store sparse delta contact when full fetch fails")
	}
	if run.coverage.FailedItems < 1 {
		t.Fatalf("expected failed coverage, got %+v", run.coverage)
	}
}

func TestSyncOutlookContactsMarksRemovedFolders(t *testing.T) {
	stubContactFullFetch(t)
	userBase := "https://g/me"
	folderMeta := pimTestPrefix + "/folders/F-GONE/" + outlook.PIMFolderMetaName
	indexKey := pimTestPrefix + "/folders/F-GONE/" + outlook.PIMIndexName
	contactKey := pimTestPrefix + "/folders/F-GONE/c-gone.json"
	store := &memPIMStore{objects: map[string][]byte{
		folderMeta: []byte(`{"id":"F-GONE","displayName":"Old"}`),
		indexKey:   []byte(`{"items":{"c-gone":{"key":"` + contactKey + `","change_key":"k1"}}}`),
		contactKey: []byte(`{"id":"c-gone"}`),
	}}
	withContactsDelta(t, map[string]*outlook.ContactsDeltaPage{
		"default": {Items: []outlook.PIMItem{pimItem("c1", "k1")}},
	})
	prev := pimContactFoldersFn
	pimContactFoldersFn = func(context.Context, string, string) ([]outlook.PIMContactFolder, error) {
		return nil, nil
	}
	t.Cleanup(func() { pimContactFoldersFn = prev })

	run := newTestContactsRun(store)
	run.synced[folderMeta], run.synced[indexKey], run.synced[contactKey] = true, true, true
	if err := syncOutlookContacts(context.Background(), run, userBase, pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	var folder outlook.PIMContactFolder
	if err := json.Unmarshal(store.objects[folderMeta], &folder); err != nil || folder.RemovedAt == "" {
		t.Fatalf("folder not marked removed: %+v err=%v", folder, err)
	}
	var index outlook.PIMIndex
	if err := json.Unmarshal(store.objects[indexKey], &index); err != nil || index.Items["c-gone"].RemovedAt == "" {
		t.Fatalf("contact not marked removed: %+v err=%v", index, err)
	}
}

func TestPIMPutJSONSkipsUnchanged(t *testing.T) {
	metaKey := pimTestPrefix + "/folders/F1/" + outlook.PIMFolderMetaName
	raw, _ := json.Marshal(outlook.PIMContactFolder{ID: "F1", DisplayName: "Work"})
	store := &memPIMStore{objects: map[string][]byte{metaKey: raw}}
	run := newTestPIMRun(store)
	run.synced[metaKey] = true
	before := len(store.puts)
	if err := run.putJSON(context.Background(), metaKey, outlook.PIMContactFolder{ID: "F1", DisplayName: "Work"}); err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != before {
		t.Fatalf("unchanged folder meta uploaded: %v", store.puts[before:])
	}
}

func TestSyncOutlookCalendarsIncludesGroupCalendar(t *testing.T) {
	userBase := "https://graph.microsoft.com/v1.0/me"
	groupRoot := outlook.PIMGroupCalendarEventRoot("gid-smart")
	groupEvents := outlook.PIMEventsURLFromRoot(groupRoot)
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{
		outlook.PIMEventsURL(userBase, "main"): {{pimItem("e1", "k1")}},
		groupEvents:                            {{pimItem("ge1", "k1")}},
	}})
	withCalendars(t, outlook.FlatCalendar{ID: "main", Name: "Calendar", IsDefault: true, Source: outlook.CalendarSourceUser})
	withGroupCalendars(t, outlook.FlatCalendar{
		ID: "gcal1", Name: "Smartiies", Source: outlook.CalendarSourceGroup, GroupID: "gid-smart",
	})

	store := &memPIMStore{objects: map[string][]byte{}}
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err != nil {
		t.Fatal(err)
	}
	meta := readCalendarMeta(t, store, "gcal1")
	if meta.Name != "Smartiies" || meta.Source != outlook.CalendarSourceGroup || meta.GroupID != "gid-smart" {
		t.Fatalf("group meta %+v", meta)
	}
	if _, ok := store.objects[outlook.PIMCalendarDir(pimTestPrefix, "gcal1")+"ge1.json"]; !ok {
		t.Fatal("group calendar event not backed up")
	}
}

func TestSyncOutlookCalendarsSkipsFailingCalendar(t *testing.T) {
	userBase := "https://g/users/u1"
	withPIMGraph(t, &stubPIMGraph{
		pages: map[string][][]outlook.PIMItem{outlook.PIMEventsURL(userBase, "ok"): {{pimItem("e1", "k1")}}},
		fail:  map[string]error{outlook.PIMEventsURL(userBase, "bad"): errors.New("http 403")},
	})
	withCalendars(t, outlook.FlatCalendar{ID: "bad", Name: "Shared"}, outlook.FlatCalendar{ID: "ok", Name: "Calendar"})

	store := &memPIMStore{objects: map[string][]byte{}}
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err != nil {
		t.Fatalf("one failing calendar must not fail the job: %v", err)
	}
	if _, ok := store.objects[outlook.PIMCalendarDir(pimTestPrefix, "ok")+"e1.json"]; !ok {
		t.Fatal("event of the readable calendar not backed up")
	}
	if !strings.Contains(string(store.objects[outlook.PIMCalendarDir(pimTestPrefix, "ok")+outlook.PIMCalendarMetaName]), `"name":"Calendar"`) {
		t.Fatal("calendar meta not written")
	}

	pimCalendarsFn = func(context.Context, string, string) ([]outlook.FlatCalendar, error) {
		return []outlook.FlatCalendar{{ID: "bad", Name: "Shared"}}, nil
	}
	if err := syncOutlookCalendars(context.Background(), newTestPIMRun(store), userBase, pimTestPrefix); err == nil {
		t.Fatal("job must fail when no calendar can be backed up")
	}
}
