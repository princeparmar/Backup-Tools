package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

// Writable event fields copied from a backed-up Graph event. Attendees are left out on purpose:
// Graph sends an invitation to every attendee of a created event, so they are listed in the body.
var eventCreateFields = []string{
	"subject", "body", "start", "end", "isAllDay", "recurrence", "reminderMinutesBeforeStart",
	"isReminderOn", "categories", "showAs", "sensitivity", "importance",
}

var eventLocationFields = []string{"displayName", "address", "coordinates", "locationEmailAddress", "locationType"}

var contactCreateFields = []string{
	"assistantName", "birthday", "businessAddress", "businessHomePage", "businessPhones", "categories",
	"children", "companyName", "department", "displayName", "emailAddresses", "fileAs", "generation",
	"givenName", "homeAddress", "homePhones", "imAddresses", "initials", "jobTitle", "manager",
	"middleName", "mobilePhone", "nickName", "officeLocation", "otherAddress", "personalNotes",
	"profession", "spouseName", "surname", "title", "yomiCompanyName", "yomiGivenName", "yomiSurname",
}

// EventCreatePayload is the Graph create body for a backed-up event: full Graph JSON, or the
// older FlatEvent summary.
func EventCreatePayload(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse calendar event: %w", err)
	}
	if _, graphShaped := m["start"].(map[string]any); !graphShaped {
		return legacyEventPayload(raw)
	}
	if s, _ := m["subject"].(string); strings.TrimSpace(s) == "" {
		m["subject"] = "(No subject)"
	}
	out := pickNonEmpty(m, eventCreateFields)
	if loc, ok := m["location"].(map[string]any); ok {
		if l := pickNonEmpty(loc, eventLocationFields); len(l) > 0 {
			out["location"] = l
		}
	}
	if names := eventAttendeeNames(m["attendees"]); len(names) > 0 {
		out["body"] = withAttendeeNote(out["body"], names)
	}
	return out, nil
}

// ContactCreatePayload is the Graph create body for a backed-up contact: full Graph JSON, or the
// older FlatContact summary.
func ContactCreatePayload(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse contact: %w", err)
	}
	if _, flat := m["display_name"]; flat {
		return legacyContactPayload(raw)
	}
	out := pickNonEmpty(m, contactCreateFields)
	if len(out) == 0 {
		return nil, fmt.Errorf("contact has no restorable fields")
	}
	return out, nil
}

// legacyEventPayload is the Graph create body for an event stored as a FlatEvent.
func legacyEventPayload(raw []byte) (map[string]any, error) {
	var ev FlatEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("parse calendar event: %w", err)
	}
	if strings.TrimSpace(ev.Subject) == "" {
		return nil, fmt.Errorf("calendar event subject missing")
	}
	tz := strings.TrimSpace(ev.TimeZone)
	if tz == "" {
		tz = "UTC"
	}
	out := map[string]any{"subject": ev.Subject, "isAllDay": ev.IsAllDay}
	if strings.TrimSpace(ev.BodyPreview) != "" {
		out["body"] = map[string]string{"contentType": "text", "content": ev.BodyPreview}
	}
	if strings.TrimSpace(ev.Start) != "" {
		out["start"] = map[string]string{"dateTime": ev.Start, "timeZone": tz}
	}
	if strings.TrimSpace(ev.End) != "" {
		out["end"] = map[string]string{"dateTime": ev.End, "timeZone": tz}
	}
	return out, nil
}

// legacyContactPayload is the Graph create body for a contact stored as a FlatContact.
func legacyContactPayload(raw []byte) (map[string]any, error) {
	var c FlatContact
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse contact: %w", err)
	}
	name := strings.TrimSpace(c.DisplayName)
	if name == "" {
		name = strings.TrimSpace(strings.TrimSpace(c.GivenName) + " " + strings.TrimSpace(c.Surname))
	}
	if name == "" {
		return nil, fmt.Errorf("contact display name missing")
	}
	out := map[string]any{"displayName": name}
	for field, v := range map[string]string{"givenName": c.GivenName, "surname": c.Surname, "companyName": c.CompanyName, "jobTitle": c.JobTitle} {
		if v != "" {
			out[field] = v
		}
	}
	var emails []map[string]any
	for _, e := range c.Emails {
		if e = strings.TrimSpace(e); e != "" {
			emails = append(emails, map[string]any{"address": e, "name": name})
		}
	}
	if len(emails) > 0 {
		out["emailAddresses"] = emails
	}
	var phones []string
	for _, p := range c.Phones {
		if p = strings.TrimSpace(p); p != "" {
			phones = append(phones, p)
		}
	}
	if len(phones) > 0 {
		out["businessPhones"] = phones
	}
	return out, nil
}

