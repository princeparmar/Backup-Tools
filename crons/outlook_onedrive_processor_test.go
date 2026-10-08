package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Wiring checks that replace live Graph E2E in CI (manual E2E still required against a tenant).
func TestOutlookOneDriveProcessorRegistered(t *testing.T) {
	p, ok := processorMap["outlook_onedrive"]
	if !ok || p == nil {
		t.Fatal("outlook_onedrive missing from processorMap")
	}
	if !repo.IsMicrosoftAutosyncMethod("outlook_onedrive") {
		t.Fatal("outlook_onedrive must be a microsoft autosync method")
	}
	found := false
	for _, m := range repo.MicrosoftAutosyncMethods {
		if m == "outlook_onedrive" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MicrosoftAutosyncMethods missing outlook_onedrive")
	}
}

const (
	driveTestPrefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid-1"
	driveTestRoot   = "https://graph/users/u/drive"
)

type memDriveObject struct {
	data []byte
	meta map[string]string
}

type memDriveStore struct {
	objects map[string]memDriveObject
	copies  []string
	removed []string
}

func newMemDriveStore() *memDriveStore { return &memDriveStore{objects: map[string]memDriveObject{}} }

func (s *memDriveStore) stat(_ context.Context, key string) (oneDriveStored, error) {
	o, ok := s.objects[key]
	if !ok {
		return oneDriveStored{}, fmt.Errorf("not found: %s", key)
	}
	return oneDriveStored{meta: maps.Clone(o.meta), size: int64(len(o.data))}, nil
}

func (s *memDriveStore) download(_ context.Context, key string) ([]byte, error) {
	o, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return o.data, nil
}

func (s *memDriveStore) put(_ context.Context, key string, body io.Reader, _ int64, meta map[string]string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.objects[key] = memDriveObject{data: b, meta: maps.Clone(meta)}
	return nil
}

func (s *memDriveStore) copy(_ context.Context, src, dst string, _ int64, meta map[string]string) error {
	o, ok := s.objects[src]
	if !ok {
		return fmt.Errorf("not found: %s", src)
	}
	s.copies = append(s.copies, src+" -> "+dst)
	s.objects[dst] = memDriveObject{data: o.data, meta: maps.Clone(meta)}
	return nil
}

func (s *memDriveStore) remove(_ context.Context, key string) {
	s.removed = append(s.removed, key)
	delete(s.objects, key)
}

func (s *memDriveStore) keySet() map[string]bool {
	out := make(map[string]bool, len(s.objects))
	for k := range s.objects {
		out[k] = true
	}
	return out
}

func (s *memDriveStore) keysUnder(section string) []string {
	var out []string
	for k := range s.objects {
		if strings.HasPrefix(k, driveTestPrefix+"/"+section+"/") {
			out = append(out, k)
		}
	}
	return out
}

func driveFolder(id, parent, name string) outlook.OneDriveItem {
	return outlook.OneDriveItem{ID: id, ParentID: parent, Name: name, IsFolder: true}
}

func driveFile(id, parent, name, ctag string) outlook.OneDriveItem {
	return outlook.OneDriveItem{ID: id, ParentID: parent, Name: name, CTag: ctag, ETag: "e-" + ctag, Size: 4, MimeType: "text/plain"}
}

func driveDeleted(id string) outlook.OneDriveItem {
	return outlook.OneDriveItem{ID: id, IsDeleted: true}
}

// driveGraph stubs the Graph seams: the delta returns items for the initial URL and for the
// saved cursor "D1"; downloads return "v:{ctag}".
type driveGraph struct {
	baseline, incremental []outlook.OneDriveItem
	items                 map[string]*outlook.OneDriveItem
	downloads             []string
	itemLookups           []string
}

