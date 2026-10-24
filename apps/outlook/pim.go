package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Calendar and contacts backups store each Graph event or contact as its full JSON:
//
//	{prefix}/{calendarId}/_calendar.json          calendar (FlatCalendar)
//	{prefix}/{calendarId}/{eventId}.json          event
//	{prefix}/{contactId}.json                     contact in the default Contacts folder
//	{prefix}/folders/{folderId}/_folder.json      other contact folder (PIMContactFolder)
//	{prefix}/folders/{folderId}/{contactId}.json  contact in that folder
//
// Every calendar and contact folder also keeps an _index.json (PIMIndex) of what is backed up.
const (
	PIMCalendarMetaName = "_calendar.json"
	PIMFolderMetaName   = "_folder.json"
	PIMIndexName        = "_index.json"
	pimFoldersSegment   = "folders"
)

// PIMIndexVersion 2 adds event summaries to calendar indexes. Older indexes have none.
const PIMIndexVersion = 2

// PIMIndex records the backed-up items of one calendar or contact folder by Graph id.
type PIMIndex struct {
	V     int                      `json:"v,omitempty"`
	Items map[string]PIMIndexEntry `json:"items"`
}

// PIMIndexEntry is one backed-up item. RemovedAt is set once the item is gone from Microsoft;
// its backup is kept. Occurrences is set when a series master was stored with its changed and
// cancelled occurrences. Summary is set for calendar events.
type PIMIndexEntry struct {
	Key         string        `json:"key"`
	ChangeKey   string        `json:"change_key"`
	RemovedAt   string        `json:"removed_at,omitempty"`
	Occurrences bool          `json:"occurrences,omitempty"`
	Summary     *EventSummary `json:"s,omitempty"`
}

// EventSummary is what a calendar view needs of an event, so a month is drawn from the index
// without reading every event. Times are UTC (RFC 3339); an all-day event spans the dates of
// Start and End. For a series master, Instances holds the start and end of its occurrences from
// From to Until, cancelled ones left out.
type EventSummary struct {
	Subject   string      `json:"subject,omitempty"`
	Start     string      `json:"start,omitempty"`
	End       string      `json:"end,omitempty"`
	AllDay    bool        `json:"all_day,omitempty"`
	Type      string      `json:"type,omitempty"`
	Cancelled bool        `json:"cancelled,omitempty"`
	Location  string      `json:"location,omitempty"`
	Instances [][2]string `json:"instances,omitempty"`
	From      string      `json:"from,omitempty"`
	Until     string      `json:"until,omitempty"`
}

// EventSummaryOf summarizes a Graph event listed in UTC.
func EventSummaryOf(raw []byte) *EventSummary {
	var ev struct {
		Subject     string        `json:"subject"`
		Start       graphDateTime `json:"start"`
		End         graphDateTime `json:"end"`
		IsAllDay    bool          `json:"isAllDay"`
		Type        string        `json:"type"`
		IsCancelled bool          `json:"isCancelled"`
		Location    struct {
			DisplayName string `json:"displayName"`
		} `json:"location"`
	}
	_ = json.Unmarshal(raw, &ev)
	return &EventSummary{
		Subject: ev.Subject, Start: ev.Start.utc(), End: ev.End.utc(), AllDay: ev.IsAllDay,
		Type: ev.Type, Cancelled: ev.IsCancelled, Location: ev.Location.DisplayName,
	}
}

// graphDateTime is a Graph dateTimeTimeZone read in UTC.
type graphDateTime struct {
	DateTime string `json:"dateTime"`
}

func (d graphDateTime) utc() string {
	t, err := time.Parse("2006-01-02T15:04:05.9999999", strings.TrimSuffix(d.DateTime, "Z"))
	if err != nil {
		return d.DateTime
	}
	return t.UTC().Format(time.RFC3339)
}

// PIMContactFolder is the stored description of a non-default contact folder.
type PIMContactFolder struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	ParentFolderID string `json:"parentFolderId,omitempty"`
}

// PIMItem is one Graph event or contact as returned by a list call.
type PIMItem struct {
	ID           string
	ChangeKey    string
	SeriesMaster bool
	Raw          json.RawMessage
}

func pimKeySegment(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, "/", "_")
	return strings.ReplaceAll(id, "\\", "_")
}

// PIMCalendarDir is the key prefix ("{prefix}/{calendarId}/") of one calendar.
func PIMCalendarDir(prefix, calendarID string) string {
	return strings.TrimRight(prefix, "/") + "/" + pimKeySegment(calendarID) + "/"
}

// PIMContactsDir is the key prefix of a contact folder; "" is the default Contacts folder.
func PIMContactsDir(prefix, folderID string) string {
	prefix = strings.TrimRight(prefix, "/") + "/"
	if strings.TrimSpace(folderID) == "" {
		return prefix
	}
	return prefix + pimFoldersSegment + "/" + pimKeySegment(folderID) + "/"
}

