package outlook

import (
	"reflect"
	"testing"
)

func TestOneDriveFileAndFolderKeys(t *testing.T) {
	prefix := ResourceKeyPrefix(mailTenant, "user", "oid-1")
	if got, want := OneDriveFileKey(prefix+"/", OneDriveSectionMyDrive, []string{"F1", "", "F2"}, "ITEM!1", "Q3 $plan.xlsx"),
		prefix+"/MY_DRIVE/F1/F2/ITEM!1$Q3 $$plan.xlsx"; got != want {
		t.Fatalf("file key:\n got %s\nwant %s", got, want)
	}
	if got, want := OneDriveFileKey(prefix, OneDriveSectionBin, []string{"F1"}, "ITEM1", "a/b.txt"),
		prefix+"/BIN/ITEM1$a_b.txt"; got != want {
		t.Fatalf("bin key:\n got %s\nwant %s", got, want)
	}
	if got, want := OneDriveFolderKey(prefix, []string{"F1"}, "F2", "Reports"),
		prefix+"/MY_DRIVE/F1/F2/.folder__Reports"; got != want {
		t.Fatalf("folder key:\n got %s\nwant %s", got, want)
	}
	if got, want := OneDriveFolderContentsPrefix(prefix, []string{"F1"}, "F2"), prefix+"/MY_DRIVE/F1/F2/"; got != want {
		t.Fatalf("contents prefix: got %s want %s", got, want)
	}
}

func TestParseOneDriveObjectKey(t *testing.T) {
	prefix := ResourceKeyPrefix(mailTenant, "user", "oid-1")
	tests := []struct {
		name string
		key  string
		want OneDriveObjectKey
		ok   bool
	}{
		{
			name: "file in folders",
			key:  OneDriveFileKey(prefix, OneDriveSectionMyDrive, []string{"F1", "F2"}, "I$1", "a $b.txt"),
			want: OneDriveObjectKey{Prefix: prefix, Section: OneDriveSectionMyDrive, ParentIDs: []string{"F1", "F2"}, ItemID: "I$1", Name: "a $b.txt"},
			ok:   true,
		},
		{
			name: "file at root",
			key:  OneDriveFileKey(prefix, OneDriveSectionMyDrive, nil, "I1", "a.txt"),
			want: OneDriveObjectKey{Prefix: prefix, Section: OneDriveSectionMyDrive, ParentIDs: []string{}, ItemID: "I1", Name: "a.txt"},
			ok:   true,
		},
		{
			name: "folder",
			key:  OneDriveFolderKey(prefix, []string{"F1"}, "F2", "Reports"),
			want: OneDriveObjectKey{Prefix: prefix, Section: OneDriveSectionMyDrive, ParentIDs: []string{"F1"}, ItemID: "F2", Name: "Reports", IsFolder: true},
			ok:   true,
		},
		{
			name: "bin",
			key:  OneDriveFileKey(prefix, OneDriveSectionBin, nil, "I1", "a.txt"),
			want: OneDriveObjectKey{Prefix: prefix, Section: OneDriveSectionBin, ParentIDs: []string{}, ItemID: "I1", Name: "a.txt"},
			ok:   true,
		},
		{name: "bin with folders", key: prefix + "/BIN/F1/I1$a.txt"},
		{name: "folder in bin", key: prefix + "/BIN/F1/.folder__x"},
		{name: "legacy meta", key: prefix + "/meta/2026/07/21/I1_a.txt.json"},
		{name: "two separators", key: prefix + "/MY_DRIVE/I1$a$b"},
		{name: "no separator", key: prefix + "/MY_DRIVE/I1"},
		{name: "placeholder", key: prefix + "/.file_placeholder"},
		{name: "no tenant", key: "a@b.com/MY_DRIVE/I1$a.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseOneDriveObjectKey(tt.key)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tt.ok, got)
			}
			if ok && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseOneDriveLegacyKey(t *testing.T) {
	prefix := ResourceKeyPrefix(mailTenant, "user", "oid-1")
	meta := prefix + "/meta/2026/07/21/01ABC_my_report.pdf.json"
	got, ok := ParseOneDriveLegacyKey(meta)
	if !ok || got != (OneDriveLegacyKey{Prefix: prefix, ItemID: "01ABC", IsMeta: true}) {
		t.Fatalf("meta: %+v %v", got, ok)
	}
	data := OneDriveLegacyDataKey(meta)
	if data != prefix+"/data/2026/07/21/01ABC_my_report.pdf" {
		t.Fatalf("data key %s", data)
	}
	if got, ok := ParseOneDriveLegacyKey(data); !ok || got.IsMeta || got.ItemID != "01ABC" {
		t.Fatalf("data: %+v %v", got, ok)
	}
	for _, bad := range []string{
		prefix + "/meta/2026/07/21/01ABC_a.pdf",
		prefix + "/meta/26/07/21/01ABC_a.json",
		prefix + "/MY_DRIVE/01ABC$a.pdf",
	} {
		if _, ok := ParseOneDriveLegacyKey(bad); ok {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestOneDriveContentUnchanged(t *testing.T) {
	item := &OneDriveItem{ID: "I", CTag: "c2", ETag: "e2", Size: 10}
	if !OneDriveContentUnchanged(map[string]string{OneDriveMetaCTag: "c2"}, item) {
		t.Fatal("same ctag")
	}
	if OneDriveContentUnchanged(map[string]string{OneDriveMetaCTag: "c1", OneDriveMetaETag: "e2", OneDriveMetaSize: "10"}, item) {
		t.Fatal("ctag differs")
	}
	noCTag := &OneDriveItem{ID: "I", ETag: "e2", Size: 10}
	if !OneDriveContentUnchanged(map[string]string{OneDriveMetaETag: "e2", OneDriveMetaSize: "10"}, noCTag) {
		t.Fatal("etag and size match")
	}
	if OneDriveContentUnchanged(map[string]string{}, item) {
		t.Fatal("no stored tags")
	}
}

func TestOneDriveObjectMeta(t *testing.T) {
	file := OneDriveObjectMeta(&OneDriveItem{ID: "I", Name: "a.txt", Size: 5, CTag: "c", MimeType: "text/plain"}, "A/B")
	want := map[string]string{
		OneDriveMetaItemID: "I", OneDriveMetaName: "a.txt", OneDriveMetaParentPath: "A/B",
		OneDriveMetaMimeType: "text/plain", OneDriveMetaCTag: "c", OneDriveMetaSize: "5",
	}
	if !reflect.DeepEqual(file, want) {
		t.Fatalf("file meta %v", file)
	}
	folder := OneDriveObjectMeta(&OneDriveItem{ID: "F", Name: "A", IsFolder: true, CTag: "c"}, "")
	if folder[OneDriveMetaIsFolder] != "true" || folder[OneDriveMetaCTag] != "" {
		t.Fatalf("folder meta %v", folder)
	}
	removed := OneDriveRemovedMeta(file, "2026-01-01T00:00:00Z")
	if removed[OneDriveMetaRemovedAt] != "2026-01-01T00:00:00Z" || file[OneDriveMetaRemovedAt] != "" {
		t.Fatalf("removed meta %v (source changed: %v)", removed, file)
	}
}

func TestOneDriveParentPathFromGraph(t *testing.T) {
	for in, want := range map[string]string{
		"/drive/root:":                  "",
		"/drive/root:/Docs/My%20Folder": "Docs/My Folder",
		"/drives/b!x/root:/A/":          "A",
	} {
		if got := OneDriveParentPathFromGraph(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
