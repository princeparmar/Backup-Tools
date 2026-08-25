package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Teams attachment classification for backup processor.
const (
	TeamsAttachmentTypeHostedContent = "hosted_content"
	TeamsAttachmentTypeReference     = "reference"
	TeamsAttachmentTypeFile          = "file"
)

// ErrTeamsDeltaInvalid means the saved deltaLink can no longer be used.
var ErrTeamsDeltaInvalid = fmt.Errorf("teams channel delta cursor invalid; rebaseline required")

// TeamSummary is a team row for browse/onboarding.
type TeamSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	WebURL      string `json:"web_url,omitempty"`
}

// TeamChannelSummary is a channel row for browse/onboarding.
type TeamChannelSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
}

// ResolvedTeam holds validated team metadata for job input_data.
type ResolvedTeam struct {
	TeamID      string
	TeamName    string
	TeamWebURL  string
	GroupID     string
	ChannelIDs  []string
}

// TeamsDeltaMessage is a channel message stub from Graph.
type TeamsDeltaMessage struct {
	ID                   string
	Subject              string
	From                 string
	CreatedDateTime      string
	LastModifiedDateTime string
	ChangeKey            string
	HasAttachments       bool
	IsRemoved            bool
}

// TeamsMessagePage is one page of channel messages.
type TeamsMessagePage struct {
	Messages  []TeamsDeltaMessage
	NextLink  string
	DeltaLink string
}

// TeamsCronBackupMeta is JSON at {teamKey}/channels/{channelId}/meta/.../{messageId}.json.
// TeamID and ChannelID are real Graph IDs (never derive Graph IDs from sanitized StorX key prefixes).
type TeamsCronBackupMeta struct {
	MessageID            string `json:"message_id"`
	TeamID               string `json:"team_id,omitempty"`
	ChannelID            string `json:"channel_id"`
	Subject              string `json:"subject,omitempty"`
	From                 string `json:"from,omitempty"`
	CreatedDateTime      string `json:"created_date_time,omitempty"`
	LastModifiedDateTime string `json:"last_modified_date_time,omitempty"`
	ChangeKey            string `json:"change_key,omitempty"`
	HasAttachments       bool   `json:"has_attachments,omitempty"`
	RemovedFromTeams     bool   `json:"removed_from_teams,omitempty"`
	DeletedAt            string `json:"deleted_at,omitempty"`
	DataObjectKey        string `json:"data_object_key,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

// TeamsTeamSnapshot is stored at {teamKey}/_team.json for restore ID resolution.
type TeamsTeamSnapshot struct {
	TeamID     string `json:"team_id"`
	TeamName   string `json:"team_name,omitempty"`
	TeamWebURL string `json:"team_web_url,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	SnapshotAt string `json:"snapshot_at,omitempty"`
}

type graphTeamsListResponse struct {
	Value     []graphTeamRow `json:"value"`
	NextLink  string         `json:"@odata.nextLink"`
}

type graphTeamRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	WebURL      string `json:"webUrl"`
}

type graphTeamDetail struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	WebURL      string `json:"webUrl"`
}

type graphChannelsListResponse struct {
	Value    []graphChannelRow `json:"value"`
	NextLink string            `json:"@odata.nextLink"`
}

type graphChannelRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

type graphTeamsMessagesResponse struct {
	Value     []graphTeamsMessageRow `json:"value"`
	NextLink  string                 `json:"@odata.nextLink"`
	DeltaLink string                 `json:"@odata.deltaLink"`
}

