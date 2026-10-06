package outlook

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

// FlatCalendar is a calendar summary for browse/autosync.
type FlatCalendar struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color,omitempty"`
	CanEdit   bool   `json:"can_edit"`
	IsDefault bool   `json:"is_default"`
}

// FlatEvent is a calendar event summary for browse/autosync.
type FlatEvent struct {
	ID                   string `json:"id"`
	Subject              string `json:"subject"`
	Start                string `json:"start,omitempty"`
	End                  string `json:"end,omitempty"`
	TimeZone             string `json:"time_zone,omitempty"`
	IsCancelled          bool   `json:"is_cancelled"`
	IsAllDay             bool   `json:"is_all_day"`
	BodyPreview          string `json:"body_preview,omitempty"`
	Organizer            string `json:"organizer,omitempty"`
	LastModifiedDateTime string `json:"last_modified,omitempty"`
}

// FlatContact is a contact summary for browse/autosync.
type FlatContact struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	GivenName   string   `json:"given_name,omitempty"`
	Surname     string   `json:"surname,omitempty"`
	Emails      []string `json:"emails,omitempty"`
	Phones      []string `json:"phones,omitempty"`
	CompanyName string   `json:"company_name,omitempty"`
	JobTitle    string   `json:"job_title,omitempty"`
	ChangeKey   string   `json:"change_key,omitempty"`
}

