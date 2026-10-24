package outlook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSanitizeTeamsTeamKey(t *testing.T) {
	got := SanitizeTeamsTeamKey("00000000-0000-0000-0000-000000000001")
	if got == "" {
		t.Fatal("expected non-empty team key")
	}
}

func TestTeamsTeamSnapshotPreservesTeamMetadata(t *testing.T) {
	resolved := &ResolvedTeam{
		TeamID:          "team-graph-id",
		TeamName:        "Platform Engineering",
		TeamDescription: "Team metadata used for restore",
		TeamWebURL:      "https://teams.microsoft.com/l/team/team-graph-id",
		GroupID:         "group-graph-id",
	}

	raw, err := TeamsTeamSnapshotJSON(resolved, nil)
	if err != nil {
		t.Fatalf("TeamsTeamSnapshotJSON() error = %v", err)
	}

	var snapshot map[string]interface{}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got := snapshot["team_description"]; got != resolved.TeamDescription {
		t.Fatalf("team_description = %v, want %q", got, resolved.TeamDescription)
	}
	if got := snapshot["team_id"]; got != resolved.TeamID {
		t.Fatalf("team_id = %v, want %q", got, resolved.TeamID)
	}
}

func TestListTeamChannelMembersPreservesGraphIdentities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/teams/team-1/channels/channel-1/members" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("$select"); got != "id,displayName" {
			t.Fatalf("unexpected $select: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"user-1","displayName":"Ada Lovelace"},{"id":"user-2","displayName":"Grace Hopper"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	members, err := ListTeamChannelMembers(context.Background(), "access-token", "team-1", "channel-1")
	if err != nil {
		t.Fatalf("ListTeamChannelMembers() error = %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members length = %d, want 2", len(members))
	}
	if members[0].ID != "user-1" || members[0].DisplayName != "Ada Lovelace" {
		t.Fatalf("members[0] = %#v", members[0])
	}
	if members[1].ID != "user-2" || members[1].DisplayName != "Grace Hopper" {
		t.Fatalf("members[1] = %#v", members[1])
	}
}

func TestListTeamChannelsIncludesMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/teams/team-1/channels":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"id":"channel-1","displayName":"General","description":"General channel"}]}`))
		case "/teams/team-1/channels/channel-1/members":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"id":"user-1","displayName":"Ada Lovelace"},{"id":"user-2","displayName":"Grace Hopper"}]}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	channels, err := ListTeamChannels(context.Background(), "access-token", "team-1")
	if err != nil {
		t.Fatalf("ListTeamChannels() error = %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("channels length = %d, want 1", len(channels))
	}
	if len(channels[0].Members) != 2 {
		t.Fatalf("channel members length = %d, want 2", len(channels[0].Members))
	}
	if channels[0].Members[0].ID != "user-1" || channels[0].Members[0].DisplayName != "Ada Lovelace" {
		t.Fatalf("channel members[0] = %#v", channels[0].Members[0])
	}
}

func TestListTeamChatsAndMessagesPreserveGraphIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chats":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"id":"chat-1","topic":"Design Review","createdDateTime":"2026-01-02T03:04:05Z","lastUpdatedDateTime":"2026-01-03T03:04:05Z"},{"id":"chat-2","topic":"Ops","createdDateTime":"2026-01-04T03:04:05Z","lastUpdatedDateTime":"2026-01-05T03:04:05Z"}]}`))
		case "/chats/chat-1/messages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"id":"msg-1","bodyPreview":"hello","createdDateTime":"2026-01-02T03:05:05Z","lastModifiedDateTime":"2026-01-02T03:05:06Z","from":{"user":{"displayName":"Ada Lovelace","id":"user-1"}}}]}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	chats, err := ListTeamChats(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("ListTeamChats() error = %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("chat count = %d, want 2", len(chats))
	}
	if chats[0].ID != "chat-1" || chats[0].Topic != "Design Review" {
		t.Fatalf("chat[0] = %#v", chats[0])
	}

	messages, err := ListChatMessages(context.Background(), "access-token", "chat-1")
	if err != nil {
		t.Fatalf("ListChatMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	if messages[0].ID != "msg-1" || messages[0].From != "Ada Lovelace" || messages[0].FromID != "user-1" {
		t.Fatalf("message[0] = %#v", messages[0])
	}
}

