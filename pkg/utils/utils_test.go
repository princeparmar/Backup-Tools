package utils

import (
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
)

func TestGenerateTitleFromGmailMessage_stripsCRLFInSubject(t *testing.T) {
	msg := &gmail.Message{
		Id:       "19e1ea6fd11a15db",
		ThreadId: "19e1ea6fd11a15db",
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "From", Value: "no-reply@youtube.com"},
				{Name: "Subject", Value: "Quarterly reminder about YouTube’s Terms of Service, Community Guidelines and Privacy Policy\r\n"},
			},
		},
	}
	got := GenerateTitleFromGmailMessage(msg)
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("title still has control chars: %q", got)
	}
	if !strings.HasSuffix(got, " - 19e1ea6fd11a15db - 19e1ea6fd11a15db.gmail") {
		t.Fatalf("unexpected title: %q", got)
	}
	if !strings.Contains(got, "Privacy Policy - 19e1ea6fd11a15db") {
		t.Fatalf("expected collapsed subject before ids, got %q", got)
	}
}

func TestSanitizeGmailObjectKeyTitle_slashAndControls(t *testing.T) {
	got := sanitizeGmailObjectKeyTitle("a/b\r\nc - mid.gmail")
	want := "a_b c - mid.gmail"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
