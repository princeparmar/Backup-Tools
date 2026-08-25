package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GroupSummary is a group row for browse/onboarding.
type GroupSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Mail        string `json:"mail,omitempty"`
	Description string `json:"description,omitempty"`
}

// ResolvedGroup holds validated group metadata for job input_data.
type ResolvedGroup struct {
	GroupID   string
	GroupName string
	GroupMail string
}

// GroupConversationThread is a thread stub from Graph.
type GroupConversationThread struct {
	ID    string
	Topic string
}

// GroupConversationPost is a post stub from Graph.
type GroupConversationPost struct {
	ID              string
	BodyPreview     string
	ReceivedDateTime string
	LastModifiedDateTime string
}

// GroupCalendarEvent is a calendar event stub.
type GroupCalendarEvent struct {
	ID                   string
	Subject              string
	StartDateTime        string
	EndDateTime          string
	TimeZone             string
	LastModifiedDateTime string
}

type graphGroupsListResponse struct {
	Value    []graphGroupRow `json:"value"`
	NextLink string          `json:"@odata.nextLink"`
}

type graphGroupRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Mail        string `json:"mail"`
	Description string `json:"description"`
}

type graphGroupDetail struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Mail        string `json:"mail"`
	Description string `json:"description"`
}

type graphConversationsResponse struct {
	Value    []graphConversationRow `json:"value"`
	NextLink string                 `json:"@odata.nextLink"`
}

type graphConversationRow struct {
	ID    string `json:"id"`
	Topic string `json:"topic"`
}

type graphThreadsResponse struct {
	Value    []graphThreadRow `json:"value"`
	NextLink string           `json:"@odata.nextLink"`
}

type graphThreadRow struct {
	ID    string `json:"id"`
	Topic string `json:"topic"`
}

type graphPostsResponse struct {
	Value    []graphPostRow `json:"value"`
	NextLink string         `json:"@odata.nextLink"`
}

type graphPostRow struct {
	ID                   string `json:"id"`
	BodyPreview          string `json:"bodyPreview"`
	ReceivedDateTime     string `json:"receivedDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
}

type graphGroupEventsResponse struct {
	Value    []graphGroupEventRow `json:"value"`
	NextLink string               `json:"@odata.nextLink"`
}

type graphGroupEventRow struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Start   *struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"start"`
	End *struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"end"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
}

// SanitizeGroupsGroupKey makes group_id safe for StorX object key prefixes.
func SanitizeGroupsGroupKey(groupID string) string {
	groupID = strings.TrimSpace(groupID)
	replacer := strings.NewReplacer(",", "_", "/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "#", "_")
	s := replacer.Replace(groupID)
	if s == "" {
		return "group"
	}
	return s
}

// GroupConversationPostKey is {groupKey}/conversations/{threadId}/posts/{postId}.json
func GroupConversationPostKey(groupKey, threadID, postID string) string {
	return fmt.Sprintf("%s/conversations/%s/posts/%s.json",
		SanitizeGroupsGroupKey(groupKey),
		url.PathEscape(strings.TrimSpace(threadID)),
		url.PathEscape(strings.TrimSpace(postID)),
	)
}

// GroupCalendarEventKey is {groupKey}/calendar/events/{eventId}.json
func GroupCalendarEventKey(groupKey, eventID string) string {
	return fmt.Sprintf("%s/calendar/events/%s.json",
		SanitizeGroupsGroupKey(groupKey),
		url.PathEscape(strings.TrimSpace(eventID)),
	)
}

// GroupDriveRootURL returns Graph URL for a group's default document library drive root.
func GroupDriveRootURL(groupID string) string {
	groupID = strings.TrimSpace(groupID)
	return graphBaseURL + "/groups/" + url.PathEscape(groupID) + "/drive"
}

// GroupDriveInitialDeltaURL is GET /groups/{id}/drive/root/delta for baseline.
func GroupDriveInitialDeltaURL(groupID string) string {
	return GroupDriveRootURL(groupID) + "/root/delta"
}

// ListGroups returns M365 groups visible to the signed-in user.
func ListGroups(ctx context.Context, accessToken string, top int32) ([]GroupSummary, error) {
	if top <= 0 {
		top = 50
	}
	reqURL := fmt.Sprintf("%s/me/memberOf/microsoft.graph.group?$top=%d&$select=id,displayName,mail,description", graphBaseURL, top)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("groups list http %d: %s", status, truncateForErr(body))
	}
	var parsed graphGroupsListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]GroupSummary, 0, len(parsed.Value))
	for i := range parsed.Value {
		row := parsed.Value[i]
		out = append(out, GroupSummary{
			ID:          strings.TrimSpace(row.ID),
			DisplayName: strings.TrimSpace(row.DisplayName),
			Mail:        strings.TrimSpace(row.Mail),
			Description: strings.TrimSpace(row.Description),
		})
	}
	return out, nil
}

