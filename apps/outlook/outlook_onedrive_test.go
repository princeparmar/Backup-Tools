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
