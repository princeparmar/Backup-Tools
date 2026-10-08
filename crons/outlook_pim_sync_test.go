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

	if err := run.syncCollection(context.Background(), "list", dir); err != nil {
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
	if err := newTestPIMRun(store).syncCollection(context.Background(), "list", dir); err != nil {
		t.Fatal(err)
	}

	g.pages["list"] = [][]outlook.PIMItem{{pimItem("e1", "k1"), pimItem("e2", "k2")}}
	store.puts = nil
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), "list", dir); err != nil {
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
	if err := newTestPIMRun(store).syncCollection(context.Background(), "list", dir); err != nil {
		t.Fatal(err)
	}
	store.puts = nil
	if err := newTestPIMRun(store).syncCollection(context.Background(), "list", dir); err != nil {
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
	if err := newTestPIMRun(store).syncCollection(context.Background(), "list", dir); err != nil {
		t.Fatal(err)
	}

	g.pages["list"] = [][]outlook.PIMItem{{pimItem("e1", "k1")}}
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), "list", dir); err != nil {
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
	if err := run.syncCollection(context.Background(), "list", dir); err != nil {
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
	if err := run.syncCollection(context.Background(), "list", dir); err != nil {
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
	err := newTestPIMRun(store).syncCollection(context.Background(), "list", dir)
	if !quota.IsStorageQuota(err) {
		t.Fatalf("err = %v, want storage quota", err)
	}
	if _, ok := readPIMIndex(t, store, dir+outlook.PIMIndexName).Items["e1"]; !ok {
		t.Fatal("index of items backed up before the limit was not saved")
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
	if err := newTestPIMRun(store).syncCollection(context.Background(), "list", dir); err != nil {
		t.Fatal(err)
	}
	store.failPut = nil
	run := newTestPIMRun(store)
	if err := run.syncCollection(context.Background(), "list", dir); err != nil {
		t.Fatal(err)
	}
	if run.uploaded != 1 {
		t.Fatalf("failed item not retried: uploaded=%d", run.uploaded)
	}
}

func TestSyncOutlookContactsCoversFolders(t *testing.T) {
	withPIMGraph(t, &stubPIMGraph{pages: map[string][][]outlook.PIMItem{
		outlook.PIMContactsURL("https://g/me", ""):   {{pimItem("c1", "k1")}},
		outlook.PIMContactsURL("https://g/me", "F1"): {{pimItem("c2", "k1")}},
	}})
	prev := pimContactFoldersFn
	pimContactFoldersFn = func(context.Context, string, string) ([]outlook.PIMContactFolder, error) {
		return []outlook.PIMContactFolder{{ID: "F1", DisplayName: "Work"}}, nil
	}
	t.Cleanup(func() { pimContactFoldersFn = prev })

	store := &memPIMStore{objects: map[string][]byte{}}
	run := newTestPIMRun(store)
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
}

func TestSyncOutlookCalendarsSkipsFailingCalendar(t *testing.T) {
	userBase := "https://g/users/u1"
	withPIMGraph(t, &stubPIMGraph{
		pages: map[string][][]outlook.PIMItem{outlook.PIMEventsURL(userBase, "ok"): {{pimItem("e1", "k1")}}},
		fail:  map[string]error{outlook.PIMEventsURL(userBase, "bad"): errors.New("http 403")},
	})
	prev := pimCalendarsFn
	pimCalendarsFn = func(context.Context, string, string) ([]outlook.FlatCalendar, error) {
		return []outlook.FlatCalendar{{ID: "bad", Name: "Shared"}, {ID: "ok", Name: "Calendar"}}, nil
	}
	t.Cleanup(func() { pimCalendarsFn = prev })

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
