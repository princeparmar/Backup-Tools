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

type graphChatsListResponse struct {
	Value    []graphChatRow `json:"value"`
	NextLink string         `json:"@odata.nextLink"`
}

type graphChatRow struct {
	ID                  string `json:"id"`
	Topic               string `json:"topic"`
	CreatedDateTime     string `json:"createdDateTime"`
	LastUpdatedDateTime string `json:"lastUpdatedDateTime"`
}

type graphChatMessagesResponse struct {
	Value    []graphChatMessageRow `json:"value"`
	NextLink string                `json:"@odata.nextLink"`
}

type graphChatMessageReactionsResponse struct {
	Value    []graphChatMessageReactionRow `json:"value"`
	NextLink string                        `json:"@odata.nextLink"`
}

type graphChatMessageReactionRow struct {
	ReactionType    string `json:"reactionType"`
	CreatedDateTime string `json:"createdDateTime"`
	User            *struct {
		DisplayName string `json:"displayName"`
		ID          string `json:"id"`
	} `json:"user"`
}

type graphChannelMessageReactionsResponse struct {
	Value    []graphChannelMessageReactionRow `json:"value"`
	NextLink string                           `json:"@odata.nextLink"`
}

type graphChannelMessageReactionRow struct {
	ReactionType    string `json:"reactionType"`
	CreatedDateTime string `json:"createdDateTime"`
	User            *struct {
		DisplayName string `json:"displayName"`
		ID          string `json:"id"`
	} `json:"user"`
}

type graphCallRecordsResponse struct {
	Value    []graphCallRecordRow `json:"value"`
	NextLink string               `json:"@odata.nextLink"`
}

type graphCallRecordRow struct {
	ID            string   `json:"id"`
	StartDateTime string   `json:"startDateTime"`
	EndDateTime   string   `json:"endDateTime"`
	Modalities    []string `json:"modalities"`
	Organizer     *struct {
		User *struct {
			DisplayName string `json:"displayName"`
			Mail        string `json:"mail"`
		} `json:"user"`
	} `json:"organizer"`
}

type graphCallRecordSessionsResponse struct {
	Value    []graphCallRecordSessionRow `json:"value"`
	NextLink string                      `json:"@odata.nextLink"`
}

type graphCallRecordSessionRow struct {
	ID            string   `json:"id"`
	StartDateTime string   `json:"startDateTime"`
	EndDateTime   string   `json:"endDateTime"`
	Modalities    []string `json:"modalities"`
}

type graphCallRecordSessionParticipantsResponse struct {
	Value    []graphCallRecordSessionParticipantRow `json:"value"`
	NextLink string                                 `json:"@odata.nextLink"`
}

