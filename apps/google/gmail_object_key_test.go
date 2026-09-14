package google

import (
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"
)

func TestGmailBackupLabelsForKey(t *testing.T) {
	got := GmailBackupLabelsForKey([]string{"STARRED", "UNREAD", "INBOX", "INBOX", "CHAT", "IMPORTANT"})
	want := []string{"IMPORTANT", "INBOX", "STARRED"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if GmailLabelsSegment(nil) != "_" {
		t.Fatal("empty → _")
	}
	if GmailLabelsSegment([]string{"UNREAD"}) != "_" {
		t.Fatal("only volatile → _")
	}
}

func TestGmailLabelsSegmentSanitize(t *testing.T) {
	seg := GmailLabelsSegment([]string{"Label/Weird", "INBOX", "a^b"})
	// sorted: INBOX, Label_Weird, a_b
	want := "INBOX^Label_Weird^a_b"
	if seg != want {
		t.Fatalf("got %q want %q", seg, want)
	}
}

func TestGmailObjectKeyLabeled(t *testing.T) {
	ts, err := time.Parse(time.RFC3339, "2026-07-21T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	msg := &gmail.Message{
		Id:           "mid1",
		InternalDate: ts.UnixMilli(),
		LabelIds:     []string{"STARRED", "INBOX", "IMPORTANT", "UNREAD"},
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "From", Value: "a@b.com"},
				{Name: "Subject", Value: "Hi"},
			},
		},
	}
	got := GmailObjectKey("user@gmail.com", msg)
	want := "user@gmail.com/IMPORTANT^INBOX^STARRED/2026/07/21/a@b.com - Hi - mid1.gmail"
	if got != want {
		t.Fatalf("GmailObjectKey() = %q, want %q", got, want)
	}
	// Stable regardless of label order
	msg.LabelIds = []string{"UNREAD", "IMPORTANT", "STARRED", "INBOX"}
	if GmailObjectKey("user@gmail.com", msg) != want {
		t.Fatal("label order must not change key")
	}
}

func TestParseGmailObjectKeyAndHasLabel(t *testing.T) {
	labeled := "user@gmail.com/IMPORTANT^INBOX^STARRED/2026/07/21/a@b.com - Hi - mid1.gmail"
	p, ok := ParseGmailObjectKey(labeled)
	if !ok || p.Legacy || p.MessageID != "mid1" || p.Email != "user@gmail.com" {
		t.Fatalf("parse labeled: %+v ok=%v", p, ok)
	}
	if !ObjectKeyHasGmailLabel(labeled, "INBOX") {
		t.Fatal("expected INBOX")
	}
	if ObjectKeyHasGmailLabel(labeled, "BOX") || ObjectKeyHasGmailLabel(labeled, "INBOX2") {
		t.Fatal("exact token only")
	}
	if ObjectKeyHasGmailLabel(labeled, "SENT") {
		t.Fatal("SENT not in key")
	}

	legacy := "user@gmail.com/2026/07/21/a@b.com - Hi - mid1.gmail"
	lp, ok := ParseGmailObjectKey(legacy)
	if !ok || !lp.Legacy || lp.MessageID != "mid1" {
		t.Fatalf("parse legacy: %+v ok=%v", lp, ok)
	}
	if ObjectKeyHasGmailLabel(legacy, "INBOX") {
		t.Fatal("legacy has no labels")
	}
}

func TestFindExistingGmailKeyByMessageID(t *testing.T) {
	m := map[string]bool{
		"user@gmail.com/2026/07/21/a@b.com - Hi - mid1.gmail":                         true,
		"user@gmail.com/INBOX/2026/07/21/a@b.com - Hi - mid2.gmail":                   true,
		"user@gmail.com/IMPORTANT^INBOX/2026/07/21/a@b.com - Other - mid3.gmail":      true,
		"other@gmail.com/IMPORTANT^INBOX/2026/07/21/a@b.com - Hi - mid1.gmail":        true,
	}
	got := FindExistingGmailKeyByMessageID(m, "user@gmail.com", "mid1")
	if got != "user@gmail.com/2026/07/21/a@b.com - Hi - mid1.gmail" {
		t.Fatalf("got %q", got)
	}
	got = FindExistingGmailKeyByMessageID(m, "user@gmail.com", "mid3")
	if !stringsHasSuffix(got, " - mid3.gmail") {
		t.Fatalf("got %q", got)
	}
	if FindExistingGmailKeyByMessageID(m, "user@gmail.com", "missing") != "" {
		t.Fatal("expected empty")
	}
}

func stringsHasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
