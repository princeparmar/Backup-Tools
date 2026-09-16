package google

import (
	"strings"
	"testing"
)

func TestBuildAndParseDriveFileKey(t *testing.T) {
	key := BuildDriveObjectKey(
		"alice@x.com",
		[]string{DriveSectionMyDrive},
		[]string{"F1", "F2", "F3"},
		"ABC123",
		"auth.pdf",
		"application/pdf",
		false,
		"",
	)
	want := "alice@x.com/MY_DRIVE/F1/F2/F3/ABC123$auth.pdf"
	if key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
	p, ok := ParseDriveObjectKey(key)
	if !ok {
		t.Fatal("parse failed")
	}
	if p.FileID != "ABC123" || p.Name != "auth.pdf" || p.IsFolder {
		t.Fatalf("parsed = %+v", p)
	}
	if len(p.ParentIDs) != 3 || p.ParentIDs[0] != "F1" || p.ParentIDs[2] != "F3" {
		t.Fatalf("parents = %v", p.ParentIDs)
	}
}

func TestBuildAndParseDriveFileKey_UnderscoreInFileID(t *testing.T) {
	key := BuildDriveObjectKey(
		"alice@x.com",
		[]string{DriveSectionMyDrive},
		[]string{"F1"},
		"1AbC_DEF_123",
		"report_final.pdf",
		"application/pdf",
		false,
		"",
	)
	want := "alice@x.com/MY_DRIVE/F1/1AbC_DEF_123$report_final.pdf"
	if key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
	p, ok := ParseDriveObjectKey(key)
	if !ok {
		t.Fatal("parse failed")
	}
	if p.FileID != "1AbC_DEF_123" || p.Name != "report_final.pdf" {
		t.Fatalf("parsed = %+v", p)
	}
}

func TestSplitDriveLeaf_DollarEscaping(t *testing.T) {
	cases := []struct {
		leaf     string
		wantID   string
		wantName string
	}{
		{leaf: "ABC123$report.pdf", wantID: "ABC123", wantName: "report.pdf"},
		{leaf: "ABC$$123$report.pdf", wantID: "ABC$123", wantName: "report.pdf"},
		{leaf: "ABC123$price$$2026.pdf", wantID: "ABC123", wantName: "price$2026.pdf"},
		{leaf: "ABC$$123$price$$2026$$.pdf", wantID: "ABC$123", wantName: "price$2026$.pdf"},
	}
	for _, tc := range cases {
		id, name, ok := splitDriveLeaf(tc.leaf)
		if !ok {
			t.Fatalf("splitDriveLeaf(%q) failed", tc.leaf)
		}
		if id != tc.wantID || name != tc.wantName {
			t.Fatalf("splitDriveLeaf(%q) = (%q,%q), want (%q,%q)", tc.leaf, id, name, tc.wantID, tc.wantName)
		}
	}

	built := DriveFileLeaf("ABC$123", "price$2026$.pdf", "application/pdf", "")
	if built != "ABC$$123$price$$2026$$.pdf" {
		t.Fatalf("DriveFileLeaf = %q", built)
	}
	id, name, ok := splitDriveLeaf(built)
	if !ok || id != "ABC$123" || name != "price$2026$.pdf" {
		t.Fatalf("round-trip = (%q,%q) ok=%v", id, name, ok)
	}
}

func TestParseDriveFileKey_RejectsLegacyFormats(t *testing.T) {
	legacy := []string{
		"alice@x.com/MY_DRIVE/F1/ABC123_auth.pdf",
		"alice@x.com/MY_DRIVE/1AbC_DEF_123__name__report.pdf",
	}
	for _, key := range legacy {
		if _, ok := ParseDriveObjectKey(key); ok {
			t.Fatalf("legacy key should not parse: %q", key)
		}
	}
}