func stubDriveGraph(t *testing.T, g *driveGraph) {
	t.Helper()
	prevDelta, prevRoot, prevItem, prevContent := oneDriveDeltaPageFn, oneDriveRootIDFn, oneDriveItemFn, oneDriveContentFn
	ctags := map[string]string{}
	for _, list := range [][]outlook.OneDriveItem{g.baseline, g.incremental} {
		for _, it := range list {
			ctags[it.ID] = it.CTag
		}
	}
	oneDriveDeltaPageFn = func(_ context.Context, _, url string) (*outlook.OneDriveDeltaPage, error) {
		switch url {
		case outlook.OneDriveInitialDeltaURL(driveTestRoot):
			return &outlook.OneDriveDeltaPage{Items: g.baseline, DeltaLink: "D2"}, nil
		case "D1":
			return &outlook.OneDriveDeltaPage{Items: g.incremental, DeltaLink: "D2"}, nil
		}
		return nil, fmt.Errorf("unexpected delta url %s", url)
	}
	oneDriveRootIDFn = func(context.Context, string, string) (string, error) { return "ROOT", nil }
	oneDriveItemFn = func(_ context.Context, _, _, id string) (*outlook.OneDriveItem, error) {
		g.itemLookups = append(g.itemLookups, id)
		if it := g.items[id]; it != nil {
			return it, nil
		}
		return nil, fmt.Errorf("no item %s", id)
	}
	oneDriveContentFn = func(_ context.Context, _, _, id string) (io.ReadCloser, int64, error) {
		g.downloads = append(g.downloads, id)
		return io.NopCloser(strings.NewReader("v:" + ctags[id])), 0, nil
	}
	t.Cleanup(func() {
		oneDriveDeltaPageFn, oneDriveRootIDFn, oneDriveItemFn, oneDriveContentFn = prevDelta, prevRoot, prevItem, prevContent
	})
}

func runDriveSync(t *testing.T, store *memDriveStore, tm *repo.TaskMemory) *oneDriveRun {
	t.Helper()
	run := newOneDriveRun(ProcessorInput{}, store, "tok", driveTestRoot, driveTestPrefix, store.keySet())
	if err := run.syncAll(context.Background(), tm); err != nil {
		t.Fatal(err)
	}
	return run
}

func incrementalTM() *repo.TaskMemory {
	link := "D1"
	return &repo.TaskMemory{OneDriveDeltaLink: &link, OneDriveLayout: repo.OneDriveLayoutTree}
}

var (
	keyFolderA  = outlook.OneDriveFolderKey(driveTestPrefix, nil, "A", "Docs")
	keyFolderB  = outlook.OneDriveFolderKey(driveTestPrefix, []string{"A"}, "B", "Reports")
	keyReport   = outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionMyDrive, []string{"A", "B"}, "F1", "q3.txt")
	keyRootFile = outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionMyDrive, nil, "F2", "notes.txt")
)

// seedDrive is the backup after a baseline of: Docs/Reports/q3.txt and notes.txt.
func seedDrive(t *testing.T) *memDriveStore {
	t.Helper()
	store := newMemDriveStore()
	stubDriveGraph(t, &driveGraph{baseline: []outlook.OneDriveItem{
		{ID: "ROOT", IsRoot: true, IsFolder: true},
		driveFile("F1", "B", "q3.txt", "c1"),
		driveFolder("B", "A", "Reports"),
		driveFolder("A", "ROOT", "Docs"),
		driveFile("F2", "ROOT", "notes.txt", "c1"),
	}})
	runDriveSync(t, store, &repo.TaskMemory{OneDriveLayout: repo.OneDriveLayoutTree})
	return store
}

func TestOneDriveBaselineBuildsTree(t *testing.T) {
	store := newMemDriveStore()
	g := &driveGraph{baseline: []outlook.OneDriveItem{
		{ID: "ROOT", IsRoot: true, IsFolder: true},
		driveFile("F1", "B", "q3.txt", "c1"),
		driveFolder("B", "A", "Reports"),
		driveFolder("A", "ROOT", "Docs"),
		driveFile("F2", "ROOT", "notes.txt", "c1"),
		{ID: "NB", ParentID: "ROOT", Name: "Notebook", IsPackage: true},
	}}
	stubDriveGraph(t, g)
	tm := &repo.TaskMemory{}
	runDriveSync(t, store, tm)

	for _, key := range []string{keyFolderA, keyFolderB, keyReport, keyRootFile} {
		if _, ok := store.objects[key]; !ok {
			t.Fatalf("missing %s; have %v", key, store.keySet())
		}
	}
	if len(store.objects) != 4 || len(g.downloads) != 2 {
		t.Fatalf("objects=%v downloads=%v", store.keySet(), g.downloads)
	}
	meta := store.objects[keyReport].meta
	if meta[outlook.OneDriveMetaParentPath] != "Docs/Reports" || meta[outlook.OneDriveMetaCTag] != "c1" || meta[outlook.OneDriveMetaBackedUpAt] == "" {
		t.Fatalf("meta %v", meta)
	}
	if store.objects[keyFolderB].meta[outlook.OneDriveMetaIsFolder] != "true" {
		t.Fatalf("folder meta %v", store.objects[keyFolderB].meta)
	}
	if tm.OneDriveLayout != repo.OneDriveLayoutTree || tm.OneDriveDeltaLink == nil || *tm.OneDriveDeltaLink != "D2" {
		t.Fatalf("task memory %+v", tm)
	}
}

