package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
	ID          string              `json:"id"`
	DisplayName string              `json:"display_name"`
	Description string              `json:"description,omitempty"`
	Members     []TeamChannelMember `json:"members,omitempty"`
}

// TeamChannelMember is a Graph identity associated with a channel.
type TeamChannelMember struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ResolvedTeam holds validated team metadata for job input_data.
type ResolvedTeam struct {
	TeamID          string
	TeamName        string
	TeamDescription string
	TeamWebURL      string
	GroupID         string
	ChannelIDs      []string
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
	TeamID          string `json:"team_id"`
	TeamName        string `json:"team_name,omitempty"`
	TeamDescription string `json:"team_description,omitempty"`
	TeamWebURL      string `json:"team_web_url,omitempty"`
	GroupID         string `json:"group_id,omitempty"`
	SnapshotAt      string `json:"snapshot_at,omitempty"`
}

// TeamsChatsSnapshot is stored at {teamKey}/chats/_chats.json for restore ID resolution.l
type TeamsChatsSnapshot struct {
	Chats      []TeamChatSummary `json:"chats"`
	SnapshotAt string            `json:"snapshot_at,omitempty"`
}

// TeamsReplySnapshot stores reply metadata for a single parent channel message.
type TeamsReplySnapshot struct {
	ParentMessageID string              `json:"parent_message_id"`
	Replies         []TeamsDeltaMessage `json:"replies"`
	SnapshotAt      string              `json:"snapshot_at,omitempty"`
}

// TeamsReplySummary describes a reply message nested under a channel message.
type TeamsReplySummary struct {
	ParentMessageID string `json:"parent_message_id"`
	ReplyID         string `json:"reply_id"`
	CreatedDateTime string `json:"created_date_time,omitempty"`
	From            string `json:"from,omitempty"`
	MessageID       string `json:"message_id,omitempty"`
}

// TeamsAttachmentReference records the storage location and type of a Teams attachment.
type TeamsAttachmentReference struct {
	MessageID      string `json:"message_id"`
	ContentID      string `json:"content_id"`
	AttachmentType string `json:"attachment_type"`
	StorageKey     string `json:"storage_key,omitempty"`
}

// TeamsAttachmentSnapshot stores hosted-content references for one channel message.
type TeamsAttachmentSnapshot struct {
	MessageID   string                     `json:"message_id"`
	Attachments []TeamsAttachmentReference `json:"attachments"`
	SnapshotAt  string                     `json:"snapshot_at,omitempty"`
}

// TeamsChatMessagesSnapshot is stored at {teamKey}/chats/{chatId}/_messages.json.
type TeamsChatMessagesSnapshot struct {
	ChatID     string            `json:"chat_id"`
	Messages   []TeamChatMessage `json:"messages"`
	SnapshotAt string            `json:"snapshot_at,omitempty"`
}

// TeamsChatMessageReactionSummary is a reaction on a Teams chat or channel message.
type TeamsChatMessageReactionSummary struct {
	Type            string `json:"type,omitempty"`
	CreatedDateTime string `json:"created_date_time,omitempty"`
	UserDisplayName string `json:"user_display_name,omitempty"`
	UserID          string `json:"user_id,omitempty"`
}

// TeamsChatMessageReactionsSnapshot stores reactions for a specific Teams chat message.
type TeamsChatMessageReactionsSnapshot struct {
	ChatID     string                            `json:"chat_id"`
	MessageID  string                            `json:"message_id"`
	Reactions  []TeamsChatMessageReactionSummary `json:"reactions"`
	SnapshotAt string                            `json:"snapshot_at,omitempty"`
}

// TeamsMessageReactionSummary is a reaction on a team channel message.
type TeamsMessageReactionSummary struct {
	Type            string `json:"type,omitempty"`
	CreatedDateTime string `json:"created_date_time,omitempty"`
	UserDisplayName string `json:"user_display_name,omitempty"`
	UserID          string `json:"user_id,omitempty"`
}

// TeamsMessageReactionsSnapshot stores reactions for a specific Teams channel message.
type TeamsMessageReactionsSnapshot struct {
	TeamID     string                        `json:"team_id"`
	ChannelID  string                        `json:"channel_id"`
	MessageID  string                        `json:"message_id"`
	Reactions  []TeamsMessageReactionSummary `json:"reactions"`
	SnapshotAt string                        `json:"snapshot_at,omitempty"`
}

// TeamsCallRecordSummary records metadata for a Teams call record.
type TeamsCallRecordSummary struct {
	ID             string   `json:"id,omitempty"`
	StartDateTime  string   `json:"start_date_time,omitempty"`
	EndDateTime    string   `json:"end_date_time,omitempty"`
	Organizer      string   `json:"organizer,omitempty"`
	OrganizerEmail string   `json:"organizer_email,omitempty"`
	Modalities     string   `json:"modalities,omitempty"`
	Participants   []string `json:"participants,omitempty"`
}

// TeamsCallRecordsSnapshot stores one call-record metadata page.
type TeamsCallRecordsSnapshot struct {
	Calls      []TeamsCallRecordSummary `json:"calls"`
	SnapshotAt string                   `json:"snapshot_at,omitempty"`
}

// TeamsCallRecordSessionSummary records metadata for one Teams call-record session.
type TeamsCallRecordSessionSummary struct {
	ID            string `json:"id,omitempty"`
	CallRecordID  string `json:"call_record_id,omitempty"`
	StartDateTime string `json:"start_date_time,omitempty"`
	EndDateTime   string `json:"end_date_time,omitempty"`
	Modalities    string `json:"modalities,omitempty"`
}

// TeamsCallRecordSessionsSnapshot stores one call-record session page.
type TeamsCallRecordSessionsSnapshot struct {
	CallRecordID string                          `json:"call_record_id"`
	Sessions     []TeamsCallRecordSessionSummary `json:"sessions"`
	SnapshotAt   string                          `json:"snapshot_at,omitempty"`
}

// TeamsCallRecordSessionParticipantSummary records a participant in a Teams call-record session.
type TeamsCallRecordSessionParticipantSummary struct {
	ID                string `json:"id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	CallRecordID      string `json:"call_record_id,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	UserPrincipalName string `json:"user_principal_name,omitempty"`
	Role              string `json:"role,omitempty"`
}