// ResolveGroup validates group_id exists and caller can access it.
func ResolveGroup(ctx context.Context, accessToken, groupID string) (*ResolvedGroup, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	reqURL := fmt.Sprintf("%s/groups/%s?$select=id,displayName,mail,description", graphBaseURL, url.PathEscape(groupID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || status == http.StatusForbidden {
		return nil, fmt.Errorf("group not found or not accessible")
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("group resolve http %d: %s", status, truncateForErr(body))
	}
	var detail graphGroupDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, err
	}
	if strings.TrimSpace(detail.ID) == "" {
		return nil, fmt.Errorf("group id missing from graph response")
	}
	name := strings.TrimSpace(detail.DisplayName)
	if name == "" {
		name = SanitizeGroupsGroupKey(detail.ID)
	}
	return &ResolvedGroup{
		GroupID:   detail.ID,
		GroupName: name,
		GroupMail: strings.TrimSpace(detail.Mail),
	}, nil
}

// FetchGroupConversationsPage GETs conversations for a group.
func FetchGroupConversationsPage(ctx context.Context, accessToken, groupID, requestURL string) ([]GroupConversationThread, string, error) {
	if strings.TrimSpace(requestURL) == "" {
		requestURL = fmt.Sprintf("%s/groups/%s/conversations", graphBaseURL, url.PathEscape(groupID))
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("group conversations http %d: %s", status, truncateForErr(body))
	}
	var parsed graphConversationsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", err
	}
	out := make([]GroupConversationThread, 0, len(parsed.Value))
	for i := range parsed.Value {
		out = append(out, GroupConversationThread{
			ID:    strings.TrimSpace(parsed.Value[i].ID),
			Topic: strings.TrimSpace(parsed.Value[i].Topic),
		})
	}
	return out, strings.TrimSpace(parsed.NextLink), nil
}

// FetchGroupThreadPostsPage GETs posts for a conversation thread.
func FetchGroupThreadPostsPage(ctx context.Context, accessToken, groupID, threadID, requestURL string) ([]GroupConversationPost, string, error) {
	if strings.TrimSpace(requestURL) == "" {
		requestURL = fmt.Sprintf("%s/groups/%s/threads/%s/posts", graphBaseURL, url.PathEscape(groupID), url.PathEscape(threadID))
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("group thread posts http %d: %s", status, truncateForErr(body))
	}
	var parsed graphPostsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", err
	}
	out := make([]GroupConversationPost, 0, len(parsed.Value))
	for i := range parsed.Value {
		row := parsed.Value[i]
		out = append(out, GroupConversationPost{
			ID:                   strings.TrimSpace(row.ID),
			BodyPreview:          strings.TrimSpace(row.BodyPreview),
			ReceivedDateTime:     strings.TrimSpace(row.ReceivedDateTime),
			LastModifiedDateTime: strings.TrimSpace(row.LastModifiedDateTime),
		})
	}
	return out, strings.TrimSpace(parsed.NextLink), nil
}

// FetchGroupCalendarEventsPage GETs calendar events for a group.
func FetchGroupCalendarEventsPage(ctx context.Context, accessToken, groupID, requestURL string) ([]GroupCalendarEvent, string, error) {
	if strings.TrimSpace(requestURL) == "" {
		requestURL = fmt.Sprintf("%s/groups/%s/calendar/events?$top=50", graphBaseURL, url.PathEscape(groupID))
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("group calendar http %d: %s", status, truncateForErr(body))
	}
	var parsed graphGroupEventsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", err
	}
	out := make([]GroupCalendarEvent, 0, len(parsed.Value))
	for i := range parsed.Value {
		row := parsed.Value[i]
		ev := GroupCalendarEvent{
			ID:                   strings.TrimSpace(row.ID),
			Subject:              strings.TrimSpace(row.Subject),
			LastModifiedDateTime: strings.TrimSpace(row.LastModifiedDateTime),
		}
		if row.Start != nil {
			ev.StartDateTime = strings.TrimSpace(row.Start.DateTime)
			ev.TimeZone = strings.TrimSpace(row.Start.TimeZone)
		}
		if row.End != nil {
			ev.EndDateTime = strings.TrimSpace(row.End.DateTime)
			if ev.TimeZone == "" {
				ev.TimeZone = strings.TrimSpace(row.End.TimeZone)
			}
		}
		out = append(out, ev)
	}
	return out, strings.TrimSpace(parsed.NextLink), nil
}

// GroupsGroupSnapshotJSON builds group metadata snapshot for StorX.
func GroupsGroupSnapshotJSON(resolved *ResolvedGroup) ([]byte, error) {
	return GroupsTeamSnapshotJSON(resolved)
}

// GroupsGroupSnapshot is stored at {groupKey}/_group.json for restore ID resolution.
type GroupsGroupSnapshot struct {
	GroupID    string `json:"group_id"`
	GroupName  string `json:"group_name,omitempty"`
	GroupMail  string `json:"group_mail,omitempty"`
	SnapshotAt string `json:"snapshot_at,omitempty"`
}

// GroupsTeamSnapshotJSON builds group metadata snapshot for StorX.
func GroupsTeamSnapshotJSON(resolved *ResolvedGroup) ([]byte, error) {
	payload := GroupsGroupSnapshot{
		GroupID:    resolved.GroupID,
		GroupName:  resolved.GroupName,
		GroupMail:  resolved.GroupMail,
		SnapshotAt: time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// ListGroupsFlatConversationsPage lists conversation threads for browse.
func ListGroupsFlatConversationsPage(ctx context.Context, accessToken, groupID string, top int32) ([]GroupConversationThread, error) {
	threads, _, err := FetchGroupConversationsPage(ctx, accessToken, groupID, "")
	if err != nil {
		return nil, err
	}
	if top > 0 && int32(len(threads)) > top {
		threads = threads[:top]
	}
	return threads, nil
}
