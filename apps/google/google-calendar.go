package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	calendarEventsPageSize = 250
	calendarReadonlyScope  = "https://www.googleapis.com/auth/calendar.readonly"
	calendarScope          = "https://www.googleapis.com/auth/calendar"
)

// FlatCalendar is a slim calendar for listing (HTTP + cron).
// Summary is the API title and stays in the object key. DisplayName is what the
// sidebar shows: the user's override when Google has one, otherwise Summary.
type FlatCalendar struct {
	ID          string `json:"id"`
	Summary     string `json:"summary"`
	DisplayName string `json:"display_name,omitempty"`
	TimeZone    string `json:"timezone,omitempty"`
	Primary     bool   `json:"primary,omitempty"`
}

// FlatCalendarEvent is a slim event for listing with optional synced flag.
type FlatCalendarEvent struct {
	ID      string `json:"id"`
	Summary string `json:"summary,omitempty"`
	Status  string `json:"status,omitempty"`
	ETag    string `json:"etag,omitempty"`
	Updated string `json:"updated,omitempty"`
	Synced  bool   `json:"synced,omitempty"`
}

// FlatCalendarsResponse lists calendars for an account.
type FlatCalendarsResponse struct {
	Calendars []FlatCalendar `json:"calendars"`
}

// FlatCalendarEventsResponse is paginated events.list (HTTP route + cron).
type FlatCalendarEventsResponse struct {
	Events         []FlatCalendarEvent `json:"events"`
	RawEvents      []*calendar.Event   `json:"-"`
	NextPageToken  string              `json:"nextPageToken"`
	NextSyncToken  string              `json:"nextSyncToken,omitempty"`
	NextPageLegacy string              `json:"next_page_token,omitempty"`
}