type graphCallRecordSessionParticipantRow struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"displayName"`
	UserPrincipalName string   `json:"userPrincipalName"`
	Roles             []string `json:"roles"`
}

type graphCallRecordSessionSegmentsResponse struct {
	Value    []graphCallRecordSessionSegmentRow `json:"value"`
	NextLink string                             `json:"@odata.nextLink"`
}

type graphCallRecordSessionSegmentRow struct {
	ID            string `json:"id"`
	StartDateTime string `json:"startDateTime"`
	EndDateTime   string `json:"endDateTime"`
	Modality      string `json:"modality"`
}

type graphOnlineMeetingsResponse struct {
	Value    []graphOnlineMeetingRow `json:"value"`
	NextLink string                  `json:"@odata.nextLink"`
}

type graphOnlineMeetingRow struct {
	ID            string `json:"id"`
	Subject       string `json:"subject"`
	JoinWebURL    string `json:"joinWebUrl"`
	StartDateTime string `json:"startDateTime"`
	EndDateTime   string `json:"endDateTime"`
	Organizer     *struct {
		User *struct {
			DisplayName string `json:"displayName"`
			Mail        string `json:"mail"`
		} `json:"user"`
	} `json:"organizer"`
}

type graphOnlineMeetingParticipantsResponse struct {
	Value    []graphOnlineMeetingParticipantRow `json:"value"`
	NextLink string                             `json:"@odata.nextLink"`
}

type graphOnlineMeetingParticipantRow struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Roles       []string `json:"roles"`
	UPN         string   `json:"upn"`
}

type graphOnlineMeetingAttendanceReportsResponse struct {
	Value    []graphOnlineMeetingAttendanceReportRow `json:"value"`
	NextLink string                                  `json:"@odata.nextLink"`
}

type graphOnlineMeetingAttendanceReportRow struct {
	ID                          string `json:"id"`
	AttendanceDurationInSeconds int    `json:"attendanceDurationInSeconds"`
	JoinDateTime                string `json:"joinDateTime"`
	LeaveDateTime               string `json:"leaveDateTime"`
	User                        *struct {
		DisplayName string `json:"displayName"`
		Mail        string `json:"mail"`
	} `json:"user"`
}

type graphOnlineMeetingTranscriptsResponse struct {
	Value    []graphOnlineMeetingTranscriptRow `json:"value"`
	NextLink string                            `json:"@odata.nextLink"`
}

type graphOnlineMeetingTranscriptRow struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ContentURL      string `json:"contentUrl"`
	Locale          string `json:"locale"`
	Status          string `json:"status"`
	CreatedDateTime string `json:"createdDateTime"`
}

type graphOnlineMeetingRecordingsResponse struct {
	Value    []graphOnlineMeetingRecordingRow `json:"value"`
	NextLink string                           `json:"@odata.nextLink"`
}

type graphOnlineMeetingRecordingRow struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ContentURL      string `json:"contentUrl"`
	Locale          string `json:"locale"`
	Status          string `json:"status"`
	CreatedDateTime string `json:"createdDateTime"`
}

type graphOnlineMeetingChatMessagesResponse struct {
	Value    []graphOnlineMeetingChatMessageRow `json:"value"`
	NextLink string                             `json:"@odata.nextLink"`
}

type graphOnlineMeetingChatMessageRow struct {
	ID                   string `json:"id"`
	BodyPreview          string `json:"bodyPreview"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	Body                 *struct {
		Content string `json:"content"`
	} `json:"body"`
	From *struct {
		User *struct {
			DisplayName string `json:"displayName"`
			ID          string `json:"id"`
		} `json:"user"`
	} `json:"from"`
}

