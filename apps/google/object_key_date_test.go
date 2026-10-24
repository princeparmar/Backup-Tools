package google

import (
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"
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
		Id:           "mid1abcdefghij",
		ThreadId:     "mid1abcdefghij",
		InternalDate: ts.UnixMilli(),
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "From", Value: "a@b.com"},
				{Name: "Subject", Value: "Hi"},
			},
		},
	}
	got := GmailObjectKey("user@gmail.com", msg)
	want := "user@gmail.com/_/2026/07/21/a@b.com - Hi - mid1abcdefghij - mid1abcdefghij.gmail"
	if got != want {
		t.Fatalf("GmailObjectKey() = %q, want %q", got, want)
	}
}

func TestCalendarIndexOneObject(t *testing.T) {
	if got, want := CalendarDisplayName("user@gmail.com", "Der Dhaval"), "Der Dhaval"; got != want {
		t.Fatalf("display name %q", got)
	}
	if got, want := CalendarDisplayName("7 IT A & B", ""), "7 IT A & B"; got != want {
		t.Fatalf("display name %q", got)
	}
	if got := CalendarIndexObjectKey("user@gmail.com"); got != "user@gmail.com/calendar/_index.json" {
		t.Fatalf("key %q", got)
	}
	row, ok := IndexEventFrom("cal1", "user@gmail.com/calendar/events/cal1_Work/2026/07/21/ev1_Standup.json", &calendar.Event{
		Id:         "ev1",
		Summary:    "Standup",
		Start:      &calendar.EventDateTime{DateTime: "2026-10-21T10:00:00Z"},
		End:        &calendar.EventDateTime{DateTime: "2026-10-21T10:30:00Z"},
		Recurrence: []string{"RRULE:FREQ=WEEKLY"},
	})
	if !ok || row.Rule != "FREQ=WEEKLY" || row.AllDay || row.CalendarID != "cal1" {
		t.Fatalf("row %+v ok %v", row, ok)
	}
	if _, ok := IndexEventFrom("cal1", "k", &calendar.Event{Id: "gone", Status: "cancelled", Start: &calendar.EventDateTime{Date: "2026-10-02"}}); ok {
		t.Fatal("cancelled event should stay out of the index")
	}
	var idx CalendarIndex
	idx.Put(row)
	idx.Put(row)
	other, ok := IndexEventFrom("cal2", "k2", &calendar.Event{
		Id:    "ev2",
		Start: &calendar.EventDateTime{Date: "2026-10-02"},
		End:   &calendar.EventDateTime{Date: "2026-10-03"},
	})
	if !ok || !other.AllDay {
		t.Fatalf("all-day %+v", other)
	}
	idx.Put(other)
	if len(idx.Events) != 2 {
		t.Fatalf("put len %d", len(idx.Events))
	}
	idx.ReplaceCalendar("cal1", nil)
	if len(idx.Events) != 1 || idx.Events[0].ID != "ev2" {
		t.Fatalf("replace %+v", idx.Events)
	}
	idx.Remove("cal2", "ev2")
	if len(idx.Events) != 0 || idx.V != CalendarIndexFileVersion {
		t.Fatalf("remove %+v", idx)
	}
	idx.SetCalendars([]FlatCalendar{
		{ID: "c_classroom32c90b75@group.calendar.google.com", Summary: "7 IT A & B", DisplayName: "7 IT A & B"},
		{ID: "c_other@group.calendar.google.com", Summary: "SEM-1 DIV-E_2022"},
	})
	if len(idx.Calendars) != 2 || idx.Calendars[0].Name != "7 IT A & B" || idx.Calendars[1].Name != "SEM-1 DIV-E_2022" {
		t.Fatalf("calendars %+v", idx.Calendars)
	}
}
