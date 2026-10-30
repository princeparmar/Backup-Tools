package outlook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListPIMGroupCalendarsUsesMeEvenWhenUserBaseIsUsers(t *testing.T) {
	prev := graphBaseURL
	t.Cleanup(func() { graphBaseURL = prev })

	var hitMe bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/me/") {
			hitMe = true
		}
		switch {
		case strings.Contains(r.URL.Path, "/me/transitiveMemberOf/") || strings.Contains(r.URL.Path, "/me/memberOf/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "g1", "displayName": "Smartiies", "groupTypes": []string{"Unified"}},
				},
			})
		case r.URL.Path == "/groups/g1/calendar":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cal-g1", "name": "Smartiies"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	graphBaseURL = srv.URL

	got, err := ListPIMGroupCalendars(context.Background(), "tok", srv.URL+"/users/u@x.com")
	if err != nil {
		t.Fatal(err)
	}
	if !hitMe || len(got) != 1 || got[0].Name != "Smartiies" {
		t.Fatalf("hitMe=%v got %+v", hitMe, got)
	}
}

func TestListPIMGroupCalendarsUnified(t *testing.T) {
	prev := graphBaseURL
	t.Cleanup(func() { graphBaseURL = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/me/memberOf/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "g1", "displayName": "Smartiies", "groupTypes": []string{"Unified"}},
					{"id": "sec", "displayName": "Security", "groupTypes": []string{}},
				},
			})
		case r.URL.Path == "/groups/g1/calendar":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "cal-g1", "name": "Smartiies", "canEdit": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	graphBaseURL = srv.URL

	got, err := ListPIMGroupCalendars(context.Background(), "tok", srv.URL+"/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Smartiies" || got[0].GroupID != "g1" || got[0].Source != CalendarSourceGroup {
		t.Fatalf("got %+v", got)
	}
}

func TestPIMGroupEventRoots(t *testing.T) {
	root := PIMGroupCalendarEventRoot("gid")
	if !strings.HasSuffix(root, "/groups/gid/calendar") {
		t.Fatalf("event root %s", root)
	}
	if got := PIMEventsURLFromRoot(root); !strings.HasSuffix(got, "/groups/gid/calendar/events?$top=100") {
		t.Fatalf("events url %s", got)
	}
	if got := PIMGroupAttachmentRoot("gid"); !strings.HasSuffix(got, "/groups/gid") {
		t.Fatalf("attach root %s", got)
	}
}

func TestListPIMCalendarsFromGroups(t *testing.T) {
	prev := graphBaseURL
	t.Cleanup(func() { graphBaseURL = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/calendarGroups"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "cg-my", "name": "My Calendars"},
					{"id": "cg-groups", "name": "Groups"},
				},
			})
		case strings.Contains(r.URL.Path, "/calendarGroups/cg-my/calendars"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "cal1", "name": "Calendar", "isDefaultCalendar": true},
				},
			})
		case strings.Contains(r.URL.Path, "/calendarGroups/cg-groups/calendars"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "smart", "name": "Smartiies"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	graphBaseURL = srv.URL

	got, err := ListPIMCalendarsFromGroups(context.Background(), "tok", srv.URL+"/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	var smart *FlatCalendar
	for i := range got {
		if got[i].Name == "Smartiies" {
			smart = &got[i]
		}
	}
	if smart == nil || smart.Source != CalendarSourceGroup || smart.ID != "smart" {
		t.Fatalf("Smartiies missing/wrong: %+v", got)
	}
}
