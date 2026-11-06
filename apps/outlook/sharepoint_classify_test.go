package outlook

import (
	"context"
	"strings"
	"testing"
)

func TestClassifySharePointListKind(t *testing.T) {
	if ClassifySharePointListKind("documentLibrary") != SharePointListKindDocumentLibrary {
		t.Fatal("documentLibrary")
	}
	if ClassifySharePointListKind("genericList") != SharePointListKindList {
		t.Fatal("genericList")
	}
	if ClassifySharePointListKind("webPageLibrary") != SharePointListKindPages {
		t.Fatal("webPageLibrary must be pages-owned")
	}
	if ClassifySharePointListKindFromList("genericList", "SitePages", "Site Pages") != SharePointListKindPages {
		t.Fatal("SitePages name must route to pages")
	}
	if n := len(SharePointNonLibraryLists([]SharePointSiteList{
		{ID: "1", Kind: SharePointListKindDocumentLibrary},
		{ID: "2", Kind: SharePointListKindList},
		{ID: "3", Kind: SharePointListKindPages},
	})); n != 1 {
		t.Fatalf("filter want 1 structured list, got %d", n)
	}
}

func TestMapSharePointPermissionPrincipal(t *testing.T) {
	dir := map[string]string{"old@x.com": "new-id"}
	id, ok := MapSharePointPermissionPrincipal(map[string]any{"userPrincipalName": "old@x.com"}, dir)
	if !ok || id != "new-id" {
		t.Fatalf("mapped %q %v", id, ok)
	}
	if _, ok := MapSharePointPermissionPrincipal(nil, dir); ok {
		t.Fatal("nil identity")
	}
	if _, ok := MapSharePointPermissionPrincipal(map[string]any{"displayName": "Ghost"}, map[string]string{}); ok {
		t.Fatal("unmapped display name must fail closed")
	}
	if _, ok := MapSharePointPermissionPrincipal(map[string]any{"id": "ghost-id"}, map[string]string{}); ok {
		t.Fatal("unmapped id must fail closed when directory provided")
	}
}

func TestRestoreSharePointBackupRequiresDriveID(t *testing.T) {
	src := OneDriveRestoreSource{
		Stat: func(ctx context.Context, key string) (map[string]string, int64, error) {
			return map[string]string{OneDriveMetaName: "a.txt"}, 1, nil
		},
	}
	err := RestoreSharePointBackup(context.Background(), "tok", "t/site/s/MY_DRIVE/a_F1.txt", src)
	if err == nil || !strings.Contains(err.Error(), "no drive id") {
		t.Fatalf("want fail-safe without drive id, got %v", err)
	}
}
