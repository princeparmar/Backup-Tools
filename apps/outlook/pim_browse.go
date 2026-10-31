package outlook

// FlatCalendar is a calendar summary stored as _calendar.json. RemovedAt is set once the calendar
// is gone from Microsoft; its events are kept in the vault.
type FlatCalendar struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color,omitempty"`
	HexColor  string `json:"hex_color,omitempty"`
	CanEdit   bool   `json:"can_edit"`
	IsDefault bool   `json:"is_default"`
	// Source is ""/"user" for My calendars, or "group" for Outlook Groups calendars (e.g. Smartiies).
	Source string `json:"source,omitempty"`
	// GroupID is set when Source is "group" (M365 unified group object id).
	GroupID   string `json:"group_id,omitempty"`
	RemovedAt string `json:"removed_at,omitempty"`
}

// Calendar source values stored on FlatCalendar.Source.
const (
	CalendarSourceUser  = "user"
	CalendarSourceGroup = "group"
)

// FlatEvent is the older slim event shape kept for legacy restore of pre-Graph backups.
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

// FlatContact is the older slim contact shape kept for legacy restore of pre-Graph backups.
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
