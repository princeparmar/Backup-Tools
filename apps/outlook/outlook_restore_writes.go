package outlook

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
// original message IDs cannot be recreated). Replies are restored via PostTeamsChannelReply.
func PostTeamsChannelMessage(ctx context.Context, accessToken, teamID, channelID, bodyContent string) error {
	_, err := PostTeamsChannelMessageID(ctx, accessToken, teamID, channelID, bodyContent)
	return err
}

// PostTeamsChannelMessageID posts a new channel message and returns its id. A Graph failure is
// a *TeamsGraphError.
func PostTeamsChannelMessageID(ctx context.Context, accessToken, teamID, channelID, bodyContent string) (string, error) {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	if teamID == "" || channelID == "" {
		return "", fmt.Errorf("team_id and channel_id are required")
	}
	reqURL := fmt.Sprintf("%s/teams/%s/channels/%s/messages", graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID))
	return postTeamsMessage(ctx, accessToken, reqURL, bodyContent, "(restored message)")
}

// PostTeamsChannelReply creates a reply under an existing channel message. A Graph failure is a
// *TeamsGraphError (404 when the parent no longer exists).
func PostTeamsChannelReply(ctx context.Context, accessToken, teamID, channelID, parentMessageID, bodyContent string) error {
	teamID = strings.TrimSpace(teamID)
	channelID = strings.TrimSpace(channelID)
	parentMessageID = strings.TrimSpace(parentMessageID)
	if teamID == "" || channelID == "" || parentMessageID == "" {
		return fmt.Errorf("team_id, channel_id, and parent_message_id are required")
	}
	reqURL := fmt.Sprintf("%s/teams/%s/channels/%s/messages/%s/replies", graphBaseURL, url.PathEscape(teamID), url.PathEscape(channelID), url.PathEscape(parentMessageID))
	_, err := postTeamsMessage(ctx, accessToken, reqURL, bodyContent, "(restored reply)")
	return err
}

// PostTeamsChatMessage posts a new message into an existing chat (ChatMessage.Send; the signed-in
// user must be a member) and returns its id.
func PostTeamsChatMessage(ctx context.Context, accessToken, chatID, bodyContent string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", fmt.Errorf("chat_id is required")
	}
	reqURL := fmt.Sprintf("%s/chats/%s/messages", graphBaseURL, url.PathEscape(chatID))
	return postTeamsMessage(ctx, accessToken, reqURL, bodyContent, "(restored message)")
}

func postTeamsMessage(ctx context.Context, accessToken, reqURL, bodyContent, empty string) (string, error) {
	if strings.TrimSpace(bodyContent) == "" {
		bodyContent = empty
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"body": map[string]string{
			"contentType": "html",
			"content":     bodyContent,
		},
	})
	b, err := teamsPostOnce(ctx, accessToken, reqURL, payload)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &created)
	return created.ID, nil
}

// ErrTeamsPostOutcomeUnknown means a Teams post may have been created: the connection failed after
// the request was sent, or Graph answered with a server error. Posting it again can duplicate it.
var ErrTeamsPostOutcomeUnknown = errors.New("teams post outcome unknown")

// teamsPostOnce sends one Teams create request. A post is not idempotent, so it is repeated only
// when Graph certainly did not act on it: throttling (429, or 503 with Retry-After) and
// connections that were never opened.
func teamsPostOnce(ctx context.Context, accessToken, reqURL string, payload []byte) ([]byte, error) {
	client := &http.Client{}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			if !isDialError(err) {
				return nil, fmt.Errorf("%w: %v", ErrTeamsPostOutcomeUnknown, err)
			}
			if attempt+1 >= maxGraphRetries {
				return nil, err
			}
			time.Sleep(time.Duration(attempt+1) * 400 * time.Millisecond)
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		retryAfter := resp.Header.Get("Retry-After")
		throttled := resp.StatusCode == http.StatusTooManyRequests ||
			(resp.StatusCode == http.StatusServiceUnavailable && strings.TrimSpace(retryAfter) != "")
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if rerr != nil {
				return nil, fmt.Errorf("%w: reading the response: %v", ErrTeamsPostOutcomeUnknown, rerr)
			}
			return b, nil
		case throttled && attempt+1 < maxGraphRetries:
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryAfterDuration(retryAfter, attempt)):
			}
		case throttled || resp.StatusCode < 500:
			return nil, parseTeamsGraphError(resp.StatusCode, b)
		default:
			return nil, fmt.Errorf("%w: %w", ErrTeamsPostOutcomeUnknown, parseTeamsGraphError(resp.StatusCode, b))
		}
	}
}

// isDialError reports a connection that was never opened, so no request reached the server.
func isDialError(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	return errors.As(err, &dns)
}

// TeamsRestoreRef is the reference added to a reposted message: the first 10 hex characters of
// sha1(object key), searchable in Teams to spot a duplicate after a retried restore.
func TeamsRestoreRef(objectKey string) string {
	sum := sha1.Sum([]byte(objectKey))
	return hex.EncodeToString(sum[:])[:10]
}

