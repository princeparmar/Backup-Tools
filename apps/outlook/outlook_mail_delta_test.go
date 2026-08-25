package outlook

import (
	"errors"
	"strings"
	"testing"
)

func TestOutlookMailIDBasedKeys(t *testing.T) {
	meta := OutlookMailIDBasedMetaKey("a@b.com", "MSG1", "2026-07-21T15:04:05Z")
	data := OutlookMailIDBasedDataKey("a@b.com", "MSG1", "2026-07-21T15:04:05Z")
	if !strings.Contains(meta, "MSG1.json") {
		t.Fatalf("meta key: %s", meta)
	}
	if !strings.Contains(data, "MSG1.json") {
		t.Fatalf("data key: %s", data)
	}
	if !strings.Contains(meta, "/meta/2026/07/21/") {
		t.Fatalf("expected date path in meta: %s", meta)
	}
}

func TestErrOutlookMailDeltaInvalid(t *testing.T) {
	if !errors.Is(ErrOutlookMailDeltaInvalid, ErrOutlookMailDeltaInvalid) {
		t.Fatal("sentinel")
	}
}

func TestMessagesDeltaURL(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		folderID string
		wantSub  string
	}{
		{name: "default inbox", base: "https://graph.microsoft.com/v1.0/me", folderID: "", wantSub: "/mailFolders/inbox/messages/delta?"},
		{name: "sentitems", base: "https://graph.microsoft.com/v1.0/users/u@x.com", folderID: "sentitems", wantSub: "/mailFolders/sentitems/messages/delta?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MessagesDeltaURL(tt.base, tt.folderID)
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("url: %s", got)
			}
			if !strings.Contains(got, "subject") || !strings.Contains(got, "from") {
				t.Fatalf("expected $select with subject/from: %s", got)
			}
		})
	}
}

func TestInboxMessagesDeltaInitialURL(t *testing.T) {
	got := InboxMessagesDeltaInitialURL("https://graph.microsoft.com/v1.0/me")
	if !strings.HasPrefix(got, "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages/delta?") {
		t.Fatalf("got %s", got)
	}
	if !strings.Contains(got, "subject") || !strings.Contains(got, "from") {
		t.Fatalf("expected $select with subject/from: %s", got)
	}
}