func TestBuildAndParseDriveFolderKey(t *testing.T) {
	key := BuildDriveObjectKey(
		"alice@x.com",
		[]string{DriveSectionMyDrive},
		[]string{"F1"},
		"F2",
		"CyberLS",
		"application/vnd.google-apps.folder",
		true,
		"",
	)
	if !strings.HasSuffix(key, "/F2/.folder__CyberLS") {
		t.Fatalf("folder key = %q", key)
	}
	p, ok := ParseDriveObjectKey(key)
	if !ok || !p.IsFolder || p.FileID != "F2" || p.Name != "CyberLS" {
		t.Fatalf("parsed = %+v ok=%v", p, ok)
	}
	if len(p.ParentIDs) != 1 || p.ParentIDs[0] != "F1" {
		t.Fatalf("parents = %v", p.ParentIDs)
	}
}

func TestDriveSectionsCanonicalAndBinExclusive(t *testing.T) {
	seg := DriveSectionsSegment([]string{DriveSectionSharedWithMe, DriveSectionMyDrive})
	if seg != "MY_DRIVE^SHARED_WITH_ME" {
		t.Fatalf("seg = %q", seg)
	}
	seg = DriveSectionsSegment([]string{DriveSectionMyDrive, DriveSectionBin})
	if seg != DriveSectionBin {
		t.Fatalf("bin exclusive: %q", seg)
	}
}

func TestDriveShortcutLeaf(t *testing.T) {
	key := BuildDriveObjectKey(
		"alice@x.com",
		[]string{DriveSectionMyDrive},
		[]string{"F1"},
		"S555",
		"report-shortcut",
		"application/vnd.google-apps.shortcut",
		false,
		"ABC123",
	)
	if !strings.Contains(key, "S555__to__ABC123$") {
		t.Fatalf("shortcut leaf missing markers: %q", key)
	}
	p, ok := ParseDriveObjectKey(key)
	if !ok || p.FileID != "S555" || p.ShortcutTarget != "ABC123" {
		t.Fatalf("parsed = %+v ok=%v", p, ok)
	}
}

func TestFindSyncedDriveKeyByFileIDExact(t *testing.T) {
	synced := map[string]bool{
		"alice@x.com/MY_DRIVE/F1/ABC123$report.pdf": true,
		"alice@x.com/MY_DRIVE/F1/ABC12$other.pdf":   true,
		"alice@x.com/MY_DRIVE/F1/F2/.folder__Work":  true,
	}
	got := FindSyncedDriveKeyByFileID(synced, "ABC12")
	if got != "alice@x.com/MY_DRIVE/F1/ABC12$other.pdf" {
		t.Fatalf("got %q", got)
	}
	if FindSyncedDriveKeyByFileID(synced, "ABC") != "" {
		t.Fatal("substring must not match")
	}
}

func TestParseRejectsMetaDataLayout(t *testing.T) {
	if _, ok := ParseDriveObjectKey("alice@x.com/meta/2026/07/21/ABC123_report.pdf.json"); ok {
		t.Fatal("meta key should not parse as tree key")
	}
	if _, ok := ParseDriveObjectKey("alice@x.com/data/2026/07/21/ABC123_report.pdf"); ok {
		t.Fatal("data key should not parse as tree key")
	}
}

func TestDriveStarredSectionJoins(t *testing.T) {
	seg := DriveSectionsSegment([]string{DriveSectionMyDrive, DriveSectionStarred})
	if seg != "MY_DRIVE^STARRED" {
		t.Fatalf("seg = %q", seg)
	}
	key := BuildDriveObjectKey(
		"alice@x.com",
		[]string{DriveSectionMyDrive, DriveSectionStarred},
		nil,
		"F1",
		"reeee",
		"application/vnd.google-apps.folder",
		true,
		"",
	)
	if !strings.HasPrefix(key, "alice@x.com/MY_DRIVE^STARRED/") {
		t.Fatalf("starred key = %q", key)
	}
}
