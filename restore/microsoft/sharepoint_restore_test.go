package microsoft

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
)

func TestShouldRestoreSharePointLibraryKeys(t *testing.T) {
	t.Parallel()
	const site = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/site/contoso.sharepoint.com_s1_w1"
	legal := outlook.SharePointLibraryPrefix(site, "b!legal")
	p := outlookSharePointProcessor()
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

func TestSharePointDestinationDrive(t *testing.T) {
	prevDefault, prevEnsure := sharePointDefaultDriveFn, sharePointEnsureLibraryFn
	t.Cleanup(func() { sharePointDefaultDriveFn, sharePointEnsureLibraryFn = prevDefault, prevEnsure })
	sharePointDefaultDriveFn = func(context.Context, string, string) (string, error) { return "drive-default", nil }
	var ensured []string
	sharePointEnsureLibraryFn = func(_ context.Context, _, _, name string) (string, error) {
		ensured = append(ensured, name)
		return "drive-" + name, nil
	}
	var cache sync.Map
	id, err := sharePointDestinationDrive(t.Context(), "tok", "site-1", "b!docs", true, "Documents", &cache)
	if err != nil || id != "drive-default" {
		t.Fatalf("default library: %q %v", id, err)
	}
	id, err = sharePointDestinationDrive(t.Context(), "tok", "site-1", "b!legal", false, "Legal", &cache)
	if err != nil || id != "drive-Legal" || len(ensured) != 1 || ensured[0] != "Legal" {
		t.Fatalf("named library: %q %v ensured=%v", id, err, ensured)
	}
	id, err = sharePointDestinationDrive(t.Context(), "tok", "site-1", "b!legal", false, "Legal", &cache)
	if err != nil || id != "drive-Legal" || len(ensured) != 1 {
		t.Fatalf("named library must be created once: %q %v ensured=%v", id, err, ensured)
	}
	id, err = sharePointDestinationDrive(t.Context(), "tok", "site-1", "b!unknown", false, "", &cache)
	if err != nil || id != "drive-Restored b!unknown" || ensured[len(ensured)-1] != "Restored b!unknown" {
		t.Fatalf("unnamed library: %q %v ensured=%v", id, err, ensured)
	}
}

func TestSharePointWriteSiteAndWorkspaceErr(t *testing.T) {
	t.Parallel()
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const graph = "space2.sharepoint.com,edf2e428-d991-4aa0-9c11-aaaaaaaaaaaa,bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const vault = "space2.sharepoint.com_edf2e428-d991-4aa0-9c11-aaaaaaaaaaaa_bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	key := outlook.ResourceKeyPrefix(tenant, "site", graph) + "/lists/Tasks_1/42.json"
	tests := []struct {
		name string
		deps *restore.RestoreDeps
		key  string
		want string
	}{
		{
			name: "in place from vault key",
			deps: &restore.RestoreDeps{Job: &repo.RestoreJobListingDB{Method: "outlook_sharepoint", LoginID: vault}},
			key:  key,
			want: graph,
		},
		{
			name: "migration dest unsanitized",
			deps: &restore.RestoreDeps{Job: &repo.RestoreJobListingDB{
				Method: "outlook_sharepoint", LoginID: "other-site", TargetEmail: vault,
			}},
			key:  key,
			want: graph,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sharePointWriteSite(tt.deps, tt.key); got != tt.want {
				t.Fatalf("site=%q want %q", got, tt.want)
			}
		})
	}

	errTests := []struct {
		name string
		err  error
		skip bool
	}{
		{name: "nil", skip: true},
		{name: "tombstone", err: errObjectSkipped, skip: true},
		{name: "forbidden", err: fmt.Errorf("create list item http 403: accessDenied"), skip: true},
		{name: "exists", err: fmt.Errorf("create site page http 409: name exists"), skip: true},
		{name: "real failure", err: errors.New("create list item http 400: invalid field"), skip: false},
	}
	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sharePointWorkspaceWriteErr(tt.err)
			if tt.skip && got != nil {
				t.Fatalf("want skip, got %v", got)
			}
			if !tt.skip && got == nil {
				t.Fatal("want error")
			}
		})
	}
}
