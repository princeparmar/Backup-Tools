package outlook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeDriveObject struct {
	meta map[string]string
	data []byte
}

func fakeDriveSource(objects map[string]fakeDriveObject, names map[string]string) OneDriveRestoreSource {
	return OneDriveRestoreSource{
		Stat: func(_ context.Context, key string) (map[string]string, int64, error) {
			o, ok := objects[key]
			if !ok {
				return nil, 0, errors.New("not found")
			}
			return o.meta, int64(len(o.data)), nil
		},
		DownloadTo: func(_ context.Context, key string, w io.Writer) error {
			o, ok := objects[key]
			if !ok {
				return errors.New("not found")
			}
			_, err := w.Write(o.data)
			return err
		},
		FolderNames: names,
	}
}

type graphCall struct {
	method, path, query string
	body                []byte
}

type fakeDriveGraph struct {
	mu    sync.Mutex
	calls []graphCall
	srv   *httptest.Server
}

func newFakeDriveGraph(t *testing.T) *fakeDriveGraph {
	g := &fakeDriveGraph{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.calls = append(g.calls, graphCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: body})
		g.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/createUploadSession") {
			_ = json.NewEncoder(w).Encode(map[string]string{"uploadUrl": g.srv.URL + "/upload/session1"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeDriveGraph) userBase() string { return g.srv.URL + "/me" }

var testDrivePrefix = ResourceKeyPrefix(mailTenant, "user", "u1")

func TestRestoreOneDriveBackupFileIntoOriginalFolder(t *testing.T) {
	g := newFakeDriveGraph(t)
	key := OneDriveFileKey(testDrivePrefix, OneDriveSectionMyDrive, []string{"f1", "f2"}, "i1", "report.txt")
	src := fakeDriveSource(map[string]fakeDriveObject{
		key: {meta: map[string]string{OneDriveMetaName: "report.txt", OneDriveMetaParentPath: "Old/Path"}, data: []byte("hello")},
	}, map[string]string{"f1": "Docs", "f2": "Work 2026"})

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), key, src); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 1 {
		t.Fatalf("calls = %+v", g.calls)
	}
	c := g.calls[0]
	if c.method != http.MethodPut || c.path != "/me/drive/root:/Docs/Work 2026/report.txt:/content" || !strings.Contains(c.query, "conflictBehavior=rename") {
		t.Fatalf("call = %s %s?%s", c.method, c.path, c.query)
	}
	if string(c.body) != "hello" {
		t.Fatalf("body = %q", c.body)
	}
}

func TestRestoreOneDriveBackupFallsBackToSavedPath(t *testing.T) {
	g := newFakeDriveGraph(t)
	key := OneDriveFileKey(testDrivePrefix, OneDriveSectionMyDrive, []string{"f1", "gone"}, "i1", "a.txt")
	src := fakeDriveSource(map[string]fakeDriveObject{
		key: {meta: map[string]string{OneDriveMetaName: "a.txt", OneDriveMetaParentPath: "Docs/Saved"}, data: []byte("x")},
	}, map[string]string{"f1": "Docs"})

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), key, src); err != nil {
		t.Fatal(err)
	}
	if got := g.calls[0].path; got != "/me/drive/root:/Docs/Saved/a.txt:/content" {
		t.Fatalf("path = %s", got)
	}
}

func TestRestoreOneDriveBackupBinUsesDeletedFromPath(t *testing.T) {
	g := newFakeDriveGraph(t)
	key := OneDriveFileKey(testDrivePrefix, OneDriveSectionBin, nil, "i1", "old.txt")
	src := fakeDriveSource(map[string]fakeDriveObject{
		key: {meta: map[string]string{OneDriveMetaName: "old.txt", OneDriveMetaParentPath: "Archive"}, data: []byte("x")},
	}, nil)

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), key, src); err != nil {
		t.Fatal(err)
	}
	if got := g.calls[0].path; got != "/me/drive/root:/Archive/old.txt:/content" {
		t.Fatalf("path = %s", got)
	}
}

func TestRestoreOneDriveBackupFolderCreatesPath(t *testing.T) {
	g := newFakeDriveGraph(t)
	key := OneDriveFolderKey(testDrivePrefix, []string{"f1"}, "f2", "Work")
	src := fakeDriveSource(map[string]fakeDriveObject{
		key: {meta: map[string]string{OneDriveMetaName: "Work", OneDriveMetaIsFolder: "true"}},
	}, map[string]string{"f1": "Docs", "f2": "Work"})

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), key, src); err != nil {
		t.Fatal(err)
	}
	want := []string{"/me/drive/root/children", "/me/drive/root:/Docs:/children"}
	if len(g.calls) != len(want) {
		t.Fatalf("calls = %+v", g.calls)
	}
	for i, p := range want {
		if g.calls[i].method != http.MethodPost || g.calls[i].path != p {
			t.Fatalf("call %d = %s %s, want POST %s", i, g.calls[i].method, g.calls[i].path, p)
		}
	}
}

