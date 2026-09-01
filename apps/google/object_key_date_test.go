package google

import (
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"
)

func TestObjectKeyDatePathFromRFC3339(t *testing.T) {
	got := ObjectKeyDatePathFromRFC3339("2026-07-21T15:04:05Z")
	if got != "2026/07/21" {
		t.Fatalf("got %q, want 2026/07/21", got)
	}
	if got := ObjectKeyDatePathFromRFC3339(""); got != FallbackObjectKeyDatePath {
		t.Fatalf("empty got %q, want %q", got, FallbackObjectKeyDatePath)
	}
}

func TestObjectKeyDatePathFromUnixMilli(t *testing.T) {
	ts, err := time.Parse(time.RFC3339, "2026-07-21T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	got := ObjectKeyDatePathFromUnixMilli(ts.UnixMilli())
	if got != "2026/07/21" {
		t.Fatalf("got %q, want 2026/07/21", got)
	}
	if got := ObjectKeyDatePathFromUnixMilli(0); got != FallbackObjectKeyDatePath {
		t.Fatalf("zero got %q, want %q", got, FallbackObjectKeyDatePath)
	}
}

func TestDriveIDBasedMetaKeyWithDate(t *testing.T) {
	got := DriveIDBasedMetaKey("user@gmail.com", "abc123", "report.pdf", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/meta/2026/07/21/abc123_report.pdf.json"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDriveIDBasedDataKeyWithDate(t *testing.T) {
	got := DriveIDBasedDataKey("user@gmail.com", "abc123", "report.pdf", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/data/2026/07/21/abc123_report.pdf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPhotosIDBasedMetaKeyWithDate(t *testing.T) {
	got := PhotosIDBasedMetaKey("user@gmail.com", "photo123", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/meta/2026/07/21/photo123.json"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPhotosIDBasedDataKeyWithDate(t *testing.T) {
	got := PhotosIDBasedDataKey("user@gmail.com", "ABC", "photo.jpg", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/data/2026/07/21/ABC_photo.jpg"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestContactsObjectKeyWithDate(t *testing.T) {
	got := ContactsObjectKey("user@gmail.com", "people/c123", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/contacts/2026/07/21/c123.json"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCalendarObjectKeyWithDate(t *testing.T) {
	got := CalendarObjectKey("user@gmail.com", "cal1", "Work", "ev1", "Standup", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/calendar/events/cal1_Work/2026/07/21/ev1_Standup.json"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseCalendarEventObjectKeyDated(t *testing.T) {
	calID, evID, ok := ParseCalendarEventObjectKey("user@gmail.com/calendar/events/cal1_Work/2026/07/21/ev1_Standup.json")
	if !ok || calID != "cal1" || evID != "ev1" {
		t.Fatalf("got cal=%q ev=%q ok=%v", calID, evID, ok)
	}
	_, _, ok = ParseCalendarEventObjectKey("user@gmail.com/calendar/events/cal1_Work/ev1_Standup.json")
	if ok {
		t.Fatal("expected undated key to be rejected")
	}
}

func TestBuildContactsSyncedIDSetDated(t *testing.T) {
	got := BuildContactsSyncedIDSet(map[string]bool{
		"user@gmail.com/contacts/2026/07/21/c123.json": true,
		"user@gmail.com/contacts/c456.json":            true,
	}, "user@gmail.com")
	if _, ok := got["c123"]; !ok {
		t.Fatalf("missing dated id c123: %v", got)
	}
	if _, ok := got["c456"]; ok {
		t.Fatalf("undated id c456 should be ignored: %v", got)
	}
}

func TestBuildPhotosSyncedIDSetDated(t *testing.T) {
	got := BuildPhotosSyncedIDSet(map[string]bool{
		"user@gmail.com/meta/2026/07/21/abc123.json":        true,
		"user@gmail.com/data/2026/07/21/xyz789_holiday.jpg": true,
	}, "user@gmail.com")
	if _, ok := got["abc123"]; !ok {
		t.Fatalf("missing meta id: %v", got)
	}
	if _, ok := got["xyz789"]; !ok {
		t.Fatalf("missing data id: %v", got)
	}
}

func TestPhotosMetaKeyFromDataOrMetaKeyDated(t *testing.T) {
	dataKey, metaKey, ok := PhotosMetaKeyFromDataOrMetaKey("user@gmail.com/data/2026/07/21/ABC_photo.jpg")
	if !ok {
		t.Fatal("expected ok")
	}
	if dataKey != "user@gmail.com/data/2026/07/21/ABC_photo.jpg" {
		t.Fatalf("dataKey=%q", dataKey)
	}
	if metaKey != "user@gmail.com/meta/2026/07/21/ABC.json" {
		t.Fatalf("metaKey=%q", metaKey)
	}
}

func TestGmailObjectKey(t *testing.T) {
	ts, err := time.Parse(time.RFC3339, "2026-07-21T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	msg := &gmail.Message{
		Id:           "mid1",
		InternalDate: ts.UnixMilli(),
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "From", Value: "a@b.com"},
				{Name: "Subject", Value: "Hi"},
			},
		},
	}
	got := GmailObjectKey("user@gmail.com", msg)
	want := "user@gmail.com/2026/07/21/a@b.com - Hi - mid1.gmail"
	if got != want {
		t.Fatalf("GmailObjectKey() = %q, want %q", got, want)
	}
}