// TeamsCallRecordSessionParticipantsSnapshot stores one participants page for a session.
type TeamsCallRecordSessionParticipantsSnapshot struct {
	CallRecordID string                                     `json:"call_record_id"`
	SessionID    string                                     `json:"session_id"`
	Participants []TeamsCallRecordSessionParticipantSummary `json:"participants"`
	SnapshotAt   string                                     `json:"snapshot_at,omitempty"`
}

// TeamsCallRecordSessionSegmentSummary records a segment in a Teams call-record session.
type TeamsCallRecordSessionSegmentSummary struct {
	ID            string `json:"id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	CallRecordID  string `json:"call_record_id,omitempty"`
	StartDateTime string `json:"start_date_time,omitempty"`
	EndDateTime   string `json:"end_date_time,omitempty"`
	Modality      string `json:"modality,omitempty"`
}

// TeamsCallRecordSessionSegmentsSnapshot stores one segment page for a session.
type TeamsCallRecordSessionSegmentsSnapshot struct {
	CallRecordID string                                 `json:"call_record_id"`
	SessionID    string                                 `json:"session_id"`
	Segments     []TeamsCallRecordSessionSegmentSummary `json:"segments"`
	SnapshotAt   string                                 `json:"snapshot_at,omitempty"`
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

type graphChannelMembersResponse struct {
	Value    []graphChannelMemberRow `json:"value"`
	NextLink string                  `json:"@odata.nextLink"`
}

type graphChannelMemberRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
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

func SanitizeTeamsTeamKey(teamID string) string {
	teamID = strings.TrimSpace(teamID)
	replacer := strings.NewReplacer(",", "_", "/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "#", "_", "@", "_")
	s := replacer.Replace(teamID)
	if s == "" {
		return "team"
	}
	return s
}

// TeamsIDBasedMetaKey is {prefix}/channels/{channelId}/meta/{yyyy/mm/dd}/{messageId}.json (prefix: ResourceKeyPrefix).
func TeamsIDBasedMetaKey(prefix, channelID, messageID, createdTime string) string {
	return fmt.Sprintf("%s/channels/%s/meta/%s/%s.json",
		strings.Trim(strings.TrimSpace(prefix), "/"),
		url.PathEscape(strings.TrimSpace(channelID)),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(messageID),
	)
}

// TeamsIDBasedDataKey is {prefix}/channels/{channelId}/data/{yyyy/mm/dd}/{messageId}.json (prefix: ResourceKeyPrefix).
func TeamsIDBasedDataKey(prefix, channelID, messageID, createdTime string) string {
	return fmt.Sprintf("%s/channels/%s/data/%s/%s.json",
		strings.Trim(strings.TrimSpace(prefix), "/"),
		url.PathEscape(strings.TrimSpace(channelID)),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(messageID),
	)
}

// TeamsHostedContentKey stores hosted content bytes.
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
		channel := TeamChannelSummary{
			ID:          strings.TrimSpace(row.ID),
			DisplayName: strings.TrimSpace(row.DisplayName),
			Description: strings.TrimSpace(row.Description),
		}
		if channel.ID != "" {
			if members, merr := ListTeamChannelMembers(ctx, accessToken, teamID, channel.ID); merr == nil {
				channel.Members = members
			}
		}
		out = append(out, channel)
	}
	return out, nil
}

// ListTeamChannelMembers lists members for a channel using Graph's channel members endpoint.
func ListTeamChannelMembers(ctx context.Context, accessToken, teamID, channelID string) ([]TeamChannelMember, error) {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	if teamID == "" || channelID == "" {
		return nil, fmt.Errorf("team_id and channel_id are required")
	}

	requestURL := fmt.Sprintf("%s/teams/%s/channels/%s/members?$select=id,displayName",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
	var out []TeamChannelMember
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("team channel members http %d: %s", status, truncateForErr(body))
		}
		var parsed graphChannelMembersResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode team channel members: %w", err)
		}
		for i := range parsed.Value {
			member := parsed.Value[i]
			if strings.TrimSpace(member.ID) == "" {
				continue
			}
			out = append(out, TeamChannelMember{
				ID:          strings.TrimSpace(member.ID),
				DisplayName: strings.TrimSpace(member.DisplayName),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
	}
	return out, nil
}

// TeamChatSummary is a chat row for Teams chat metadata discovery.
type TeamChatSummary struct {
	ID                  string `json:"id"`
	Topic               string `json:"topic,omitempty"`
	CreatedDateTime     string `json:"created_date_time,omitempty"`
	LastUpdatedDateTime string `json:"last_updated_date_time,omitempty"`
}

// TeamsMeetingSummary is a meeting reference discovered from Graph onlineMeetings.
type TeamsMeetingSummary struct {
	ID             string `json:"id"`
	Subject        string `json:"subject,omitempty"`
	JoinURL        string `json:"join_url,omitempty"`
	StartDateTime  string `json:"start_date_time,omitempty"`
	EndDateTime    string `json:"end_date_time,omitempty"`
	Organizer      string `json:"organizer,omitempty"`
	OrganizerEmail string `json:"organizer_email,omitempty"`
}

// TeamsMeetingParticipantSummary is a participant recorded for an online meeting.
type TeamsMeetingParticipantSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role,omitempty"`
	UPN         string `json:"upn,omitempty"`
}

// TeamsMeetingAttendanceSummary is a participant attendance record for an online meeting.
type TeamsMeetingAttendanceSummary struct {
	ID                string `json:"id"`
	UserDisplayName   string `json:"user_display_name,omitempty"`
	UserEmail         string `json:"user_email,omitempty"`
	AttendanceSeconds int    `json:"attendance_seconds,omitempty"`
	JoinDateTime      string `json:"join_date_time,omitempty"`
	LeaveDateTime     string `json:"leave_date_time,omitempty"`
}

// TeamsMeetingSnapshot stores a list of online meeting metadata objects.
type TeamsMeetingSnapshot struct {
	Meetings   []TeamsMeetingSummary `json:"meetings"`
	SnapshotAt string                `json:"snapshot_at,omitempty"`
}

// TeamsMeetingParticipantsSnapshot stores online meeting participant metadata.
type TeamsMeetingParticipantsSnapshot struct {
	MeetingID    string                           `json:"meeting_id"`
	Participants []TeamsMeetingParticipantSummary `json:"participants"`
	SnapshotAt   string                           `json:"snapshot_at,omitempty"`
}