type graphChatMessageRow struct {
	ID                   string `json:"id"`
	BodyPreview          string `json:"bodyPreview"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	From                 *struct {
		User *struct {
			DisplayName string `json:"displayName"`
			ID          string `json:"id"`
		} `json:"user"`
	} `json:"from"`
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
func TeamsHostedContentKey(prefix, channelID, messageID, contentID string) string {
	return fmt.Sprintf("%s/channels/%s/hosted/%s/%s",
		strings.Trim(strings.TrimSpace(prefix), "/"),
		url.PathEscape(strings.TrimSpace(channelID)),
		strings.TrimSpace(messageID),
		strings.TrimSpace(contentID),
	)
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
func ListTeamChats(ctx context.Context, accessToken string) ([]TeamChatSummary, error) {
	requestURL := fmt.Sprintf("%s/chats?$select=id,topic,createdDateTime,lastUpdatedDateTime",
		graphBaseURL)
	var out []TeamChatSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("team chats http %d: %s", status, truncateForErr(body))
		}
		var parsed graphChatsListResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode team chats: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			out = append(out, TeamChatSummary{
				ID:                  strings.TrimSpace(row.ID),
				Topic:               strings.TrimSpace(row.Topic),
				CreatedDateTime:     strings.TrimSpace(row.CreatedDateTime),
				LastUpdatedDateTime: strings.TrimSpace(row.LastUpdatedDateTime),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
	}
	return out, nil
}

// ListChatMessages lists messages for a given chat.
func ListChatMessages(ctx context.Context, accessToken, chatID string) ([]TeamChatMessage, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	requestURL := fmt.Sprintf("%s/chats/%s/messages?$select=id,bodyPreview,createdDateTime,lastModifiedDateTime,from",
		graphBaseURL, url.PathEscape(chatID))
	var out []TeamChatMessage
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("chat messages http %d: %s", status, truncateForErr(body))
		}
		var parsed graphChatMessagesResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode chat messages: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			fromName := ""
			fromID := ""
			if row.From != nil && row.From.User != nil {
				fromName = strings.TrimSpace(row.From.User.DisplayName)
				fromID = strings.TrimSpace(row.From.User.ID)
			}
			out = append(out, TeamChatMessage{
				ID:                   strings.TrimSpace(row.ID),
				BodyPreview:          strings.TrimSpace(row.BodyPreview),
				CreatedDateTime:      strings.TrimSpace(row.CreatedDateTime),
				LastModifiedDateTime: strings.TrimSpace(row.LastModifiedDateTime),
				From:                 fromName,
				FromID:               fromID,
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
	}
	return out, nil
}

// ListTeamsChatMessageReactions lists reactions for a specific chat message.
func ListTeamsChatMessageReactions(ctx context.Context, accessToken, chatID, messageID string) ([]TeamsChatMessageReactionSummary, error) {
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)
	if chatID == "" || messageID == "" {
		return nil, fmt.Errorf("chat_id and message_id are required")
	}
	requestURL := fmt.Sprintf("%s/chats/%s/messages/%s/reactions?$select=reactionType,createdDateTime,user",
		graphBaseURL, url.PathEscape(chatID), url.PathEscape(messageID))
	var out []TeamsChatMessageReactionSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("chat message reactions http %d: %s", status, truncateForErr(body))
		}
		var parsed graphChatMessageReactionsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode chat message reactions: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			userDisplayName := ""
			userID := ""
			if row.User != nil {
				userDisplayName = strings.TrimSpace(row.User.DisplayName)
				userID = strings.TrimSpace(row.User.ID)
			}
			out = append(out, TeamsChatMessageReactionSummary{
				Type:            strings.TrimSpace(row.ReactionType),
				CreatedDateTime: strings.TrimSpace(row.CreatedDateTime),
				UserDisplayName: userDisplayName,
				UserID:          userID,
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsMessageReactions lists reactions for a specific team channel message.
func ListTeamsMessageReactions(ctx context.Context, accessToken, teamID, channelID, messageID string) ([]TeamsMessageReactionSummary, error) {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	messageID = strings.TrimSpace(messageID)
	if teamID == "" || channelID == "" || messageID == "" {
		return nil, fmt.Errorf("team_id, channel_id, and message_id are required")
	}
	requestURL := fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/reactions?$select=reactionType,createdDateTime,user",
		graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID), urlPathEscape(messageID))
	var out []TeamsMessageReactionSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("team message reactions http %d: %s", status, truncateForErr(body))
		}
		var parsed graphChannelMessageReactionsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode team message reactions: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			userDisplayName := ""
			userID := ""
			if row.User != nil {
				userDisplayName = strings.TrimSpace(row.User.DisplayName)
				userID = strings.TrimSpace(row.User.ID)
			}
			out = append(out, TeamsMessageReactionSummary{
				Type:            strings.TrimSpace(row.ReactionType),
				CreatedDateTime: strings.TrimSpace(row.CreatedDateTime),
				UserDisplayName: userDisplayName,
				UserID:          userID,
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsCallRecords lists Teams call records visible to the caller.
func ListTeamsCallRecords(ctx context.Context, accessToken string) ([]TeamsCallRecordSummary, error) {
	requestURL := fmt.Sprintf("%s/communications/callRecords?$select=id,modalities,startDateTime,endDateTime,organizer",
		graphBaseURL)
	var out []TeamsCallRecordSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("call records http %d: %s", status, truncateForErr(body))
		}
		var parsed graphCallRecordsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode call records: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			organizer := ""
			organizerEmail := ""
			if row.Organizer != nil && row.Organizer.User != nil {
				organizer = strings.TrimSpace(row.Organizer.User.DisplayName)
				organizerEmail = strings.TrimSpace(row.Organizer.User.Mail)
			}
			out = append(out, TeamsCallRecordSummary{
				ID:             strings.TrimSpace(row.ID),
				StartDateTime:  strings.TrimSpace(row.StartDateTime),
				EndDateTime:    strings.TrimSpace(row.EndDateTime),
				Organizer:      organizer,
				OrganizerEmail: organizerEmail,
				Modalities:     strings.Join(row.Modalities, ", "),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsCallRecordSessions lists sessions for a specific Teams call record.
func ListTeamsCallRecordSessions(ctx context.Context, accessToken, callRecordID string) ([]TeamsCallRecordSessionSummary, error) {
	callRecordID = strings.TrimSpace(callRecordID)
	if callRecordID == "" {
		return nil, fmt.Errorf("call_record_id is required")
	}
	requestURL := fmt.Sprintf("%s/communications/callRecords/%s/sessions?$select=id,startDateTime,endDateTime,modalities",
		graphBaseURL, url.PathEscape(callRecordID))
	var out []TeamsCallRecordSessionSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("call record sessions http %d: %s", status, truncateForErr(body))
		}
		var parsed graphCallRecordSessionsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode call record sessions: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			out = append(out, TeamsCallRecordSessionSummary{
				ID:            strings.TrimSpace(row.ID),
				CallRecordID:  strings.TrimSpace(callRecordID),
				StartDateTime: strings.TrimSpace(row.StartDateTime),
				EndDateTime:   strings.TrimSpace(row.EndDateTime),
				Modalities:    strings.Join(row.Modalities, ", "),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsCallRecordSessionParticipants lists participants for a specific Teams call-record session.
func ListTeamsCallRecordSessionParticipants(ctx context.Context, accessToken, callRecordID, sessionID string) ([]TeamsCallRecordSessionParticipantSummary, error) {
	callRecordID = strings.TrimSpace(callRecordID)
	sessionID = strings.TrimSpace(sessionID)
	if callRecordID == "" || sessionID == "" {
		return nil, fmt.Errorf("call_record_id and session_id are required")
	}
	requestURL := fmt.Sprintf("%s/communications/callRecords/%s/sessions/%s/participants?$select=id,displayName,roles,userPrincipalName",
		graphBaseURL, url.PathEscape(callRecordID), url.PathEscape(sessionID))
	var out []TeamsCallRecordSessionParticipantSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("call record session participants http %d: %s", status, truncateForErr(body))
		}
		var parsed graphCallRecordSessionParticipantsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode call record session participants: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			role := ""
			if len(row.Roles) > 0 {
				cleanRoles := make([]string, 0, len(row.Roles))
				for _, r := range row.Roles {
					trimmed := strings.TrimSpace(r)
					if trimmed != "" {
						cleanRoles = append(cleanRoles, trimmed)
					}
				}
				role = strings.Join(cleanRoles, ", ")
			}
			out = append(out, TeamsCallRecordSessionParticipantSummary{
				ID:                strings.TrimSpace(row.ID),
				SessionID:         strings.TrimSpace(sessionID),
				CallRecordID:      strings.TrimSpace(callRecordID),
				DisplayName:       strings.TrimSpace(row.DisplayName),
				UserPrincipalName: strings.TrimSpace(row.UserPrincipalName),
				Role:              role,
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsCallRecordSessionSegments lists segments for a specific Teams call-record session.
func ListTeamsCallRecordSessionSegments(ctx context.Context, accessToken, callRecordID, sessionID string) ([]TeamsCallRecordSessionSegmentSummary, error) {
	callRecordID = strings.TrimSpace(callRecordID)
	sessionID = strings.TrimSpace(sessionID)
	if callRecordID == "" || sessionID == "" {
		return nil, fmt.Errorf("call_record_id and session_id are required")
	}
	requestURL := fmt.Sprintf("%s/communications/callRecords/%s/sessions/%s/segments?$select=id,startDateTime,endDateTime,modality",
		graphBaseURL, url.PathEscape(callRecordID), url.PathEscape(sessionID))
	var out []TeamsCallRecordSessionSegmentSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("call record session segments http %d: %s", status, truncateForErr(body))
		}
		var parsed graphCallRecordSessionSegmentsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode call record session segments: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			out = append(out, TeamsCallRecordSessionSegmentSummary{
				ID:            strings.TrimSpace(row.ID),
				SessionID:     strings.TrimSpace(sessionID),
				CallRecordID:  strings.TrimSpace(callRecordID),
				StartDateTime: strings.TrimSpace(row.StartDateTime),
				EndDateTime:   strings.TrimSpace(row.EndDateTime),
				Modality:      strings.TrimSpace(row.Modality),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsOnlineMeetings lists Teams online meetings visible to the caller.
func ListTeamsOnlineMeetings(ctx context.Context, accessToken string) ([]TeamsMeetingSummary, error) {
	requestURL := fmt.Sprintf("%s/me/onlineMeetings?$select=id,subject,joinWebUrl,startDateTime,endDateTime,organizer",
		graphBaseURL)
	var out []TeamsMeetingSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("online meetings http %d: %s", status, truncateForErr(body))
		}
		var parsed graphOnlineMeetingsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode online meetings: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			organizer := ""
			organizerEmail := ""
			if row.Organizer != nil && row.Organizer.User != nil {
				organizer = strings.TrimSpace(row.Organizer.User.DisplayName)
				organizerEmail = strings.TrimSpace(row.Organizer.User.Mail)
			}
			out = append(out, TeamsMeetingSummary{
				ID:             strings.TrimSpace(row.ID),
				Subject:        strings.TrimSpace(row.Subject),
				JoinURL:        strings.TrimSpace(row.JoinWebURL),
				StartDateTime:  strings.TrimSpace(row.StartDateTime),
				EndDateTime:    strings.TrimSpace(row.EndDateTime),
				Organizer:      organizer,
				OrganizerEmail: organizerEmail,
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
	}
	return out, nil
}

// ListTeamsMeetingParticipants lists participants for a Teams online meeting.
func ListTeamsMeetingParticipants(ctx context.Context, accessToken, meetingID string) ([]TeamsMeetingParticipantSummary, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	requestURL := fmt.Sprintf("%s/me/onlineMeetings/%s/participants?$select=id,displayName,roles,upn",
		graphBaseURL, url.PathEscape(meetingID))
	var out []TeamsMeetingParticipantSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("meeting participants http %d: %s", status, truncateForErr(body))
		}
		var parsed graphOnlineMeetingParticipantsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode meeting participants: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			role := ""
			if len(row.Roles) > 0 {
				cleanRoles := make([]string, 0, len(row.Roles))
				for _, r := range row.Roles {
					trimmed := strings.TrimSpace(r)
					if trimmed != "" {
						cleanRoles = append(cleanRoles, trimmed)
					}
				}
				role = strings.Join(cleanRoles, ", ")
			}
			out = append(out, TeamsMeetingParticipantSummary{
				ID:          strings.TrimSpace(row.ID),
				DisplayName: strings.TrimSpace(row.DisplayName),
				Role:        role,
				UPN:         strings.TrimSpace(row.UPN),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsMeetingAttendanceReports lists attendance reports for a Teams online meeting.
func ListTeamsMeetingAttendanceReports(ctx context.Context, accessToken, meetingID string) ([]TeamsMeetingAttendanceSummary, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	requestURL := fmt.Sprintf("%s/me/onlineMeetings/%s/attendanceReports?$select=id,attendanceDurationInSeconds,joinDateTime,leaveDateTime,user",
		graphBaseURL, url.PathEscape(meetingID))
	var out []TeamsMeetingAttendanceSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("meeting attendance reports http %d: %s", status, truncateForErr(body))
		}
		var parsed graphOnlineMeetingAttendanceReportsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode meeting attendance reports: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			userDisplayName := ""
			userEmail := ""
			if row.User != nil {
				userDisplayName = strings.TrimSpace(row.User.DisplayName)
				userEmail = strings.TrimSpace(row.User.Mail)
			}
			out = append(out, TeamsMeetingAttendanceSummary{
				ID:                strings.TrimSpace(row.ID),
				UserDisplayName:   userDisplayName,
				UserEmail:         userEmail,
				AttendanceSeconds: row.AttendanceDurationInSeconds,
				JoinDateTime:      strings.TrimSpace(row.JoinDateTime),
				LeaveDateTime:     strings.TrimSpace(row.LeaveDateTime),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsMeetingTranscripts lists transcript metadata for a Teams online meeting.
func ListTeamsMeetingTranscripts(ctx context.Context, accessToken, meetingID string) ([]TeamsMeetingTranscriptSummary, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	requestURL := fmt.Sprintf("%s/me/onlineMeetings/%s/transcripts?$select=id,createdDateTime,type,contentUrl,locale,status",
		graphBaseURL, url.PathEscape(meetingID))
	var out []TeamsMeetingTranscriptSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("meeting transcripts http %d: %s", status, truncateForErr(body))
		}
		var parsed graphOnlineMeetingTranscriptsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode meeting transcripts: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			out = append(out, TeamsMeetingTranscriptSummary{
				ID:              strings.TrimSpace(row.ID),
				Type:            strings.TrimSpace(row.Type),
				ContentURL:      strings.TrimSpace(row.ContentURL),
				Locale:          strings.TrimSpace(row.Locale),
				Status:          strings.TrimSpace(row.Status),
				CreatedDateTime: strings.TrimSpace(row.CreatedDateTime),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsMeetingRecordings lists recording metadata for a Teams online meeting.
func ListTeamsMeetingRecordings(ctx context.Context, accessToken, meetingID string) ([]TeamsMeetingRecordingSummary, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	requestURL := fmt.Sprintf("%s/me/onlineMeetings/%s/recordings?$select=id,createdDateTime,type,contentUrl,locale,status",
		graphBaseURL, url.PathEscape(meetingID))
	var out []TeamsMeetingRecordingSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("meeting recordings http %d: %s", status, truncateForErr(body))
		}
		var parsed graphOnlineMeetingRecordingsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode meeting recordings: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			out = append(out, TeamsMeetingRecordingSummary{
				ID:              strings.TrimSpace(row.ID),
				Type:            strings.TrimSpace(row.Type),
				ContentURL:      strings.TrimSpace(row.ContentURL),
				Locale:          strings.TrimSpace(row.Locale),
				Status:          strings.TrimSpace(row.Status),
				CreatedDateTime: strings.TrimSpace(row.CreatedDateTime),
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
	}
	return out, nil
}

// ListTeamsMeetingChatMessages lists chat-message metadata for a Teams online meeting.
func ListTeamsMeetingChatMessages(ctx context.Context, accessToken, meetingID string) ([]TeamsMeetingChatMessageSummary, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	requestURL := fmt.Sprintf("%s/me/onlineMeetings/%s/chatMessages?$select=id,createdDateTime,lastModifiedDateTime,body,from",
		graphBaseURL, url.PathEscape(meetingID))
	var out []TeamsMeetingChatMessageSummary
	for requestURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("meeting chat messages http %d: %s", status, truncateForErr(body))
		}
		var parsed graphOnlineMeetingChatMessagesResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode meeting chat messages: %w", err)
		}
		for i := range parsed.Value {
			row := parsed.Value[i]
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			bodyPreview := ""
			if row.Body != nil {
				bodyPreview = strings.TrimSpace(row.Body.Content)
			}
			fromName := ""
			fromID := ""
			if row.From != nil && row.From.User != nil {
				fromName = strings.TrimSpace(row.From.User.DisplayName)
				fromID = strings.TrimSpace(row.From.User.ID)
			}
			out = append(out, TeamsMeetingChatMessageSummary{
				ID:                   strings.TrimSpace(row.ID),
				BodyPreview:          bodyPreview,
				CreatedDateTime:      strings.TrimSpace(row.CreatedDateTime),
				LastModifiedDateTime: strings.TrimSpace(row.LastModifiedDateTime),
				From:                 fromName,
				FromID:               fromID,
			})
		}
		requestURL = strings.TrimSpace(parsed.NextLink)
		if requestURL != "" && !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if strings.HasPrefix(requestURL, "/") {
				requestURL = graphBaseURL + requestURL
			}
		}
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
		"team_id":          resolved.TeamID,
		"team_name":        resolved.TeamName,
		"team_description": resolved.TeamDescription,
		"team_web_url":     resolved.TeamWebURL,
		"group_id":         resolved.GroupID,
		"channels":         channels,
		"snapshot_at":      time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsChatsSnapshotJSON builds a list of Teams chat objects for StorX snapshot storage.
func TeamsChatsSnapshotJSON(chats []TeamChatSummary) ([]byte, error) {
	payload := map[string]interface{}{
		"chats":       chats,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsChatMessagesSnapshotJSON builds a chat-message snapshot for StorX snapshot storage.
func TeamsChatMessagesSnapshotJSON(chatID string, messages []TeamChatMessage) ([]byte, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	payload := map[string]interface{}{
		"chat_id":     chatID,
		"messages":    messages,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsChatMessageReactionsSnapshotJSON builds a reaction snapshot for a specific chat message.
func TeamsChatMessageReactionsSnapshotJSON(chatID, messageID string, reactions []TeamsChatMessageReactionSummary) ([]byte, error) {
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)
	if chatID == "" || messageID == "" {
		return nil, fmt.Errorf("chat_id and message_id are required")
	}
	payload := map[string]interface{}{
		"chat_id":     chatID,
		"message_id":  messageID,
		"reactions":   reactions,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMessageReactionsSnapshotJSON builds a reaction snapshot for a specific team channel message.
func TeamsMessageReactionsSnapshotJSON(teamID, channelID, messageID string, reactions []TeamsMessageReactionSummary) ([]byte, error) {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	messageID = strings.TrimSpace(messageID)
	if teamID == "" || channelID == "" || messageID == "" {
		return nil, fmt.Errorf("team_id, channel_id, and message_id are required")
	}
	payload := map[string]interface{}{
		"team_id":     teamID,
		"channel_id":  channelID,
		"message_id":  messageID,
		"reactions":   reactions,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsCallRecordsSnapshotJSON builds a call-record snapshot for StorX metadata persistence.
func TeamsCallRecordsSnapshotJSON(calls []TeamsCallRecordSummary) ([]byte, error) {
	payload := map[string]interface{}{
		"calls":       calls,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// ParseTeamsCallRecordsSnapshot unmarshals a Teams call-records snapshot.
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
func TeamsCallRecordSessionsSnapshotJSON(callRecordID string, sessions []TeamsCallRecordSessionSummary) ([]byte, error) {
	callRecordID = strings.TrimSpace(callRecordID)
	if callRecordID == "" {
		return nil, fmt.Errorf("call_record_id is required")
	}
	payload := map[string]interface{}{
		"call_record_id": callRecordID,
		"sessions":       sessions,
		"snapshot_at":    time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsCallRecordSessionParticipantsSnapshotJSON builds a call-record session participants snapshot for StorX metadata persistence.
func TeamsCallRecordSessionParticipantsSnapshotJSON(callRecordID, sessionID string, participants []TeamsCallRecordSessionParticipantSummary) ([]byte, error) {
	callRecordID = strings.TrimSpace(callRecordID)
	sessionID = strings.TrimSpace(sessionID)
	if callRecordID == "" || sessionID == "" {
		return nil, fmt.Errorf("call_record_id and session_id are required")
	}
	payload := map[string]interface{}{
		"call_record_id": callRecordID,
		"session_id":     sessionID,
		"participants":   participants,
		"snapshot_at":    time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsCallRecordSessionSegmentsSnapshotJSON builds a call-record session segments snapshot for StorX metadata persistence.
func TeamsCallRecordSessionSegmentsSnapshotJSON(callRecordID, sessionID string, segments []TeamsCallRecordSessionSegmentSummary) ([]byte, error) {
	callRecordID = strings.TrimSpace(callRecordID)
	sessionID = strings.TrimSpace(sessionID)
	if callRecordID == "" || sessionID == "" {
		return nil, fmt.Errorf("call_record_id and session_id are required")
	}
	payload := map[string]interface{}{
		"call_record_id": callRecordID,
		"session_id":     sessionID,
		"segments":       segments,
		"snapshot_at":    time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMeetingSnapshotJSON builds a meeting snapshot for StorX metadata persistence.
func TeamsMeetingSnapshotJSON(meetings []TeamsMeetingSummary) ([]byte, error) {
	payload := map[string]interface{}{
		"meetings":    meetings,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMeetingParticipantsSnapshotJSON builds a participant snapshot for a single meeting.
func TeamsMeetingParticipantsSnapshotJSON(meetingID string, participants []TeamsMeetingParticipantSummary) ([]byte, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	payload := map[string]interface{}{
		"meeting_id":   meetingID,
		"participants": participants,
		"snapshot_at":  time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMeetingAttendanceSnapshotJSON builds an attendance snapshot for a single meeting.
func TeamsMeetingAttendanceSnapshotJSON(meetingID string, reports []TeamsMeetingAttendanceSummary) ([]byte, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	payload := map[string]interface{}{
		"meeting_id":  meetingID,
		"reports":     reports,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMeetingTranscriptSnapshotJSON builds a transcript snapshot for a single meeting.
func TeamsMeetingTranscriptSnapshotJSON(meetingID string, transcripts []TeamsMeetingTranscriptSummary) ([]byte, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	payload := map[string]interface{}{
		"meeting_id":  meetingID,
		"transcripts": transcripts,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMeetingRecordingSnapshotJSON builds a recording snapshot for a single meeting.
func TeamsMeetingRecordingSnapshotJSON(meetingID string, recordings []TeamsMeetingRecordingSummary) ([]byte, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	payload := map[string]interface{}{
		"meeting_id":  meetingID,
		"recordings":  recordings,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsMeetingChatMessagesSnapshotJSON builds a meeting chat snapshot for a single meeting.
func TeamsMeetingChatMessagesSnapshotJSON(meetingID string, messages []TeamsMeetingChatMessageSummary) ([]byte, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return nil, fmt.Errorf("meeting_id is required")
	}
	payload := map[string]interface{}{
		"meeting_id":  meetingID,
		"messages":    messages,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsReplySnapshotJSON stores replies under a parent message for recoverability.
func TeamsReplySnapshotJSON(parentMessageID string, replies []TeamsDeltaMessage) ([]byte, error) {
	parentMessageID = strings.TrimSpace(parentMessageID)
	if parentMessageID == "" {
		return nil, fmt.Errorf("parent_message_id is required")
	}
	payload := map[string]interface{}{
		"parent_message_id": parentMessageID,
		"replies":           replies,
		"snapshot_at":       time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

// TeamsAttachmentSnapshotJSON stores attachment metadata and the storage key for hosted content.
func TeamsAttachmentSnapshotJSON(messageID string, refs []TeamsAttachmentReference) ([]byte, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	payload := map[string]interface{}{
		"message_id":  messageID,
		"attachments": refs,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
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
