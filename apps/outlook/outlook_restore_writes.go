package outlook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RestoreCalendarEventInput is a minimal event payload for Graph create.
// Full fidelity (attendees, recurrence, etc.) is deferred; TimeZone must match backup.
type RestoreCalendarEventInput struct {
	Subject  string `json:"subject"`
	Body     string `json:"body,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	TimeZone string `json:"time_zone,omitempty"`
	IsAllDay bool   `json:"is_all_day,omitempty"`
}

// RestoreContactInput is a minimal contact payload for Graph create.
type RestoreContactInput struct {
	DisplayName string   `json:"display_name"`
	GivenName   string   `json:"given_name,omitempty"`
	Surname     string   `json:"surname,omitempty"`
	Emails      []string `json:"emails,omitempty"`
	Phones      []string `json:"phones,omitempty"`
	CompanyName string   `json:"company_name,omitempty"`
	JobTitle    string   `json:"job_title,omitempty"`
}

func calendarRestoreTimeZone(tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return "UTC"
	}
	return tz
}

// ParseRestoreCalendarEvent maps FlatEvent JSON or Graph-shaped JSON into a create payload.
func ParseRestoreCalendarEvent(raw []byte) (*RestoreCalendarEventInput, error) {
	var flat FlatEvent
	if err := json.Unmarshal(raw, &flat); err == nil && strings.TrimSpace(flat.Subject) != "" {
		return &RestoreCalendarEventInput{
			Subject:  flat.Subject,
			Body:     flat.BodyPreview,
			Start:    flat.Start,
			End:      flat.End,
			TimeZone: flat.TimeZone,
			IsAllDay: flat.IsAllDay,
		}, nil
	}
	var graph struct {
		Subject string `json:"subject"`
		Body    *struct {
			Content string `json:"content"`
		} `json:"body"`
		Start *struct {
			DateTime string `json:"dateTime"`
			TimeZone string `json:"timeZone"`
		} `json:"start"`
		End *struct {
			DateTime string `json:"dateTime"`
			TimeZone string `json:"timeZone"`
		} `json:"end"`
		IsAllDay bool `json:"isAllDay"`
	}
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("parse calendar event: %w", err)
	}
	if strings.TrimSpace(graph.Subject) == "" {
		return nil, fmt.Errorf("calendar event subject missing")
	}
	out := &RestoreCalendarEventInput{Subject: graph.Subject, IsAllDay: graph.IsAllDay}
	if graph.Body != nil {
		out.Body = graph.Body.Content
	}
	if graph.Start != nil {
		out.Start = graph.Start.DateTime
		out.TimeZone = graph.Start.TimeZone
	}
	if graph.End != nil {
		out.End = graph.End.DateTime
		if out.TimeZone == "" {
			out.TimeZone = graph.End.TimeZone
		}
	}
	return out, nil
}

// ParseRestoreContact maps FlatContact JSON into a create payload.
func ParseRestoreContact(raw []byte) (*RestoreContactInput, error) {
	var flat FlatContact
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("parse contact: %w", err)
	}
	if strings.TrimSpace(flat.DisplayName) == "" && strings.TrimSpace(flat.GivenName) == "" {
		return nil, fmt.Errorf("contact display name missing")
	}
	name := strings.TrimSpace(flat.DisplayName)
	if name == "" {
		name = strings.TrimSpace(strings.TrimSpace(flat.GivenName) + " " + strings.TrimSpace(flat.Surname))
	}
	return &RestoreContactInput{
		DisplayName: name,
		GivenName:   flat.GivenName,
		Surname:     flat.Surname,
		Emails:      flat.Emails,
		Phones:      flat.Phones,
		CompanyName: flat.CompanyName,
		JobTitle:    flat.JobTitle,
	}, nil
}

// CreateCalendarEvent posts a new event to the signed-in user's default calendar.
func CreateCalendarEvent(ctx context.Context, accessToken string, in *RestoreCalendarEventInput) error {
	if in == nil || strings.TrimSpace(in.Subject) == "" {
		return fmt.Errorf("calendar event subject is required")
	}
	tz := calendarRestoreTimeZone(in.TimeZone)
	body := map[string]interface{}{
		"subject":  in.Subject,
		"isAllDay": in.IsAllDay,
	}
	if strings.TrimSpace(in.Body) != "" {
		body["body"] = map[string]string{"contentType": "text", "content": in.Body}
	}
	if strings.TrimSpace(in.Start) != "" {
		body["start"] = map[string]string{"dateTime": in.Start, "timeZone": tz}
	}
	if strings.TrimSpace(in.End) != "" {
		body["end"] = map[string]string{"dateTime": in.End, "timeZone": tz}
	}
	payload, _ := json.Marshal(body)
	_, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, graphBaseURL+"/me/events", payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create calendar event http %d", status)
	}
	return nil
}

// CreateContact posts a new contact to the signed-in user's default contacts folder.
func CreateContact(ctx context.Context, accessToken string, in *RestoreContactInput) error {
	if in == nil || strings.TrimSpace(in.DisplayName) == "" {
		return fmt.Errorf("contact display name is required")
	}
	body := map[string]interface{}{
		"displayName": in.DisplayName,
	}
	if in.GivenName != "" {
		body["givenName"] = in.GivenName
	}
	if in.Surname != "" {
		body["surname"] = in.Surname
	}
	if in.CompanyName != "" {
		body["companyName"] = in.CompanyName
	}
	if in.JobTitle != "" {
		body["jobTitle"] = in.JobTitle
	}
	if len(in.Emails) > 0 {
		addrs := make([]map[string]interface{}, 0, len(in.Emails))
		for _, e := range in.Emails {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			addrs = append(addrs, map[string]interface{}{
				"address": e,
				"name":    in.DisplayName,
			})
		}
		if len(addrs) > 0 {
			body["emailAddresses"] = addrs
		}
	}
	if len(in.Phones) > 0 {
		phones := make([]map[string]string, 0, len(in.Phones))
		for _, p := range in.Phones {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			phones = append(phones, map[string]string{"number": p})
		}
		if len(phones) > 0 {
			body["businessPhones"] = phones
		}
	}
	payload, _ := json.Marshal(body)
	_, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, graphBaseURL+"/me/contacts", payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create contact http %d", status)
	}
	return nil
}

// UploadDriveFile uploads bytes to the signed-in user's OneDrive root via a single PUT.
// MVP: simple upload only. Large files should later use createUploadSession + chunked upload.
func UploadDriveFile(ctx context.Context, accessToken, fileName string, data []byte) error {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = "restored.bin"
	}
	reqURL := fmt.Sprintf("%s/me/drive/root:/%s:/content", graphBaseURL, url.PathEscape(fileName))
	return putDriveContent(ctx, accessToken, reqURL, data)
}

// UploadSharePointDriveFile uploads bytes into a SharePoint/group drive root (simple PUT; see UploadDriveFile).
func UploadSharePointDriveFile(ctx context.Context, accessToken, driveID, fileName string, data []byte) error {
	driveID = strings.TrimSpace(driveID)
	fileName = strings.TrimSpace(fileName)
	if driveID == "" {
		return fmt.Errorf("drive_id is required")
	}
	if fileName == "" {
		fileName = "restored.bin"
	}
	reqURL := fmt.Sprintf("%s/drives/%s/root:/%s:/content", graphBaseURL, url.PathEscape(driveID), url.PathEscape(fileName))
	return putDriveContent(ctx, accessToken, reqURL, data)
}

func putDriveContent(ctx context.Context, accessToken, reqURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("drive upload http %d: %s", resp.StatusCode, truncateForErr(b))
	}
	return nil
}

// PostTeamsChannelMessage posts a channel message as a *new* Graph message (restore-as-new;
// original message IDs cannot be recreated). Replies and hosted content are not restored here.
func PostTeamsChannelMessage(ctx context.Context, accessToken, teamID, channelID, bodyContent string) error {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	if teamID == "" || channelID == "" {
		return fmt.Errorf("team_id and channel_id are required")
	}
	if strings.TrimSpace(bodyContent) == "" {
		bodyContent = "(restored message)"
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"body": map[string]string{
			"contentType": "html",
			"content":     bodyContent,
		},
	})
	reqURL := fmt.Sprintf("%s/teams/%s/channels/%s/messages", graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
	_, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, reqURL, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("post teams message http %d", status)
	}
	return nil
}

// ExtractTeamsMessageBody pulls HTML/text body from Graph message JSON.
func ExtractTeamsMessageBody(raw []byte) string {
	var msg struct {
		Body *struct {
			Content string `json:"content"`
		} `json:"body"`
		Subject string `json:"subject"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return string(raw)
	}
	if msg.Body != nil && strings.TrimSpace(msg.Body.Content) != "" {
		return msg.Body.Content
	}
	if strings.TrimSpace(msg.Subject) != "" {
		return msg.Subject
	}
	return "(restored message)"
}

