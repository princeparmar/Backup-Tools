package outlook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPIMKeys(t *testing.T) {
	prefix := ResourceKeyPrefix(mailTenant, "user", "u1")
	if got, want := PIMCalendarDir(prefix, "AQMk/cal"), prefix+"/AQMk_cal/"; got != want {
		t.Fatalf("calendar dir = %q, want %q", got, want)
	}
	if got, want := PIMContactsDir(prefix, ""), prefix+"/"; got != want {
		t.Fatalf("default contacts dir = %q", got)
	}
	folderDir := PIMContactsDir(prefix, "F/1")
	if got, want := folderDir, prefix+"/folders/F_1/"; got != want {
		t.Fatalf("folder dir = %q, want %q", got, want)
	}
	if got, want := PIMItemKey(folderDir, "c/1"), prefix+"/folders/F_1/c_1.json"; got != want {
		t.Fatalf("item key = %q", got)
	}

	if dir, in := ParsePIMContactKey(prefix + "/folders/F_1/c1.json"); !in || dir != folderDir {
		t.Fatalf("folder contact = %q %v", dir, in)
	}
	if _, in := ParsePIMContactKey(prefix + "/c1.json"); in {
		t.Fatal("default contact parsed as folder contact")
	}

	for key, want := range map[string]bool{
		prefix + "/cal/e1.json":                    true,
		prefix + "/c1.json":                        true,
		prefix + "/cal/" + PIMCalendarMetaName:     false,
		prefix + "/cal/" + PIMIndexName:            false,
		prefix + "/folders/F/" + PIMFolderMetaName: false,
		prefix + "/.file_placeholder":              false,
	} {
		if got := IsPIMItemKey(key); got != want {
			t.Errorf("IsPIMItemKey(%q) = %v", key, got)
		}
	}
}

const graphEventJSON = `{
  "id": "AAMk1", "changeKey": "ck", "createdDateTime": "2026-01-01T00:00:00Z",
  "subject": "Planning", "isAllDay": false, "isReminderOn": true, "reminderMinutesBeforeStart": 15,
  "body": {"contentType": "html", "content": "<p>Agenda</p>"},
  "start": {"dateTime": "2026-02-01T10:00:00.0000000", "timeZone": "UTC"},
  "end": {"dateTime": "2026-02-01T11:00:00.0000000", "timeZone": "UTC"},
  "location": {"displayName": "Room 1", "uniqueId": "x", "uniqueIdType": "private", "address": {}},
  "recurrence": {"pattern": {"type": "weekly", "interval": 1, "daysOfWeek": ["monday"]}, "range": {"type": "noEnd", "startDate": "2026-02-01"}},
  "attendees": [{"emailAddress": {"name": "Ann", "address": "ann@x.com"}, "type": "required"}],
  "categories": [], "onlineMeeting": {"joinUrl": "https://teams"}, "organizer": {"emailAddress": {"address": "me@x.com"}}
}`