func TestTeamsChatSnapshotsPersistMetadata(t *testing.T) {
	chats := []TeamChatSummary{{
		ID:                  "chat-1",
		Topic:               "Design Review",
		CreatedDateTime:     "2026-01-02T03:04:05Z",
		LastUpdatedDateTime: "2026-01-03T03:04:05Z",
	}}
	messages := []TeamChatMessage{{
		ID:                   "msg-1",
		BodyPreview:          "hello",
		CreatedDateTime:      "2026-01-02T03:05:05Z",
		LastModifiedDateTime: "2026-01-02T03:05:06Z",
		From:                 "Ada Lovelace",
		FromID:               "user-1",
	}}

	chatJSON, err := TeamsChatsSnapshotJSON(chats)
	if err != nil {
		t.Fatalf("TeamsChatsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(chatJSON) {
		t.Fatal("TeamsChatsSnapshotJSON returned invalid JSON")
	}
	msgJSON, err := TeamsChatMessagesSnapshotJSON("chat-1", messages)
	if err != nil {
		t.Fatalf("TeamsChatMessagesSnapshotJSON() error = %v", err)
	}
	if !json.Valid(msgJSON) {
		t.Fatal("TeamsChatMessagesSnapshotJSON returned invalid JSON")
	}
	if !containsSubstring(string(chatJSON), "chat-1") || !containsSubstring(string(msgJSON), "msg-1") {
		t.Fatalf("snapshot missing chat data: chats=%s msgs=%s", string(chatJSON), string(msgJSON))
	}
}

func TestParseTeamsChatSnapshotsAndResolveGraphID(t *testing.T) {
	chatSnapJSON := []byte(`{"chats":[{"id":"chat-1","topic":"Design Review"}]}`)
	chatSnap, err := ParseTeamsChatsSnapshot(chatSnapJSON)
	if err != nil {
		t.Fatalf("ParseTeamsChatsSnapshot() error = %v", err)
	}
	if len(chatSnap.Chats) != 1 || chatSnap.Chats[0].ID != "chat-1" {
		t.Fatalf("chatSnap = %#v", chatSnap)
	}

	msgSnapJSON := []byte(`{"chat_id":"chat-1","messages":[{"id":"msg-1","from":"Ada Lovelace","from_id":"user-1"}]}`)
	msgSnap, err := ParseTeamsChatMessagesSnapshot(msgSnapJSON)
	if err != nil {
		t.Fatalf("ParseTeamsChatMessagesSnapshot() error = %v", err)
	}
	if msgSnap.ChatID != "chat-1" || len(msgSnap.Messages) != 1 || msgSnap.Messages[0].ID != "msg-1" {
		t.Fatalf("msgSnap = %#v", msgSnap)
	}

	chatID, err := ResolveTeamsChatGraphID("teamKey/chats/chat-1/_messages.json", chatSnap, "")
	if err != nil {
		t.Fatalf("ResolveTeamsChatGraphID() error = %v", err)
	}
	if chatID != "chat-1" {
		t.Fatalf("ResolveTeamsChatGraphID = %q, want %q", chatID, "chat-1")
	}
}

func TestTeamsReplyAndAttachmentMetadataSnapshots(t *testing.T) {
	replies := []TeamsDeltaMessage{{ID: "reply-1", Subject: "Re: hello", From: "Ada Lovelace"}}
	replyJSON, err := TeamsReplySnapshotJSON("msg-1", replies)
	if err != nil {
		t.Fatalf("TeamsReplySnapshotJSON() error = %v", err)
	}
	if !json.Valid(replyJSON) || !containsSubstring(string(replyJSON), "reply-1") {
		t.Fatalf("invalid reply snapshot: %s", string(replyJSON))
	}

	attachments := []TeamsAttachmentReference{{MessageID: "msg-1", ContentID: "hosted-42", AttachmentType: TeamsAttachmentTypeHostedContent, StorageKey: "teamKey/channels/chan-1/hosted/msg-1/hosted-42"}}
	attachJSON, err := TeamsAttachmentSnapshotJSON("msg-1", attachments)
	if err != nil {
		t.Fatalf("TeamsAttachmentSnapshotJSON() error = %v", err)
	}
	if !json.Valid(attachJSON) || !containsSubstring(string(attachJSON), TeamsAttachmentTypeHostedContent) {
		t.Fatalf("invalid attachment snapshot: %s", string(attachJSON))
	}
}

func TestTeamsMeetingMetadataSnapshots(t *testing.T) {
	meetings := []TeamsMeetingSummary{{
		ID:             "meet-1",
		Subject:        "Sprint sync",
		JoinURL:        "https://teams.microsoft.com/l/meetup-join/abc",
		StartDateTime:  "2026-01-02T09:00:00Z",
		EndDateTime:    "2026-01-02T09:30:00Z",
		Organizer:      "Ada Lovelace",
		OrganizerEmail: "ada@example.com",
	}}
	meetingJSON, err := TeamsMeetingSnapshotJSON(meetings)
	if err != nil {
		t.Fatalf("TeamsMeetingSnapshotJSON() error = %v", err)
	}
	if !json.Valid(meetingJSON) || !containsSubstring(string(meetingJSON), "meet-1") {
		t.Fatalf("invalid meeting snapshot: %s", string(meetingJSON))
	}
	parsed, err := ParseTeamsMeetingSnapshot(meetingJSON)
	if err != nil {
		t.Fatalf("ParseTeamsMeetingSnapshot() error = %v", err)
	}
	if len(parsed.Meetings) != 1 || parsed.Meetings[0].ID != "meet-1" {
		t.Fatalf("parsed meeting snapshot = %#v", parsed)
	}
}

func TestListTeamsOnlineMeetingsPreservesMeetingData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/onlineMeetings" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"meet-1","subject":"Sprint sync","joinWebUrl":"https://teams.microsoft.com/l/meetup-join/abc","startDateTime":"2026-01-02T09:00:00Z","endDateTime":"2026-01-02T09:30:00Z","organizer":{"user":{"displayName":"Ada Lovelace","mail":"ada@example.com"}}}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	meetings, err := ListTeamsOnlineMeetings(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("ListTeamsOnlineMeetings() error = %v", err)
	}
	if len(meetings) != 1 || meetings[0].ID != "meet-1" || meetings[0].JoinURL == "" {
		t.Fatalf("meetings = %#v", meetings)
	}
}