type graphTeamsMessageRow struct {
	ID                   string `json:"id"`
	Subject              string `json:"subject"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	ChangeKey            string `json:"changeKey"`
	HasAttachments       bool   `json:"hasAttachments"`
	From                 *struct {
		User *struct {
			DisplayName string `json:"displayName"`
			ID          string `json:"id"`
		} `json:"user"`
	} `json:"from"`
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

type graphHostedContentList struct {
	Value []struct {
		ID string `json:"id"`
	} `json:"value"`
}

// SanitizeTeamsTeamKey makes team_id safe for StorX object key prefixes.
func SanitizeTeamsTeamKey(teamID string) string {
	teamID = strings.TrimSpace(teamID)
	replacer := strings.NewReplacer(",", "_", "/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "#", "_", "@", "_")
	s := replacer.Replace(teamID)
	if s == "" {
		return "team"
	}
	return s
}

// TeamsIDBasedMetaKey is {teamKey}/channels/{channelId}/meta/{yyyy/mm/dd}/{messageId}.json
func TeamsIDBasedMetaKey(teamKey, channelID, messageID, createdTime string) string {
	return fmt.Sprintf("%s/channels/%s/meta/%s/%s.json",
		SanitizeTeamsTeamKey(teamKey),
		url.PathEscape(strings.TrimSpace(channelID)),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(messageID),
	)
}

// TeamsIDBasedDataKey is {teamKey}/channels/{channelId}/data/{yyyy/mm/dd}/{messageId}.json
func TeamsIDBasedDataKey(teamKey, channelID, messageID, createdTime string) string {
	return fmt.Sprintf("%s/channels/%s/data/%s/%s.json",
		SanitizeTeamsTeamKey(teamKey),
		url.PathEscape(strings.TrimSpace(channelID)),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(messageID),
	)
}

// TeamsHostedContentKey stores hosted content bytes.
func TeamsHostedContentKey(teamKey, channelID, messageID, contentID string) string {
	return fmt.Sprintf("%s/channels/%s/hosted/%s/%s",
		SanitizeTeamsTeamKey(teamKey),
		url.PathEscape(strings.TrimSpace(channelID)),
		strings.TrimSpace(messageID),
		strings.TrimSpace(contentID),
	)
}

// ListTeams returns teams visible to the signed-in user.
func ListTeams(ctx context.Context, accessToken string, top int32) ([]TeamSummary, error) {
	if top <= 0 {
		top = 50
	}
	reqURL := fmt.Sprintf("%s/me/joinedTeams?$top=%d&$select=id,displayName,description,webUrl", graphBaseURL, top)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("teams list http %d: %s", status, truncateForErr(body))
	}
	var parsed graphTeamsListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]TeamSummary, 0, len(parsed.Value))
	for i := range parsed.Value {
		row := parsed.Value[i]
		out = append(out, TeamSummary{
			ID:          strings.TrimSpace(row.ID),
			DisplayName: strings.TrimSpace(row.DisplayName),
			Description: strings.TrimSpace(row.Description),
			WebURL:      strings.TrimSpace(row.WebURL),
		})
	}
	return out, nil
}

// ListTeamChannels lists channels for a team.
func ListTeamChannels(ctx context.Context, accessToken, teamID string) ([]TeamChannelSummary, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team_id is required")
	}
	reqURL := fmt.Sprintf("%s/teams/%s/channels?$select=id,displayName,description",
		graphBaseURL, url.PathEscape(teamID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("team channels http %d: %s", status, truncateForErr(body))
	}
	var parsed graphChannelsListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]TeamChannelSummary, 0, len(parsed.Value))
	for i := range parsed.Value {
		row := parsed.Value[i]
		out = append(out, TeamChannelSummary{
			ID:          strings.TrimSpace(row.ID),
			DisplayName: strings.TrimSpace(row.DisplayName),
			Description: strings.TrimSpace(row.Description),
		})
	}
	return out, nil
}

// ResolveTeam validates team_id exists and caller can access it; optionally validates channel IDs.
func ResolveTeam(ctx context.Context, accessToken, teamID string, channelIDs []string) (*ResolvedTeam, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team_id is required")
	}
	reqURL := fmt.Sprintf("%s/teams/%s?$select=id,displayName,description,webUrl", graphBaseURL, url.PathEscape(teamID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || status == http.StatusForbidden {
		return nil, fmt.Errorf("team not found or not accessible")
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("team resolve http %d: %s", status, truncateForErr(body))
	}
	var detail graphTeamDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, err
	}
	if strings.TrimSpace(detail.ID) == "" {
		return nil, fmt.Errorf("team id missing from graph response")
	}

	groupID := ""
	if gbody, gstatus, gerr := graphDoJSON(ctx, accessToken, http.MethodGet,
		fmt.Sprintf("%s/groups?$filter=resourceProvisioningOptions/Any(x:x eq 'Team') and id eq '%s'", graphBaseURL, strings.ReplaceAll(teamID, "'", "''")), nil); gerr == nil && gstatus >= 200 && gstatus < 300 {
		var gresp struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		if json.Unmarshal(gbody, &gresp) == nil && len(gresp.Value) > 0 {
			groupID = strings.TrimSpace(gresp.Value[0].ID)
		}
	}

	channels, err := ListTeamChannels(ctx, accessToken, detail.ID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		allowed[ch.ID] = struct{}{}
	}
	filtered := make([]string, 0)
	if len(channelIDs) > 0 {
		for _, cid := range channelIDs {
			cid = strings.TrimSpace(cid)
			if cid == "" {
				continue
			}
			if _, ok := allowed[cid]; !ok {
				return nil, fmt.Errorf("channel %s is not accessible on team", cid)
			}
			filtered = append(filtered, cid)
		}
	} else {
		for _, ch := range channels {
			filtered = append(filtered, ch.ID)
		}
	}

	name := strings.TrimSpace(detail.DisplayName)
	if name == "" {
		name = SanitizeTeamsTeamKey(detail.ID)
	}
	return &ResolvedTeam{
		TeamID:     detail.ID,
		TeamName:   name,
		TeamWebURL: strings.TrimSpace(detail.WebURL),
		GroupID:    groupID,
		ChannelIDs: filtered,
	}, nil
}

// TeamsChannelMessagesInitialURL is the first page URL for channel message enumeration.
func TeamsChannelMessagesInitialURL(teamID, channelID string) string {
	return fmt.Sprintf("%s/teams/%s/channels/%s/messages?$top=50",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
}

// TeamsChannelMessagesDeltaInitialURL attempts delta baseline when supported.
func TeamsChannelMessagesDeltaInitialURL(teamID, channelID string) string {
	return fmt.Sprintf("%s/teams/%s/channels/%s/messages/delta",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
}

// FetchTeamsChannelMessagesPage GETs an absolute Graph messages URL (list, nextLink, or deltaLink).
func FetchTeamsChannelMessagesPage(ctx context.Context, accessToken, requestURL string) (*TeamsMessagePage, error) {
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return nil, fmt.Errorf("teams messages url is required")
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusGone || isDeltaResyncRequired(body) {
		return nil, ErrTeamsDeltaInvalid
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("teams messages http %d: %s", status, truncateForErr(body))
	}
	var parsed graphTeamsMessagesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode teams messages: %w", err)
	}
	out := &TeamsMessagePage{
		NextLink:  strings.TrimSpace(parsed.NextLink),
		DeltaLink: strings.TrimSpace(parsed.DeltaLink),
		Messages:  make([]TeamsDeltaMessage, 0, len(parsed.Value)),
	}
	for i := range parsed.Value {
		out.Messages = append(out.Messages, mapGraphTeamsMessage(parsed.Value[i]))
	}
	return out, nil
}

func mapGraphTeamsMessage(row graphTeamsMessageRow) TeamsDeltaMessage {
	msg := TeamsDeltaMessage{
		ID:                   strings.TrimSpace(row.ID),
		Subject:              strings.TrimSpace(row.Subject),
		CreatedDateTime:      strings.TrimSpace(row.CreatedDateTime),
		LastModifiedDateTime: strings.TrimSpace(row.LastModifiedDateTime),
		ChangeKey:            strings.TrimSpace(row.ChangeKey),
		HasAttachments:       row.HasAttachments,
		IsRemoved:            row.Removed != nil,
	}
	if row.From != nil && row.From.User != nil {
		msg.From = strings.TrimSpace(row.From.User.DisplayName)
	}
	return msg
}

// FetchTeamsMessageRaw loads full channel message JSON including replies expansion stub.
func FetchTeamsMessageRaw(ctx context.Context, accessToken, teamID, channelID, messageID string) ([]byte, error) {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	messageID = strings.TrimSpace(messageID)
	urlStr := fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID), urlPathEscape(messageID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("teams message http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}

// ListTeamsMessageReplies lists reply messages for a channel message.
func ListTeamsMessageReplies(ctx context.Context, accessToken, teamID, channelID, messageID string) ([]TeamsDeltaMessage, error) {
	urlStr := fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/replies",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID), urlPathEscape(messageID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("teams replies http %d: %s", status, truncateForErr(body))
	}
	var parsed graphTeamsMessagesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]TeamsDeltaMessage, 0, len(parsed.Value))
	for i := range parsed.Value {
		out = append(out, mapGraphTeamsMessage(parsed.Value[i]))
	}
	return out, nil
}

// ListTeamsMessageHostedContentIDs returns hosted content IDs for a message.
func ListTeamsMessageHostedContentIDs(ctx context.Context, accessToken, teamID, channelID, messageID string) ([]string, error) {
	urlStr := fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/hostedContents",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID), urlPathEscape(messageID))
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("teams hosted contents http %d: %s", status, truncateForErr(body))
	}
	var parsed graphHostedContentList
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Value))
	for i := range parsed.Value {
		if id := strings.TrimSpace(parsed.Value[i].ID); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// FetchTeamsHostedContentBytes downloads hosted content bytes.
func FetchTeamsHostedContentBytes(ctx context.Context, accessToken, teamID, channelID, messageID, contentID string) ([]byte, error) {
	urlStr := fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/hostedContents/%s/$value",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID), urlPathEscape(messageID), urlPathEscape(contentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("teams hosted content http %d: %s", resp.StatusCode, truncateForErr(b))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// TeamsTeamSnapshotJSON builds team metadata snapshot for StorX.
func TeamsTeamSnapshotJSON(resolved *ResolvedTeam, channels []TeamChannelSummary) ([]byte, error) {
	payload := map[string]interface{}{
		"team_id":      resolved.TeamID,
		"team_name":    resolved.TeamName,
		"team_web_url": resolved.TeamWebURL,
		"group_id":     resolved.GroupID,
		"channels":     channels,
		"snapshot_at":  time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// ListTeamsFlatMessagesPage lists messages for browse (non-incremental).
func ListTeamsFlatMessagesPage(ctx context.Context, accessToken, teamID, channelID string, skip, top int32) ([]TeamsDeltaMessage, error) {
	if top <= 0 {
		top = 50
	}
	startURL := TeamsChannelMessagesInitialURL(teamID, channelID)
	page, err := FetchTeamsChannelMessagesPage(ctx, accessToken, startURL)
	if err != nil {
		return nil, err
	}
	all := page.Messages
	for skip > 0 && strings.TrimSpace(page.NextLink) != "" {
		skip -= int32(len(page.Messages))
		if skip > 0 {
			page, err = FetchTeamsChannelMessagesPage(ctx, accessToken, page.NextLink)
			if err != nil {
				return nil, err
			}
			all = append(all, page.Messages...)
		}
	}
	if int32(len(all)) > top {
		all = all[:top]
	}
	return all, nil
}

// TeamsGetAllMessagesURL is application-only tenant export (Phase 4).
func TeamsGetAllMessagesURL(teamID string) string {
	return fmt.Sprintf("%s/teams/%s/channels/getAllMessages", graphBaseURL, url.PathEscape(teamID))
}