func TestOneDriveIncrementalContent(t *testing.T) {
	store := seedDrive(t)
	g := &driveGraph{incremental: []outlook.OneDriveItem{
		driveFile("F1", "B", "q3.txt", "c1"),
		driveFile("F2", "ROOT", "notes.txt", "c2"),
	}}
	stubDriveGraph(t, g)
	runDriveSync(t, store, incrementalTM())
	if len(g.downloads) != 1 || g.downloads[0] != "F2" {
		t.Fatalf("downloads %v", g.downloads)
	}
	if string(store.objects[keyRootFile].data) != "v:c2" {
		t.Fatalf("content %q", store.objects[keyRootFile].data)
	}
}

func TestOneDriveRenameCopiesStoredFile(t *testing.T) {
	store := seedDrive(t)
	g := &driveGraph{incremental: []outlook.OneDriveItem{driveFile("F2", "A", "renamed.txt", "c1")}}
	stubDriveGraph(t, g)
	runDriveSync(t, store, incrementalTM())
	newKey := outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionMyDrive, []string{"A"}, "F2", "renamed.txt")
	if _, ok := store.objects[newKey]; !ok || len(g.downloads) != 0 {
		t.Fatalf("objects=%v downloads=%v", store.keySet(), g.downloads)
	}
	if _, ok := store.objects[keyRootFile]; ok {
		t.Fatal("old key kept")
	}
	if store.objects[newKey].meta[outlook.OneDriveMetaParentPath] != "Docs" {
		t.Fatalf("meta %v", store.objects[newKey].meta)
	}
}

func TestOneDriveFolderMoveCarriesContents(t *testing.T) {
	store := seedDrive(t)
	g := &driveGraph{incremental: []outlook.OneDriveItem{driveFolder("B", "ROOT", "Reports")}}
	stubDriveGraph(t, g)
	runDriveSync(t, store, incrementalTM())
	movedFolder := outlook.OneDriveFolderKey(driveTestPrefix, nil, "B", "Reports")
	movedFile := outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionMyDrive, []string{"B"}, "F1", "q3.txt")
	for _, key := range []string{movedFolder, movedFile, keyFolderA} {
		if _, ok := store.objects[key]; !ok {
			t.Fatalf("missing %s; have %v", key, store.keySet())
		}
	}
	if _, ok := store.objects[keyReport]; ok || len(g.downloads) != 0 {
		t.Fatalf("old file kept or downloaded: %v %v", store.keySet(), g.downloads)
	}
}

func TestOneDriveFolderRenameKeepsContents(t *testing.T) {
	store := seedDrive(t)
	stubDriveGraph(t, &driveGraph{incremental: []outlook.OneDriveItem{driveFolder("A", "ROOT", "Documents")}})
	runDriveSync(t, store, incrementalTM())
	if _, ok := store.objects[outlook.OneDriveFolderKey(driveTestPrefix, nil, "A", "Documents")]; !ok {
		t.Fatalf("renamed placeholder missing: %v", store.keySet())
	}
	if _, ok := store.objects[keyFolderA]; ok {
		t.Fatal("old placeholder kept")
	}
	if _, ok := store.objects[keyReport]; !ok {
		t.Fatal("contents moved on rename")
	}
}

func TestOneDriveDeleteMovesToBin(t *testing.T) {
	store := seedDrive(t)
	stubDriveGraph(t, &driveGraph{incremental: []outlook.OneDriveItem{driveDeleted("F2"), driveDeleted("A")}})
	runDriveSync(t, store, incrementalTM())
	if mine := store.keysUnder(outlook.OneDriveSectionMyDrive); len(mine) != 0 {
		t.Fatalf("MY_DRIVE still has %v", mine)
	}
	for _, id := range []string{"F1", "F2"} {
		name := map[string]string{"F1": "q3.txt", "F2": "notes.txt"}[id]
		key := outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionBin, nil, id, name)
		o, ok := store.objects[key]
		if !ok || o.meta[outlook.OneDriveMetaRemovedAt] == "" {
			t.Fatalf("%s not in bin: %v", id, store.keySet())
		}
	}
	if store.objects[outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionBin, nil, "F1", "q3.txt")].meta[outlook.OneDriveMetaParentPath] != "Docs/Reports" {
		t.Fatal("bin copy lost its folder path")
	}
}