// CalendarMetadata is stored at {email}/calendar/calendars/{calendarId}.json
type CalendarMetadata struct {
	ID            string `json:"id"`
	Summary       string `json:"summary,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	TimeZone      string `json:"timezone,omitempty"`
	NextSyncToken string `json:"next_sync_token,omitempty"`
	Primary       bool   `json:"primary,omitempty"`
}

// ListCalendarsFlat returns calendarList for HTTP handlers.
func ListCalendarsFlat(c echo.Context) (*FlatCalendarsResponse, error) {
	service, err := NewCalendarServiceFromContext(c)
	if err != nil {
		return nil, err
	}
	return ListCalendarsWithService(service)
}

// NewCalendarServiceFromContext builds a Calendar API client from the request JWT token.
func NewCalendarServiceFromContext(c echo.Context) (*calendar.Service, error) {
	httpClient, err := client(c)
	if err != nil {
		return nil, err
	}
	return calendar.NewService(c.Request().Context(), option.WithHTTPClient(httpClient))
}

// NewCalendarServiceWithAccessToken builds a Calendar API client for cron autosync.
func NewCalendarServiceWithAccessToken(ctx context.Context, accessToken string) (*calendar.Service, error) {
	return newCalendarServiceWithAccessToken(ctx, accessToken, calendarReadonlyScope)
}

// NewCalendarServiceForRestore builds a Calendar API client for restore (requires calendar.events on token).
func NewCalendarServiceForRestore(ctx context.Context, accessToken string) (*calendar.Service, error) {
	return newCalendarServiceWithAccessToken(ctx, accessToken, restoreCalendarScope)
}

func newCalendarServiceWithAccessToken(ctx context.Context, accessToken, scope string) (*calendar.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, scope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	token := &oauth2.Token{AccessToken: accessToken}
	httpClient := config.Client(ctx, token)
	return calendar.NewService(ctx, option.WithHTTPClient(httpClient))
}

// ListCalendarsWithService lists calendars via calendarList.list.
func ListCalendarsWithService(service *calendar.Service) (*FlatCalendarsResponse, error) {
	if service == nil {
		return nil, fmt.Errorf("calendar service is nil")
	}
	items := make([]FlatCalendar, 0)
	pageToken := ""
	for {
		// Birthdays and Tasks are hidden in the API while the Calendar page still shows them.
		call := service.CalendarList.List().MaxResults(250).ShowHidden(true)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list calendars: %w", err)
		}
		for _, cal := range resp.Items {
			if cal == nil || strings.TrimSpace(cal.Id) == "" || cal.Deleted {
				continue
			}
			summary := strings.TrimSpace(cal.Summary)
			items = append(items, FlatCalendar{
				ID:          strings.TrimSpace(cal.Id),
				Summary:     summary,
				DisplayName: CalendarDisplayName(summary, cal.SummaryOverride),
				TimeZone:    strings.TrimSpace(cal.TimeZone),
				Primary:     cal.Primary,
			})
		}
		pageToken = strings.TrimSpace(resp.NextPageToken)
		if pageToken == "" {
			break
		}
	}
	return &FlatCalendarsResponse{Calendars: items}, nil
}

// ListCalendarEventsWithService lists events for one calendar (baseline pageToken or incremental syncToken).
func ListCalendarEventsWithService(service *calendar.Service, calendarID, pageToken, syncToken string) (*FlatCalendarEventsResponse, error) {
	if service == nil {
		return nil, fmt.Errorf("calendar service is nil")
	}
	calendarID = strings.TrimSpace(calendarID)
	if calendarID == "" {
		return nil, fmt.Errorf("calendar_id is required")
	}
	pageToken = strings.TrimSpace(pageToken)
	syncToken = strings.TrimSpace(syncToken)
	if pageToken != "" && syncToken != "" {
		return nil, fmt.Errorf("pageToken and syncToken are mutually exclusive")
	}

	call := service.Events.List(calendarID).
		SingleEvents(false).
		MaxResults(calendarEventsPageSize)
	if syncToken != "" {
		call = call.SyncToken(syncToken).ShowDeleted(true)
	} else if pageToken != "" {
		call = call.PageToken(pageToken)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	out := &FlatCalendarEventsResponse{
		RawEvents:     resp.Items,
		NextPageToken: strings.TrimSpace(resp.NextPageToken),
		NextSyncToken: strings.TrimSpace(resp.NextSyncToken),
	}
	out.NextPageLegacy = out.NextPageToken
	out.Events = make([]FlatCalendarEvent, 0, len(resp.Items))
	for _, ev := range resp.Items {
		if ev == nil || strings.TrimSpace(ev.Id) == "" {
			continue
		}
		out.Events = append(out.Events, FlatCalendarEvent{
			ID:      strings.TrimSpace(ev.Id),
			Summary: strings.TrimSpace(ev.Summary),
			Status:  strings.TrimSpace(ev.Status),
			ETag:    strings.TrimSpace(ev.Etag),
			Updated: strings.TrimSpace(ev.Updated),
		})
	}
	return out, nil
}

// IsSyncTokenGone reports whether err is HTTP 410 (invalid sync token).
func IsSyncTokenGone(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == 410
	}
	return strings.Contains(err.Error(), "410")
}

// EventShouldBackup returns false for cancelled tombstones (append-only backup: skip, do not delete vault).
func EventShouldBackup(ev *calendar.Event) bool {
	if ev == nil || strings.TrimSpace(ev.Id) == "" {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(ev.Status), "cancelled")
}

// SanitizeCalendarPathSegment makes calendar/event ids safe for object keys.
func SanitizeCalendarPathSegment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "_")
	return s
}

// CalendarDisplayName is the sidebar title. summaryOverride wins when the user renamed the calendar.
func CalendarDisplayName(summary, summaryOverride string) string {
	if name := strings.TrimSpace(summaryOverride); name != "" {
		return name
	}
	return strings.TrimSpace(summary)
}

// CalendarBackupDisplayName returns a vault-safe calendar or event summary for object keys.
func CalendarBackupDisplayName(summary string) string {
	summary = SanitizeCalendarPathSegment(summary)
	if summary == "" {
		return "untitled"
	}
	return summary
}

// CalendarMetaObjectKey returns {email}/calendar/calendars/{calendarId}_{summary}.json
func CalendarMetaObjectKey(email, calendarID, summary string) string {
	return fmt.Sprintf("%s/calendar/calendars/%s_%s.json",
		strings.TrimSpace(email),
		SanitizeCalendarPathSegment(calendarID),
		CalendarBackupDisplayName(summary),
	)
}

// CalendarIndexFileVersion is the vault browser index. One object lists every event
// the month view needs, so opening Calendar is one read.
const CalendarIndexFileVersion = 1

// CalendarIndexObjectKey returns {email}/calendar/_index.json.
func CalendarIndexObjectKey(email string) string {
	return fmt.Sprintf("%s/calendar/_index.json", strings.TrimSpace(email))
}

// CalendarIndexCalendar is one calendar on the sidebar. The id is the Calendar API id, not a piece of the file name.
type CalendarIndexCalendar struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Primary bool   `json:"primary,omitempty"`
}

// CalendarIndex is the single object the calendar view reads.
type CalendarIndex struct {
	V         int                     `json:"v"`
	Calendars []CalendarIndexCalendar `json:"calendars,omitempty"`
	Events    []CalendarIndexEvent    `json:"events"`
}

// CalendarIndexGuest is one attendee on the view index.
type CalendarIndexGuest struct {
	Name     string `json:"name"`
	Response string `json:"response,omitempty"`
}

// CalendarIndexEvent is one event as the month grid needs it. The full event
// stays in its own object for restore.
type CalendarIndexEvent struct {
	ID               string               `json:"id"`
	Key              string               `json:"key"`
	CalendarID       string               `json:"calendar_id"`
	Summary          string               `json:"summary,omitempty"`
	Location         string               `json:"location,omitempty"`
	Description      string               `json:"description,omitempty"`
	Start            string               `json:"start"`
	End              string               `json:"end,omitempty"`
	AllDay           bool                 `json:"all_day,omitempty"`
	Rule             string               `json:"rule,omitempty"`
	RecurringEventID string               `json:"recurring_event_id,omitempty"`
	OriginalStart    string               `json:"original_start,omitempty"`
	ColorID          string               `json:"color_id,omitempty"`
	Meet             string               `json:"meet,omitempty"`
	Organizer        string               `json:"organizer,omitempty"`
	Guests           []CalendarIndexGuest `json:"guests,omitempty"`
}

// Normalize sets the version and a non-null event list.
func (idx *CalendarIndex) Normalize() {
	if idx == nil {
		return
	}
	if idx.Events == nil {
		idx.Events = []CalendarIndexEvent{}
	}
	idx.V = CalendarIndexFileVersion
}

// SetCalendars records every listed calendar, including ones whose events failed to sync.
func (idx *CalendarIndex) SetCalendars(cals []FlatCalendar) {
	if idx == nil {
		return
	}
	out := make([]CalendarIndexCalendar, 0, len(cals))
	for _, cal := range cals {
		id := strings.TrimSpace(cal.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(cal.DisplayName)
		if name == "" {
			name = strings.TrimSpace(cal.Summary)
		}
		if name == "" {
			name = id
		}
		out = append(out, CalendarIndexCalendar{ID: id, Name: name, Primary: cal.Primary})
	}
	idx.Calendars = out
	idx.Normalize()
}

// Put inserts or replaces one event.
func (idx *CalendarIndex) Put(ev CalendarIndexEvent) {
	if idx == nil || strings.TrimSpace(ev.ID) == "" || strings.TrimSpace(ev.CalendarID) == "" {
		return
	}
	for i := range idx.Events {
		if idx.Events[i].CalendarID == ev.CalendarID && idx.Events[i].ID == ev.ID {
			idx.Events[i] = ev
			idx.Normalize()
			return
		}
	}
	idx.Events = append(idx.Events, ev)
	idx.Normalize()
}

// Remove drops one event from the index. The backup file stays.
func (idx *CalendarIndex) Remove(calendarID, eventID string) {
	if idx == nil {
		return
	}
	calendarID = strings.TrimSpace(calendarID)
	eventID = strings.TrimSpace(eventID)
	next := idx.Events[:0]
	for _, ev := range idx.Events {
		if ev.CalendarID == calendarID && ev.ID == eventID {
			continue
		}
		next = append(next, ev)
	}
	idx.Events = next
	idx.Normalize()
}

// ReplaceCalendar swaps every indexed event for one calendar.
func (idx *CalendarIndex) ReplaceCalendar(calendarID string, events []CalendarIndexEvent) {
	if idx == nil {
		return
	}
	calendarID = strings.TrimSpace(calendarID)
	next := make([]CalendarIndexEvent, 0, len(idx.Events)+len(events))
	for _, ev := range idx.Events {
		if ev.CalendarID != calendarID {
			next = append(next, ev)
		}
	}
	next = append(next, events...)
	idx.Events = next
	idx.Normalize()
}

// IndexEventFrom builds the view row for one Calendar API event.
func IndexEventFrom(calendarID, objectKey string, ev *calendar.Event) (CalendarIndexEvent, bool) {
	if !EventShouldBackup(ev) || ev.Start == nil {
		return CalendarIndexEvent{}, false
	}
	allDay := strings.TrimSpace(ev.Start.Date) != ""
	start := strings.TrimSpace(ev.Start.Date)
	end := ""
	if ev.End != nil {
		end = strings.TrimSpace(ev.End.Date)
	}
	if !allDay {
		start = strings.TrimSpace(ev.Start.DateTime)
		if ev.End != nil {
			end = strings.TrimSpace(ev.End.DateTime)
		}
	}
	if start == "" {
		return CalendarIndexEvent{}, false
	}
	row := CalendarIndexEvent{
		ID:               strings.TrimSpace(ev.Id),
		Key:              objectKey,
		CalendarID:       strings.TrimSpace(calendarID),
		Summary:          strings.TrimSpace(ev.Summary),
		Location:         strings.TrimSpace(ev.Location),
		Description:      trimRunes(ev.Description, 2000),
		Start:            start,
		End:              end,
		AllDay:           allDay,
		Rule:             eventRule(ev),
		RecurringEventID: strings.TrimSpace(ev.RecurringEventId),
		OriginalStart:    eventOriginalStart(ev),
		ColorID:          strings.TrimSpace(ev.ColorId),
		Meet:             eventMeetLink(ev),
		Organizer:        eventOrganizer(ev),
		Guests:           eventGuests(ev),
	}
	return row, row.ID != "" && row.CalendarID != ""
}

func trimRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func eventRule(ev *calendar.Event) string {
	for _, line := range ev.Recurrence {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "RRULE:") {
			return strings.TrimSpace(line[len("RRULE:"):])
		}
	}
	return ""
}

func eventOriginalStart(ev *calendar.Event) string {
	if ev.OriginalStartTime == nil {
		return ""
	}
	if d := strings.TrimSpace(ev.OriginalStartTime.Date); d != "" {
		return d
	}
	return strings.TrimSpace(ev.OriginalStartTime.DateTime)
}

func eventOrganizer(ev *calendar.Event) string {
	if ev.Organizer == nil {
		return ""
	}
	if name := strings.TrimSpace(ev.Organizer.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(ev.Organizer.Email)
}

func eventMeetLink(ev *calendar.Event) string {
	if link := strings.TrimSpace(ev.HangoutLink); link != "" {
		return link
	}
	if ev.ConferenceData == nil {
		return ""
	}
	for _, point := range ev.ConferenceData.EntryPoints {
		if point == nil || !strings.EqualFold(point.EntryPointType, "video") {
			continue
		}
		if uri := strings.TrimSpace(point.Uri); uri != "" {
			return uri
		}
	}
	return ""
}

func eventGuests(ev *calendar.Event) []CalendarIndexGuest {
	if len(ev.Attendees) == 0 {
		return nil
	}
	out := make([]CalendarIndexGuest, 0, len(ev.Attendees))
	for _, guest := range ev.Attendees {
		if guest == nil {
			continue
		}
		name := strings.TrimSpace(guest.DisplayName)
		if name == "" {
			name = strings.TrimSpace(guest.Email)
		}
		if name == "" {
			continue
		}
		out = append(out, CalendarIndexGuest{Name: name, Response: strings.TrimSpace(guest.ResponseStatus)})
		if len(out) == 30 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CalendarObjectKey returns {email}/calendar/events/{calendarId}_{calSummary}/{yyyy}/{mm}/{dd}/{eventId}_{eventSummary}.json
// createdTime is the Calendar API event.created (RFC3339).
func CalendarObjectKey(email, calendarID, calendarSummary, eventID, eventSummary, createdTime string) string {
	return fmt.Sprintf("%s/calendar/events/%s_%s/%s/%s_%s.json",
		strings.TrimSpace(email),
		SanitizeCalendarPathSegment(calendarID),
		CalendarBackupDisplayName(calendarSummary),
		ObjectKeyDatePathFromRFC3339(createdTime),
		SanitizeCalendarPathSegment(eventID),
		CalendarBackupDisplayName(eventSummary),
	)
}

// ParseCalendarIDFromPathSegment extracts calendarId from a path segment ({id} or {id}_{summary}).
func ParseCalendarIDFromPathSegment(segment string) string {
	id, _ := SplitCalendarPathSegment(segment)
	return id
}

// SplitCalendarPathSegment splits {calendarId}_{summary}. Google calendar IDs are email-like and
// often contain underscores in the local part (c_classroom…@group.calendar.google.com). Split on
// the first '_' after the domain, not the first '_' in the whole segment.
func SplitCalendarPathSegment(segment string) (calendarID, summary string) {
	segment = SanitizeCalendarPathSegment(segment)
	if segment == "" {
		return "", ""
	}
	at := strings.LastIndexByte(segment, '@')
	if at >= 0 {
		i := at + 1
		for i < len(segment) && isCalendarDomainChar(segment[i]) {
			i++
		}
		calendarID = segment[:i]
		if i < len(segment) && segment[i] == '_' {
			summary = segment[i+1:]
		}
		return calendarID, summary
	}
	if idx := strings.IndexByte(segment, '_'); idx > 0 {
		return segment[:idx], segment[idx+1:]
	}
	return segment, ""
}

func isCalendarDomainChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '.' || b == '-'
}

// IsReadOnlyGoogleCalendar is true for Google-owned holiday/birthday calendars that cannot be written.
func IsReadOnlyGoogleCalendar(calendarID string) bool {
	id := strings.ToLower(strings.TrimSpace(calendarID))
	if id == "" {
		return false
	}
	if strings.Contains(id, "group.v.calendar.google.com") {
		return true
	}
	return strings.Contains(id, "#holiday@") || strings.Contains(id, "#contacts@")
}

// ParseEventIDFromPathSegment extracts eventId from a path segment ({id} or {id}_{summary}).
func ParseEventIDFromPathSegment(segment string) string {
	segment = strings.TrimSpace(segment)
	if idx := strings.Index(segment, "_"); idx > 0 {
		return segment[:idx]
	}
	return segment
}

// IsCalendarEventSynced checks whether an event object exists in synced_objects paths.
func IsCalendarEventSynced(syncedMap map[string]bool, email, calendarID, eventID, calendarSummary, eventSummary string) bool {
	if syncedMap == nil {
		return false
	}
	return calendarEventSyncedByScan(syncedMap, email, calendarID, eventID)
}

func calendarEventSyncedByScan(syncedMap map[string]bool, email, calendarID, eventID string) bool {
	wantCalID := SanitizeCalendarPathSegment(calendarID)
	wantEventID := SanitizeCalendarPathSegment(eventID)
	prefix := fmt.Sprintf("%s/calendar/events/", strings.TrimSpace(email))
	for key := range syncedMap {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".json") {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
		parts := strings.Split(rest, "/")
		// Dated: {calSeg}/{yyyy}/{mm}/{dd}/{eventSeg}
		if len(parts) != 5 {
			continue
		}
		gotCalID := ParseCalendarIDFromPathSegment(parts[0])
		gotEventID := ParseEventIDFromPathSegment(parts[4])
		if gotCalID == wantCalID && gotEventID == wantEventID {
			return true
		}
	}
	return false
}

// BuildCalendarSyncedEventIDSet builds eventId set from dated synced keys under email/calendar/events/.
func BuildCalendarSyncedEventIDSet(objectKeys map[string]bool, email, calendarID string) map[string]struct{} {
	set := make(map[string]struct{})
	email = strings.TrimSpace(email)
	wantCalID := SanitizeCalendarPathSegment(calendarID)
	eventRoot := fmt.Sprintf("%s/calendar/events/", email)
	for key := range objectKeys {
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, eventRoot) || !strings.HasSuffix(key, ".json") {
			continue
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(key, eventRoot), ".json")
		parts := strings.Split(rest, "/")
		if len(parts) != 5 {
			continue
		}
		if ParseCalendarIDFromPathSegment(parts[0]) != wantCalID {
			continue
		}
		if id := ParseEventIDFromPathSegment(parts[4]); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

// IsCalendarEventRestoreObjectKey returns true for backed-up event JSON paths (not metadata or placeholders).
func IsCalendarEventRestoreObjectKey(objectKey string) bool {
	objectKey = strings.TrimSpace(objectKey)
	if !strings.Contains(objectKey, "/calendar/events/") || !strings.HasSuffix(objectKey, ".json") {
		return false
	}
	if strings.Contains(objectKey, "/calendar/calendars/") {
		return false
	}
	return !strings.Contains(objectKey, ".file_placeholder")
}

// ParseCalendarEventObjectKey extracts calendarId and eventId from a dated vault object key.
func ParseCalendarEventObjectKey(objectKey string) (calendarID, eventID string, ok bool) {
	objectKey = strings.TrimSpace(objectKey)
	const marker = "/calendar/events/"
	idx := strings.Index(objectKey, marker)
	if idx < 0 {
		return "", "", false
	}
	rest := strings.TrimSuffix(objectKey[idx+len(marker):], ".json")
	parts := strings.Split(rest, "/")
	// Dated: {calSeg}/{yyyy}/{mm}/{dd}/{eventSeg}
	if len(parts) != 5 {
		return "", "", false
	}
	calendarID = ParseCalendarIDFromPathSegment(parts[0])
	eventID = ParseEventIDFromPathSegment(parts[4])
	if calendarID == "" || eventID == "" {
		return "", "", false
	}
	return calendarID, eventID, true
}

// RestoreCalendarEventFromBackup inserts an event into Google Calendar from vault JSON.
// Cancelled events and Google-owned holiday/birthday calendars are skipped (not failures).
// Insert 400/403 from Google are failures so the UI cannot report a no-op as success.
// A missing destination calendar falls back to the user's primary calendar so the event is not lost.
func RestoreCalendarEventFromBackup(ctx context.Context, service *calendar.Service, calendarID string, data []byte) error {
	if service == nil {
		return fmt.Errorf("calendar service is nil")
	}
	calendarID = strings.TrimSpace(calendarID)
	if calendarID == "" {
		return fmt.Errorf("calendar_id is required")
	}
	var ev calendar.Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("parse calendar event backup: %w", err)
	}
	if calendarEventRestoreSkip(&ev, calendarID) {
		return nil
	}
	toInsert := prepareCalendarEventForInsert(&ev)
	err := insertCalendarEvent(service, calendarID, toInsert)
	if err == nil {
		return nil
	}
	if isGoogleAPINotFound(err) && !strings.EqualFold(calendarID, "primary") {
		if retryErr := insertCalendarEvent(service, "primary", toInsert); retryErr == nil {
			return nil
		} else {
			err = retryErr
		}
	}
	return fmt.Errorf("insert calendar event: %w", err)
}

func calendarEventRestoreSkip(ev *calendar.Event, calendarID string) bool {
	if IsReadOnlyGoogleCalendar(calendarID) {
		return true
	}
	return ev != nil && strings.EqualFold(strings.TrimSpace(ev.Status), "cancelled")
}

func insertCalendarEvent(service *calendar.Service, calendarID string, ev *calendar.Event) error {
	_, err := service.Events.Insert(calendarID, ev).Do()
	if err != nil && isReminderConflict(err) {
		if ev != nil {
			ev.Reminders = nil
		}
		_, err = service.Events.Insert(calendarID, ev).Do()
	}
	return err
}

func isReminderConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannotusedefaultremindersandspecifyoverride") ||
		strings.Contains(msg, "default reminders and overrides")
}

func isGoogleAPINotFound(err error) bool {
	return googleAPICode(err) == 404
}

func googleAPICode(err error) int {
	if err == nil {
		return 0
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.Code
	}
	return 0
}

func prepareCalendarEventForInsert(ev *calendar.Event) *calendar.Event {
	if ev == nil {
		return &calendar.Event{}
	}
	out := *ev
	out.Id = ""
	out.Etag = ""
	out.ICalUID = ""
	out.HtmlLink = ""
	out.Created = ""
	out.Updated = ""
	out.HangoutLink = ""
	out.RecurringEventId = ""
	out.OriginalStartTime = nil
	out.Organizer = nil
	out.Creator = nil
	out.Source = nil
	out.Gadget = nil
	out.ConferenceData = nil
	out.Attachments = nil
	out.Reminders = sanitizeCalendarReminders(ev.Reminders)
	out.Locked = false
	out.PrivateCopy = false
	switch strings.ToLower(strings.TrimSpace(out.EventType)) {
	case "", "default":
		out.EventType = ""
	default:
		out.EventType = "default"
	}
	return &out
}

// sanitizeCalendarReminders drops the Google insert 400
// cannotUseDefaultRemindersAndSpecifyOverride: vault JSON often has both useDefault
// and overrides from events.list even though insert allows only one.
func sanitizeCalendarReminders(in *calendar.EventReminders) *calendar.EventReminders {
	if in == nil {
		return nil
	}
	overrides := make([]*calendar.EventReminder, 0, len(in.Overrides))
	for _, item := range in.Overrides {
		if item == nil {
			continue
		}
		overrides = append(overrides, &calendar.EventReminder{
			Method:  strings.TrimSpace(item.Method),
			Minutes: item.Minutes,
		})
		if len(overrides) == 5 {
			break
		}
	}
	if len(overrides) > 0 && in.UseDefault {
		// events.list often returns both. Insert allows only one. Keep calendar defaults.
		return &calendar.EventReminders{UseDefault: true}
	}
	if len(overrides) > 0 {
		return &calendar.EventReminders{Overrides: overrides}
	}
	return &calendar.EventReminders{UseDefault: in.UseDefault}
}