// PIMItemKey is the key of one event or contact under dir.
func PIMItemKey(dir, itemID string) string {
	return dir + pimKeySegment(itemID) + ".json"
}

// IsPIMItemKey reports keys of backed-up events or contacts (not calendar, folder or index files).
func IsPIMItemKey(key string) bool {
	base := path.Base(key)
	return strings.HasSuffix(strings.ToLower(base), ".json") && !strings.HasPrefix(base, "_")
}

// ParsePIMContactKey returns the folder key prefix holding a contact and whether it is a
// non-default folder.
func ParsePIMContactKey(key string) (dir string, inFolder bool) {
	dir = path.Dir(key) + "/"
	parent := path.Base(path.Dir(path.Dir(key)))
	return dir, parent == pimFoldersSegment
}

// ListPIMPage GETs one page of a Graph collection of events (times in UTC) or contacts. Cancelled
// events are kept so the backup holds their latest state; restore skips them.
func ListPIMPage(ctx context.Context, accessToken, pageURL string) ([]PIMItem, string, error) {
	body, status, err := graphRequest(ctx, accessToken, http.MethodGet, pageURL, nil, "UTC")
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("graph list http %d: %s", status, truncateForErr(body))
	}
	var page struct {
		Value    []json.RawMessage `json:"value"`
		NextLink string            `json:"@odata.nextLink"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("decode graph list: %w", err)
	}
	items := make([]PIMItem, 0, len(page.Value))
	for _, raw := range page.Value {
		var head struct {
			ID        string `json:"id"`
			ChangeKey string `json:"changeKey"`
			Type      string `json:"type"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID == "" {
			continue
		}
		items = append(items, PIMItem{ID: head.ID, ChangeKey: head.ChangeKey, SeriesMaster: head.Type == eventTypeSeriesMaster, Raw: raw})
	}
	return items, page.NextLink, nil
}

// PIMEventsURL lists the events of one calendar: single events and recurring series masters.
func PIMEventsURL(userBase, calendarID string) string {
	return strings.TrimRight(userBase, "/") + "/calendars/" + url.PathEscape(calendarID) + "/events?$top=100"
}

// PIMContactsURL lists the contacts of a folder; "" is the default Contacts folder.
func PIMContactsURL(userBase, folderID string) string {
	base := strings.TrimRight(userBase, "/")
	if strings.TrimSpace(folderID) == "" {
		return base + "/contacts?$top=100"
	}
	return base + "/contactFolders/" + url.PathEscape(folderID) + "/contacts?$top=100"
}

// ListPIMCalendars returns every calendar of the user.
func ListPIMCalendars(ctx context.Context, accessToken, userBase string) ([]FlatCalendar, error) {
	next := strings.TrimRight(userBase, "/") + "/calendars?$top=100&$select=id,name,color,hexColor,canEdit,isDefaultCalendar"
	var out []FlatCalendar
	for next != "" {
		var page struct {
			Value []struct {
				ID                string `json:"id"`
				Name              string `json:"name"`
				Color             string `json:"color"`
				HexColor          string `json:"hexColor"`
				CanEdit           bool   `json:"canEdit"`
				IsDefaultCalendar bool   `json:"isDefaultCalendar"`
			} `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := graphGetInto(ctx, accessToken, next, &page); err != nil {
			return nil, fmt.Errorf("list calendars: %w", err)
		}
		for _, c := range page.Value {
			if c.ID != "" {
				out = append(out, FlatCalendar{ID: c.ID, Name: c.Name, Color: c.Color, HexColor: c.HexColor, CanEdit: c.CanEdit, IsDefault: c.IsDefaultCalendar})
			}
		}
		next = page.NextLink
	}
	return out, nil
}

// ListPIMContactFolders returns every contact folder below the default Contacts folder, nested
// folders included.
func ListPIMContactFolders(ctx context.Context, accessToken, userBase string) ([]PIMContactFolder, error) {
	base := strings.TrimRight(userBase, "/")
	var out []PIMContactFolder
	queue := []string{base + "/contactFolders?$top=100"}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for next != "" {
			var page struct {
				Value    []PIMContactFolder `json:"value"`
				NextLink string             `json:"@odata.nextLink"`
			}
			if err := graphGetInto(ctx, accessToken, next, &page); err != nil {
				return nil, fmt.Errorf("list contact folders: %w", err)
			}
			for _, f := range page.Value {
				if f.ID == "" {
					continue
				}
				out = append(out, f)
				queue = append(queue, base+"/contactFolders/"+url.PathEscape(f.ID)+"/childFolders?$top=100")
			}
			next = page.NextLink
		}
	}
	return out, nil
}

func graphGetInto(ctx context.Context, accessToken, reqURL string, v any) error {
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("http %d: %s", status, truncateForErr(body))
	}
	return json.Unmarshal(body, v)
}