// TeamsRepostBody is the HTML of a reposted backup message: a note with the original sender,
// time and ref, an "In reply to" line when the original thread root can't be used, then the body.
func TeamsRepostBody(raw []byte, objectKey, inReplyTo string) string {
	var m struct {
		CreatedDateTime string `json:"createdDateTime"`
		From            *struct {
			User        *struct{ DisplayName string } `json:"user"`
			Application *struct{ DisplayName string } `json:"application"`
		} `json:"from"`
	}
	_ = json.Unmarshal(raw, &m)
	who := "unknown sender"
	if m.From != nil {
		switch {
		case m.From.User != nil && strings.TrimSpace(m.From.User.DisplayName) != "":
			who = m.From.User.DisplayName
		case m.From.Application != nil && strings.TrimSpace(m.From.Application.DisplayName) != "":
			who = m.From.Application.DisplayName
		}
	}
	when := strings.TrimSpace(m.CreatedDateTime)
	if t, ok := ParseTeamsTime(when); ok {
		when = t.Format("2006-01-02 15:04 UTC")
	}
	note := fmt.Sprintf("<p><em>Originally posted by %s on %s (ref %s)</em></p>",
		html.EscapeString(strings.TrimSpace(who)), html.EscapeString(when), TeamsRestoreRef(objectKey))
	if strings.TrimSpace(inReplyTo) != "" {
		note += fmt.Sprintf("<p><em>In reply to message %s</em></p>", html.EscapeString(inReplyTo))
	}
	return note + ExtractTeamsMessageBody(raw)
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

// ParseTeamsIDsFromKey extracts StorX path segments only ({prefix}/channels/{channelId}/...).
// teamKey is the storage prefix of the team (holding _team.json) and must NOT be treated as a
// Graph team ID. Prefer ResolveTeamsGraphIDs.
func ParseTeamsIDsFromKey(objectKey string) (teamKey, channelKeySegment string) {
	parts := strings.Split(strings.Trim(objectKey, "/"), "/")
	for i := 1; i+1 < len(parts); i++ {
		if parts[i] == "channels" {
			return strings.Join(parts[:i], "/"), parts[i+1]
		}
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

// ParseTeamsChatsSnapshot unmarshals {teamKey}/chats/_chats.json.
func ParseTeamsChatsSnapshot(raw []byte) (*TeamsChatsSnapshot, error) {
	var snap TeamsChatsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if len(snap.Chats) == 0 {
		return nil, fmt.Errorf("chats missing in _chats.json")
	}
	return &snap, nil
}

// ParseTeamsChatMessagesSnapshot unmarshals {teamKey}/chats/{chatId}/_messages.json.
func ParseTeamsChatMessagesSnapshot(raw []byte) (*TeamsChatMessagesSnapshot, error) {
	var snap TeamsChatMessagesSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.ChatID) == "" {
		return nil, fmt.Errorf("chat_id missing in _messages.json")
	}
	return &snap, nil
}

// ParseTeamsChatMessageReactionsSnapshot unmarshals a Teams chat-message reactions snapshot.
func ParseTeamsChatMessageReactionsSnapshot(raw []byte) (*TeamsChatMessageReactionsSnapshot, error) {
	var snap TeamsChatMessageReactionsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.ChatID) == "" || strings.TrimSpace(snap.MessageID) == "" {
		return nil, fmt.Errorf("chat_id and message_id required in reactions snapshot")
	}
	return &snap, nil
}

// ParseTeamsMessageReactionsSnapshot unmarshals a Teams channel-message reactions snapshot.
func ParseTeamsMessageReactionsSnapshot(raw []byte) (*TeamsMessageReactionsSnapshot, error) {
	var snap TeamsMessageReactionsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.TeamID) == "" || strings.TrimSpace(snap.ChannelID) == "" || strings.TrimSpace(snap.MessageID) == "" {
		return nil, fmt.Errorf("team_id, channel_id, and message_id required in reactions snapshot")
	}
	return &snap, nil
}

// ParseTeamsReplySnapshot unmarshals {teamKey}/channels/{channelId}/replies/{messageId}.json.
func ParseTeamsReplySnapshot(raw []byte) (*TeamsReplySnapshot, error) {
	var snap TeamsReplySnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.ParentMessageID) == "" {
		return nil, fmt.Errorf("parent_message_id missing in replies snapshot")
	}
	return &snap, nil
}

// ParseTeamsAttachmentSnapshot unmarshals {teamKey}/channels/{channelId}/attachments/{messageId}.json.
func ParseTeamsAttachmentSnapshot(raw []byte) (*TeamsAttachmentSnapshot, error) {
	var snap TeamsAttachmentSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.MessageID) == "" {
		return nil, fmt.Errorf("message_id missing in attachment snapshot")
	}
	return &snap, nil
}