func TestEventCreatePayloadFromGraphEvent(t *testing.T) {
	p, err := EventCreatePayload([]byte(graphEventJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"id", "changeKey", "createdDateTime", "attendees", "onlineMeeting", "organizer", "categories"} {
		if _, ok := p[gone]; ok {
			t.Errorf("payload keeps %q", gone)
		}
	}
	for _, kept := range []string{"subject", "start", "end", "recurrence", "isAllDay", "isReminderOn", "reminderMinutesBeforeStart"} {
		if _, ok := p[kept]; !ok {
			t.Errorf("payload drops %q", kept)
		}
	}
	loc := p["location"].(map[string]any)
	if loc["displayName"] != "Room 1" || loc["uniqueId"] != nil || loc["address"] != nil {
		t.Fatalf("location = %v", loc)
	}
	body := p["body"].(map[string]any)
	if body["contentType"] != "html" || body["content"] != "<p>Agenda</p><p>Attendees: Ann &lt;ann@x.com&gt;</p>" {
		t.Fatalf("body = %v", body)
	}
}

func TestEventCreatePayloadFromLegacyFlatEvent(t *testing.T) {
	raw := `{"id":"e1","subject":"Old","start":"2026-02-01T10:00:00","end":"2026-02-01T11:00:00","time_zone":"UTC","body_preview":"hi"}`
	p, err := EventCreatePayload([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p["subject"] != "Old" || p["start"].(map[string]string)["dateTime"] != "2026-02-01T10:00:00" {
		t.Fatalf("payload = %v", p)
	}
}

func TestContactCreatePayloadFromGraphContact(t *testing.T) {
	raw := `{"id":"c1","changeKey":"ck","parentFolderId":"F","displayName":"Ann Lee","givenName":"Ann",
	  "emailAddresses":[{"name":"Ann","address":"ann@x.com"}],"businessPhones":["+1 1"],"homePhones":[],
	  "mobilePhone":"+1 2","homeAddress":{"street":"1 Main","city":""},"businessAddress":{},
	  "birthday":"1990-05-01T11:59:00Z","personalNotes":"vip","middleName":null}`
	p, err := ContactCreatePayload([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"id", "changeKey", "parentFolderId", "homePhones", "businessAddress", "middleName"} {
		if _, ok := p[gone]; ok {
			t.Errorf("payload keeps %q", gone)
		}
	}
	for _, kept := range []string{"displayName", "emailAddresses", "businessPhones", "mobilePhone", "homeAddress", "birthday", "personalNotes"} {
		if _, ok := p[kept]; !ok {
			t.Errorf("payload drops %q", kept)
		}
	}
}

func TestContactCreatePayloadFromLegacyFlatContactSendsPhoneStrings(t *testing.T) {
	raw := `{"id":"c1","display_name":"Bob","emails":["bob@x.com"],"phones":["+1 1","+1 2"]}`
	p, err := ContactCreatePayload([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p["businessPhones"])
	if string(b) != `["+1 1","+1 2"]` {
		t.Fatalf("businessPhones = %s", b)
	}
}

type pimGraphCall struct {
	method, path string
	body         map[string]any
}

func newPIMGraph(t *testing.T, status func(path string) int, respond func(path string) string) (*httptest.Server, *[]pimGraphCall) {
	var mu sync.Mutex
	var calls []pimGraphCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, pimGraphCall{method: r.Method, path: r.URL.Path, body: body})
		mu.Unlock()
		code := http.StatusCreated
		if status != nil {
			code = status(r.URL.Path)
		}
		w.WriteHeader(code)
		if respond != nil {
			_, _ = w.Write([]byte(respond(r.URL.Path + "?" + r.URL.RawQuery)))
		} else {
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRestoreCalendarEventTargetsOriginalCalendar(t *testing.T) {
	srv, calls := newPIMGraph(t, nil, nil)
	if err := RestoreCalendarEvent(context.Background(), "tok", srv.URL+"/users/u1", "CAL1", []byte(graphEventJSON)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/users/u1/calendars/CAL1/events" || (*calls)[0].body["subject"] != "Planning" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestRestoreCalendarEventFallsBackToDefaultCalendar(t *testing.T) {
	srv, calls := newPIMGraph(t, func(p string) int {
		if strings.Contains(p, "/calendars/") {
			return http.StatusNotFound
		}
		return http.StatusCreated
	}, nil)
	if err := RestoreCalendarEvent(context.Background(), "tok", srv.URL+"/users/u1", "GONE", []byte(graphEventJSON)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[1].path != "/users/u1/events" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestRestoreContactTargetsFolderOrDefault(t *testing.T) {
	srv, calls := newPIMGraph(t, nil, nil)
	raw := []byte(`{"displayName":"Ann","emailAddresses":[{"address":"ann@x.com"}]}`)
	if err := RestoreContact(context.Background(), "tok", srv.URL+"/me", "F1", raw); err != nil {
		t.Fatal(err)
	}
	if err := RestoreContact(context.Background(), "tok", srv.URL+"/me", "", raw); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].path != "/me/contactFolders/F1/contacts" || (*calls)[1].path != "/me/contacts" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestRestoreContactReportsGraphError(t *testing.T) {
	srv, _ := newPIMGraph(t, func(string) int { return http.StatusBadRequest }, nil)
	if err := RestoreContact(context.Background(), "tok", srv.URL+"/me", "", []byte(`{"displayName":"Ann"}`)); err == nil {
		t.Fatal("want error on http 400")
	}
}

func TestListPIMPageSkipsCancelledAndFollowsNextLink(t *testing.T) {
	var srvURL string
	srv, _ := newPIMGraph(t, func(string) int { return http.StatusOK }, func(p string) string {
		return `{"value":[{"id":"e1","changeKey":"a"},{"id":"e2","changeKey":"b","isCancelled":true},{"changeKey":"noid"}],
		  "@odata.nextLink":"` + srvURL + `/next"}`
	})
	srvURL = srv.URL
	items, next, err := ListPIMPage(context.Background(), "tok", srv.URL+"/me/calendars/c/events")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "e1" || items[0].ChangeKey != "a" || next != srv.URL+"/next" {
		t.Fatalf("items=%+v next=%q", items, next)
	}
}

func TestListPIMContactFoldersIncludesNested(t *testing.T) {
	srv, _ := newPIMGraph(t, func(string) int { return http.StatusOK }, func(p string) string {
		switch {
		case strings.HasPrefix(p, "/me/contactFolders?"):
			return `{"value":[{"id":"F1","displayName":"Work"}]}`
		case strings.HasPrefix(p, "/me/contactFolders/F1/childFolders"):
			return `{"value":[{"id":"F2","displayName":"Team","parentFolderId":"F1"}]}`
		}
		return `{"value":[]}`
	})
	folders, err := ListPIMContactFolders(context.Background(), "tok", srv.URL+"/me")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 || folders[1].ID != "F2" || folders[1].ParentFolderID != "F1" {
		t.Fatalf("folders = %+v", folders)
	}
}
