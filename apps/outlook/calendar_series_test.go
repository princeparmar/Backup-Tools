package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type seriesCall struct {
	method, path, query, prefer string
	body                        map[string]any
}

// newSeriesGraph serves Graph by "METHOD /path"; unknown routes answer 404.
func newSeriesGraph(t *testing.T, routes map[string]func() (int, string)) (string, *[]seriesCall) {
	var mu sync.Mutex
	var calls []seriesCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, seriesCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Prefer"), body})
		mu.Unlock()
		route, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		code, resp := route()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

const listedSeriesMaster = `{"id":"M1","changeKey":"k1","type":"seriesMaster","subject":"Standup",
	"start":{"dateTime":"2026-10-05T03:30:00.0000000","timeZone":"UTC"},
	"end":{"dateTime":"2026-10-05T04:00:00.0000000","timeZone":"UTC"},
	"originalStartTimeZone":"India Standard Time",
	"recurrence":{"pattern":{"type":"daily","interval":1},"range":{"type":"noEnd","startDate":"2026-10-05"}}}`

const seriesDetails = `{"start":{"dateTime":"2026-10-05T09:00:00.0000000","timeZone":"India Standard Time"},
	"end":{"dateTime":"2026-10-05T09:30:00.0000000","timeZone":"India Standard Time"},
	"cancelledOccurrences":["OID.M1.2026-10-07"],
	"exceptionOccurrences":[{"id":"X1","type":"exception","originalStart":"2026-10-08T03:30:00Z","subject":"Standup (moved)",
		"start":{"dateTime":"2026-10-08T11:00:00.0000000","timeZone":"India Standard Time"},
		"end":{"dateTime":"2026-10-08T11:30:00.0000000","timeZone":"India Standard Time"}}]}`

func TestAddSeriesOccurrencesStoresOccurrencesInSeriesTimeZone(t *testing.T) {
	base, calls := newSeriesGraph(t, map[string]func() (int, string){
		"GET /me/calendars/CAL1/events/M1": func() (int, string) { return http.StatusOK, seriesDetails },
	})
	out, err := AddSeriesOccurrences(context.Background(), "tok", PIMSeriesURL(base+"/me", "CAL1", "M1"), []byte(listedSeriesMaster))
	if err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.prefer != `outlook.timezone="India Standard Time"` || !strings.Contains(c.query, "$expand=exceptionOccurrences") ||
		!strings.Contains(c.query, "cancelledOccurrences") {
		t.Fatalf("request = %+v", c)
	}
	var got struct {
		ID, Subject string
		Start       struct{ TimeZone string }
		Cancelled   []string          `json:"cancelledOccurrences"`
		Exceptions  []json.RawMessage `json:"exceptionOccurrences"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "M1" || got.Subject != "Standup" || got.Start.TimeZone != "India Standard Time" ||
		len(got.Cancelled) != 1 || len(got.Exceptions) != 1 {
		t.Fatalf("stored master = %s", out)
	}
}

func TestAddSeriesOccurrencesReportsGraphError(t *testing.T) {
	base, _ := newSeriesGraph(t, nil)
	if _, err := AddSeriesOccurrences(context.Background(), "tok", PIMSeriesURL(base+"/me", "CAL1", "M1"), []byte(listedSeriesMaster)); err == nil {
		t.Fatal("want error on http 404")
	}
}

func storedSeries(t *testing.T) []byte {
	t.Helper()
	var master, details map[string]json.RawMessage
	if err := json.Unmarshal([]byte(listedSeriesMaster), &master); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(seriesDetails), &details); err != nil {
		t.Fatal(err)
	}
	for k, v := range details {
		master[k] = v
	}
	raw, _ := json.Marshal(master)
	return raw
}

// restoredInstances are the occurrences of the new series on 6-9 Oct, 09:00 India time.
const restoredInstances = `{"value":[
	{"id":"I6","originalStart":"2026-10-06T03:30:00Z","start":{"dateTime":"2026-10-06T09:00:00.0000000"}},
	{"id":"I7","originalStart":"2026-10-07T03:30:00Z","start":{"dateTime":"2026-10-07T09:00:00.0000000"}},
	{"id":"I8","originalStart":"2026-10-08T03:30:00.0000000Z","start":{"dateTime":"2026-10-08T09:00:00.0000000"}},
	{"id":"I9","originalStart":"2026-10-09T03:30:00Z","start":{"dateTime":"2026-10-09T09:00:00.0000000"}}]}`

func seriesRestoreRoutes(patchStatus int) map[string]func() (int, string) {
	return map[string]func() (int, string){
		"POST /me/calendars/CAL1/events": func() (int, string) { return http.StatusCreated, `{"id":"NEW"}` },
		"GET /me/events/NEW/instances":   func() (int, string) { return http.StatusOK, restoredInstances },
		"DELETE /me/events/I7":           func() (int, string) { return http.StatusNoContent, "" },
		"PATCH /me/events/I8":            func() (int, string) { return patchStatus, `{}` },
	}
}

func TestRestoreCalendarEventRestoresSeriesOccurrences(t *testing.T) {
	base, calls := newSeriesGraph(t, seriesRestoreRoutes(http.StatusOK))
	if err := RestoreCalendarEvent(context.Background(), "tok", base+"/me", "CAL1", storedSeries(t)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range *calls {
		got = append(got, c.method+" "+c.path)
	}
	want := "POST /me/calendars/CAL1/events,GET /me/events/NEW/instances,DELETE /me/events/I7,GET /me/events/NEW/instances,PATCH /me/events/I8"
	if strings.Join(got, ",") != want {
		t.Fatalf("calls = %v", got)
	}
	create := (*calls)[0].body
	if _, ok := create["cancelledOccurrences"]; ok {
		t.Fatalf("series created with backup-only fields: %v", create)
	}
	if create["recurrence"] == nil || create["start"].(map[string]any)["timeZone"] != "India Standard Time" {
		t.Fatalf("series not created in its time zone: %v", create)
	}
	for _, i := range []int{1, 3} {
		if (*calls)[i].prefer != `outlook.timezone="India Standard Time"` {
			t.Fatalf("instances read without series time zone: %+v", (*calls)[i])
		}
	}
	patch := (*calls)[4].body
	if patch["subject"] != "Standup (moved)" || patch["recurrence"] != nil {
		t.Fatalf("exception patch = %v", patch)
	}
}

func TestRestoreCalendarEventSeriesPartialFailureKeepsSeries(t *testing.T) {
	base, calls := newSeriesGraph(t, seriesRestoreRoutes(http.StatusBadRequest))
	err := RestoreCalendarEvent(context.Background(), "tok", base+"/me", "CAL1", storedSeries(t))
	var serr *SeriesRestoreError
	if !errors.As(err, &serr) || serr.RestoreErrorCode() != "outcome_unknown" {
		t.Fatalf("err = %v, want SeriesRestoreError", err)
	}
	for _, c := range *calls {
		if c.method == http.MethodDelete && c.path == "/me/events/NEW" {
			t.Fatal("created series must be kept")
		}
	}
}

func TestRestoreCalendarEventSkipsCancelledEvent(t *testing.T) {
	base, calls := newSeriesGraph(t, seriesRestoreRoutes(http.StatusOK))
	raw := []byte(`{"id":"E1","subject":"Canceled: Review","isCancelled":true,"start":{"dateTime":"2026-10-05T03:30:00","timeZone":"UTC"}}`)
	if err := RestoreCalendarEvent(context.Background(), "tok", base+"/me", "CAL1", raw); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("cancelled event was restored: %+v", *calls)
	}
}

func TestRestoreCalendarEventSeriesWithoutOccurrencesIsOneCreate(t *testing.T) {
	base, calls := newSeriesGraph(t, seriesRestoreRoutes(http.StatusOK))
	if err := RestoreCalendarEvent(context.Background(), "tok", base+"/me", "CAL1", []byte(listedSeriesMaster)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestEnsureCalendarReusesOrCreates(t *testing.T) {
	cases := []struct {
		name, color, wantID string
		wantPost            bool
	}{
		{name: "Project", wantID: "C1"},
		{name: "Team", color: "lightGreen", wantID: "C2", wantPost: true},
		{name: "Holidays", color: "auto", wantID: "C2", wantPost: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, calls := newSeriesGraph(t, map[string]func() (int, string){
				"GET /me/calendars": func() (int, string) {
					return http.StatusOK, `{"value":[{"id":"RO","name":"Team","canEdit":false},{"id":"C1","name":"Project","canEdit":true}]}`
				},
				"POST /me/calendars": func() (int, string) { return http.StatusCreated, `{"id":"C2"}` },
			})
			id, err := EnsureCalendar(context.Background(), "tok", base+"/me", tc.name, tc.color)
			if err != nil || id != tc.wantID {
				t.Fatalf("id = %q, %v want %q", id, err, tc.wantID)
			}
			if !tc.wantPost {
				for _, c := range *calls {
					if c.method == http.MethodPost {
						t.Fatalf("existing calendar must not be created: %+v", *calls)
					}
				}
				return
			}
			last := (*calls)[len(*calls)-1]
			if last.method != http.MethodPost || last.body["name"] != tc.name {
				t.Fatalf("create call = %+v", last)
			}
			if tc.color == "" || strings.EqualFold(tc.color, "auto") {
				if _, ok := last.body["color"]; ok {
					t.Fatalf("auto/empty color must be omitted: %+v", last.body)
				}
			} else if last.body["color"] != tc.color {
				t.Fatalf("color = %v want %q", last.body["color"], tc.color)
			}
		})
	}
}

func TestListSeriesInstancesPagesInUTCAndSkipsCancelled(t *testing.T) {
	var base string
	base, calls := newSeriesGraph(t, map[string]func() (int, string){
		"GET /me/calendars/CAL1/events/M1/instances": func() (int, string) {
			return http.StatusOK, `{"value":[
				{"id":"I1","start":{"dateTime":"2026-10-06T03:30:00.0000000"},"end":{"dateTime":"2026-10-06T04:00:00.0000000"}},
				{"id":"I2","isCancelled":true,"start":{"dateTime":"2026-10-07T03:30:00.0000000"},"end":{"dateTime":"2026-10-07T04:00:00.0000000"}}],
				"@odata.nextLink":"` + base + `/me/next"}`
		},
		"GET /me/next": func() (int, string) {
			return http.StatusOK, `{"value":[{"id":"I3","start":{"dateTime":"2026-10-08T05:00:00.0000000"},"end":{"dateTime":"2026-10-08T05:30:00.0000000"}}]}`
		},
	})
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got, err := ListSeriesInstances(context.Background(), "tok", PIMInstancesURL(base+"/me", "CAL1", "M1", from, from.AddDate(0, 1, 0)))
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"2026-10-06T03:30:00Z", "2026-10-06T04:00:00Z"}, {"2026-10-08T05:00:00Z", "2026-10-08T05:30:00Z"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("instances = %v", got)
	}
	first := (*calls)[0]
	if first.prefer != `outlook.timezone="UTC"` || !strings.Contains(first.query, "startDateTime=2026-10-01T00%3A00%3A00Z") {
		t.Fatalf("request = %+v", first)
	}
}

func TestEventSummaryOf(t *testing.T) {
	s := EventSummaryOf([]byte(`{"subject":"Offsite","isAllDay":true,"type":"singleInstance","isCancelled":true,
		"start":{"dateTime":"2026-10-05T00:00:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-10-07T00:00:00.0000000","timeZone":"UTC"},
		"location":{"displayName":"Goa"}}`))
	if s.Subject != "Offsite" || !s.AllDay || !s.Cancelled || s.Start != "2026-10-05T00:00:00Z" || s.End != "2026-10-07T00:00:00Z" || s.Location != "Goa" {
		t.Fatalf("summary = %+v", s)
	}
}

func TestOccurrenceDate(t *testing.T) {
	if d, err := occurrenceDate("OID.AAMkAG-x_y=.2026-10-07"); err != nil || d.Format("2006-01-02") != "2026-10-07" {
		t.Fatalf("date = %v, %v", d, err)
	}
	for _, bad := range []string{"", "OID.M1", "OID.M1.notadate"} {
		if _, err := occurrenceDate(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}