func TestTeamsMeetingParticipantsSnapshot(t *testing.T) {
	participants := []TeamsMeetingParticipantSummary{{
		ID:          "user-1",
		DisplayName: "Ada Lovelace",
		Role:        "organizer",
		UPN:         "ada@example.com",
	}}
	jsonBody, err := TeamsMeetingParticipantsSnapshotJSON("meet-1", participants)
	if err != nil {
		t.Fatalf("TeamsMeetingParticipantsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "user-1") {
		t.Fatalf("invalid participant snapshot: %s", string(jsonBody))
	}

	parsed, err := ParseTeamsMeetingParticipantsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsMeetingParticipantsSnapshot() error = %v", err)
	}
	if parsed.MeetingID != "meet-1" || len(parsed.Participants) != 1 || parsed.Participants[0].ID != "user-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsMeetingParticipantsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/onlineMeetings/meet-1/participants" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"user-1","displayName":"Ada Lovelace","roles":["organizer"],"upn":"ada@example.com"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	participants, err := ListTeamsMeetingParticipants(context.Background(), "access-token", "meet-1")
	if err != nil {
		t.Fatalf("ListTeamsMeetingParticipants() error = %v", err)
	}
	if len(participants) != 1 || participants[0].ID != "user-1" || participants[0].Role != "organizer" {
		t.Fatalf("participants = %#v", participants)
	}
}

func TestListTeamsMeetingParticipantsHandlesPaginationAndMultiRole(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		switch callCount {
		case 1:
			_, _ = w.Write([]byte(`{"@odata.nextLink":"/me/onlineMeetings/meet-1/participants?page=2","value":[{"id":"user-1","displayName":"Ada Lovelace","roles":["organizer","presenter"],"upn":"ada@example.com"}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"value":[{"id":"user-2","displayName":"Grace Hopper","roles":["attendee"],"upn":"grace@example.com"}]}`))
		default:
			t.Fatalf("unexpected request count: %d", callCount)
		}
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	participants, err := ListTeamsMeetingParticipants(context.Background(), "access-token", "meet-1")
	if err != nil {
		t.Fatalf("ListTeamsMeetingParticipants() error = %v", err)
	}
	if len(participants) != 2 {
		t.Fatalf("expected 2 participants, got %#v", participants)
	}
	if participants[0].Role != "organizer, presenter" {
		t.Fatalf("first participant role = %q", participants[0].Role)
	}
	if participants[1].ID != "user-2" || participants[1].Role != "attendee" {
		t.Fatalf("second participant = %#v", participants[1])
	}
}