// ListCalendars returns calendars for the signed-in user.
func (client *OutlookClient) ListCalendars() ([]FlatCalendar, error) {
	result, err := client.user().Calendars().Get(context.Background(), &users.ItemCalendarsRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemCalendarsRequestBuilderGetQueryParameters{
			Top:    int32Ptr(100),
			Select: []string{"id", "name", "color", "canEdit", "isDefaultCalendar"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list calendars (needs Calendars.Read scope; reconnect Microsoft account if missing): %w", err)
	}
	out := make([]FlatCalendar, 0)
	if result == nil || result.GetValue() == nil {
		return out, nil
	}
	for _, cal := range result.GetValue() {
		if cal == nil || cal.GetId() == nil {
			continue
		}
		item := FlatCalendar{ID: *cal.GetId()}
		if cal.GetName() != nil {
			item.Name = *cal.GetName()
		}
		if cal.GetCanEdit() != nil {
			item.CanEdit = *cal.GetCanEdit()
		}
		if cal.GetIsDefaultCalendar() != nil {
			item.IsDefault = *cal.GetIsDefaultCalendar()
		}
		out = append(out, item)
	}
	return out, nil
}

// ListCalendarEvents lists events for a calendar (paged via skip).
func (client *OutlookClient) ListCalendarEvents(calendarID string, skip, top int32) ([]FlatEvent, error) {
	calendarID = strings.TrimSpace(calendarID)
	if calendarID == "" {
		return nil, fmt.Errorf("calendar_id is required")
	}
	if top <= 0 {
		top = 50
	}
	result, err := client.user().Calendars().ByCalendarId(calendarID).Events().Get(context.Background(), &users.ItemCalendarsItemEventsRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemCalendarsItemEventsRequestBuilderGetQueryParameters{
			Top:     &top,
			Skip:    &skip,
			Select:  []string{"id", "subject", "start", "end", "isCancelled", "isAllDay", "bodyPreview", "organizer", "lastModifiedDateTime"},
			Orderby: []string{"start/dateTime"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]FlatEvent, 0)
	if result == nil || result.GetValue() == nil {
		return out, nil
	}
	for _, ev := range result.GetValue() {
		if ev == nil || ev.GetId() == nil {
			continue
		}
		item := FlatEvent{ID: *ev.GetId()}
		if ev.GetSubject() != nil {
			item.Subject = *ev.GetSubject()
		}
		if ev.GetIsCancelled() != nil {
			item.IsCancelled = *ev.GetIsCancelled()
		}
		if ev.GetIsAllDay() != nil {
			item.IsAllDay = *ev.GetIsAllDay()
		}
		if ev.GetBodyPreview() != nil {
			item.BodyPreview = *ev.GetBodyPreview()
		}
		if ev.GetStart() != nil {
			if ev.GetStart().GetDateTime() != nil {
				item.Start = *ev.GetStart().GetDateTime()
			}
			if ev.GetStart().GetTimeZone() != nil {
				item.TimeZone = *ev.GetStart().GetTimeZone()
			}
		}
		if ev.GetEnd() != nil && ev.GetEnd().GetDateTime() != nil {
			item.End = *ev.GetEnd().GetDateTime()
		}
		if item.TimeZone == "" && ev.GetEnd() != nil && ev.GetEnd().GetTimeZone() != nil {
			item.TimeZone = *ev.GetEnd().GetTimeZone()
		}
		if ev.GetLastModifiedDateTime() != nil {
			item.LastModifiedDateTime = ev.GetLastModifiedDateTime().Format("2006-01-02T15:04:05Z07:00")
		}
		if ev.GetOrganizer() != nil && ev.GetOrganizer().GetEmailAddress() != nil && ev.GetOrganizer().GetEmailAddress().GetAddress() != nil {
			item.Organizer = *ev.GetOrganizer().GetEmailAddress().GetAddress()
		}
		out = append(out, item)
	}
	return out, nil
}

// GetCalendarEvent returns full event JSON-friendly map fields via models.Event.
func (client *OutlookClient) GetCalendarEvent(calendarID, eventID string) (models.Eventable, error) {
	calendarID = strings.TrimSpace(calendarID)
	eventID = strings.TrimSpace(eventID)
	if calendarID == "" || eventID == "" {
		return nil, fmt.Errorf("calendar_id and event_id are required")
	}
	ev, err := client.user().Calendars().ByCalendarId(calendarID).Events().ByEventId(eventID).Get(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("get event: %w", err)
	}
	return ev, nil
}

// ListContacts lists contacts for the signed-in user.
func (client *OutlookClient) ListContacts(skip, top int32) ([]FlatContact, error) {
	if top <= 0 {
		top = 100
	}
	result, err := client.user().Contacts().Get(context.Background(), &users.ItemContactsRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemContactsRequestBuilderGetQueryParameters{
			Top:    &top,
			Skip:   &skip,
			Select: []string{"id", "displayName", "givenName", "surname", "emailAddresses", "businessPhones", "mobilePhone", "companyName", "jobTitle", "changeKey"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list contacts (needs Contacts.Read scope; reconnect Microsoft account if missing): %w", err)
	}
	out := make([]FlatContact, 0)
	if result == nil || result.GetValue() == nil {
		return out, nil
	}
	for _, c := range result.GetValue() {
		if c == nil || c.GetId() == nil {
			continue
		}
		item := FlatContact{ID: *c.GetId()}
		if c.GetDisplayName() != nil {
			item.DisplayName = *c.GetDisplayName()
		}
		if c.GetGivenName() != nil {
			item.GivenName = *c.GetGivenName()
		}
		if c.GetSurname() != nil {
			item.Surname = *c.GetSurname()
		}
		if c.GetCompanyName() != nil {
			item.CompanyName = *c.GetCompanyName()
		}
		if c.GetJobTitle() != nil {
			item.JobTitle = *c.GetJobTitle()
		}
		if c.GetChangeKey() != nil {
			item.ChangeKey = *c.GetChangeKey()
		}
		for _, ea := range c.GetEmailAddresses() {
			if ea != nil && ea.GetAddress() != nil && strings.TrimSpace(*ea.GetAddress()) != "" {
				item.Emails = append(item.Emails, strings.TrimSpace(*ea.GetAddress()))
			}
		}
		for _, p := range c.GetBusinessPhones() {
			if strings.TrimSpace(p) != "" {
				item.Phones = append(item.Phones, strings.TrimSpace(p))
			}
		}
		if c.GetMobilePhone() != nil && strings.TrimSpace(*c.GetMobilePhone()) != "" {
			item.Phones = append(item.Phones, strings.TrimSpace(*c.GetMobilePhone()))
		}
		out = append(out, item)
	}
	return out, nil
}