// RestoreCalendarEvent creates a backed-up event in calendarID of the user at userBase. When that
// calendar is gone or read-only the event goes to the default calendar. A cancelled event is not
// restored, as that would bring back a meeting that was called off. A recurring series is restored
// with its changed and cancelled occurrences; when not all of them apply the series stays and a
// *SeriesRestoreError is returned.
func RestoreCalendarEvent(ctx context.Context, accessToken, userBase, calendarID string, raw []byte) error {
	var head struct {
		IsCancelled bool `json:"isCancelled"`
		seriesBackup
	}
	_ = json.Unmarshal(raw, &head)
	if head.IsCancelled {
		return nil
	}
	payload, err := EventCreatePayload(raw)
	if err != nil {
		return err
	}
	base := strings.TrimRight(userBase, "/")
	target := base + "/events"
	if id := strings.TrimSpace(calendarID); id != "" {
		target = base + "/calendars/" + url.PathEscape(id) + "/events"
	}
	resp, err := pimCreate(ctx, accessToken, target, base+"/events", payload)
	if err != nil {
		return err
	}
	s := head.seriesBackup
	if s.Type != eventTypeSeriesMaster || len(s.CancelledOccurrences)+len(s.ExceptionOccurrences) == 0 {
		return nil
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(resp, &created) != nil || created.ID == "" {
		return &SeriesRestoreError{Err: fmt.Errorf("created series has no id")}
	}
	if err := restoreSeriesOccurrences(ctx, accessToken, base, created.ID, s, raw); err != nil {
		return &SeriesRestoreError{Err: err}
	}
	return nil
}

// EnsureCalendar returns the id of the user's writable calendar named name, creating it when
// missing. Used to restore events of a calendar deleted from Microsoft.
func EnsureCalendar(ctx context.Context, accessToken, userBase, name string) (string, error) {
	calendars, err := ListPIMCalendars(ctx, accessToken, userBase)
	if err != nil {
		return "", err
	}
	for _, c := range calendars {
		if c.CanEdit && c.Name == name {
			return c.ID, nil
		}
	}
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, strings.TrimRight(userBase, "/")+"/calendars", body)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if status < 200 || status >= 300 || json.Unmarshal(resp, &created) != nil || created.ID == "" {
		return "", fmt.Errorf("create calendar http %d: %s", status, truncateForErr(resp))
	}
	return created.ID, nil
}

// RestoreContact creates a backed-up contact in folderID ("" is the default Contacts folder) of the
// user at userBase. When that folder is gone the contact goes to the default folder.
func RestoreContact(ctx context.Context, accessToken, userBase, folderID string, raw []byte) error {
	payload, err := ContactCreatePayload(raw)
	if err != nil {
		return err
	}
	base := strings.TrimRight(userBase, "/")
	target := base + "/contacts"
	if id := strings.TrimSpace(folderID); id != "" {
		target = base + "/contactFolders/" + url.PathEscape(id) + "/contacts"
	}
	_, err = pimCreate(ctx, accessToken, target, base+"/contacts", payload)
	return err
}

// pimCreate POSTs payload to target, or to fallback when target is gone or read-only, and returns
// the created item.
func pimCreate(ctx context.Context, accessToken, target, fallback string, payload map[string]any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	if (status == http.StatusNotFound || status == http.StatusForbidden) && target != fallback {
		resp, status, err = graphDoJSONWrite(ctx, accessToken, http.MethodPost, fallback, body)
		if err != nil {
			return nil, err
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("graph create http %d: %s", status, truncateForErr(resp))
	}
	return resp, nil
}

func pickNonEmpty(m map[string]any, fields []string) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if v, ok := m[f]; ok && !isEmptyJSON(v) {
			out[f] = v
		}
	}
	return out
}

func isEmptyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		for _, inner := range t {
			if !isEmptyJSON(inner) {
				return false
			}
		}
		return true
	}
	return false
}

func eventAttendeeNames(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, a := range list {
		att, _ := a.(map[string]any)
		email, _ := att["emailAddress"].(map[string]any)
		name, _ := email["name"].(string)
		addr, _ := email["address"].(string)
		switch {
		case name != "" && addr != "" && name != addr:
			out = append(out, name+" <"+addr+">")
		case addr != "":
			out = append(out, addr)
		case name != "":
			out = append(out, name)
		}
	}
	return out
}

func withAttendeeNote(body any, names []string) map[string]any {
	b, _ := body.(map[string]any)
	contentType, _ := b["contentType"].(string)
	content, _ := b["content"].(string)
	note := "Attendees: " + strings.Join(names, "; ")
	if strings.EqualFold(contentType, "html") {
		content += "<p>" + html.EscapeString(note) + "</p>"
	} else {
		contentType = "text"
		if content != "" {
			content += "\n\n"
		}
		content += note
	}
	return map[string]any{"contentType": contentType, "content": content}
}