func TestTeamsMeetingAttendanceReportsSnapshot(t *testing.T) {
	reports := []TeamsMeetingAttendanceSummary{{
		ID:                "attendance-1",
		UserDisplayName:   "Ada Lovelace",
		UserEmail:         "ada@example.com",
		AttendanceSeconds: 1800,
		JoinDateTime:      "2026-01-02T09:00:00Z",
		LeaveDateTime:     "2026-01-02T09:30:00Z",
	}}
	jsonBody, err := TeamsMeetingAttendanceSnapshotJSON("meet-1", reports)
	if err != nil {
		t.Fatalf("TeamsMeetingAttendanceSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "attendance-1") {
		t.Fatalf("invalid report snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsMeetingAttendanceSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsMeetingAttendanceSnapshot() error = %v", err)
	}
	if parsed.MeetingID != "meet-1" || len(parsed.Reports) != 1 || parsed.Reports[0].ID != "attendance-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsMeetingAttendanceReportsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/onlineMeetings/meet-1/attendanceReports" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"attendance-1","attendanceDurationInSeconds":1800,"joinDateTime":"2026-01-02T09:00:00Z","leaveDateTime":"2026-01-02T09:30:00Z","user":{"displayName":"Ada Lovelace","mail":"ada@example.com"}}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	reports, err := ListTeamsMeetingAttendanceReports(context.Background(), "access-token", "meet-1")
	if err != nil {
		t.Fatalf("ListTeamsMeetingAttendanceReports() error = %v", err)
	}
	if len(reports) != 1 || reports[0].ID != "attendance-1" || reports[0].AttendanceSeconds != 1800 {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestTeamsMeetingTranscriptSnapshot(t *testing.T) {
	transcripts := []TeamsMeetingTranscriptSummary{{
		ID:              "transcript-1",
		Type:            "transcript",
		ContentURL:      "https://example.com/transcript.vtt",
		Locale:          "en-US",
		Status:          "ready",
		CreatedDateTime: "2026-01-02T09:00:00Z",
	}}
	jsonBody, err := TeamsMeetingTranscriptSnapshotJSON("meet-1", transcripts)
	if err != nil {
		t.Fatalf("TeamsMeetingTranscriptSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "transcript-1") {
		t.Fatalf("invalid transcript snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsMeetingTranscriptSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsMeetingTranscriptSnapshot() error = %v", err)
	}
	if parsed.MeetingID != "meet-1" || len(parsed.Transcripts) != 1 || parsed.Transcripts[0].ID != "transcript-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsMeetingTranscriptsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/onlineMeetings/meet-1/transcripts" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"transcript-1","createdDateTime":"2026-01-02T09:00:00Z","type":"transcript","contentUrl":"https://example.com/transcript.vtt","locale":"en-US","status":"ready"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	transcripts, err := ListTeamsMeetingTranscripts(context.Background(), "access-token", "meet-1")
	if err != nil {
		t.Fatalf("ListTeamsMeetingTranscripts() error = %v", err)
	}
	if len(transcripts) != 1 || transcripts[0].ID != "transcript-1" || transcripts[0].Status != "ready" {
		t.Fatalf("transcripts = %#v", transcripts)
	}
}

func TestTeamsMeetingRecordingSnapshot(t *testing.T) {
	recordings := []TeamsMeetingRecordingSummary{{
		ID:              "recording-1",
		Type:            "video",
		ContentURL:      "https://example.com/recording.mp4",
		Locale:          "en-US",
		Status:          "ready",
		CreatedDateTime: "2026-01-02T09:00:00Z",
	}}
	jsonBody, err := TeamsMeetingRecordingSnapshotJSON("meet-1", recordings)
	if err != nil {
		t.Fatalf("TeamsMeetingRecordingSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "recording-1") {
		t.Fatalf("invalid recording snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsMeetingRecordingSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsMeetingRecordingSnapshot() error = %v", err)
	}
	if parsed.MeetingID != "meet-1" || len(parsed.Recordings) != 1 || parsed.Recordings[0].ID != "recording-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsMeetingRecordingsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/onlineMeetings/meet-1/recordings" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"recording-1","createdDateTime":"2026-01-02T09:00:00Z","type":"video","contentUrl":"https://example.com/recording.mp4","locale":"en-US","status":"ready"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	recordings, err := ListTeamsMeetingRecordings(context.Background(), "access-token", "meet-1")
	if err != nil {
		t.Fatalf("ListTeamsMeetingRecordings() error = %v", err)
	}
	if len(recordings) != 1 || recordings[0].ID != "recording-1" || recordings[0].Status != "ready" {
		t.Fatalf("recordings = %#v", recordings)
	}
}

func TestTeamsMeetingChatMessagesSnapshot(t *testing.T) {
	messages := []TeamsMeetingChatMessageSummary{{
		ID:                   "chat-msg-1",
		BodyPreview:          "hello from the meeting",
		CreatedDateTime:      "2026-01-02T09:00:00Z",
		LastModifiedDateTime: "2026-01-02T09:00:01Z",
		From:                 "Ada Lovelace",
		FromID:               "user-1",
	}}
	jsonBody, err := TeamsMeetingChatMessagesSnapshotJSON("meet-1", messages)
	if err != nil {
		t.Fatalf("TeamsMeetingChatMessagesSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "chat-msg-1") {
		t.Fatalf("invalid meeting chat snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsMeetingChatMessagesSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsMeetingChatMessagesSnapshot() error = %v", err)
	}
	if parsed.MeetingID != "meet-1" || len(parsed.Messages) != 1 || parsed.Messages[0].ID != "chat-msg-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsMeetingChatMessagesPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/onlineMeetings/meet-1/chatMessages" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"chat-msg-1","createdDateTime":"2026-01-02T09:00:00Z","lastModifiedDateTime":"2026-01-02T09:00:01Z","body":{"content":"hello from the meeting"},"from":{"user":{"displayName":"Ada Lovelace","id":"user-1"}}}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	messages, err := ListTeamsMeetingChatMessages(context.Background(), "access-token", "meet-1")
	if err != nil {
		t.Fatalf("ListTeamsMeetingChatMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "chat-msg-1" || messages[0].BodyPreview != "hello from the meeting" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestTeamsChatMessageReactionsSnapshot(t *testing.T) {
	reactions := []TeamsChatMessageReactionSummary{{
		Type:            "heart",
		CreatedDateTime: "2026-01-02T09:00:00Z",
		UserDisplayName: "Ada Lovelace",
		UserID:          "user-1",
	}}
	jsonBody, err := TeamsChatMessageReactionsSnapshotJSON("chat-1", "msg-1", reactions)
	if err != nil {
		t.Fatalf("TeamsChatMessageReactionsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "heart") {
		t.Fatalf("invalid reaction snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsChatMessageReactionsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsChatMessageReactionsSnapshot() error = %v", err)
	}
	if parsed.ChatID != "chat-1" || parsed.MessageID != "msg-1" || len(parsed.Reactions) != 1 || parsed.Reactions[0].Type != "heart" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsChatMessageReactionsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chats/chat-1/messages/msg-1/reactions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"reactionType":"heart","createdDateTime":"2026-01-02T09:00:00Z","user":{"displayName":"Ada Lovelace","id":"user-1"}}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	reactions, err := ListTeamsChatMessageReactions(context.Background(), "access-token", "chat-1", "msg-1")
	if err != nil {
		t.Fatalf("ListTeamsChatMessageReactions() error = %v", err)
	}
	if len(reactions) != 1 || reactions[0].Type != "heart" || reactions[0].UserID != "user-1" {
		t.Fatalf("reactions = %#v", reactions)
	}
}

func TestTeamsMessageReactionsSnapshot(t *testing.T) {
	reactions := []TeamsMessageReactionSummary{{
		Type:            "heart",
		CreatedDateTime: "2026-01-02T09:00:00Z",
		UserDisplayName: "Ada Lovelace",
		UserID:          "user-1",
	}}
	jsonBody, err := TeamsMessageReactionsSnapshotJSON("team-1", "channel-1", "msg-1", reactions)
	if err != nil {
		t.Fatalf("TeamsMessageReactionsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "heart") {
		t.Fatalf("invalid message reaction snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsMessageReactionsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsMessageReactionsSnapshot() error = %v", err)
	}
	if parsed.TeamID != "team-1" || parsed.ChannelID != "channel-1" || parsed.MessageID != "msg-1" || len(parsed.Reactions) != 1 || parsed.Reactions[0].Type != "heart" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsMessageReactionsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/teams/team-1/channels/channel-1/messages/msg-1/reactions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"reactionType":"heart","createdDateTime":"2026-01-02T09:00:00Z","user":{"displayName":"Ada Lovelace","id":"user-1"}}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	reactions, err := ListTeamsMessageReactions(context.Background(), "access-token", "team-1", "channel-1", "msg-1")
	if err != nil {
		t.Fatalf("ListTeamsMessageReactions() error = %v", err)
	}
	if len(reactions) != 1 || reactions[0].Type != "heart" || reactions[0].UserID != "user-1" {
		t.Fatalf("reactions = %#v", reactions)
	}
}

func TestTeamsCallRecordsSnapshot(t *testing.T) {
	calls := []TeamsCallRecordSummary{{
		ID:             "call-1",
		StartDateTime:  "2026-01-02T09:00:00Z",
		EndDateTime:    "2026-01-02T09:30:00Z",
		Organizer:      "Ada Lovelace",
		OrganizerEmail: "ada@example.com",
		Modalities:     "audio, video",
	}}
	jsonBody, err := TeamsCallRecordsSnapshotJSON(calls)
	if err != nil {
		t.Fatalf("TeamsCallRecordsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "call-1") {
		t.Fatalf("invalid call records snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsCallRecordsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsCallRecordsSnapshot() error = %v", err)
	}
	if len(parsed.Calls) != 1 || parsed.Calls[0].ID != "call-1" || parsed.Calls[0].Modalities != "audio, video" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsCallRecordsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/communications/callRecords" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"call-1","startDateTime":"2026-01-02T09:00:00Z","endDateTime":"2026-01-02T09:30:00Z","modalities":["audio","video"],"organizer":{"user":{"displayName":"Ada Lovelace","mail":"ada@example.com"}}}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	calls, err := ListTeamsCallRecords(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("ListTeamsCallRecords() error = %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Organizer != "Ada Lovelace" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestTeamsCallRecordSessionsSnapshot(t *testing.T) {
	sessions := []TeamsCallRecordSessionSummary{{
		ID:            "session-1",
		CallRecordID:  "call-1",
		StartDateTime: "2026-01-02T09:00:00Z",
		EndDateTime:   "2026-01-02T09:15:00Z",
		Modalities:    "audio",
	}}
	jsonBody, err := TeamsCallRecordSessionsSnapshotJSON("call-1", sessions)
	if err != nil {
		t.Fatalf("TeamsCallRecordSessionsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "session-1") {
		t.Fatalf("invalid call-record sessions snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsCallRecordSessionsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsCallRecordSessionsSnapshot() error = %v", err)
	}
	if parsed.CallRecordID != "call-1" || len(parsed.Sessions) != 1 || parsed.Sessions[0].ID != "session-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsCallRecordSessionsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/communications/callRecords/call-1/sessions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"session-1","startDateTime":"2026-01-02T09:00:00Z","endDateTime":"2026-01-02T09:15:00Z","modalities":["audio"]}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	sessions, err := ListTeamsCallRecordSessions(context.Background(), "access-token", "call-1")
	if err != nil {
		t.Fatalf("ListTeamsCallRecordSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-1" || sessions[0].Modalities != "audio" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestTeamsCallRecordSessionParticipantsSnapshot(t *testing.T) {
	participants := []TeamsCallRecordSessionParticipantSummary{{
		ID:                "p-1",
		SessionID:         "session-1",
		CallRecordID:      "call-1",
		DisplayName:       "Ada Lovelace",
		UserPrincipalName: "ada@example.com",
		Role:              "Presenter",
	}}
	jsonBody, err := TeamsCallRecordSessionParticipantsSnapshotJSON("call-1", "session-1", participants)
	if err != nil {
		t.Fatalf("TeamsCallRecordSessionParticipantsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "Presenter") {
		t.Fatalf("invalid session participants snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsCallRecordSessionParticipantsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsCallRecordSessionParticipantsSnapshot() error = %v", err)
	}
	if parsed.CallRecordID != "call-1" || parsed.SessionID != "session-1" || len(parsed.Participants) != 1 || parsed.Participants[0].ID != "p-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsCallRecordSessionParticipantsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/communications/callRecords/call-1/sessions/session-1/participants" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"p-1","displayName":"Ada Lovelace","roles":["Presenter"],"userPrincipalName":"ada@example.com"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	participants, err := ListTeamsCallRecordSessionParticipants(context.Background(), "access-token", "call-1", "session-1")
	if err != nil {
		t.Fatalf("ListTeamsCallRecordSessionParticipants() error = %v", err)
	}
	if len(participants) != 1 || participants[0].ID != "p-1" || participants[0].Role != "Presenter" {
		t.Fatalf("participants = %#v", participants)
	}
}

func TestTeamsCallRecordSessionSegmentsSnapshot(t *testing.T) {
	segments := []TeamsCallRecordSessionSegmentSummary{{
		ID:            "segment-1",
		SessionID:     "session-1",
		CallRecordID:  "call-1",
		StartDateTime: "2026-01-02T09:00:00Z",
		EndDateTime:   "2026-01-02T09:05:00Z",
		Modality:      "audio",
	}}
	jsonBody, err := TeamsCallRecordSessionSegmentsSnapshotJSON("call-1", "session-1", segments)
	if err != nil {
		t.Fatalf("TeamsCallRecordSessionSegmentsSnapshotJSON() error = %v", err)
	}
	if !json.Valid(jsonBody) || !containsSubstring(string(jsonBody), "segment-1") {
		t.Fatalf("invalid session segments snapshot: %s", string(jsonBody))
	}
	parsed, err := ParseTeamsCallRecordSessionSegmentsSnapshot(jsonBody)
	if err != nil {
		t.Fatalf("ParseTeamsCallRecordSessionSegmentsSnapshot() error = %v", err)
	}
	if parsed.CallRecordID != "call-1" || parsed.SessionID != "session-1" || len(parsed.Segments) != 1 || parsed.Segments[0].ID != "segment-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestListTeamsCallRecordSessionSegmentsPreservesGraphData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/communications/callRecords/call-1/sessions/session-1/segments" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"segment-1","startDateTime":"2026-01-02T09:00:00Z","endDateTime":"2026-01-02T09:05:00Z","modality":"audio"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	segments, err := ListTeamsCallRecordSessionSegments(context.Background(), "access-token", "call-1", "session-1")
	if err != nil {
		t.Fatalf("ListTeamsCallRecordSessionSegments() error = %v", err)
	}
	if len(segments) != 1 || segments[0].ID != "segment-1" || segments[0].Modality != "audio" {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestTeamsChannelMessagesInitialURL(t *testing.T) {
	url := TeamsChannelMessagesInitialURL("team-1", "channel-1")
	if url == "" || !containsAll(url, "team-1", "channel-1", "messages") {
		t.Fatalf("unexpected url: %s", url)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if p == "" {
			continue
		}
		if !containsSubstring(s, p) {
			return false
		}
	}
	return true
}

func containsSubstring(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ConsistencyLevel: eventual is sent only when the caller asks for it (hosted content of an
// edited or deleted message), never on other hosted fetches.
func TestFetchTeamsBytesSendsConsistencyLevelOnlyWhenAsked(t *testing.T) {
	var levels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chats/chat-1/messages/m1/hostedContents/c1/$value" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		levels = append(levels, r.Header.Get("ConsistencyLevel"))
		_, _ = w.Write([]byte("PNG"))
	}))
	defer server.Close()
	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	u := TeamsHostedValueURL(TeamsChatHostedListURL("chat-1", "m1"), "c1")
	for _, eventual := range []bool{true, false} {
		b, err := FetchTeamsBytes(context.Background(), "tok", u, eventual)
		if err != nil || string(b) != "PNG" {
			t.Fatalf("eventual=%v: %q %v", eventual, b, err)
		}
	}
	if len(levels) != 2 || levels[0] != "eventual" || levels[1] != "" {
		t.Fatalf("ConsistencyLevel headers = %q", levels)
	}
}

func TestParseTeamsHostedRef(t *testing.T) {
	const g = "https://graph.microsoft.com/v1.0"
	chat := "19:bcf84b15c2994a909770f7d05bc4fe16@thread.v2"
	ok := map[string]TeamsHostedRef{
		g + "/chats/" + chat + "/messages/1706763669648/hostedContents/aWQ9eA==/$value":    {ChatID: chat, MessageID: "1706763669648", ContentID: "aWQ9eA=="},
		g + "/users/u-1/chats/Chat-1/messages/M1/hostedContents/C1/$value":                 {ChatID: "Chat-1", MessageID: "M1", ContentID: "C1"},
		g + "/teams/team-1/channels/ch-a/messages/100/hostedContents/img/$value":           {TeamID: "team-1", ChannelID: "ch-a", RootID: "100", MessageID: "100", ContentID: "img"},
		g + "/teams/team-1/channels/ch-a/messages/100/replies/101/hostedContents/r/$value": {TeamID: "team-1", ChannelID: "ch-a", RootID: "100", MessageID: "101", ContentID: "r"},
		g + "/chats('chat-1')/messages('owner')/hostedContents('emo')/$value":              {ChatID: "chat-1", MessageID: "owner", ContentID: "emo"},
		"HTTPS://Graph.Microsoft.com/V1.0/Chats/chat-1/Messages/m/HostedContents/c/$value": {ChatID: "chat-1", MessageID: "m", ContentID: "c"},
	}
	for link, want := range ok {
		got, parsed := ParseTeamsHostedRef(link)
		if !parsed || got != want {
			t.Errorf("%s: got %+v %v, want %+v", link, got, parsed, want)
		}
	}
	for _, link := range []string{
		"",
		"hostedContents/c/$value",
		"../hostedContents/c/$value",
		"/chats/chat-1/messages/m/hostedContents/c/$value",
		"https://evil.example/v1.0/chats/chat-1/messages/m/hostedContents/c/$value",
		"http://graph.microsoft.com/v1.0/chats/chat-1/messages/m/hostedContents/c/$value",
		"https://user@graph.microsoft.com/v1.0/chats/chat-1/messages/m/hostedContents/c/$value",
		g[:len(g)-4] + "beta/chats/chat-1/messages/m/hostedContents/c/$value",
		g + "/chats/chat-1/messages/m/hostedContents/c/$value?x=1",
		g + "/chats/chat-1/messages/m/hostedContents/c/$value#f",
		g + "/chats/chat-1/messages/m/hostedContents/c",
		g + "/chats/chat-1/messages/m/../../chat-2/messages/x/hostedContents/c/$value",
		g + "/chats/chat-1/messages/a%2Fb/hostedContents/c/$value",
		g + "/chats/chat-1/hostedContents/c/$value",
		g + "/me/chats/chat-1/messages/m/hostedContents/c/$value",
		g + "/drives/d/items/i/hostedContents/c/$value",
		g + "/teams/t/channels/c/messages/m/replies/r/extra/hostedContents/c/$value",
	} {
		if ref, parsed := ParseTeamsHostedRef(link); parsed {
			t.Errorf("%q must be rejected, got %+v", link, ref)
		}
	}
	ref := TeamsHostedRef{TeamID: "team-1", ChannelID: "ch-a", RootID: "100", MessageID: "101", ContentID: "r"}
	if got := ref.ValueURL(); got != g+"/teams/team-1/channels/ch-a/messages/100/replies/101/hostedContents/r/$value" {
		t.Fatalf("reply ValueURL = %s", got)
	}
	ref = TeamsHostedRef{ChatID: "chat-1", MessageID: "owner", ContentID: "emo"}
	if got := ref.ValueURL(); got != g+"/chats/chat-1/messages/owner/hostedContents/emo/$value" {
		t.Fatalf("chat ValueURL = %s", got)
	}
}