func TestOneDriveUndeleteCopiesFromBin(t *testing.T) {
	store := seedDrive(t)
	stubDriveGraph(t, &driveGraph{incremental: []outlook.OneDriveItem{driveDeleted("F2")}})
	runDriveSync(t, store, incrementalTM())
	g := &driveGraph{incremental: []outlook.OneDriveItem{driveFile("F2", "ROOT", "notes.txt", "c1")}}
	stubDriveGraph(t, g)
	runDriveSync(t, store, incrementalTM())
	if _, ok := store.objects[keyRootFile]; !ok || len(g.downloads) != 0 {
		t.Fatalf("objects=%v downloads=%v", store.keySet(), g.downloads)
	}
	if bin := store.keysUnder(outlook.OneDriveSectionBin); len(bin) != 0 {
		t.Fatalf("bin copy kept: %v", bin)
	}
	if store.objects[keyRootFile].meta[outlook.OneDriveMetaRemovedAt] != "" {
		t.Fatal("removed-at kept after undelete")
	}
}

func TestOneDriveBaselineSweepsMissingFiles(t *testing.T) {
	store := seedDrive(t)
	stubDriveGraph(t, &driveGraph{baseline: []outlook.OneDriveItem{
		driveFolder("A", "ROOT", "Docs"),
		driveFolder("B", "A", "Reports"),
		driveFile("F1", "B", "q3.txt", "c1"),
	}})
	runDriveSync(t, store, &repo.TaskMemory{OneDriveLayout: repo.OneDriveLayoutTree})
	if _, ok := store.objects[outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionBin, nil, "F2", "notes.txt")]; !ok {
		t.Fatalf("missing file not moved to bin: %v", store.keySet())
	}
}

func TestOneDriveUnknownParentFetched(t *testing.T) {
	store := seedDrive(t)
	g := &driveGraph{
		incremental: []outlook.OneDriveItem{driveFile("F3", "C", "new.txt", "c1")},
		items:       map[string]*outlook.OneDriveItem{"C": {ID: "C", ParentID: "A", Name: "Archive", IsFolder: true}},
	}
	stubDriveGraph(t, g)
	runDriveSync(t, store, incrementalTM())
	for _, key := range []string{
		outlook.OneDriveFolderKey(driveTestPrefix, []string{"A"}, "C", "Archive"),
		outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionMyDrive, []string{"A", "C"}, "F3", "new.txt"),
	} {
		if _, ok := store.objects[key]; !ok {
			t.Fatalf("missing %s; have %v", key, store.keySet())
		}
	}
	if len(g.itemLookups) != 1 {
		t.Fatalf("lookups %v", g.itemLookups)
	}
}

