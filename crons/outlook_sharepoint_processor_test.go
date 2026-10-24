package crons

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestOutlookSharePointProcessorRegistered(t *testing.T) {
	p, ok := processorMap["outlook_sharepoint"]
	if !ok || p == nil {
		t.Fatal("outlook_sharepoint missing from processorMap")
	}
	if !repo.IsMicrosoftAutosyncMethod("outlook_sharepoint") {
		t.Fatal("outlook_sharepoint must be a microsoft autosync method")
	}
	found := false
	for _, m := range repo.MicrosoftAutosyncMethods {
		if m == "outlook_sharepoint" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MicrosoftAutosyncMethods missing outlook_sharepoint")
	}
}

const (
	spSitePrefix = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/site/contoso.sharepoint.com_s1_w1"
	spDefault    = "b!default"
	spLegal      = "b!legal"
	spHR         = "b!hr"
)

var errGraphForbidden = errors.New("graph http 403 accessDenied")

// spDrive is one library in the fake Graph: the baseline answers the initial delta URL and the
// incremental answers the saved cursor "D-{drive}".
type spDrive struct {
	baseline, incremental []outlook.OneDriveItem
	fail                  error
}

type spGraph struct {
	libraries []outlook.SharePointLibrary
	listErr   error
	drives    map[string]*spDrive
	deltaURLs []string
	downloads []string
}

// stubSharePoint wires the library list, the per-drive delta and the backup store to fakes.
func stubSharePoint(t *testing.T, g *spGraph, store *memDriveStore) {
	t.Helper()
	prevList, prevKeys, prevStore := sharePointLibrariesFn, libraryKeysFn, libraryStoreFn
	prevDelta, prevRoot, prevItem, prevContent := oneDriveDeltaPageFn, oneDriveRootIDFn, oneDriveItemFn, oneDriveContentFn
	sharePointLibrariesFn = func(context.Context, string, string) ([]outlook.SharePointLibrary, error) {
		if g.listErr != nil {
			return nil, g.listErr
		}
		return g.libraries, nil
	}
	libraryKeysFn = func(_ context.Context, _ ProcessorInput, d libraryDrive) (map[string]bool, error) {
		out := map[string]bool{}
		for k := range store.objects {
			if strings.HasPrefix(k, d.prefix+"/") {
				out[k] = true
			}
		}
		return out, nil
	}
	libraryStoreFn = func(_ ProcessorInput, d libraryDrive) oneDriveStore {
		return &driveIDStore{memDriveStore: store, driveID: d.driveID}
	}
	ctags := map[string]string{}
	for _, d := range g.drives {
		for _, list := range [][]outlook.OneDriveItem{d.baseline, d.incremental} {
			for _, it := range list {
				ctags[it.ID] = it.CTag
			}
		}
	}
	oneDriveDeltaPageFn = func(_ context.Context, _, url string) (*outlook.OneDriveDeltaPage, error) {
		g.deltaURLs = append(g.deltaURLs, url)
		for id, d := range g.drives {
			initial := outlook.OneDriveInitialDeltaURL(outlook.DriveRootURLFromDriveID(id))
			if url != initial && url != spCursor(id) {
				continue
			}
			if d.fail != nil {
				return nil, d.fail
			}
			items := d.baseline
			if url == spCursor(id) {
				items = d.incremental
			}
			return &outlook.OneDriveDeltaPage{Items: items, DeltaLink: spCursor(id)}, nil
		}
		return nil, fmt.Errorf("unexpected delta url %s", url)
	}
	oneDriveRootIDFn = func(context.Context, string, string) (string, error) { return "ROOT", nil }
	oneDriveItemFn = func(_ context.Context, _, _, id string) (*outlook.OneDriveItem, error) {
		return nil, fmt.Errorf("no item %s", id)
	}
	oneDriveContentFn = func(_ context.Context, _, _, id string) (io.ReadCloser, int64, error) {
		g.downloads = append(g.downloads, id)
		return io.NopCloser(strings.NewReader("v:" + ctags[id])), 0, nil
	}
	t.Cleanup(func() {
		sharePointLibrariesFn, libraryKeysFn, libraryStoreFn = prevList, prevKeys, prevStore
		oneDriveDeltaPageFn, oneDriveRootIDFn, oneDriveItemFn, oneDriveContentFn = prevDelta, prevRoot, prevItem, prevContent
	})
}

