package restore

import (
	"testing"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
)

func TestDriveProcessor_ShouldRestoreKey(t *testing.T) {
	p := &driveProcessor{}
	cases := []struct {
		key  string
		want bool
	}{
		{key: "alice@x.com/MY_DRIVE/F1/ABC123$auth.pdf", want: true},
		{key: "alice@x.com/MY_DRIVE/F1/F2/.folder__Work", want: true},
		{key: "alice@x.com/SHARED_DRIVE~0ACx/S1/.shared_drive__test", want: false},
		{key: "alice@x.com/data/2026/07/21/ABC123_report.pdf", want: false},
		{key: "alice@x.com/meta/2026/07/21/ABC123_report.pdf.json", want: true}, // meta is restored; pulls data via meta
		{key: "alice@x.com/MY_DRIVE/F1/.file_placeholder", want: false},
		{key: "", want: false},
	}
	for _, tc := range cases {
		got := p.ShouldRestoreKey(tc.key)
		if got != tc.want {
			t.Fatalf("ShouldRestoreKey(%q)=%v want %v (folder=%v tree=%v data=%v meta=%v)",
				tc.key, got, tc.want,
				google.IsDriveFolderPlaceholderKey(tc.key),
				google.IsDriveTreeObjectKey(tc.key),
				google.IsDriveIDBasedDataKey(tc.key),
				google.IsDriveIDBasedMetaKey(tc.key),
			)
		}
	}
}
