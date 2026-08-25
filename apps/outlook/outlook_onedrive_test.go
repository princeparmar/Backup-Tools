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

func TestOneDriveIDBasedKeys(t *testing.T) {
	meta := OneDriveIDBasedMetaKey("a@b.com", "ITEM1", "report.pdf", "2026-07-21T15:04:05Z")
	data := OneDriveIDBasedDataKey("a@b.com", "ITEM1", "report.pdf", "2026-07-21T15:04:05Z")
	if !strings.Contains(meta, "ITEM1_") || !strings.HasSuffix(meta, ".json") {
		t.Fatalf("meta key: %s", meta)
	}
	if !strings.Contains(data, "ITEM1_") || strings.HasSuffix(data, ".json") {
		t.Fatalf("data key: %s", data)
	}
	if !strings.Contains(meta, "/meta/2026/07/21/") {
		t.Fatalf("expected date path in meta: %s", meta)
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