// ParseTeamsIDsFromKey extracts StorX path segments only ({teamKey}/channels/{channelId}/...).
// teamKey is sanitized and must NOT be treated as a Graph team ID. Prefer ResolveTeamsGraphIDs.
func ParseTeamsIDsFromKey(objectKey string) (teamKey, channelKeySegment string) {
	parts := strings.Split(strings.Trim(objectKey, "/"), "/")
	if len(parts) >= 3 && parts[1] == "channels" {
		return parts[0], parts[2]
	}
	return "", ""
}

// ParseTeamsTeamSnapshot unmarshals {teamKey}/_team.json.
func ParseTeamsTeamSnapshot(raw []byte) (*TeamsTeamSnapshot, error) {
	var snap TeamsTeamSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.TeamID) == "" {
		return nil, fmt.Errorf("team_id missing in _team.json")
	}
	return &snap, nil
}

// ResolveTeamsGraphIDs returns real Graph team/channel IDs for restore.
// Priority: explicit overrides → message meta → _team.json. Never invent team_id from sanitized object keys.
func ResolveTeamsGraphIDs(meta TeamsCronBackupMeta, teamSnap *TeamsTeamSnapshot, objectKey, overrideTeamID, overrideChannelID string) (teamID, channelID string, err error) {
	teamID = strings.TrimSpace(overrideTeamID)
	channelID = strings.TrimSpace(overrideChannelID)
	if teamID == "" {
		teamID = strings.TrimSpace(meta.TeamID)
	}
	if teamID == "" && teamSnap != nil {
		teamID = strings.TrimSpace(teamSnap.TeamID)
	}
	if channelID == "" {
		channelID = strings.TrimSpace(meta.ChannelID)
	}
	if channelID == "" {
		_, seg := ParseTeamsIDsFromKey(objectKey)
		if unesc, uerr := url.PathUnescape(seg); uerr == nil {
			channelID = strings.TrimSpace(unesc)
		} else {
			channelID = strings.TrimSpace(seg)
		}
	}
	if teamID == "" {
		return "", "", fmt.Errorf("team_id missing from backup metadata/_team.json (cannot use sanitized object key)")
	}
	if channelID == "" {
		return "", "", fmt.Errorf("channel_id missing from backup metadata")
	}
	return teamID, channelID, nil
}