// TeamsMeetingAttendanceSnapshot stores online meeting attendance-report metadata.
type TeamsMeetingAttendanceSnapshot struct {
	MeetingID  string                          `json:"meeting_id"`
	Reports    []TeamsMeetingAttendanceSummary `json:"reports"`
	SnapshotAt string                          `json:"snapshot_at,omitempty"`
}

// TeamsMeetingTranscriptSummary is a transcript record for an online meeting.
type TeamsMeetingTranscriptSummary struct {
	ID              string `json:"id"`
	Type            string `json:"type,omitempty"`
	ContentURL      string `json:"content_url,omitempty"`
	Locale          string `json:"locale,omitempty"`
	Status          string `json:"status,omitempty"`
	CreatedDateTime string `json:"created_date_time,omitempty"`
}

// TeamsMeetingTranscriptSnapshot stores online meeting transcript metadata.
type TeamsMeetingTranscriptSnapshot struct {
	MeetingID   string                          `json:"meeting_id"`
	Transcripts []TeamsMeetingTranscriptSummary `json:"transcripts"`
	SnapshotAt  string                          `json:"snapshot_at,omitempty"`
}

// TeamsMeetingRecordingSummary is a recording record for an online meeting.
type TeamsMeetingRecordingSummary struct {
	ID              string `json:"id"`
	Type            string `json:"type,omitempty"`
	ContentURL      string `json:"content_url,omitempty"`
	Locale          string `json:"locale,omitempty"`
	Status          string `json:"status,omitempty"`
	CreatedDateTime string `json:"created_date_time,omitempty"`
}

// TeamsMeetingRecordingSnapshot stores online meeting recording metadata.
type TeamsMeetingRecordingSnapshot struct {
	MeetingID  string                         `json:"meeting_id"`
	Recordings []TeamsMeetingRecordingSummary `json:"recordings"`
	SnapshotAt string                         `json:"snapshot_at,omitempty"`
}

// TeamsMeetingChatMessageSummary is a chat message record for an online meeting.
type TeamsMeetingChatMessageSummary struct {
	ID                   string `json:"id"`
	BodyPreview          string `json:"body_preview,omitempty"`
	CreatedDateTime      string `json:"created_date_time,omitempty"`
	LastModifiedDateTime string `json:"last_modified_date_time,omitempty"`
	From                 string `json:"from,omitempty"`
	FromID               string `json:"from_id,omitempty"`
}

// TeamsMeetingChatMessagesSnapshot stores online meeting chat messages.
type TeamsMeetingChatMessagesSnapshot struct {
	MeetingID  string                           `json:"meeting_id"`
	Messages   []TeamsMeetingChatMessageSummary `json:"messages"`
	SnapshotAt string                           `json:"snapshot_at,omitempty"`
}

// TeamChatMessage is a chat message stub returned by Graph.
type TeamChatMessage struct {
	ID                   string `json:"id"`
	BodyPreview          string `json:"body_preview,omitempty"`
	CreatedDateTime      string `json:"created_date_time,omitempty"`
	LastModifiedDateTime string `json:"last_modified_date_time,omitempty"`
	From                 string `json:"from,omitempty"`
	FromID               string `json:"from_id,omitempty"`
}

// ListTeamChats lists Teams chats accessible to the token.
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
		TeamID:          detail.ID,
		TeamName:        name,
		TeamDescription: strings.TrimSpace(detail.Description),
		TeamWebURL:      strings.TrimSpace(detail.WebURL),
		GroupID:         groupID,
		ChannelIDs:      filtered,
	}, nil
}