func TestRestoreOneDriveBackupLargeFileUsesUploadSession(t *testing.T) {
	g := newFakeDriveGraph(t)
	key := OneDriveFileKey(testDrivePrefix, OneDriveSectionMyDrive, nil, "i1", "big.bin")
	data := bytes.Repeat([]byte("a"), oneDriveSimpleUploadMax+1)
	src := fakeDriveSource(map[string]fakeDriveObject{
		key: {meta: map[string]string{OneDriveMetaName: "big.bin"}, data: data},
	}, nil)

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), key, src); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 2 || g.calls[0].path != "/me/drive/root:/big.bin:/createUploadSession" || g.calls[1].path != "/upload/session1" {
		t.Fatalf("calls = %d", len(g.calls))
	}
	if !bytes.Equal(g.calls[1].body, data) {
		t.Fatalf("uploaded %d bytes, want %d", len(g.calls[1].body), len(data))
	}
}

func TestRestoreOneDriveBackupLegacyMeta(t *testing.T) {
	g := newFakeDriveGraph(t)
	metaKey := testDrivePrefix + "/meta/2026/01/02/i1_notes.txt.json"
	dataKey := OneDriveLegacyDataKey(metaKey)
	metaJSON, _ := json.Marshal(OneDriveCronBackupMeta{Name: "notes.txt", ParentPath: "/drive/root:/My%20Notes", DataObjectKey: dataKey})
	src := fakeDriveSource(map[string]fakeDriveObject{
		metaKey: {data: metaJSON},
		dataKey: {data: []byte("legacy")},
	}, nil)

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), metaKey, src); err != nil {
		t.Fatal(err)
	}
	if got := g.calls[0].path; got != "/me/drive/root:/My Notes/notes.txt:/content" {
		t.Fatalf("path = %s", got)
	}
	if string(g.calls[0].body) != "legacy" {
		t.Fatalf("body = %q", g.calls[0].body)
	}
}

func TestRestoreOneDriveBackupLegacyTombstoneSkipped(t *testing.T) {
	g := newFakeDriveGraph(t)
	metaKey := testDrivePrefix + "/meta/2026/01/02/i1_gone.txt.json"
	metaJSON, _ := json.Marshal(OneDriveCronBackupMeta{Name: "gone.txt", RemovedFromOneDrive: true})
	src := fakeDriveSource(map[string]fakeDriveObject{metaKey: {data: metaJSON}}, nil)

	if err := RestoreOneDriveBackup(context.Background(), "tok", g.userBase(), metaKey, src); err == nil {
		t.Fatal("want skip error")
	}
	if len(g.calls) != 0 {
		t.Fatalf("calls = %+v", g.calls)
	}
}

func TestIsOneDriveRestoreKey(t *testing.T) {
	metaKey := testDrivePrefix + "/meta/2026/01/02/i1_a.txt.json"
	cases := map[string]bool{
		OneDriveFileKey(testDrivePrefix, OneDriveSectionMyDrive, nil, "i1", "a.txt"): true,
		OneDriveFileKey(testDrivePrefix, OneDriveSectionBin, nil, "i1", "a.txt"):     true,
		OneDriveFolderKey(testDrivePrefix, nil, "f1", "Docs"):                        true,
		metaKey:                          true,
		OneDriveLegacyDataKey(metaKey):   false,
		testDrivePrefix + "/random.json": false,
	}
	for key, want := range cases {
		if got := IsOneDriveRestoreKey(key); got != want {
			t.Errorf("IsOneDriveRestoreKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestOneDriveFolderNames(t *testing.T) {
	keys := []string{
		OneDriveFolderKey(testDrivePrefix, nil, "f1", "Docs"),
		OneDriveFolderKey(testDrivePrefix, []string{"f1"}, "f2", "Work"),
		OneDriveFileKey(testDrivePrefix, OneDriveSectionMyDrive, []string{"f1"}, "i1", "a.txt"),
	}
	got := OneDriveFolderNames(keys)
	if len(got) != 2 || got["f1"] != "Docs" || got["f2"] != "Work" {
		t.Fatalf("names = %v", got)
	}
}

func TestRestoreSharePointBackupUsesRecordedDrive(t *testing.T) {
	g := newFakeDriveGraph(t)
	prev := graphBaseURL
	graphBaseURL = g.srv.URL
	t.Cleanup(func() { graphBaseURL = prev })

	sitePrefix := ResourceKeyPrefix(mailTenant, "site", "s1")
	key := OneDriveFileKey(sitePrefix, OneDriveSectionMyDrive, []string{"f1"}, "i1", "plan.docx")
	src := fakeDriveSource(map[string]fakeDriveObject{
		key: {meta: map[string]string{OneDriveMetaName: "plan.docx", OneDriveMetaDriveID: "b!lib"}, data: []byte("doc")},
	}, map[string]string{"f1": "Shared Documents"})

	if err := RestoreSharePointBackup(context.Background(), "tok", key, src); err != nil {
		t.Fatal(err)
	}
	if got := g.calls[0].path; got != "/drives/b!lib/root:/Shared Documents/plan.docx:/content" {
		t.Fatalf("path = %s", got)
	}

	noDrive := OneDriveFileKey(sitePrefix, OneDriveSectionMyDrive, nil, "i2", "x.txt")
	src = fakeDriveSource(map[string]fakeDriveObject{noDrive: {meta: map[string]string{}, data: []byte("x")}}, nil)
	if err := RestoreSharePointBackup(context.Background(), "tok", noDrive, src); err == nil {
		t.Fatal("a backup without a drive id must not restore")
	}
}