func TestOneDriveQuotaSkipKeepsOldCopy(t *testing.T) {
	store := seedDrive(t)
	stubDriveGraph(t, &driveGraph{incremental: []outlook.OneDriveItem{driveFile("F2", "ROOT", "big.txt", "c9")}})
	run := newOneDriveRun(ProcessorInput{}, store, "tok", driveTestRoot, driveTestPrefix, store.keySet())
	run.quota = &driveQuotaSession{ok: true, remaining: 0}
	if err := run.syncAll(context.Background(), incrementalTM()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.objects[keyRootFile]; !ok || run.quota.skippedQuota != 1 {
		t.Fatalf("old copy lost or not skipped: %v", store.keySet())
	}
}

func TestOneDriveLegacyMigration(t *testing.T) {
	store := newMemDriveStore()
	legacy := func(id, name, ctag string, removed bool, withData bool) string {
		metaKey := fmt.Sprintf("%s/meta/2026/07/21/%s_%s.json", driveTestPrefix, id, name)
		m := outlook.OneDriveCronBackupMeta{ItemID: id, Name: name, CTag: ctag, ETag: "e-" + ctag, Size: 4, ParentPath: "/drive/root:/Old%20Place", RemovedFromOneDrive: removed}
		if withData {
			m.DataObjectKey = outlook.OneDriveLegacyDataKey(metaKey)
			store.objects[m.DataObjectKey] = memDriveObject{data: []byte("old:" + id)}
		}
		b, _ := json.Marshal(m)
		store.objects[metaKey] = memDriveObject{data: b}
		return metaKey
	}
	legacy("F1", "q3.txt", "c1", false, true)     // unchanged: copied
	legacy("F1", "q3-old.txt", "c0", false, true) // rename leftover: dropped
	legacy("F2", "notes.txt", "c0", false, true)  // changed: downloaded
	legacy("G1", "gone.txt", "c1", false, true)   // deleted from OneDrive: to bin
	legacy("T1", "T1", "", true, false)           // tombstone without data: dropped
	store.objects[driveTestPrefix+"/.file_placeholder"] = memDriveObject{}

	g := &driveGraph{baseline: []outlook.OneDriveItem{
		driveFolder("A", "ROOT", "Docs"),
		driveFolder("B", "A", "Reports"),
		driveFile("F1", "B", "q3.txt", "c1"),
		driveFile("F2", "ROOT", "notes.txt", "c1"),
	}}
	stubDriveGraph(t, g)
	saved := "OLD-CURSOR"
	tm := &repo.TaskMemory{OneDriveDeltaLink: &saved}
	runDriveSync(t, store, tm)

	if len(g.downloads) != 1 || g.downloads[0] != "F2" {
		t.Fatalf("downloads %v", g.downloads)
	}
	if string(store.objects[keyReport].data) != "old:F1" {
		t.Fatalf("F1 not copied from legacy: %q", store.objects[keyReport].data)
	}
	bin := outlook.OneDriveFileKey(driveTestPrefix, outlook.OneDriveSectionBin, nil, "G1", "gone.txt")
	if o, ok := store.objects[bin]; !ok || string(o.data) != "old:G1" || o.meta[outlook.OneDriveMetaParentPath] != "Old Place" || o.meta[outlook.OneDriveMetaRemovedAt] == "" {
		t.Fatalf("deleted file not in bin: %v", store.keySet())
	}
	for key := range store.objects {
		if _, ok := outlook.ParseOneDriveLegacyKey(key); ok {
			t.Fatalf("legacy key left: %s", key)
		}
	}
	if tm.OneDriveLayout != repo.OneDriveLayoutTree || *tm.OneDriveDeltaLink != "D2" {
		t.Fatalf("task memory %+v", tm)
	}
}

func TestSharePointLibraryMigratesToTree(t *testing.T) {
	store := newMemDriveStore()
	metaKey := outlook.SharePointIDBasedMetaKey(driveTestPrefix, "F1", "q3.txt", "2026-07-21T00:00:00Z")
	dataKey := outlook.SharePointIDBasedDataKey(driveTestPrefix, "F1", "q3.txt", "2026-07-21T00:00:00Z")
	b, _ := json.Marshal(outlook.SharePointCronBackupMeta{ItemID: "F1", Name: "q3.txt", CTag: "c1", ETag: "e-c1", Size: 4, SiteID: "s1", DriveID: "b!lib", DataObjectKey: dataKey})
	store.objects[metaKey] = memDriveObject{data: b}
	store.objects[dataKey] = memDriveObject{data: []byte("old:F1")}

	g := &driveGraph{baseline: []outlook.OneDriveItem{
		driveFolder("A", "ROOT", "Docs"),
		driveFolder("B", "A", "Reports"),
		driveFile("F1", "B", "q3.txt", "c1"),
		driveFile("F2", "ROOT", "notes.txt", "c1"),
	}}
	stubDriveGraph(t, g)
	tm := &repo.TaskMemory{SharePointDeltaLink: new(string)}
	*tm.SharePointDeltaLink = "OLD-CURSOR"
	run := newOneDriveRun(ProcessorInput{}, store, "tok", driveTestRoot, driveTestPrefix, store.keySet())
	if err := run.syncDrive(context.Background(), &tm.SharePointDeltaLink, &tm.SharePointLayout); err != nil {
		t.Fatal(err)
	}

	if string(store.objects[keyReport].data) != "old:F1" || len(g.downloads) != 1 {
		t.Fatalf("q3.txt not copied from the old backup: downloads=%v keys=%v", g.downloads, store.keySet())
	}
	if _, ok := store.objects[metaKey]; ok {
		t.Fatal("old meta key left")
	}
	if _, ok := store.objects[dataKey]; ok {
		t.Fatal("old data key left")
	}
	if tm.SharePointLayout != repo.OneDriveLayoutTree || *tm.SharePointDeltaLink != "D2" || tm.OneDriveDeltaLink != nil {
		t.Fatalf("task memory %+v", tm)
	}
}
