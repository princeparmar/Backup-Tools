package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

func TestIsReadOnlyGoogleCalendar(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{id: "en.indian#holiday@group.v.calendar.google.com", want: true},
		{id: "addressbook#contacts@group.v.calendar.google.com", want: true},
		{id: "c_classroom32c90b75@group.calendar.google.com", want: false},
		{id: "dhavalder.office93@gmail.com", want: false},
		{id: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := IsReadOnlyGoogleCalendar(tt.id); got != tt.want {
				t.Fatalf("IsReadOnlyGoogleCalendar(%q)=%v want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestCalendarEventRestoreSkip(t *testing.T) {
	tests := []struct {
		name       string
		calendarID string
		status     string
		wantSkip   bool
	}{
		{name: "cancelled", calendarID: "cal1", status: "cancelled", wantSkip: true},
		{name: "holiday", calendarID: "en.usa#holiday@group.v.calendar.google.com", status: "confirmed", wantSkip: true},
		{name: "normal", calendarID: "c_abc@group.calendar.google.com", status: "confirmed", wantSkip: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &calendar.Event{Status: tt.status}
			if got := calendarEventRestoreSkip(ev, tt.calendarID); got != tt.wantSkip {
				t.Fatalf("skip=%v want %v", got, tt.wantSkip)
			}
		})
	}
}

func TestPrepareCalendarEventForInsert(t *testing.T) {
	tests := []struct {
		name string
		in   calendar.Event
		want calendar.Event
	}{
		{
			name: "clears server fields keeps summary",
			in: calendar.Event{
				Id:        "keep-out",
				Etag:      "e",
				ICalUID:   "uid",
				Organizer: &calendar.EventOrganizer{Email: "org@x.com"},
				Creator:   &calendar.EventCreator{Email: "me@x.com"},
				EventType: "outOfOffice",
				Summary:   "OOO",
			},
			want: calendar.Event{Summary: "OOO", EventType: "default"},
		},
		{
			name: "keeps recurrence rule",
			in: calendar.Event{
				Summary:    "Standup",
				Recurrence: []string{"RRULE:FREQ=DAILY"},
			},
			want: calendar.Event{Summary: "Standup", Recurrence: []string{"RRULE:FREQ=DAILY"}},
		},
		{
			name: "drops default reminders when overrides exist",
			in: calendar.Event{
				Summary: "Testing",
				Reminders: &calendar.EventReminders{
					UseDefault: true,
					Overrides:  []*calendar.EventReminder{{Method: "popup", Minutes: 10}},
				},
			},
			want: calendar.Event{Summary: "Testing"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prepareCalendarEventForInsert(&tt.in)
			if got.Id != "" || got.Etag != "" || got.ICalUID != "" {
				t.Fatalf("server fields not cleared: %+v", got)
			}
			if got.Organizer != nil || got.Creator != nil {
				t.Fatal("organizer/creator must be cleared")
			}
			if got.Summary != tt.want.Summary {
				t.Fatalf("summary=%q want %q", got.Summary, tt.want.Summary)
			}
			if got.EventType != tt.want.EventType {
				t.Fatalf("eventType=%q want %q", got.EventType, tt.want.EventType)
			}
			if len(got.Recurrence) != len(tt.want.Recurrence) {
				t.Fatalf("recurrence=%v want %v", got.Recurrence, tt.want.Recurrence)
			}
			for i := range tt.want.Recurrence {
				if got.Recurrence[i] != tt.want.Recurrence[i] {
					t.Fatalf("recurrence=%v want %v", got.Recurrence, tt.want.Recurrence)
				}
			}
			if tt.in.Reminders != nil && tt.in.Reminders.UseDefault && len(tt.in.Reminders.Overrides) > 0 {
				if got.Reminders == nil || !got.Reminders.UseDefault || len(got.Reminders.Overrides) != 0 {
					t.Fatalf("reminders not sanitized: %+v", got.Reminders)
				}
			}
		})
	}
}

func TestSanitizeCalendarReminders(t *testing.T) {
	tests := []struct {
		name          string
		in            *calendar.EventReminders
		wantNil       bool
		wantDefault   bool
		wantOverrides int
	}{
		{name: "nil", wantNil: true},
		{
			name:        "default only",
			in:          &calendar.EventReminders{UseDefault: true},
			wantDefault: true,
		},
		{
			name:          "overrides only",
			in:            &calendar.EventReminders{Overrides: []*calendar.EventReminder{{Method: "email", Minutes: 30}}},
			wantOverrides: 1,
		},
		{
			name: "both default and overrides",
			in: &calendar.EventReminders{
				UseDefault: true,
				Overrides:  []*calendar.EventReminder{{Method: "popup", Minutes: 10}, {Method: "email", Minutes: 60}},
			},
			wantDefault: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeCalendarReminders(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("got %+v want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("nil reminders")
			}
			if got.UseDefault != tt.wantDefault {
				t.Fatalf("useDefault=%v want %v", got.UseDefault, tt.wantDefault)
			}
			if len(got.Overrides) != tt.wantOverrides {
				t.Fatalf("overrides=%d want %d", len(got.Overrides), tt.wantOverrides)
			}
			if tt.wantOverrides > 0 && got.UseDefault {
				t.Fatal("cannot send useDefault with overrides")
			}
		})
	}
}

func TestRestoreCalendarEventFromBackup(t *testing.T) {
	eventJSON := []byte(`{"summary":"Standup","status":"confirmed","start":{"dateTime":"2026-07-21T10:00:00Z"},"end":{"dateTime":"2026-07-21T10:30:00Z"}}`)
	cancelledJSON := []byte(`{"summary":"gone","status":"cancelled"}`)

	tests := []struct {
		name       string
		calendarID string
		data       []byte
		handler    http.HandlerFunc
		wantErr    bool
		wantPosts  int
	}{
		{
			name:       "cancelled skipped",
			calendarID: "cal1",
			data:       cancelledJSON,
			handler:    func(http.ResponseWriter, *http.Request) { t.Fatal("insert must not run for cancelled") },
		},
		{
			name:       "holiday skipped",
			calendarID: "en.indian#holiday@group.v.calendar.google.com",
			data:       eventJSON,
			handler:    func(http.ResponseWriter, *http.Request) { t.Fatal("insert must not run for holiday calendar") },
		},
		{
			name:       "insert into existing calendar",
			calendarID: "c_classroom32c90b75@group.calendar.google.com",
			data:       eventJSON,
			wantPosts:  1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Fatalf("method %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "c_classroom32c90b75") || !strings.Contains(r.URL.Path, "/events") {
					t.Fatalf("path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"new1"}`)
			},
		},
		{
			name:       "strips default reminders when overrides exist",
			calendarID: "support@salestalker.com",
			data:       []byte(`{"summary":"Testing","status":"confirmed","start":{"date":"2026-10-30"},"end":{"date":"2026-10-31"},"reminders":{"useDefault":true,"overrides":[{"method":"popup","minutes":10}]}}`),
			wantPosts:  1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var got calendar.Event
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatalf("insert body: %v", err)
				}
				if got.Reminders == nil || !got.Reminders.UseDefault || len(got.Reminders.Overrides) != 0 {
					t.Fatalf("insert reminders=%+v", got.Reminders)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"testing1"}`)
			},
		},
		{
			name:       "retries insert without reminders after conflict",
			calendarID: "support@salestalker.com",
			data:       []byte(`{"summary":"Testing","status":"confirmed","start":{"date":"2026-10-30"},"end":{"date":"2026-10-31"},"reminders":{"useDefault":true,"overrides":[{"method":"popup","minutes":10}]}}`),
			wantPosts:  2,
			handler: func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var got calendar.Event
				_ = json.Unmarshal(body, &got)
				w.Header().Set("Content-Type", "application/json")
				if got.Reminders != nil {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"code":400,"message":"Cannot specify both default reminders and overrides at the same time.","errors":[{"reason":"cannotUseDefaultRemindersAndSpecifyOverride"}]}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"testing2"}`)
			},
		},
		{
			name:       "missing calendar falls back to primary",
			calendarID: "c_deleted@group.calendar.google.com",
			data:       eventJSON,
			wantPosts:  2,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/calendars/primary/events") {
					_, _ = io.WriteString(w, `{"id":"p1"}`)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
			},
		},
		{
			name:       "writer 403 is a failure",
			calendarID: "c_shared@group.calendar.google.com",
			data:       eventJSON,
			wantErr:    true,
			wantPosts:  1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":{"code":403,"message":"You need to have writer access to this calendar."}}`)
			},
		},
		{
			name:       "invalid payload 400 is a failure",
			calendarID: "support@salestalker.com",
			data:       eventJSON,
			wantErr:    true,
			wantPosts:  1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":400,"message":"Invalid start time."}}`)
			},
		},
		{
			name:       "insufficient scope still fails",
			calendarID: "c_abc@group.calendar.google.com",
			data:       eventJSON,
			wantErr:    true,
			wantPosts:  1,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":{"code":403,"message":"Request had insufficient authentication scopes.","status":"PERMISSION_DENIED","details":[{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`)
			},
		},
		{
			name:       "bad json",
			calendarID: "cal1",
			data:       []byte(`{`),
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			posts := 0
			svc := testCalendarService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
				}
				if tt.handler != nil {
					tt.handler(w, r)
				}
			}))
			err := RestoreCalendarEventFromBackup(context.Background(), svc, tt.calendarID, tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantPosts != 0 && posts != tt.wantPosts {
				t.Fatalf("posts=%d want %d", posts, tt.wantPosts)
			}
		})
	}
}

func testCalendarService(t *testing.T, h http.Handler) *calendar.Service {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	svc, err := calendar.NewService(context.Background(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL+"/"),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestRestoreCalendarEventJSONRoundTrip(t *testing.T) {
	raw, err := json.Marshal(&calendar.Event{Summary: "Standup", Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	var ev calendar.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Summary != "Standup" {
		t.Fatalf("summary %q", ev.Summary)
	}
}