// driveIDStore records the library's drive id on every object, like satelliteOneDriveStore.
type driveIDStore struct {
	*memDriveStore
	driveID string
}

func (s *driveIDStore) put(ctx context.Context, key string, body io.Reader, size int64, meta map[string]string) error {
	meta = maps.Clone(meta)
	if meta == nil {
		meta = map[string]string{}
	}
	meta[outlook.OneDriveMetaDriveID] = s.driveID
	return s.memDriveStore.put(ctx, key, body, size, meta)
}

func (s *driveIDStore) copy(ctx context.Context, src, dst string, size int64, meta map[string]string) error {
	meta = maps.Clone(meta)
	if meta == nil {
		meta = map[string]string{}
	}
	meta[outlook.OneDriveMetaDriveID] = s.driveID
	return s.memDriveStore.copy(ctx, src, dst, size, meta)
}

func spInput() ProcessorInput {
	return ProcessorInput{Job: &repo.CronJobListingDB{Method: "outlook_sharepoint"}}
}

func spSite(q *driveQuotaSession) sharePointSite {
	if q == nil {
		q = &driveQuotaSession{}
	}
	return sharePointSite{siteID: "contoso.sharepoint.com,s1,w1", siteKey: spSitePrefix, defaultDriveID: spDefault, token: "tok", quota: q}
}

func spLibs(ids ...string) []outlook.SharePointLibrary {
	names := map[string]string{spDefault: "Documents", spLegal: "Legal", spHR: "HR"}
	out := make([]outlook.SharePointLibrary, 0, len(ids))
	for _, id := range ids {
		out = append(out, outlook.SharePointLibrary{ID: id, Name: names[id], WebURL: "https://contoso/" + names[id]})
	}
	return out
}

func spFileKey(prefix, id, name string) string {
	return outlook.OneDriveFileKey(prefix, outlook.OneDriveSectionMyDrive, nil, id, name)
}

func spBinKey(prefix, id, name string) string {
	return outlook.OneDriveFileKey(prefix, outlook.OneDriveSectionBin, nil, id, name)
}

func runSite(t *testing.T, in ProcessorInput, q *driveQuotaSession) sharePointSiteResult {
	t.Helper()
	res, err := syncSharePointSite(context.Background(), in, spSite(q))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSharePointSyncsEveryLibraryWithItsOwnCursor(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	if res := runSite(t, in, nil); res.synced != 2 || res.warning() != "" {
		t.Fatalf("result %+v warning %q", res, res.warning())
	}

	legalPrefix := outlook.SharePointLibraryPrefix(spSitePrefix, spLegal)
	defaultKey, legalKey := spFileKey(spSitePrefix, "D1", "plan.docx"), spFileKey(legalPrefix, "L1", "nda.pdf")
	if _, ok := store.objects[defaultKey]; !ok {
		t.Fatalf("default library key moved: %v", store.keySet())
	}
	if !strings.HasPrefix(defaultKey, spSitePrefix+"/MY_DRIVE/") {
		t.Fatalf("default library key %s", defaultKey)
	}
	if o, ok := store.objects[legalKey]; !ok || o.meta[outlook.OneDriveMetaDriveID] != spLegal {
		t.Fatalf("legal library object %v (keys %v)", o, store.keySet())
	}
	if store.objects[defaultKey].meta[outlook.OneDriveMetaDriveID] != spDefault {
		t.Fatalf("default drive id %v", store.objects[defaultKey].meta)
	}

	tm := in.Job.TaskMemory
	if tm.SharePointDeltaLink == nil || *tm.SharePointDeltaLink != spCursor(spDefault) || tm.SharePointLayout != repo.OneDriveLayoutTree || !tm.SharePointBaselineDone {
		t.Fatalf("default cursor %+v", tm)
	}
	legal := tm.SharePointLibraries[spLegal]
	if legal.DeltaLink == nil || *legal.DeltaLink != spCursor(spLegal) || legal.Layout != repo.OneDriveLayoutTree || legal.Default || legal.Name != "Legal" {
		t.Fatalf("legal state %+v", legal)
	}
	def := tm.SharePointLibraries[spDefault]
	if !def.Default || def.DeltaLink != nil || def.Name != "Documents" {
		t.Fatalf("default state %+v", def)
	}

	// Second run continues each library from its own cursor.
	g.deltaURLs = nil
	g.drives[spLegal].incremental = []outlook.OneDriveItem{driveFile("L2", "ROOT", "msa.pdf", "c1")}
	runSite(t, in, nil)
	if !containsAll(g.deltaURLs, spCursor(spDefault), spCursor(spLegal)) || len(g.deltaURLs) != 2 {
		t.Fatalf("delta urls %v", g.deltaURLs)
	}
	if _, ok := store.objects[spFileKey(legalPrefix, "L2", "msa.pdf")]; !ok {
		t.Fatalf("incremental file missing: %v", store.keySet())
	}
}

func TestSharePointDefaultBaselineLeavesOtherLibrariesAlone(t *testing.T) {
	store := newMemDriveStore()
	legalPrefix := outlook.SharePointLibraryPrefix(spSitePrefix, spLegal)
	legalKey := spFileKey(legalPrefix, "L1", "nda.pdf")
	store.objects[legalKey] = memDriveObject{data: []byte("v:c1"), meta: map[string]string{outlook.OneDriveMetaCTag: "c1"}}
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {fail: errGraphForbidden},
		},
	}
	stubSharePoint(t, g, store)
	runSite(t, spInput(), nil)
	if _, ok := store.objects[legalKey]; !ok {
		t.Fatalf("default library baseline swept another library: %v", store.keySet())
	}
	for k := range store.objects {
		if strings.Contains(k, "/BIN/") {
			t.Fatalf("unexpected bin key %s", k)
		}
	}
}