// CreateGroupConversationThread creates a new group conversation (restore-as-new).
// Requires Group.ReadWrite.All (delegated); application permission is not supported by Graph for this API.
func CreateGroupConversationThread(ctx context.Context, accessToken, groupID, topic, bodyPreview string) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return fmt.Errorf("group_id is required")
	}
	if strings.TrimSpace(topic) == "" {
		topic = "Restored conversation"
	}
	if strings.TrimSpace(bodyPreview) == "" {
		bodyPreview = topic
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"topic": topic,
		"threads": []map[string]interface{}{
			{
				"posts": []map[string]interface{}{
					{
						"body": map[string]string{
							"contentType": "text",
							"content":     bodyPreview,
						},
					},
				},
			},
		},
	})
	reqURL := fmt.Sprintf("%s/groups/%s/conversations", graphBaseURL, url.PathEscape(groupID))
	_, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, reqURL, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create group conversation http %d", status)
	}
	return nil
}

// CreateGroupCalendarEvent creates an event on the group's calendar (restore-as-new).
// Requires Group.ReadWrite.All (delegated); application permission is not supported for this API.
func CreateGroupCalendarEvent(ctx context.Context, accessToken, groupID string, in *RestoreCalendarEventInput) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return fmt.Errorf("group_id is required")
	}
	if in == nil || strings.TrimSpace(in.Subject) == "" {
		return fmt.Errorf("calendar event subject is required")
	}
	tz := calendarRestoreTimeZone(in.TimeZone)
	body := map[string]interface{}{
		"subject":  in.Subject,
		"isAllDay": in.IsAllDay,
	}
	if strings.TrimSpace(in.Body) != "" {
		body["body"] = map[string]string{"contentType": "text", "content": in.Body}
	}
	if strings.TrimSpace(in.Start) != "" {
		body["start"] = map[string]string{"dateTime": in.Start, "timeZone": tz}
	}
	if strings.TrimSpace(in.End) != "" {
		body["end"] = map[string]string{"dateTime": in.End, "timeZone": tz}
	}
	payload, _ := json.Marshal(body)
	reqURL := fmt.Sprintf("%s/groups/%s/events", graphBaseURL, url.PathEscape(groupID))
	_, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, reqURL, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("create group calendar event http %d", status)
	}
	return nil
}