// ParseTeamsCallRecordSessionsSnapshot unmarshals a Teams call-record session snapshot.
func ParseTeamsCallRecordSessionsSnapshot(raw []byte) (*TeamsCallRecordSessionsSnapshot, error) {
	var snap TeamsCallRecordSessionsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.CallRecordID) == "" {
		return nil, fmt.Errorf("call_record_id missing in call-record session snapshot")
	}
	return &snap, nil
}

// ParseTeamsCallRecordSessionParticipantsSnapshot unmarshals a Teams call-record session participants snapshot.
func ParseTeamsCallRecordSessionParticipantsSnapshot(raw []byte) (*TeamsCallRecordSessionParticipantsSnapshot, error) {
	var snap TeamsCallRecordSessionParticipantsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.CallRecordID) == "" || strings.TrimSpace(snap.SessionID) == "" {
		return nil, fmt.Errorf("call_record_id and session_id missing in call-record session participants snapshot")
	}
	return &snap, nil
}

// ParseTeamsCallRecordSessionSegmentsSnapshot unmarshals a Teams call-record session segments snapshot.
func ParseTeamsCallRecordSessionSegmentsSnapshot(raw []byte) (*TeamsCallRecordSessionSegmentsSnapshot, error) {
	var snap TeamsCallRecordSessionSegmentsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.CallRecordID) == "" || strings.TrimSpace(snap.SessionID) == "" {
		return nil, fmt.Errorf("call_record_id and session_id missing in call-record session segments snapshot")
	}
	return &snap, nil
}

// ParseTeamsMeetingSnapshot unmarshals a Teams meeting metadata snapshot.
func ParseTeamsMeetingSnapshot(raw []byte) (*TeamsMeetingSnapshot, error) {
	var snap TeamsMeetingSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if len(snap.Meetings) == 0 {
		return nil, fmt.Errorf("meetings missing in snapshot")
	}
	return &snap, nil
}

// ParseTeamsMeetingParticipantsSnapshot unmarshals a Teams meeting participant snapshot.
func ParseTeamsMeetingParticipantsSnapshot(raw []byte) (*TeamsMeetingParticipantsSnapshot, error) {
	var snap TeamsMeetingParticipantsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.MeetingID) == "" {
		return nil, fmt.Errorf("meeting_id missing in participants snapshot")
	}
	return &snap, nil
}

// ParseTeamsMeetingAttendanceSnapshot unmarshals a Teams meeting attendance-report snapshot.
func ParseTeamsMeetingAttendanceSnapshot(raw []byte) (*TeamsMeetingAttendanceSnapshot, error) {
	var snap TeamsMeetingAttendanceSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.MeetingID) == "" {
		return nil, fmt.Errorf("meeting_id missing in attendance snapshot")
	}
	return &snap, nil
}

// ParseTeamsMeetingTranscriptSnapshot unmarshals a Teams meeting transcript snapshot.
func ParseTeamsMeetingTranscriptSnapshot(raw []byte) (*TeamsMeetingTranscriptSnapshot, error) {
	var snap TeamsMeetingTranscriptSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.MeetingID) == "" {
		return nil, fmt.Errorf("meeting_id missing in transcript snapshot")
	}
	return &snap, nil
}

// ParseTeamsMeetingRecordingSnapshot unmarshals a Teams meeting recording snapshot.
func ParseTeamsMeetingRecordingSnapshot(raw []byte) (*TeamsMeetingRecordingSnapshot, error) {
	var snap TeamsMeetingRecordingSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.MeetingID) == "" {
		return nil, fmt.Errorf("meeting_id missing in recording snapshot")
	}
	return &snap, nil
}

// ParseTeamsMeetingChatMessagesSnapshot unmarshals a Teams meeting chat message snapshot.
func ParseTeamsMeetingChatMessagesSnapshot(raw []byte) (*TeamsMeetingChatMessagesSnapshot, error) {
	var snap TeamsMeetingChatMessagesSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snap.MeetingID) == "" {
		return nil, fmt.Errorf("meeting_id missing in meeting chat messages snapshot")
	}
	return &snap, nil
}

// ResolveTeamsChatGraphID returns a real Graph chat ID for restore.
func ResolveTeamsChatGraphID(objectKey string, chatSnap *TeamsChatsSnapshot, overrideChatID string) (string, error) {
	chatID := strings.TrimSpace(overrideChatID)
	if chatID == "" {
		parts := strings.Split(strings.Trim(objectKey, "/"), "/")
		for i := 0; i+2 < len(parts); i++ {
			if parts[i] == "chats" {
				chatID = strings.TrimSpace(parts[i+1])
				if unesc, err := url.PathUnescape(chatID); err == nil {
					chatID = strings.TrimSpace(unesc)
				}
				break
			}
		}
	}
	if chatID == "" && chatSnap != nil && len(chatSnap.Chats) == 1 {
		chatID = strings.TrimSpace(chatSnap.Chats[0].ID)
	}
	if chatID == "" {
		return "", fmt.Errorf("chat_id missing from backup snapshot or object key")
	}
	return chatID, nil
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