// TeamsChannelMessagesInitialURL is the first page URL for channel message enumeration.
func TeamsChannelMessagesInitialURL(teamID, channelID string) string {
	return fmt.Sprintf("%s/teams/%s/channels/%s/messages?$top=50",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
}

// TeamsChannelMessagesDeltaInitialURL attempts delta baseline when supported.
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
func ParseTeamsCallRecordsSnapshot(raw []byte) (*TeamsCallRecordsSnapshot, error) {
	var snap TeamsCallRecordsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if len(snap.Calls) == 0 {
		return nil, fmt.Errorf("calls missing in Teams call-record snapshot")
	}
	return &snap, nil
}

// TeamsCallRecordSessionsSnapshotJSON builds a call-record session snapshot for StorX metadata persistence.
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

// Tenant checks (graph-matrix / tenant-verify) not yet run on a real tenant. Every switch starts
// off; while off, the feature runs on its safe fallback (window or round kept open and replayed,
// no meeting merge, no single-item lookups, coverage shown as not verified). A switch is turned on
// only by a code change after its tenant check passed.
const (
	TeamsCheckSharedChannels       = "shared_channels_in_get_all_messages"
	TeamsCheckPerChannelFallback   = "per_channel_listing_fallback"
	TeamsCheckReplayNextLink       = "next_link_replayable"
	TeamsCheckLookupChatMessage    = "lookup_chat_message"
	TeamsCheckLookupChannelMessage = "lookup_channel_message"
	TeamsCheckLookupEvent          = "lookup_event"
	TeamsCheckLookupCallRecord     = "lookup_call_record"
	TeamsCheckLookupAttendance     = "lookup_attendance_report"
	TeamsCheckNotFoundCodes        = "not_found_codes"
	TeamsCheckDeltaOldEdits        = "chat_delta_returns_old_edits"
	TeamsCheckMergeSingleMeeting   = "meeting_merge_single"
	TeamsCheckMergeOccurrence      = "meeting_merge_occurrence"
	TeamsCheckMergeException       = "meeting_merge_exception"
	TeamsCheckMergeExternal        = "meeting_merge_external_organizer"
	TeamsCheckCallJoinWebURL       = "call_record_join_web_url"
	TeamsCheckCallDetailsComplete  = "call_record_details_complete"
	TeamsCheckPstnRowIDStable      = "pstn_row_id_stable"
	TeamsCheckRecordingFileLink    = "recording_file_link"
	TeamsCheckAttendancePolicy     = "attendance_with_access_policy"
)

var teamsVerified = map[string]bool{
	TeamsCheckSharedChannels:       false,
	TeamsCheckPerChannelFallback:   false,
	TeamsCheckReplayNextLink:       false,
	TeamsCheckLookupChatMessage:    false,
	TeamsCheckLookupChannelMessage: false,
	TeamsCheckLookupEvent:          false,
	TeamsCheckLookupCallRecord:     false,
	TeamsCheckLookupAttendance:     false,
	TeamsCheckNotFoundCodes:        false,
	TeamsCheckDeltaOldEdits:        false,
	TeamsCheckMergeSingleMeeting:   false,
	TeamsCheckMergeOccurrence:      false,
	TeamsCheckMergeException:       false,
	TeamsCheckMergeExternal:        false,
	TeamsCheckCallJoinWebURL:       false,
	TeamsCheckCallDetailsComplete:  false,
	TeamsCheckPstnRowIDStable:      false,
	TeamsCheckRecordingFileLink:    false,
	TeamsCheckAttendancePolicy:     false,
}

// TeamsVerified reports whether a tenant check passed (see teamsVerified).
func TeamsVerified(check string) bool { return teamsVerified[check] }

// SetTeamsVerified overrides a switch and returns a function restoring it. Tests only.
func SetTeamsVerified(check string, on bool) (restore func()) {
	prev, had := teamsVerified[check]
	teamsVerified[check] = on
	return func() {
		if had {
			teamsVerified[check] = prev
		} else {
			delete(teamsVerified, check)
		}
	}
}

// Status of a Teams feature, stream or resource from a Graph error (by HTTP status and error
// code, never by message text).
const (
	TeamsStatusOK                = "ok"
	TeamsStatusOKEmpty           = "ok_empty"
	TeamsStatusNotConsented      = "not_consented"
	TeamsStatusNotApproved       = "not_approved"
	TeamsStatusMissingPermission = "missing_permission"
	TeamsStatusBlockedByPolicy   = "blocked_by_policy"
	TeamsStatusDisabledByTenant  = "disabled_by_tenant"
	TeamsStatusNotLicensed       = "not_licensed"
	TeamsStatusNotAvailable      = "not_available"
	TeamsStatusPaidAPI           = "not_included_paid_api"
	// TeamsStatusContentNotCollected means media bytes are intentionally not stored in the Teams
	// vault (OneDrive/SharePoint own file backup). Distinct from PaidAPI, which is a Graph 402.
	TeamsStatusContentNotCollected = "content_not_collected"
	TeamsStatusNotFound          = "not_found"
	TeamsStatusResync            = "resync"
	TeamsStatusBadRequest        = "bad_request"
	TeamsStatusUnauthorized      = "unauthorized"
	TeamsStatusThrottled         = "throttled"
	TeamsStatusPaging            = "paging_error"
	TeamsStatusError             = "error"
)

// TeamsGraphError is a failed Graph call of the Teams backup. Message is already sanitized.
type TeamsGraphError struct {
	Status    int
	Code      string
	InnerCode string
	Message   string
}

func (e *TeamsGraphError) Error() string {
	code := e.Code
	if e.InnerCode != "" {
		code += "/" + e.InnerCode
	}
	if e.Message == "" {
		return fmt.Sprintf("graph http %d %s", e.Status, code)
	}
	return fmt.Sprintf("graph http %d %s: %s", e.Status, code, e.Message)
}

func parseTeamsGraphError(status int, body []byte) *TeamsGraphError {
	var doc struct {
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			InnerError struct {
				Code string `json:"code"`
			} `json:"innerError"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &doc)
	return &TeamsGraphError{
		Status:    status,
		Code:      strings.TrimSpace(doc.Error.Code),
		InnerCode: strings.TrimSpace(doc.Error.InnerError.Code),
		Message:   SanitizeTeamsErrorText(doc.Error.Message),
	}
}

// ErrTeamsPaging means a listing returned a malformed or looping page; the stream stops with
// paging_error and its cursor stays on the last fully processed page.
var ErrTeamsPaging = errors.New("teams paging error")

// ErrTeamsContentAPI is returned for transcript or recording content URLs, which are never called.
var ErrTeamsContentAPI = errors.New("teams transcript/recording content APIs are not used")

var teamsContentURL = regexp.MustCompile(`(?i)/(transcripts|recordings)/[^/?]+/(content|metadataContent)(\b|$)`)

// IsTeamsContentURL reports whether u downloads transcript or recording content.
func IsTeamsContentURL(u string) bool {
	return teamsContentURL.MatchString(u)
}

// ClassifyTeamsError maps an error from a Teams Graph call to a status.
func ClassifyTeamsError(err error) string {
	if err == nil {
		return TeamsStatusOK
	}
	if errors.Is(err, ErrTeamsPaging) {
		return TeamsStatusPaging
	}
	if errors.Is(err, ErrTeamsContentAPI) {
		return TeamsStatusPaidAPI
	}
	var ge *TeamsGraphError
	if !errors.As(err, &ge) {
		return TeamsStatusError
	}
	codes := strings.ToLower(ge.Code + " " + ge.InnerCode)
	switch {
	case ge.Status == http.StatusUnauthorized:
		return TeamsStatusUnauthorized
	case ge.Status == http.StatusPaymentRequired:
		return TeamsStatusPaidAPI
	case ge.Status == http.StatusGone || strings.Contains(codes, "syncstatenotfound") || strings.Contains(codes, "resyncrequired"):
		return TeamsStatusResync
	case ge.Status == http.StatusTooManyRequests || ge.Status >= 500:
		return TeamsStatusThrottled
	case ge.Status == http.StatusNotFound:
		return TeamsStatusNotFound
	case ge.Status == http.StatusBadRequest:
		return TeamsStatusBadRequest
	case ge.Status == http.StatusForbidden:
		switch {
		case strings.Contains(codes, "graphaccesstotranscriptsdisabled"):
			return TeamsStatusDisabledByTenant
		case strings.Contains(codes, "policy"):
			return TeamsStatusBlockedByPolicy
		case strings.Contains(codes, "protectedapi") || strings.Contains(codes, "protected_api"):
			return TeamsStatusNotApproved
		case strings.Contains(codes, "license"):
			return TeamsStatusNotLicensed
		default:
			return TeamsStatusMissingPermission
		}
	}
	return TeamsStatusError
}

// IsTeamsPermanentStatus reports statuses that are not retried in a loop (backed off for a day
// and re-probed on a later run).
func IsTeamsPermanentStatus(status string) bool {
	switch status {
	case TeamsStatusNotConsented, TeamsStatusNotApproved, TeamsStatusMissingPermission, TeamsStatusBlockedByPolicy,
		TeamsStatusDisabledByTenant, TeamsStatusNotLicensed, TeamsStatusPaidAPI, TeamsStatusBadRequest, TeamsStatusNotAvailable:
		return true
	}
	return false
}

func teamsAbsURL(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "/") {
		return graphBaseURL + u
	}
	return u
}

// FetchTeamsJSON GETs one Graph JSON document for the Teams backup.
func FetchTeamsJSON(ctx context.Context, accessToken, reqURL string) ([]byte, error) {
	reqURL = teamsAbsURL(reqURL)
	if IsTeamsContentURL(reqURL) {
		return nil, ErrTeamsContentAPI
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, parseTeamsGraphError(status, body)
	}
	return body, nil
}

// FetchTeamsBytes downloads a hosted content $value. eventual sends ConsistencyLevel: eventual,
// which Graph needs to return hosted content of an edited or deleted message.
func FetchTeamsBytes(ctx context.Context, accessToken, reqURL string, eventual bool) ([]byte, error) {
	reqURL = teamsAbsURL(reqURL)
	if IsTeamsContentURL(reqURL) {
		return nil, ErrTeamsContentAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	if eventual {
		req.Header.Set("ConsistencyLevel", "eventual")
	}
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, parseTeamsGraphError(resp.StatusCode, b)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// TeamsPage is one page of a Teams listing with the items as raw Graph JSON.
type TeamsPage struct {
	Items     []json.RawMessage
	NextLink  string
	DeltaLink string
}

// FetchTeamsPage GETs one listing page (first URL, nextLink or deltaLink).
func FetchTeamsPage(ctx context.Context, accessToken, pageURL string) (TeamsPage, error) {
	body, err := FetchTeamsJSON(ctx, accessToken, pageURL)
	if err != nil {
		return TeamsPage{}, err
	}
	var doc struct {
		Value     *[]json.RawMessage `json:"value"`
		NextLink  string             `json:"@odata.nextLink"`
		DeltaLink string             `json:"@odata.deltaLink"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return TeamsPage{}, fmt.Errorf("%w: decode page: %v", ErrTeamsPaging, err)
	}
	if doc.Value == nil {
		return TeamsPage{}, fmt.Errorf("%w: page has no value array", ErrTeamsPaging)
	}
	return TeamsPage{Items: *doc.Value, NextLink: teamsAbsURL(doc.NextLink), DeltaLink: teamsAbsURL(doc.DeltaLink)}, nil
}

// DefaultTeamsMaxPages is the page cap of one listing round.
const DefaultTeamsMaxPages = 2000

// TeamsPageGuard stops a listing round on a repeated nextLink, a nextLink off the Graph host or
// to another resource than the round's first request, or more than Max pages. Graph writes some
// nextLinks in OData key form (/teams('id')/channels/getallMessages for a request to
// /teams/id/channels/getAllMessages), so paths are compared in canonical form; the link itself is
// followed unchanged.
type TeamsPageGuard struct {
	Max   int
	seen  map[string]bool
	pages int
	host  string
	path  string
	bad   bool
	// sameResource compares canonical paths; nil means equal paths only.
	sameResource func(first, next string) bool
}

func (g *TeamsPageGuard) onResource(path string) bool {
	if g.sameResource != nil {
		return g.sameResource(g.path, path)
	}
	return path == g.path
}

// NewTeamsPageGuard starts a round at firstURL. A first URL off the Graph host makes every
// nextLink of the round fail.
func NewTeamsPageGuard(firstURL string) *TeamsPageGuard {
	g := &TeamsPageGuard{Max: DefaultTeamsMaxPages, seen: map[string]bool{}}
	host, path, query, ok := teamsGraphLink(teamsAbsURL(firstURL))
	if !ok {
		g.bad = true
		return g
	}
	g.host, g.path = host, path
	g.seen[path+"?"+query] = true
	return g
}

// Next accepts the next page URL of the round or returns ErrTeamsPaging.
func (g *TeamsPageGuard) Next(next string) error {
	g.pages++
	if g.Max > 0 && g.pages > g.Max {
		return fmt.Errorf("%w: more than %d pages", ErrTeamsPaging, g.Max)
	}
	if g.bad {
		return fmt.Errorf("%w: round did not start on the Graph host", ErrTeamsPaging)
	}
	host, path, query, ok := teamsGraphLink(teamsAbsURL(next))
	if !ok {
		return fmt.Errorf("%w: malformed nextLink", ErrTeamsPaging)
	}
	if host != g.host || !g.onResource(path) {
		return fmt.Errorf("%w: nextLink to another resource", ErrTeamsPaging)
	}
	if g.seen[path+"?"+query] {
		return fmt.Errorf("%w: repeated nextLink", ErrTeamsPaging)
	}
	g.seen[path+"?"+query] = true
	return nil
}

// Valid reports whether the round started on the Graph host.
func (g *TeamsPageGuard) Valid() bool { return !g.bad }

// Delta accepts the deltaLink that ends the round when it continues the round's resource on the
// Graph host, the same checks as Next without counting it as a page.
func (g *TeamsPageGuard) Delta(link string) error {
	if g.bad {
		return fmt.Errorf("%w: round did not start on the Graph host", ErrTeamsPaging)
	}
	host, path, _, ok := teamsGraphLink(teamsAbsURL(link))
	if !ok {
		return fmt.Errorf("%w: malformed deltaLink", ErrTeamsPaging)
	}
	if host != g.host || !g.onResource(path) {
		return fmt.Errorf("%w: deltaLink to another resource", ErrTeamsPaging)
	}
	return nil
}

var (
	teamsODataName = regexp.MustCompile(`^[A-Za-z]+$`)
	teamsODataKey  = regexp.MustCompile(`^([A-Za-z]+)\('([^'/]+)'\)$`)
	teamsODataGUID = regexp.MustCompile(`^([A-Za-z]+)\(([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})\)$`)
	teamsODataCall = regexp.MustCompile(`^([A-Za-z]+)\((.*=.*)\)$`)
)

// teamsODataParamAlias maps parameter names Graph writes back in nextLinks to the names of the
// request: getPstnCalls documents from/to in its nextLink for fromDateTime/toDateTime.
var teamsODataParamAlias = map[string]map[string]string{
	"getpstncalls":          {"from": "fromdatetime", "to": "todatetime"},
	"getdirectroutingcalls": {"from": "fromdatetime", "to": "todatetime"},
}

// teamsODataParams is the canonical form of a function's parameters: names lower-cased (and
// aliased), sorted, unquoted date or date-time values as UTC instants. Quoted values are kept as
// sent. false when the list doesn't parse.
func teamsODataParams(fn, inner string) (string, bool) {
	var parts []string
	quoted, start := false, 0
	for i, c := range inner {
		switch {
		case c == '\'':
			quoted = !quoted
		case c == ',' && !quoted:
			parts = append(parts, inner[start:i])
			start = i + 1
		}
	}
	if quoted {
		return "", false
	}
	parts = append(parts, inner[start:])
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		name, value, found := strings.Cut(strings.TrimSpace(p), "=")
		name, value = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)
		if !found || !teamsODataName.MatchString(name) || value == "" {
			return "", false
		}
		if alias, ok := teamsODataParamAlias[fn][name]; ok {
			name = alias
		}
		if seen[name] {
			return "", false
		}
		seen[name] = true
		if !strings.HasPrefix(value, "'") {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
				if t, err := time.Parse(layout, value); err == nil {
					value = t.UTC().Format(time.RFC3339Nano)
					break
				}
			}
		}
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return fn + "(" + strings.Join(out, ",") + ")", true
}

