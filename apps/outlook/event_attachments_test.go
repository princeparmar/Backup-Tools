package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsureEventAttachmentBytesFillsContent(t *testing.T) {
	const eventID = "EV1"
	const attID = "ATT1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/events/"+eventID+"/attachments") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"value":[{"@odata.type":"#microsoft.graph.fileAttachment","id":"ATT1","name":"a.txt","contentType":"text/plain"}]}`))
		case strings.HasSuffix(r.URL.Path, "/attachments/"+attID) && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"@odata.type":"#microsoft.graph.fileAttachment","id":"ATT1","name":"a.txt","contentBytes":"` + base64.StdEncoding.EncodeToString([]byte("hi")) + `"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	raw := []byte(`{"id":"EV1","subject":"Meet","hasAttachments":true}`)
	out, err := EnsureEventAttachmentBytes(context.Background(), "tok", srv.URL+"/me", eventID, raw)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Attachments []struct {
			Name         string `json:"name"`
			ContentBytes string `json:"contentBytes"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Attachments) != 1 || doc.Attachments[0].Name != "a.txt" || doc.Attachments[0].ContentBytes == "" {
		t.Fatalf("attachments = %+v", doc.Attachments)
	}
}

func TestRestoreEventFileAttachmentsPostsFiles(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events/NEW/attachments") {
			posted = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"A1"}`))
			return
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	raw := []byte(`{"attachments":[{"@odata.type":"#microsoft.graph.fileAttachment","name":"a.txt","contentBytes":"aGk="}]}`)
	if err := RestoreEventFileAttachments(context.Background(), "tok", srv.URL+"/me", "NEW", raw); err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Fatal("attachment not posted")
	}
}
