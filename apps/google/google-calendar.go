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
	calendarReadonlyScope = "https://www.googleapis.com/auth/calendar.readonly"
	calendarScope         = "https://www.googleapis.com/auth/calendar"
)

// FlatCalendar is a slim calendar for listing (HTTP + cron).
type FlatCalendar struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	TimeZone string `json:"timezone,omitempty"`
	Primary  bool   `json:"primary,omitempty"`
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
	TimeZone      string `json:"timezone,omitempty"`
	NextSyncToken string `json:"next_sync_token,omitempty"`
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
	resp, err := service.CalendarList.List().Do()
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	items := make([]FlatCalendar, 0, len(resp.Items))
	for _, cal := range resp.Items {
		if cal == nil || strings.TrimSpace(cal.Id) == "" {
			continue
		}
		items = append(items, FlatCalendar{
			ID:       strings.TrimSpace(cal.Id),
			Summary:  strings.TrimSpace(cal.Summary),
			TimeZone: strings.TrimSpace(cal.TimeZone),
			Primary:  cal.Primary,
		})
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

// CalendarMetaObjectKey returns {email}/calendar/calendars/{calendarId}.json
func CalendarMetaObjectKey(email, calendarID string) string {
	return fmt.Sprintf("%s/calendar/calendars/%s.json", strings.TrimSpace(email), SanitizeCalendarPathSegment(calendarID))
}

// CalendarObjectKey returns {email}/calendar/events/{calendarId}/{eventId}.json
func CalendarObjectKey(email, calendarID, eventID string) string {
	return fmt.Sprintf("%s/calendar/events/%s/%s.json",
		strings.TrimSpace(email),
		SanitizeCalendarPathSegment(calendarID),
		SanitizeCalendarPathSegment(eventID),
	)
}

// IsCalendarEventSynced checks whether an event object exists in synced_objects paths.
func IsCalendarEventSynced(syncedMap map[string]bool, email, calendarID, eventID string) bool {
	if syncedMap == nil {
		return false
	}
	return syncedMap[CalendarObjectKey(email, calendarID, eventID)]
}

// BuildCalendarSyncedEventIDSet builds eventId set from synced keys under email/calendar/events/{calendarId}/.
func BuildCalendarSyncedEventIDSet(objectKeys map[string]bool, email, calendarID string) map[string]struct{} {
	set := make(map[string]struct{})
	email = strings.TrimSpace(email)
	calendarID = SanitizeCalendarPathSegment(calendarID)
	prefix := fmt.Sprintf("%s/calendar/events/%s/", email, calendarID)
	for key := range objectKeys {
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".json") {
			continue
		}
		segment := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
		if id := strings.TrimSpace(segment); id != "" {
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

// ParseCalendarEventObjectKey extracts calendarId and eventId from a vault object key.
func ParseCalendarEventObjectKey(objectKey string) (calendarID, eventID string, ok bool) {
	objectKey = strings.TrimSpace(objectKey)
	const marker = "/calendar/events/"
	idx := strings.Index(objectKey, marker)
	if idx < 0 {
		return "", "", false
	}
	rest := strings.TrimSuffix(objectKey[idx+len(marker):], ".json")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	calendarID = strings.TrimSpace(parts[0])
	eventID = strings.TrimSpace(parts[1])
	if calendarID == "" || eventID == "" {
		return "", "", false
	}
	return calendarID, eventID, true
}

// RestoreCalendarEventFromBackup inserts an event into Google Calendar from vault JSON.
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
	if strings.EqualFold(strings.TrimSpace(ev.Status), "cancelled") {
		return fmt.Errorf("cannot restore cancelled event")
	}
	toInsert := prepareCalendarEventForInsert(&ev)
	_, err := service.Events.Insert(calendarID, toInsert).Do()
	if err != nil {
		return fmt.Errorf("insert calendar event: %w", err)
	}
	return nil
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
	out.Recurrence = nil
	out.RecurringEventId = ""
	out.OriginalStartTime = nil
	return &out
}