// teamsGraphLink parses an absolute link on the configured Graph host and returns its host, its
// canonical path and its raw query. Canonical form: name('key') and name(guid) become name/key,
// OData names (letters only, also a function's name) are lower-cased and a function's parameters
// are in teamsODataParams form. Ids, quoted parameter values and the query are kept as sent.
func teamsGraphLink(link string) (host, path, query string, ok bool) {
	base, err := url.Parse(graphBaseURL)
	if err != nil {
		return "", "", "", false
	}
	u, err := url.Parse(link)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.Fragment != "" ||
		!strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return "", "", "", false
	}
	raw := strings.Trim(u.EscapedPath(), "/")
	if raw == "" {
		return "", "", "", false
	}
	var segs []string
	for _, seg := range strings.Split(raw, "/") {
		s, err := url.PathUnescape(seg)
		if err != nil || s == "" || s == "." || s == ".." {
			return "", "", "", false
		}
		switch {
		case teamsODataName.MatchString(s):
			segs = append(segs, strings.ToLower(s))
		case teamsODataKey.MatchString(s):
			m := teamsODataKey.FindStringSubmatch(s)
			segs = append(segs, strings.ToLower(m[1]), m[2])
		case teamsODataGUID.MatchString(s):
			m := teamsODataGUID.FindStringSubmatch(s)
			segs = append(segs, strings.ToLower(m[1]), m[2])
		case teamsODataCall.MatchString(s):
			m := teamsODataCall.FindStringSubmatch(s)
			call, ok := teamsODataParams(strings.ToLower(m[1]), m[2])
			if !ok {
				return "", "", "", false
			}
			segs = append(segs, call)
		default:
			segs = append(segs, s)
		}
	}
	return strings.ToLower(u.Host), "/" + strings.Join(segs, "/"), u.RawQuery, true
}

