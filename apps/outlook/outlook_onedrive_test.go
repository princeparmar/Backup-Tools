package outlook

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeOneDrivePathSegment(t *testing.T) {
	got := SanitizeOneDrivePathSegment(`my/file:name?.txt`)
	if strings.ContainsAny(got, `/\:?*`) {
		t.Fatalf("unsafe chars remain: %q", got)
	}
	if got == "" {
		t.Fatal("empty sanitize result")
	}
}

func TestErrOneDriveDeltaInvalid(t *testing.T) {
	if !errors.Is(ErrOneDriveDeltaInvalid, ErrOneDriveDeltaInvalid) {
		t.Fatal("sentinel")
	}
}

func TestIsDeltaResyncRequired(t *testing.T) {
	if !isDeltaResyncRequired([]byte(`{"error":{"code":"resyncRequired"}}`)) {
		t.Fatal("expected resyncRequired detection")
	}
	if isDeltaResyncRequired([]byte(`{"value":[]}`)) {
		t.Fatal("should not flag normal body")
	}
}

func TestIsPersonalOneDriveRoot(t *testing.T) {
	if !IsPersonalOneDriveRoot("https://graph.microsoft.com/v1.0/me/drive") {
		t.Fatal("expected personal /me/drive")
	}
	if !IsPersonalOneDriveRoot("https://graph.microsoft.com/v1.0/me/drive/") {
		t.Fatal("trailing slash should still match")
	}
	if IsPersonalOneDriveRoot("https://graph.microsoft.com/v1.0/users/u@x.com/drive") {
		t.Fatal("org mailbox drive must not be personal")
	}
}

func TestMapSharedWithMeItemUsesRemoteItem(t *testing.T) {
	host := graphDriveItem{
		ID:   "host-stub",
		Name: "FromHost.docx",
		RemoteItem: &graphDriveItem{
			ID:   "remote-1",
			Name: "Shared.docx",
			Size: 12,
			CTag: "c1",
			File: &struct {
				MimeType string `json:"mimeType"`
			}{MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
			ParentReference: &struct {
				ID      string `json:"id"`
				Path    string `json:"path"`
				DriveID string `json:"driveId"`
			}{DriveID: "b!remote", ID: "parent-x"},
		},
	}
	got := mapSharedWithMeItem(host)
	if got.ID != "remote-1" || got.DriveID != "b!remote" || got.Name != "Shared.docx" || got.Size != 12 {
		t.Fatalf("mapped %+v", got)
	}
	if got.ParentID != "" {
		t.Fatalf("shared roots must clear parent id, got %q", got.ParentID)
	}
	if got.MimeType == "" {
		t.Fatal("expected mime from remote file facet")
	}
}
