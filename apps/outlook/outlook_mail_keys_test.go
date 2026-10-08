package outlook

import (
	"encoding/json"
	"strings"
	"testing"
)

const mailTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestOutlookMailObjectKey(t *testing.T) {
	prefix := ResourceKeyPrefix(mailTenant, "user", "oid-1")
	got := OutlookMailObjectKey(prefix+"/", "/Inbox/Projects/", "2026-07-21T15:04:05Z", "ann@contoso.com", "Q3 plan", "CONV1", "MSG1")
	want := prefix + "/Inbox/Projects/2026/07/21/ann@contoso.com - Q3 plan - CONV1 - MSG1.outlook"
	if got != want {
		t.Fatalf("key:\n got %s\nwant %s", got, want)
	}
}

func TestOutlookMailFileName(t *testing.T) {
	tests := []struct {
		name                       string
		from, subject, conv, msgID string
		want                       string
	}{
		{name: "empty fields", msgID: "M", want: "unknown - (no subject) - M - M.outlook"},
		{name: "unsafe characters", from: "a/b", subject: "re:\n a/b  c", conv: "C", msgID: "M", want: "a_b - re: a_b c - C - M.outlook"},
		{name: "sender separator", from: "Ops - Team", subject: "s", conv: "C", msgID: "M", want: "Ops-Team - s - C - M.outlook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OutlookMailFileName(tt.from, tt.subject, tt.conv, tt.msgID); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
	long := OutlookMailFileName("a@b.c", strings.Repeat("é", 400), "C", "M")
	if n := len([]rune(strings.Split(long, " - ")[1])); n != mailKeySubjectMaxRunes {
		t.Fatalf("subject not cut: %d runes", n)
	}
}

func TestParseOutlookMailObjectKey(t *testing.T) {
	prefix := ResourceKeyPrefix(mailTenant, "user", "oid-1")
	tests := []struct {
		name string
		key  string
		ok   bool
		want OutlookMailObject
	}{
		{
			name: "subject with separators",
			key:  OutlookMailObjectKey(prefix, "Inbox", "2026-07-21T15:04:05Z", "ann@contoso.com", "Re: A - B - C", "CONV-1_x=", "MSG-1_y="),
			ok:   true,
			want: OutlookMailObject{Prefix: prefix, FolderPath: "Inbox", DatePath: "2026/07/21", From: "ann@contoso.com", Subject: "Re: A - B - C", ConversationID: "CONV-1_x=", MessageID: "MSG-1_y="},
		},
		{
			name: "numeric nested folder",
			key:  prefix + "/Archive/2024/2026/01/02/a - s - C - M.outlook",
			ok:   true,
			want: OutlookMailObject{Prefix: prefix, FolderPath: "Archive/2024", DatePath: "2026/01/02", From: "a", Subject: "s", ConversationID: "C", MessageID: "M"},
		},
		{name: "legacy meta", key: prefix + "/meta/2026/01/02/M.json"},
		{name: "folder list", key: prefix + "/_folders.json"},
		{name: "no date", key: prefix + "/Inbox/a - s - C - M.outlook"},
		{name: "manual backup", key: "ann@contoso.com/a - s - M.outlook"},
		{name: "missing ids", key: prefix + "/Inbox/2026/01/02/a.outlook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseOutlookMailObjectKey(tt.key)
			if ok != tt.ok {
				t.Fatalf("ok=%v want %v (%+v)", ok, tt.ok, got)
			}
			if ok && got != tt.want {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseOutlookMailLegacyKey(t *testing.T) {
	k, ok := ParseOutlookMailLegacyKey("ann@contoso.com/meta/2026/01/02/M.json")
	if !ok || !k.IsMeta || k.Prefix != "ann@contoso.com" || k.MessageID != "M" || k.DatePath != "2026/01/02" {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
	if got := OutlookMailLegacyDataKey("ann@contoso.com/meta/2026/01/02/M.json"); got != "ann@contoso.com/data/2026/01/02/M.json" {
		t.Fatalf("data key %s", got)
	}
	for _, key := range []string{
		"ann@contoso.com/data/2026/01/02/M",
		"meta/2026/01/02/M.json",
		"ann@contoso.com/meta/2026/1/02/M.json",
		"ann@contoso.com/meta/2026/01/02/.json",
	} {
		if _, ok := ParseOutlookMailLegacyKey(key); ok {
			t.Errorf("accepted %q", key)
		}
	}
}

func TestPatchOutlookMailBackup(t *testing.T) {
	raw := []byte(`{"id":"M","subject":"s","isRead":false,"categories":["Old"],"flag":{"flagStatus":"notFlagged","dueDateTime":{"dateTime":"x"}},"storx_backup":{"backed_up_at":"T0"}}`)
	msg := &OutlookMailDeltaMessage{IsRead: true, FlagStatus: "flagged", Importance: "high", ParentFolderID: "F2"}
	out, err := PatchOutlookMailBackup(raw, msg, OutlookMailBackupState{FolderPath: "Inbox/X", WellKnownFolder: ""})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ID         string                 `json:"id"`
		Subject    string                 `json:"subject"`
		IsRead     bool                   `json:"isRead"`
		Categories []string               `json:"categories"`
		Flag       map[string]interface{} `json:"flag"`
		Importance string                 `json:"importance"`
		Parent     string                 `json:"parentFolderId"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "M" || doc.Subject != "s" || !doc.IsRead || len(doc.Categories) != 0 || doc.Importance != "high" || doc.Parent != "F2" {
		t.Fatalf("patched doc %+v", doc)
	}
	if doc.Flag["flagStatus"] != "flagged" || doc.Flag["dueDateTime"] == nil {
		t.Fatalf("flag %+v", doc.Flag)
	}
	state := ReadOutlookMailBackupState(out)
	if state.FolderPath != "Inbox/X" || state.BackedUpAt != "T0" {
		t.Fatalf("state %+v", state)
	}

	again, err := PatchOutlookMailBackup(out, msg, OutlookMailBackupState{FolderPath: "Inbox/X"})
	if err != nil || string(again) != string(out) {
		t.Fatalf("patch is not stable: %v\n%s\n%s", err, out, again)
	}

	removed, err := PatchOutlookMailBackup(out, nil, OutlookMailBackupState{FolderPath: "Inbox/X", RemovedFromMailbox: true, RemovedAt: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	if s := ReadOutlookMailBackupState(removed); !s.RemovedFromMailbox || s.RemovedAt != "T1" || s.BackedUpAt != "T0" {
		t.Fatalf("removed state %+v", s)
	}
}

func TestParseMailRestoreMessageFromBackup(t *testing.T) {
	raw := []byte(`{"id":"M","subject":"s","body":{"contentType":"html","content":"<p>x</p>"},"from":{"emailAddress":{"address":"a@b.c"}},
		"receivedDateTime":"2026-01-02T03:04:05Z","sentDateTime":"2026-01-02T03:04:00Z","isRead":true,"categories":["Red"],"flag":{"flagStatus":"flagged"},
		"attachments":[{"@odata.type":"#microsoft.graph.fileAttachment","name":"a.txt","contentBytes":"eA=="},{"@odata.type":"#microsoft.graph.itemAttachment","name":"m"}],
		"storx_backup":{"folder_path":"Inbox"}}`)
	msg, err := parseMailRestoreMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.IsRead || msg.FlagStatus != "flagged" || len(msg.Categories) != 1 || msg.Received != "2026-01-02T03:04:05Z" {
		t.Fatalf("state %+v", msg)
	}
	if len(msg.Attachments) != 1 || msg.Skipped != 1 {
		t.Fatalf("attachments %d skipped %d", len(msg.Attachments), msg.Skipped)
	}
	if _, ok := msg.Fields["storx_backup"]; ok {
		t.Fatal("backup state must not be sent to Graph")
	}
	if _, ok := msg.Fields["from"]; !ok {
		t.Fatal("from not copied")
	}
}