// FetchTeamsAll follows every page of a small listing (config, members, details).
func FetchTeamsAll(ctx context.Context, accessToken, firstURL string) ([]json.RawMessage, error) {
	guard := NewTeamsPageGuard(firstURL)
	var out []json.RawMessage
	next := firstURL
	for next != "" {
		page, err := FetchTeamsPage(ctx, accessToken, next)
			if err != nil {
			return out, err
		}
		out = append(out, page.Items...)
		if page.NextLink == "" {
			break
		}
		if err := guard.Next(page.NextLink); err != nil {
			return out, err
		}
		next = page.NextLink
	}
	return out, nil
}

func teamsFilterQuery(filter string) string {
	return "$filter=" + strings.ReplaceAll(url.QueryEscape(filter), "+", "%20")
}

func teamsPath(format string, ids ...string) string {
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = url.PathEscape(strings.TrimSpace(id))
	}
	return graphBaseURL + fmt.Sprintf(format, args...)
}

// TeamsTenantTeamsURL lists every team of the tenant.
func TeamsTenantTeamsURL() string {
	return graphBaseURL + "/groups?" + teamsFilterQuery("resourceProvisioningOptions/Any(x:x eq 'Team')") +
		"&$select=id,displayName,description,visibility,createdDateTime,mail&$top=999"
}

// TeamsTenantUsersURL lists the tenant's users.
func TeamsTenantUsersURL() string {
	return graphBaseURL + "/users?$select=id,displayName,userPrincipalName,mail,accountEnabled,userType&$top=999"
}

func TeamsTeamURL(teamID string) string { return teamsPath("/teams/%s", teamID) }

func TeamsAllChannelsURL(teamID string) string { return teamsPath("/teams/%s/allChannels", teamID) }

// TeamsIncomingChannelsURL lists the channels shared with a team that another team hosts.
func TeamsIncomingChannelsURL(teamID string) string {
	return teamsPath("/teams/%s/incomingChannels", teamID) + "?$select=id,membershipType,tenantId"
}

// TeamsTeamConfigURLs are the team configuration parts stored under CONFIG/.
func TeamsTeamConfigURLs(teamID string) map[string]string {
	return map[string]string{
		"members": teamsPath("/teams/%s/members", teamID),
		"apps":    teamsPath("/teams/%s/installedApps?$expand=teamsAppDefinition", teamID),
		"tags":    teamsPath("/teams/%s/tags", teamID),
	}
}

// TeamsChannelConfigURLs are the channel configuration parts stored under the channel's CONFIG/.
func TeamsChannelConfigURLs(teamID, channelID string) map[string]string {
	return map[string]string{
		"members": teamsPath("/teams/%s/channels/%s/members", teamID, channelID),
		"tabs":    teamsPath("/teams/%s/channels/%s/tabs?$expand=teamsApp", teamID, channelID),
	}
}