// GroupKeyFromObjectKey returns the StorX group key prefix (may be sanitized; not always a Graph GUID).
func GroupKeyFromObjectKey(objectKey string) string {
	parts := strings.Split(strings.Trim(objectKey, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// GroupIDFromObjectKey is deprecated: prefer ResolveGroupGraphID. Kept for callers that already
// verified the key prefix is an unsanitized GUID (SanitizeGroupsGroupKey(id) == id).
func GroupIDFromObjectKey(objectKey string) string {
	return GroupKeyFromObjectKey(objectKey)
}

// ParseGroupsGroupSnapshot unmarshals {groupKey}/_group.json.
func ParseGroupsGroupSnapshot(raw []byte) (*GroupsGroupSnapshot, error) {
	var snap GroupsGroupSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.GroupID) == "" {
		return nil, fmt.Errorf("group_id missing in _group.json")
	}
	return &snap, nil
}

// ResolveGroupGraphID returns the real Graph group GUID for restore.
// Priority: override → embedded JSON group_id → _group.json → key only if sanitize is identity.
func ResolveGroupGraphID(objectKey string, objectJSON []byte, groupSnap *GroupsGroupSnapshot, overrideGroupID string) (string, error) {
	if id := strings.TrimSpace(overrideGroupID); id != "" {
		return id, nil
	}
	if len(objectJSON) > 0 {
		var embedded struct {
			GroupID string `json:"group_id"`
		}
		if json.Unmarshal(objectJSON, &embedded) == nil {
			if id := strings.TrimSpace(embedded.GroupID); id != "" {
				return id, nil
			}
		}
	}
	if groupSnap != nil {
		if id := strings.TrimSpace(groupSnap.GroupID); id != "" {
			return id, nil
		}
	}
	key := GroupKeyFromObjectKey(objectKey)
	if key != "" && SanitizeGroupsGroupKey(key) == key {
		// Sanitize did not alter the prefix — typically a GUID; safe enough as last resort.
		return key, nil
	}
	return "", fmt.Errorf("group_id missing from object/_group.json (cannot use sanitized object key)")
}

func graphDoJSONWrite(ctx context.Context, accessToken, method, reqURL string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}
