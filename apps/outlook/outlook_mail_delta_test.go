package outlook

import (
	"errors"
	"strings"
	"testing"
)

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