// TeamsChannelAllMessagesURL lists a team's channel posts and replies changed in (from, to).
// A zero from lists everything changed before to.
func TeamsChannelAllMessagesURL(teamID string, from, to time.Time) string {
	if from.IsZero() {
		from = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return teamsPath("/teams/%s/channels/getAllMessages?", teamID) +
		teamsFilterQuery("lastModifiedDateTime gt "+TeamsUTC(from)+" and lastModifiedDateTime lt "+TeamsUTC(to))
}

// TeamsChannelMessageURL is the lookup of one root post; TeamsChannelReplyURL of one reply.
func TeamsChannelMessageURL(teamID, channelID, messageID string) string {
	return teamsPath("/teams/%s/channels/%s/messages/%s", teamID, channelID, messageID)
}

func TeamsChannelReplyURL(teamID, channelID, rootID, replyID string) string {
	return teamsPath("/teams/%s/channels/%s/messages/%s/replies/%s", teamID, channelID, rootID, replyID)
}

// TeamsChannelRootsURL lists one channel's root posts with their replies expanded. App-only
// access works only from the tenant that owns the channel (the channel's tenantId).
func TeamsChannelRootsURL(teamID, channelID string) string {
	return teamsPath("/teams/%s/channels/%s/messages", teamID, channelID) + "?$top=50&$expand=replies"
}

// TeamsChannelRootRepliesURL lists the replies of one root post.
func TeamsChannelRootRepliesURL(teamID, channelID, rootID string) string {
	return teamsPath("/teams/%s/channels/%s/messages/%s/replies", teamID, channelID, rootID) + "?$top=50"
}

// TeamsChannelHostedListURL lists a channel message's hosted contents (root or reply).
func TeamsChannelHostedListURL(teamID, channelID, rootID, messageID string) string {
	if rootID == "" || rootID == messageID {
		return teamsPath("/teams/%s/channels/%s/messages/%s/hostedContents", teamID, channelID, messageID)
	}
	return teamsPath("/teams/%s/channels/%s/messages/%s/replies/%s/hostedContents", teamID, channelID, rootID, messageID)
}

func TeamsUserChatsURL(userID string) string {
	return teamsPath("/users/%s/chats?$expand=members&$top=50", userID)
}

func TeamsChatURL(chatID string) string { return teamsPath("/chats/%s?$expand=members", chatID) }

func TeamsChatPinnedURL(chatID string) string {
	return teamsPath("/chats/%s/pinnedMessages?$expand=message", chatID)
}

// TeamsUserChatDeltaURL starts a user's chat message delta (last 8 months).
func TeamsUserChatDeltaURL(userID string) string {
	return teamsPath("/users/%s/chats/getAllMessages/delta?$top=50", userID)
}

// TeamsUserChatMessagesURL lists a user's chat messages changed in (from, to); a zero from has
// no lower bound.
func TeamsUserChatMessagesURL(userID string, from, to time.Time) string {
	if from.IsZero() {
		from = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return teamsPath("/users/%s/chats/getAllMessages?", userID) +
		teamsFilterQuery("lastModifiedDateTime gt "+TeamsUTC(from)+" and lastModifiedDateTime lt "+TeamsUTC(to))
}

func TeamsChatMessageURL(chatID, messageID string) string {
	return teamsPath("/chats/%s/messages/%s", chatID, messageID)
}

func TeamsChatHostedListURL(chatID, messageID string) string {
	return teamsPath("/chats/%s/messages/%s/hostedContents", chatID, messageID)
}

// TeamsHostedValueURL is the bytes of one hosted content under a hostedContents list URL.
func TeamsHostedValueURL(listURL, contentID string) string {
	return strings.TrimRight(listURL, "/") + "/" + url.PathEscape(contentID) + "/$value"
}

// TeamsHostedRef is a hosted content link split into the message that owns the content and the
// content id: ChatID for a chat message; TeamID, ChannelID and RootID for a channel post or reply.
type TeamsHostedRef struct {
	ChatID    string
	TeamID    string
	ChannelID string
	RootID    string
	MessageID string
	ContentID string
}

// ValueURL is the documented $value route of the content under the message that owns it.
func (h TeamsHostedRef) ValueURL() string {
	if h.ChatID != "" {
		return TeamsHostedValueURL(TeamsChatHostedListURL(h.ChatID, h.MessageID), h.ContentID)
	}
	return TeamsHostedValueURL(TeamsChannelHostedListURL(h.TeamID, h.ChannelID, h.RootID, h.MessageID), h.ContentID)
}

// ParseTeamsHostedRef parses an absolute hosted content link on the configured Graph host and
// version, ending in hostedContents/{id}/$value under one of the documented message routes:
// chats/{chat}/messages/{m}, users/{u}/chats/{chat}/messages/{m},
// teams/{t}/channels/{c}/messages/{m} or teams/{t}/channels/{c}/messages/{root}/replies/{m}
// (OData key form name('id') accepted). Any other link is rejected.
func ParseTeamsHostedRef(link string) (TeamsHostedRef, bool) {
	base, err := url.Parse(graphBaseURL)
	if err != nil {
		return TeamsHostedRef{}, false
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		!strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return TeamsHostedRef{}, false
	}
	baseSegs, ok := teamsHostedSegments(base.EscapedPath())
	if !ok {
		return TeamsHostedRef{}, false
	}
	segs, ok := teamsHostedSegments(u.EscapedPath())
	if !ok || len(segs) < len(baseSegs) {
		return TeamsHostedRef{}, false
	}
	for i, s := range baseSegs {
		if !strings.EqualFold(segs[i], s) {
			return TeamsHostedRef{}, false
		}
	}
	segs = segs[len(baseSegs):]
	n := len(segs)
	if n < 7 || !strings.EqualFold(segs[n-3], "hostedContents") || segs[n-1] != "$value" {
		return TeamsHostedRef{}, false
	}
	ref := TeamsHostedRef{ContentID: segs[n-2]}
	p := segs[:n-3]
	is := func(i int, name string) bool { return strings.EqualFold(p[i], name) }
	switch {
	case len(p) == 4 && is(0, "chats") && is(2, "messages"):
		ref.ChatID, ref.MessageID = p[1], p[3]
	case len(p) == 6 && is(0, "users") && is(2, "chats") && is(4, "messages"):
		ref.ChatID, ref.MessageID = p[3], p[5]
	case len(p) == 6 && is(0, "teams") && is(2, "channels") && is(4, "messages"):
		ref.TeamID, ref.ChannelID, ref.RootID, ref.MessageID = p[1], p[3], p[5], p[5]
	case len(p) == 8 && is(0, "teams") && is(2, "channels") && is(4, "messages") && is(6, "replies"):
		ref.TeamID, ref.ChannelID, ref.RootID, ref.MessageID = p[1], p[3], p[5], p[7]
	default:
		return TeamsHostedRef{}, false
	}
	return ref, true
}

// teamsHostedSegments splits an escaped path into unescaped segments, expanding name('key') into
// name and key; empty, "." and ".." segments and escaped slashes are rejected.
func teamsHostedSegments(escaped string) ([]string, bool) {
	escaped = strings.Trim(escaped, "/")
	if escaped == "" {
		return nil, true
	}
	var out []string
	for _, seg := range strings.Split(escaped, "/") {
		s, err := url.PathUnescape(seg)
		if err != nil || s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
			return nil, false
		}
		if m := teamsODataKey.FindStringSubmatch(s); m != nil {
			out = append(out, m[1], m[2])
			continue
		}
		out = append(out, s)
	}
	return out, true
}

// TeamsCalendarViewDeltaURL starts a calendar view delta for one fixed range.
func TeamsCalendarViewDeltaURL(userID string, start, end time.Time) string {
	return teamsPath("/users/%s/calendarView/delta?", userID) +
		"startDateTime=" + url.QueryEscape(TeamsUTC(start)) + "&endDateTime=" + url.QueryEscape(TeamsUTC(end))
}

// TeamsUserCalendarsURL lists the user's mailboxes calendars (primary, holidays, birthdays, …).
func TeamsUserCalendarsURL(userID string) string {
	return teamsPath("/users/%s/calendars?$top=50&$select=id,name,color,hexColor,isDefaultCalendar", userID)
}

func TeamsUserCalendarGroupsURL(userID string) string {
	return teamsPath("/users/%s/calendarGroups?$select=id,name&$top=50", userID)
}

func TeamsUserCalendarGroupCalendarsURL(userID, groupID string) string {
	return teamsPath("/users/%s/calendarGroups/%s/calendars?$top=50&$select=id,name,color,hexColor,isDefaultCalendar", userID, groupID)
}

// TeamsNamedCalendarViewDeltaURL is calendarView/delta for one named calendar (not the default).
func TeamsNamedCalendarViewDeltaURL(userID, calendarID string, start, end time.Time) string {
	return teamsPath("/users/%s/calendars/%s/calendarView/delta?", userID, calendarID) +
		"startDateTime=" + url.QueryEscape(TeamsUTC(start)) + "&endDateTime=" + url.QueryEscape(TeamsUTC(end))
}

func TeamsUserEventURL(userID, eventID string) string {
	return teamsPath("/users/%s/events/%s", userID, eventID)
}

// TeamsOnlineMeetingByJoinURL finds an organizer's online meeting by its join URL.
func TeamsOnlineMeetingByJoinURL(userID, joinURL string) string {
	return teamsPath("/users/%s/onlineMeetings?", userID) +
		teamsFilterQuery("JoinWebUrl eq '"+strings.ReplaceAll(joinURL, "'", "''")+"'")
}

func TeamsAttendanceReportsURL(userID, meetingID string) string {
	return teamsPath("/users/%s/onlineMeetings/%s/attendanceReports", userID, meetingID)
}

func TeamsAttendanceReportURL(userID, meetingID, reportID string) string {
	return teamsPath("/users/%s/onlineMeetings/%s/attendanceReports/%s?$expand=attendanceRecords", userID, meetingID, reportID)
}

// TeamsAllTranscriptsDeltaURL lists an organizer's transcripts (metadata only).
func TeamsAllTranscriptsDeltaURL(userID string) string {
	id := url.PathEscape(strings.TrimSpace(userID))
	return graphBaseURL + "/users/" + id + "/onlineMeetings/getAllTranscripts(meetingOrganizerUserId='" + id + "')/delta"
}

// TeamsChannelRetainedMessagesURL lists a team's retained channel messages: earlier versions of
// edited messages and deleted messages kept by a Teams retention policy.
func TeamsChannelRetainedMessagesURL(teamID string) string {
	return teamsPath("/teams/%s/channels/getAllRetainedMessages", teamID)
}

// TeamsUserRetainedChatMessagesURL lists the retained messages of all chats a user is in.
func TeamsUserRetainedChatMessagesURL(userID string) string {
	return teamsPath("/users/%s/chats/getAllRetainedMessages", userID)
}

// TeamsAllRecordingsDeltaURL lists an organizer's recordings (metadata only).
func TeamsAllRecordingsDeltaURL(userID string) string {
	id := url.PathEscape(strings.TrimSpace(userID))
	return graphBaseURL + "/users/" + id + "/onlineMeetings/getAllRecordings(meetingOrganizerUserId='" + id + "')/delta"
}

// TeamsQAMessagesURL lists every Teams meeting Q&A message of the tenant with its conversation.
// The function takes no filter and has no delta: each listing is a full snapshot.
func TeamsQAMessagesURL() string {
	return graphBaseURL + "/communications/getAllOnlineMeetingMessages?$expand=conversation"
}

func TeamsQAReactionsURL(conversationID, messageID string) string {
	return teamsPath("/communications/onlineMeetingConversations/%s/messages/%s/reactions", conversationID, messageID)
}

// TeamsCallRecordsURL lists call records that started in [from, to).
func TeamsCallRecordsURL(from, to time.Time) string {
	return graphBaseURL + "/communications/callRecords?" +
		teamsFilterQuery("startDateTime ge "+TeamsUTC(from)+" and startDateTime lt "+TeamsUTC(to))
}

func TeamsCallRecordURL(id string) string { return teamsPath("/communications/callRecords/%s", id) }

func TeamsCallParticipantsURL(id string) string {
	return teamsPath("/communications/callRecords/%s/participants_v2", id)
}

func TeamsCallSessionsURL(id string) string {
	return teamsPath("/communications/callRecords/%s/sessions?$expand=segments", id)
}

// TeamsPhoneLogsURL lists PSTN (kind pstn) or Direct Routing (kind direct-routing) log rows
// for [from, to] (both ends inclusive, at most 90 days).
func TeamsPhoneLogsURL(kind string, from, to time.Time) string {
	fn := "getPstnCalls"
	if kind == TeamsKeyDirectRouting {
		fn = "getDirectRoutingCalls"
	}
	return graphBaseURL + "/communications/callRecords/" + fn + "(fromDateTime=" + TeamsUTC(from) + ",toDateTime=" + TeamsUTC(to) + ")"
}

// TeamsSharedDriveItemURL resolves a sharing or file URL to its drive item.
func TeamsSharedDriveItemURL(fileURL string) string {
	enc := "u!" + strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(strings.TrimSpace(fileURL))), "=")
	return graphBaseURL + "/shares/" + enc + "/driveItem?$select=id,name,eTag,cTag,parentReference,webUrl"
}

func TeamsUserDriveURL(userID string) string { return teamsPath("/users/%s/drive?$select=id", userID) }

// TeamsDriveURL reads a drive's type and owner (the OneDrive user for a personal drive).
func TeamsDriveURL(driveID string) string {
	return teamsPath("/drives/%s?$select=id,driveType,owner", driveID)
}
