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
	got := ContactsObjectKey("user@gmail.com", "people/c123", "2026-07-21T10:00:00Z", "")
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
	tests := []struct {
		name   string
		key    string
		wantID string
		wantEv string
		ok     bool
	}{
		{
			name:   "legacy short id",
			key:    "user@gmail.com/calendar/events/cal1_Work/2026/07/21/ev1_Standup.json",
			wantID: "cal1",
			wantEv: "ev1",
			ok:     true,
		},
		{
			name: "classroom c_ id keeps local-part underscore",
			key:  "dhavalder.office93@gmail.com/calendar/events/c_classroom32c90b75@group.calendar.google.com_7 IT A & B/2026/07/21/ev1_Standup.json",
			wantID: "c_classroom32c90b75@group.calendar.google.com",
			wantEv: "ev1",
			ok:     true,
		},
		{
			name:   "primary mailbox id",
			key:    "dhavalder.office93@gmail.com/calendar/events/dhavalder.office93@gmail.com_untitled/2026/07/21/ev1_Standup.json",
			wantID: "dhavalder.office93@gmail.com",
			wantEv: "ev1",
			ok:     true,
		},
		{
			name:   "summary with underscores",
			key:    "user@gmail.com/calendar/events/c_other@group.calendar.google.com_SEM-1 DIV-E_2022/2026/07/21/ev9_Quiz.json",
			wantID: "c_other@group.calendar.google.com",
			wantEv: "ev9",
			ok:     true,
		},
		{
			name: "undated key rejected",
			key:  "user@gmail.com/calendar/events/cal1_Work/ev1_Standup.json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calID, evID, ok := ParseCalendarEventObjectKey(tt.key)
			if ok != tt.ok || calID != tt.wantID || evID != tt.wantEv {
				t.Fatalf("got cal=%q ev=%q ok=%v, want cal=%q ev=%q ok=%v", calID, evID, ok, tt.wantID, tt.wantEv, tt.ok)
			}
		})
	}
}

func TestSplitCalendarPathSegment(t *testing.T) {
	tests := []struct {
		name        string
		segment     string
		wantID      string
		wantSummary string
	}{
		{name: "legacy", segment: "cal1_Work", wantID: "cal1", wantSummary: "Work"},
		{name: "id only", segment: "cal1", wantID: "cal1"},
		{
			name:        "classroom",
			segment:     "c_classroom32c90b75@group.calendar.google.com_7 IT A & B",
			wantID:      "c_classroom32c90b75@group.calendar.google.com",
			wantSummary: "7 IT A & B",
		},
		{
			name:        "holiday",
			segment:     "en.indian#holiday@group.v.calendar.google.com_Holidays in India",
			wantID:      "en.indian#holiday@group.v.calendar.google.com",
			wantSummary: "Holidays in India",
		},
		{
			name:        "primary",
			segment:     "dhavalder.office93@gmail.com_My Calendar",
			wantID:      "dhavalder.office93@gmail.com",
			wantSummary: "My Calendar",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, summary := SplitCalendarPathSegment(tt.segment)
			if id != tt.wantID || summary != tt.wantSummary {
				t.Fatalf("got id=%q summary=%q, want id=%q summary=%q", id, summary, tt.wantID, tt.wantSummary)
			}
			if got := ParseCalendarIDFromPathSegment(tt.segment); got != tt.wantID {
				t.Fatalf("ParseCalendarIDFromPathSegment=%q want %q", got, tt.wantID)
			}
		})
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

func TestIsCalendarEventSyncedClassroomID(t *testing.T) {
	calID := "c_classroom32c90b75@group.calendar.google.com"
	key := CalendarObjectKey("user@gmail.com", calID, "7 IT A & B", "ev1", "Standup", "2026-07-21T10:00:00Z")
	synced := map[string]bool{key: true}
	tests := []struct {
		name string
		id   string
		ev   string
		want bool
	}{
		{name: "classroom match", id: calID, ev: "ev1", want: true},
		{name: "truncated c must not match", id: "c", ev: "ev1", want: false},
		{name: "other event", id: calID, ev: "ev2", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCalendarEventSynced(synced, "user@gmail.com", tt.id, tt.ev, "", ""); got != tt.want {
				t.Fatalf("synced=%v want %v key=%s", got, tt.want, key)
			}
		})
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
