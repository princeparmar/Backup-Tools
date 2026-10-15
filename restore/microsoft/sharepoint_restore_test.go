package microsoft

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

func TestShouldRestoreSharePointLibraryKeys(t *testing.T) {
	t.Parallel()
	const site = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/site/contoso.sharepoint.com_s1_w1"
	legal := outlook.SharePointLibraryPrefix(site, "b!legal")
	p := &outlookSharePointProcessor{}
	tests := []struct {
		key  string
		want bool
	}{
		{key: site + "/MY_DRIVE/F1/ITEM1$plan.docx", want: true},
		{key: legal + "/MY_DRIVE/F1/ITEM2$nda.pdf", want: true},
		{key: legal + "/MY_DRIVE/F1/.folder__Contracts", want: true},
		{key: legal + "/BIN/ITEM3$old.pdf", want: true},
		{key: site + "/meta/2026/01/01/ITEM4_a.txt.json", want: true},
		{key: site + "/data/2026/01/01/ITEM4_a.txt", want: false},
		{key: legal + "/.file_placeholder", want: false},
		{key: site + "/.file_placeholder", want: false},
	}
	for _, tt := range tests {
		if got := p.ShouldRestoreKey(tt.key); got != tt.want {
			t.Fatalf("%s: got %v want %v", tt.key, got, tt.want)
		}
	}
	// Library keys go to the tree-layout restore, which restores into the object's drive-id.
	if parsed, ok := outlook.ParseOneDriveObjectKey(legal + "/MY_DRIVE/F1/ITEM2$nda.pdf"); !ok || parsed.Prefix != legal {
		t.Fatalf("library key not routed to the library restore: %+v %v", parsed, ok)
	}
}
