package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestEnsureMailAttachmentBytesFillsMissingContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/attachments/ATT1") && !strings.HasSuffix(r.URL.Path, "/$value"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"@odata.type": "#microsoft.graph.fileAttachment",
				"id":          "ATT1",
				"name":        "a.txt",
				"contentBytes": base64.StdEncoding.EncodeToString([]byte("hello")),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	raw := []byte(`{"id":"M1","hasAttachments":true,"attachments":[{"@odata.type":"#microsoft.graph.fileAttachment","id":"ATT1","name":"a.txt"}]}`)
	// Point graph calls at the test server by rewriting userBase to the server URL shape used in helpers.
	// ensureMailAttachmentBytes builds "{userBase}/messages/{id}/attachments/{att}".
	out, err := ensureMailAttachmentBytes(context.Background(), "tok", srv.URL, "M1", raw)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Attachments []struct {
			ContentBytes string `json:"contentBytes"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Attachments) != 1 || doc.Attachments[0].ContentBytes == "" {
		t.Fatalf("attachments not filled: %+v", doc.Attachments)
	}
	got, _ := base64.StdEncoding.DecodeString(doc.Attachments[0].ContentBytes)
	if string(got) != "hello" {
		t.Fatalf("content %q", got)
	}
}

func TestMailRestoreCacheScopeIsolatesTokens(t *testing.T) {
	a := mailRestoreCacheScope("token-a", "https://graph.microsoft.com/v1.0/me")
	b := mailRestoreCacheScope("token-b", "https://graph.microsoft.com/v1.0/me")
	if a == b {
		t.Fatal("expected different cache scopes for different tokens")
	}
	if mailRestoreCacheScope("token-a", "https://graph.microsoft.com/v1.0/me") != a {
		t.Fatal("scope not stable")
	}
}
