package google

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
)

func TestDecodeGmailBodyData_acceptsStdAndURL(t *testing.T) {
	payload := []byte("hello-attachment-bytes")
	std := base64.StdEncoding.EncodeToString(payload)
	urlSafe := base64.URLEncoding.EncodeToString(payload)

	for _, in := range []string{std, urlSafe} {
		got, err := decodeGmailBodyData(in)
		if err != nil {
			t.Fatalf("decode %q: %v", in, err)
		}
		if string(got) != string(payload) {
			t.Fatalf("got %q want %q", got, payload)
		}
	}
}

func TestNormalizeRFC822RootHeaders_singleFrom(t *testing.T) {
	part := &gmail.MessagePart{
		Headers: []*gmail.MessagePartHeader{
			{Name: "From", Value: ""},
			{Name: "From", Value: "a@b.com"},
			{Name: "From", Value: "c@d.com"},
			{Name: "Subject", Value: "Hi"},
		},
	}
	normalizeRFC822RootHeaders(part)
	if !strings.EqualFold(part.Headers[0].Name, "From") || part.Headers[0].Value != "a@b.com" {
		t.Fatalf("From must be first: %+v", part.Headers[0])
	}
	fromCount := 0
	for _, h := range part.Headers {
		if strings.EqualFold(h.Name, "From") {
			fromCount++
		}
	}
	if fromCount != 1 {
		t.Fatalf("From count = %d, want 1", fromCount)
	}
}

func TestNormalizeRFC822RootHeaders_addsMissingFrom(t *testing.T) {
	part := &gmail.MessagePart{
		Headers: []*gmail.MessagePartHeader{
			{Name: "Subject", Value: "No from"},
		},
	}
	normalizeRFC822RootHeaders(part)
	if len(part.Headers) < 1 || !strings.EqualFold(part.Headers[0].Name, "From") {
		t.Fatalf("expected synthesized From, got %+v", part.Headers)
	}
}

func TestCreateRawMessage_subjectCRLFDoesNotDropFrom(t *testing.T) {
	// YouTube-style subject with embedded CRLF used to end the header block before From.
	body := base64.StdEncoding.EncodeToString([]byte("body"))
	msg := &gmail.Message{
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "Subject", Value: "Quarterly reminder about YouTube’s Terms\r\n"},
				{Name: "From", Value: "no-reply@youtube.com"},
				{Name: "Content-Type", Value: "text/plain"},
			},
			Body: &gmail.MessagePartBody{Data: body},
		},
	}
	raw, err := createRawMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := string(decoded)
	// From must appear before the blank line that ends headers.
	headerEnd := strings.Index(s, "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatalf("no header terminator in %q", s)
	}
	headers := s[:headerEnd]
	if !strings.HasPrefix(headers, "From: no-reply@youtube.com") {
		t.Fatalf("From must lead headers, got %q", headers)
	}
	if strings.Count(strings.ToLower(headers), "\nfrom:")+strings.Count(strings.ToLower(headers), "from:") < 1 {
		t.Fatalf("missing From in headers: %q", headers)
	}
	fromLines := 0
	var subjectLine string
	for _, line := range strings.Split(headers, "\r\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "from:") {
			fromLines++
		}
		if strings.HasPrefix(lower, "subject:") {
			subjectLine = line
		}
	}
	if fromLines != 1 {
		t.Fatalf("From lines = %d in %q", fromLines, headers)
	}
	if strings.ContainsAny(strings.TrimPrefix(subjectLine, "Subject: "), "\r\n") {
		t.Fatalf("subject value still has raw CRLF: %q", subjectLine)
	}
}

func TestCreateRawMessage_stdBase64Body(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("plain text body"))
	msg := &gmail.Message{
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "From", Value: "a@b.com"},
				{Name: "Subject", Value: "Test"},
				{Name: "Content-Type", Value: "text/plain"},
			},
			Body: &gmail.MessagePartBody{Data: body},
		},
	}
	raw, err := createRawMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := string(decoded)
	if !strings.Contains(s, "From: a@b.com") || !strings.Contains(s, "plain text body") {
		t.Fatalf("raw message missing fields: %q", s)
	}
}