func TestSharePointLibraryFailureIsIsolated(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal, spHR),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {fail: errGraphForbidden},
			spHR:      {baseline: []outlook.OneDriveItem{driveFile("H1", "ROOT", "policy.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	res := runSite(t, in, nil)
	if res.synced != 2 || len(res.failed) != 1 || res.failed[0] != "Legal" {
		t.Fatalf("result %+v", res)
	}
	if w := res.warning(); !strings.HasPrefix(w, sharePointWarningPrefix) || !strings.Contains(w, "1 of 3 libraries") {
		t.Fatalf("warning %q", w)
	}
	legal := in.Job.TaskMemory.SharePointLibraries[spLegal]
	if legal.DeltaLink != nil || legal.LastError == "" || legal.BaselineDone {
		t.Fatalf("failed library state %+v", legal)
	}
	if _, ok := store.objects[spFileKey(outlook.SharePointLibraryPrefix(spSitePrefix, spHR), "H1", "policy.pdf")]; !ok {
		t.Fatalf("library after the failing one not synced: %v", store.keySet())
	}

	// The failing library recovers on a later run; the warning is not carried over.
	in.Job.Message, in.Job.MessageStatus = res.warning(), repo.JobMessageStatusWarning
	g.drives[spLegal] = &spDrive{baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}}
	stubSharePoint(t, g, store)
	res = runSite(t, in, nil)
	if res.warning() != "" || in.Job.Message != "" || in.Job.TaskMemory.SharePointLibraries[spLegal].LastError != "" {
		t.Fatalf("recovered run: %+v message %q state %+v", res, in.Job.Message, in.Job.TaskMemory.SharePointLibraries[spLegal])
	}
}

func TestSharePointEveryLibraryFailingFailsTheRun(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives:    map[string]*spDrive{spDefault: {fail: errGraphForbidden}, spLegal: {fail: errGraphForbidden}},
	}
	stubSharePoint(t, g, store)
	if _, err := syncSharePointSite(context.Background(), spInput(), spSite(nil)); !errors.Is(err, errGraphForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestSharePointStopsOnCancellation(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	in.HeartBeatFunc = func() error { return errJobDeactivated }
	if _, err := syncSharePointSite(context.Background(), in, spSite(nil)); !errors.Is(err, errJobDeactivated) {
		t.Fatalf("err = %v", err)
	}
	if len(g.deltaURLs) != 0 || len(store.objects) != 0 {
		t.Fatalf("run continued after the stop: urls=%v keys=%v", g.deltaURLs, store.keySet())
	}
}

func TestSharePointStorageLimitStopsTheRun(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {fail: &quota.ErrStorageQuota{Method: "outlook_sharepoint", MidRun: true}},
			spLegal:   {baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	if _, err := syncSharePointSite(context.Background(), spInput(), spSite(nil)); !quota.IsStorageQuota(err) {
		t.Fatalf("err = %v", err)
	}
	if len(store.objects) != 0 {
		t.Fatalf("libraries synced after a storage stop: %v", store.keySet())
	}
}

func TestSharePointQuotaSessionIsSharedAcrossLibraries(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	q := &driveQuotaSession{ok: true, remaining: quota.RequiredBytes(4)}
	runSite(t, spInput(), q)
	if len(g.downloads) != 1 || q.skippedQuota != 1 {
		t.Fatalf("downloads=%v skipped=%d", g.downloads, q.skippedQuota)
	}
}

func TestSharePointListFailureSyncsKnownLibrariesWithoutRemoval(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	runSite(t, in, nil)

	for _, listErr := range []error{
		errGraphForbidden,
		errors.New("graph http 429 TooManyRequests"),
		errors.New("graph http 503 serviceNotAvailable"),
		fmt.Errorf("list sharepoint libraries: %w: nextLink to another resource", outlook.ErrTeamsPaging),
	} {
		g.listErr = listErr
		g.deltaURLs = nil
		res := runSite(t, in, nil)
		if res.listErr == nil || !strings.Contains(res.warning(), "library list could not be read") {
			t.Fatalf("%v: result %+v", listErr, res)
		}
		if !containsAll(g.deltaURLs, spCursor(spDefault), spCursor(spLegal)) {
			t.Fatalf("%v: known libraries not synced: %v", listErr, g.deltaURLs)
		}
		if st := in.Job.TaskMemory.SharePointLibraries[spLegal]; st.RemovedAt != "" {
			t.Fatalf("%v: library removed after a failed listing", listErr)
		}
		for k := range store.objects {
			if strings.Contains(k, "/BIN/") {
				t.Fatalf("%v: moved to bin after a failed listing: %s", listErr, k)
			}
		}
	}
}

func TestSharePointRemovedLibraryMovesToBinOnceAndReturns(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal: {baseline: []outlook.OneDriveItem{
				driveFolder("LF", "ROOT", "Contracts"),
				driveFile("L1", "LF", "nda.pdf", "c1"),
			}},
		},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	runSite(t, in, nil)
	legalPrefix := outlook.SharePointLibraryPrefix(spSitePrefix, spLegal)
	liveKey := outlook.OneDriveFileKey(legalPrefix, outlook.OneDriveSectionMyDrive, []string{"LF"}, "L1", "nda.pdf")
	if _, ok := store.objects[liveKey]; !ok {
		t.Fatalf("legal file missing: %v", store.keySet())
	}

	// A complete listing without the library moves its backup to BIN.
	g.libraries = spLibs(spDefault)
	runSite(t, in, nil)
	binKey := spBinKey(legalPrefix, "L1", "nda.pdf")
	if o, ok := store.objects[binKey]; !ok || o.meta[outlook.OneDriveMetaRemovedAt] == "" {
		t.Fatalf("legal file not in bin: %v", store.keySet())
	}
	if _, ok := store.objects[liveKey]; ok {
		t.Fatal("legal file left in MY_DRIVE")
	}
	if _, ok := store.objects[outlook.OneDriveFolderKey(legalPrefix, nil, "LF", "Contracts")]; ok {
		t.Fatal("legal folder placeholder left")
	}
	if _, ok := store.objects[spFileKey(spSitePrefix, "D1", "plan.docx")]; !ok {
		t.Fatal("default library touched by the removal")
	}
	st := in.Job.TaskMemory.SharePointLibraries[spLegal]
	if st.RemovedAt == "" || st.DeltaLink != nil {
		t.Fatalf("removed state %+v", st)
	}

	// Removal is idempotent.
	copies := len(store.copies)
	removedAt := st.RemovedAt
	runSite(t, in, nil)
	if len(store.copies) != copies || in.Job.TaskMemory.SharePointLibraries[spLegal].RemovedAt != removedAt {
		t.Fatalf("second removal changed the backup: copies %d -> %d", copies, len(store.copies))
	}

	// The library comes back: a new baseline returns the unchanged file from BIN.
	g.libraries = spLibs(spDefault, spLegal)
	g.downloads = nil
	runSite(t, in, nil)
	if _, ok := store.objects[liveKey]; !ok {
		t.Fatalf("legal file not restored to MY_DRIVE: %v", store.keySet())
	}
	if _, ok := store.objects[binKey]; ok {
		t.Fatal("bin copy left after the library returned")
	}
	if len(g.downloads) != 0 {
		t.Fatalf("unchanged file downloaded again: %v", g.downloads)
	}
	st = in.Job.TaskMemory.SharePointLibraries[spLegal]
	if st.RemovedAt != "" || st.DeltaLink == nil || *st.DeltaLink != spCursor(spLegal) {
		t.Fatalf("returned state %+v", st)
	}
}

func TestSharePointNoRemovalWhenListingLacksDefaultLibrary(t *testing.T) {
	store := newMemDriveStore()
	g := &spGraph{
		libraries: spLibs(spDefault, spLegal),
		drives: map[string]*spDrive{
			spDefault: {baseline: []outlook.OneDriveItem{driveFile("D1", "ROOT", "plan.docx", "c1")}},
			spLegal:   {baseline: []outlook.OneDriveItem{driveFile("L1", "ROOT", "nda.pdf", "c1")}},
		},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	runSite(t, in, nil)
	g.libraries = nil
	runSite(t, in, nil)
	if in.Job.TaskMemory.SharePointLibraries[spLegal].RemovedAt != "" {
		t.Fatal("library removed by a listing that lacks the default library")
	}
	if _, ok := store.objects[spFileKey(outlook.SharePointLibraryPrefix(spSitePrefix, spLegal), "L1", "nda.pdf")]; !ok {
		t.Fatalf("legal file moved: %v", store.keySet())
	}
}

func TestSharePointLegacyJobKeepsDefaultCursor(t *testing.T) {
	store := newMemDriveStore()
	defaultKey := spFileKey(spSitePrefix, "D1", "plan.docx")
	store.objects[defaultKey] = memDriveObject{data: []byte("v:c1"), meta: map[string]string{outlook.OneDriveMetaCTag: "c1"}}
	g := &spGraph{
		libraries: spLibs(spDefault),
		drives:    map[string]*spDrive{spDefault: {incremental: []outlook.OneDriveItem{driveFile("D2", "ROOT", "new.docx", "c1")}}},
	}
	stubSharePoint(t, g, store)
	in := spInput()
	link := spCursor(spDefault)
	in.Job.TaskMemory = repo.TaskMemory{SharePointDeltaLink: &link, SharePointLayout: repo.OneDriveLayoutTree, SharePointBaselineDone: true}
	runSite(t, in, nil)
	if len(g.deltaURLs) != 1 || g.deltaURLs[0] != link {
		t.Fatalf("legacy job did not continue its cursor: %v", g.deltaURLs)
	}
	if _, ok := store.objects[defaultKey]; !ok {
		t.Fatal("existing default key moved")
	}
	if _, ok := store.objects[spFileKey(spSitePrefix, "D2", "new.docx")]; !ok {
		t.Fatalf("new file missing: %v", store.keySet())
	}
}

func TestSharePointRunLibrariesOrder(t *testing.T) {
	known := map[string]repo.SharePointLibraryState{
		spHR:    {Name: "HR"},
		spLegal: {Name: "Legal", RemovedAt: "2026-01-01T00:00:00Z"},
	}
	got := sharePointRunLibraries(spDefault, nil, known, false)
	if len(got) != 2 || got[0].ID != spDefault || got[1].ID != spHR {
		t.Fatalf("known libraries %+v", got)
	}
	got = sharePointRunLibraries(spDefault, spLibs(spLegal, spDefault), known, true)
	if len(got) != 2 || got[0].ID != spDefault || got[0].Name != "Documents" || got[1].ID != spLegal {
		t.Fatalf("listed libraries %+v", got)
	}
}

// spCursor is the deltaLink the stub Graph returns for a library.
func spCursor(driveID string) string {
	return outlook.DriveRootURLFromDriveID(driveID) + "/root/delta?token=" + driveID
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
