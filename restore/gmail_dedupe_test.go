package restore

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestDedupeGmailRestoreRowsPrefersLabeled(t *testing.T) {
	legacy := "user@x.com/2026/03/10/Alice - Hello - abc123.gmail"
	labeled := "user@x.com/INBOX^STARRED/2026/03/10/Alice - Hello - abc123.gmail"
	other := "user@x.com/SENT/2026/03/11/Bob - Hi - def456.gmail"

	rows := []repo.SyncedObject{
		{ObjectKey: legacy},
		{ObjectKey: labeled},
		{ObjectKey: other},
	}
	out := DedupeGmailRestoreRows(rows)
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	if out[0].ObjectKey != labeled {
		t.Fatalf("preferred key = %q, want labeled", out[0].ObjectKey)
	}
	if out[1].ObjectKey != other {
		t.Fatalf("second key = %q, want other", out[1].ObjectKey)
	}
}

func TestDedupeGmailRestoreRowsSingle(t *testing.T) {
	rows := []repo.SyncedObject{{ObjectKey: "user@x.com/INBOX/2026/01/01/A - B - id1.gmail"}}
	out := DedupeGmailRestoreRows(rows)
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
}
