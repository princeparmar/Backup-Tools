package outlook

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Teams object layout in the outlook-teams bucket. Keys carry ids and dates only: names,
// subjects and previews live inside the objects and in _team.json / _chat.json / _meeting.json,
// so no message text reaches synced_objects and edits or renames never move a key.
//
//	{tenant}/team/{teamId}/_team.json, CONFIG/{part}.json
//	{tenant}/team/{teamId}/CHANNELS/{channelId}/_channel.json, CONFIG/{part}.json
//	{tenant}/team/{teamId}/CHANNELS/{channelId}/{yyyy}/{mm}/{dd}/{rootId} - {messageId}.teams
//	{tenant}/team/{teamId}/CHANNELS/{channelId}/hosted/{messageId}/{contentId}
//	{tenant}/team/{teamId}/CHANNELS/{channelId}/history/{messageId}/{versionId}.json
//	{tenant}/chat/{chatId}/_chat.json, {yyyy}/{mm}/{dd}/{messageId}.teams, hosted/{messageId}/{contentId}
//	{tenant}/chat/{chatId}/history/{messageId}/{versionId}.json
//	{tenant}/user/{userId}/CHATS/.chat__{chatId}
//	{tenant}/user/{userId}/MEETINGS/{yyyy}/{mm}/{dd}/.meeting__{mid}, MEETINGS/_coverage.json
//	{tenant}/user/{userId}/CALLS/{yyyy}/{mm}/{dd}/.call__{callRecordId}
//	{tenant}/meeting/{mid}/_meeting.json, event.json, online_meeting.json, attendance/{reportId}.json, qa/{conversationId}/{messageId}.json
//	{tenant}/calls/{yyyy-mm}/{dd}/{callRecordId}.json
//	{tenant}/pstn/{yyyy-mm}/{dd}/{rowKey}.json, {tenant}/direct-routing/{yyyy-mm}/{dd}/{rowKey}.json
//	{tenant}/tenant/{tenantId}/_status.json, _files_coverage.json, _reconcile/, _state/, _failed/
const (
	TeamsKeyTeam          = "team"
	TeamsKeyUser          = "user"
	TeamsKeyTenant        = "tenant"
	TeamsKeyChat          = "chat"
	TeamsKeyMeeting       = "meeting"
	TeamsKeyCalls         = "calls"
	TeamsKeyPstn          = "pstn"
	TeamsKeyDirectRouting = "direct-routing"

	TeamsSectionChannels = "CHANNELS"
	TeamsSectionConfig   = "CONFIG"
	TeamsSectionChats    = "CHATS"
	TeamsSectionMeetings = "MEETINGS"
	TeamsSectionCalls    = "CALLS"

	TeamsMessageExt = ".teams"

	teamsLeafSep          = " - "
	teamsHostedDir        = "hosted"
	teamsHistoryDir       = "history"
	teamsChatPointer      = ".chat__"
	teamsMeetingPointer   = ".meeting__"
	teamsCallPointer      = ".call__"
	teamsUnparsedDir      = "_unparsed"
	teamsUndatedPath      = "0000/00/00"
	teamsUndatedMonth     = "0000-00"
	teamsUndatedDay       = "00"
	teamsMaxObjectKeySize = 1000
)

// Custom metadata on Teams objects.
const (
	TeamsMetaMessageID = "teams-message-id"
	TeamsMetaReplyToID = "teams-reply-to-id"
	TeamsMetaChangeKey = "change-key"
	TeamsMetaETag      = "etag"
	TeamsMetaModified  = "last-modified-date-time"
	TeamsMetaRemovedAt = "removed-at"
	TeamsMetaPhysical  = "physical-prefix"
	TeamsMetaRole      = "role"
	TeamsMetaResponse  = "response"
	TeamsMetaLeftAt    = "left-at"
	TeamsMetaComplete  = "call-complete"
	TeamsMetaReactions = "reactions"
	// TeamsMetaHostedMissing is the number of a message's hosted contents not stored yet.
	TeamsMetaHostedMissing = "hosted-missing"
)

// Kinds of Teams object keys.
const (
	TeamsKindTeamMeta       = "team_meta"
	TeamsKindConfig         = "config"
	TeamsKindChannelMeta    = "channel_meta"
	TeamsKindChannelMessage = "channel_message"
	TeamsKindChatMeta       = "chat_meta"
	TeamsKindChatMessage    = "chat_message"
	TeamsKindHosted         = "hosted"
	TeamsKindMessageVersion = "message_version"
	TeamsKindPointer        = "pointer"
	TeamsKindMeetingMeta    = "meeting_meta"
	TeamsKindMeetingEvent   = "meeting_event"
	TeamsKindOnlineMeeting  = "online_meeting"
	TeamsKindAttendance     = "attendance"
	TeamsKindMeetingQA      = "meeting_qa"
	TeamsKindCallRecord     = "call_record"
	TeamsKindPhoneLog       = "phone_log"
	TeamsKindInternal       = "internal"
	// Old per-team layout: {team}/channels/{channelId}/meta|data/{yyyy/mm/dd}/{messageId}.json
	// plus replies/, attachments/, hosted/ index objects and the /me chats and meetings snapshots.
	TeamsKindLegacyMeta  = "legacy_meta"
	TeamsKindLegacyData  = "legacy_data"
	TeamsKindLegacyOther = "legacy_other"
)

// TeamsObjectKey is a parsed Teams object key. Ids are the real Graph ids (unescaped).
type TeamsObjectKey struct {
	Prefix       string
	TenantID     string
	ResourceType string
	ResourceID   string
	Kind         string
	ChannelID    string
	ChatID       string
	RootID       string
	MessageID    string
	ContentID    string
	VersionID    string
	MeetingID    string
	CallID       string
	Section      string
}

func teamsKeyID(id string) string {
	return url.PathEscape(strings.TrimSpace(id))
}

func teamsParseKeyID(seg string) (string, bool) {
	s, err := url.PathUnescape(seg)
	if err != nil || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

// TeamsUTC formats t as UTC RFC 3339 with milliseconds, the only time format Teams uses.
func TeamsUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// ParseTeamsTime parses a Graph timestamp to UTC with millisecond precision.
func ParseTeamsTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Truncate(time.Millisecond), true
		}
	}
	return time.Time{}, false
}

func teamsDatePath(ts string) string {
	if t, ok := ParseTeamsTime(ts); ok {
		return t.Format("2006/01/02")
	}
	return teamsUndatedPath
}

func teamsMonthDay(ts string) (string, string) {
	if t, ok := ParseTeamsTime(ts); ok {
		return t.Format("2006-01"), t.Format("02")
	}
	return teamsUndatedMonth, teamsUndatedDay
}

func isTeamsDatePath(parts []string) bool {
	return len(parts) == 3 && allDigits(parts[0], 4) && allDigits(parts[1], 2) && allDigits(parts[2], 2)
}

// TeamsTeamPrefix is {tenant}/team/{teamId}.
func TeamsTeamPrefix(tenantID, teamID string) string {
	return ResourceKeyPrefix(tenantID, TeamsKeyTeam, teamID)
}

// TeamsChatPrefix is {tenant}/chat/{chatId}.
func TeamsChatPrefix(tenantID, chatID string) string {
	return ResourceKeyPrefix(tenantID, TeamsKeyChat, chatID)
}

// TeamsUserPrefix is {tenant}/user/{userId}; in the Teams bucket it holds pointers only.
func TeamsUserPrefix(tenantID, userID string) string {
	return ResourceKeyPrefix(tenantID, TeamsKeyUser, userID)
}

// TeamsMeetingPrefix is {tenant}/meeting/{mid}.
func TeamsMeetingPrefix(tenantID, mid string) string {
	return ResourceKeyPrefix(tenantID, TeamsKeyMeeting, mid)
}

// TeamsTenantPrefix is the tenant job's own prefix {tenant}/tenant/{tenantId}.
func TeamsTenantPrefix(tenantID string) string {
	return ResourceKeyPrefix(tenantID, TeamsKeyTenant, strings.ToLower(strings.TrimSpace(tenantID)))
}

func TeamsTeamMetaKey(teamPrefix string) string { return teamPrefix + "/_team.json" }

func TeamsTeamConfigKey(teamPrefix, part string) string {
	return teamPrefix + "/" + TeamsSectionConfig + "/" + teamsKeyID(part) + ".json"
}

// TeamsChannelPrefix is the channel's section; the folder is the channel id, never its name.
func TeamsChannelPrefix(teamPrefix, channelID string) string {
	return teamPrefix + "/" + TeamsSectionChannels + "/" + teamsKeyID(channelID)
}

func TeamsChannelMetaKey(teamPrefix, channelID string) string {
	return TeamsChannelPrefix(teamPrefix, channelID) + "/_channel.json"
}

func TeamsChannelConfigKey(teamPrefix, channelID, part string) string {
	return TeamsChannelPrefix(teamPrefix, channelID) + "/" + TeamsSectionConfig + "/" + teamsKeyID(part) + ".json"
}

// TeamsChannelMessageKey is one channel post or reply, dated by its creation (which never
// changes) and grouped by its thread root (rootId = messageId for a root post).
func TeamsChannelMessageKey(teamPrefix, channelID, rootID, messageID, created string) string {
	if strings.TrimSpace(rootID) == "" {
		rootID = messageID
	}
	return TeamsChannelPrefix(teamPrefix, channelID) + "/" + teamsDatePath(created) + "/" +
		teamsKeyID(rootID) + teamsLeafSep + teamsKeyID(messageID) + TeamsMessageExt
}

func TeamsChannelHostedKey(teamPrefix, channelID, messageID, contentID string) string {
	return TeamsChannelPrefix(teamPrefix, channelID) + "/" + teamsHostedDir + "/" + teamsKeyID(messageID) + "/" + teamsKeyID(contentID)
}

func TeamsChatMetaKey(chatPrefix string) string { return chatPrefix + "/_chat.json" }

// TeamsChatMessageKey is one chat message, stored once per chat whoever returned it.
func TeamsChatMessageKey(chatPrefix, messageID, created string) string {
	return chatPrefix + "/" + teamsDatePath(created) + "/" + teamsKeyID(messageID) + TeamsMessageExt
}

func TeamsChatHostedKey(chatPrefix, messageID, contentID string) string {
	return chatPrefix + "/" + teamsHostedDir + "/" + teamsKeyID(messageID) + "/" + teamsKeyID(contentID)
}

// TeamsChannelVersionKey is one retained earlier version of a channel message, kept beside the
// current message object and never replacing it.
func TeamsChannelVersionKey(teamPrefix, channelID, messageID, versionID string) string {
	return TeamsChannelPrefix(teamPrefix, channelID) + "/" + teamsHistoryDir + "/" + teamsKeyID(messageID) + "/" + teamsKeyID(versionID) + ".json"
}

// TeamsChatVersionKey is one retained earlier version of a chat message.
func TeamsChatVersionKey(chatPrefix, messageID, versionID string) string {
	return chatPrefix + "/" + teamsHistoryDir + "/" + teamsKeyID(messageID) + "/" + teamsKeyID(versionID) + ".json"
}

// TeamsMessageVersionID names one retained message version: its etag, else its
// lastModifiedDateTime, else a hash of the object.
func TeamsMessageVersionID(etag, modified string, raw []byte) string {
	if v := strings.TrimSpace(etag); v != "" {
		return v
	}
	if v := strings.TrimSpace(modified); v != "" {
		return v
	}
	sum := sha256.Sum256(raw)
	return "sha256-" + hex.EncodeToString(sum[:16])
}

// TeamsUserChatPointerKey is the zero-byte pointer from a member to a shared chat.
func TeamsUserChatPointerKey(userPrefix, chatID string) string {
	return userPrefix + "/" + TeamsSectionChats + "/" + teamsChatPointer + teamsKeyID(chatID)
}

// TeamsUserMeetingPointerKey is the zero-byte pointer from an organizer or attendee to a meeting,
// dated by the meeting start (a reschedule moves only this pointer).
func TeamsUserMeetingPointerKey(userPrefix, mid, start string) string {
	return userPrefix + "/" + TeamsSectionMeetings + "/" + teamsDatePath(start) + "/" + teamsMeetingPointer + teamsKeyID(mid)
}

func TeamsUserMeetingCoverageKey(userPrefix string) string {
	return userPrefix + "/" + TeamsSectionMeetings + "/_coverage.json"
}

// TeamsUserCallPointerKey is the zero-byte pointer from a participant to a call record or log row.
func TeamsUserCallPointerKey(userPrefix, callID, start string) string {
	return userPrefix + "/" + TeamsSectionCalls + "/" + teamsDatePath(start) + "/" + teamsCallPointer + teamsKeyID(callID)
}

func TeamsMeetingMetaKey(meetingPrefix string) string  { return meetingPrefix + "/_meeting.json" }
func TeamsMeetingEventKey(meetingPrefix string) string { return meetingPrefix + "/event.json" }

// TeamsMeetingOnlineMeetingKey is the organizer's onlineMeeting object of the meeting.
func TeamsMeetingOnlineMeetingKey(meetingPrefix string) string {
	return meetingPrefix + "/online_meeting.json"
}

func TeamsMeetingAttendanceKey(meetingPrefix, reportID string) string {
	return meetingPrefix + "/attendance/" + teamsKeyID(reportID) + ".json"
}

// TeamsMeetingQAKey is one Q&A message (question, answer or system message) of a meeting,
// grouped by its Q&A conversation.
func TeamsMeetingQAKey(meetingPrefix, conversationID, messageID string) string {
	return meetingPrefix + "/qa/" + teamsKeyID(conversationID) + "/" + teamsKeyID(messageID) + ".json"
}

// TeamsCallRecordKey is {tenant}/calls/{yyyy-mm}/{dd}/{callRecordId}.json, dated by call start.
func TeamsCallRecordKey(tenantID, callID, start string) string {
	month, day := teamsMonthDay(start)
	return ResourceKeyPrefix(tenantID, TeamsKeyCalls, month) + "/" + day + "/" + teamsKeyID(callID) + ".json"
}

// TeamsPhoneLogKey is one PSTN (kind pstn) or Direct Routing (kind direct-routing) log row.
func TeamsPhoneLogKey(tenantID, kind, rowKey, start string) string {
	month, day := teamsMonthDay(start)
	return ResourceKeyPrefix(tenantID, kind, month) + "/" + day + "/" + teamsKeyID(rowKey) + ".json"
}

// TeamsPhoneLogUnparsedKey keeps a log row that has no usable id or start, by content hash.
func TeamsPhoneLogUnparsedKey(tenantID, kind string, at time.Time, raw []byte) string {
	sum := sha1.Sum(raw)
	return ResourceKeyPrefix(tenantID, kind, at.UTC().Format("2006-01")) + "/" + teamsUnparsedDir + "/" + hex.EncodeToString(sum[:]) + ".json"
}

func TeamsStatusKey(tenantPrefix string) string        { return tenantPrefix + "/_status.json" }
func TeamsFilesCoverageKey(tenantPrefix string) string { return tenantPrefix + "/_files_coverage.json" }

func TeamsReconcileMeetingKey(tenantPrefix, mid string) string {
	return tenantPrefix + "/_reconcile/meetings/" + teamsKeyID(mid) + ".json"
}

func TeamsUserStateKey(tenantPrefix, userID string) string {
	return tenantPrefix + "/_state/users/" + teamsKeyID(userID) + ".json"
}

func TeamsTeamStateKey(tenantPrefix, teamID string) string {
	return tenantPrefix + "/_state/teams/" + teamsKeyID(teamID) + ".json"
}

func TeamsQAStateKey(tenantPrefix string) string { return tenantPrefix + "/_state/qa.json" }

// TeamsFailedKey is the ids-only fallback key of an item whose normal key could not be written.
func TeamsFailedKey(tenantPrefix, compositeID string) string {
	sum := sha256.Sum256([]byte(compositeID))
	return tenantPrefix + "/_failed/" + hex.EncodeToString(sum[:]) + ".json"
}

// TeamsKeyFits reports whether a key fits the synced_objects key column.
func TeamsKeyFits(key string) bool { return len(key) <= teamsMaxObjectKeySize }

// ParseTeamsObjectKey parses any Teams key, new or old layout.
func ParseTeamsObjectKey(key string) (TeamsObjectKey, bool) {
	prefix, tenantID, rt, rid, rest, ok := SplitResourceKey(strings.TrimSpace(key))
	if !ok || rest == "" {
		return TeamsObjectKey{}, false
	}
	out := TeamsObjectKey{Prefix: prefix, TenantID: tenantID, ResourceType: rt, ResourceID: rid}
	parts := strings.Split(rest, "/")
	leaf := parts[len(parts)-1]
	switch rt {
	case TeamsKeyTeam:
		return parseTeamsTeamKey(out, parts)
	case TeamsKeyChat:
		switch {
		case rest == "_chat.json":
			out.Kind = TeamsKindChatMeta
			return out, true
		case len(parts) == 3 && parts[0] == teamsHostedDir:
			return teamsHosted(out, parts[1], parts[2])
		case len(parts) == 3 && parts[0] == teamsHistoryDir:
			return teamsVersion(out, parts[1], parts[2])
		case len(parts) == 4 && isTeamsDatePath(parts[:3]) && strings.HasSuffix(leaf, TeamsMessageExt):
			id, ok := teamsParseKeyID(strings.TrimSuffix(leaf, TeamsMessageExt))
			if !ok {
				return TeamsObjectKey{}, false
			}
			out.Kind, out.MessageID = TeamsKindChatMessage, id
			return out, true
		}
	case TeamsKeyUser:
		return parseTeamsUserKey(out, parts)
	case TeamsKeyMeeting:
		out.MeetingID = rid
		switch {
		case rest == "_meeting.json":
			out.Kind = TeamsKindMeetingMeta
			return out, true
		case rest == "event.json":
			out.Kind = TeamsKindMeetingEvent
			return out, true
		case rest == "online_meeting.json":
			out.Kind = TeamsKindOnlineMeeting
			return out, true
		case len(parts) == 2 && parts[0] == "attendance" && strings.HasSuffix(leaf, ".json"):
			out.Kind = TeamsKindAttendance
			return out, true
		case len(parts) == 3 && parts[0] == "qa" && strings.HasSuffix(leaf, ".json"):
			conv, ok1 := teamsParseKeyID(parts[1])
			msg, ok2 := teamsParseKeyID(strings.TrimSuffix(leaf, ".json"))
			if !ok1 || !ok2 {
				return TeamsObjectKey{}, false
			}
			out.Kind, out.RootID, out.MessageID = TeamsKindMeetingQA, conv, msg
			return out, true
		}
	case TeamsKeyCalls:
		if len(parts) == 2 && strings.HasSuffix(leaf, ".json") {
			if id, ok := teamsParseKeyID(strings.TrimSuffix(leaf, ".json")); ok {
				out.Kind, out.CallID = TeamsKindCallRecord, id
				return out, true
			}
		}
	case TeamsKeyPstn, TeamsKeyDirectRouting:
		if len(parts) == 2 && strings.HasSuffix(leaf, ".json") {
			if id, ok := teamsParseKeyID(strings.TrimSuffix(leaf, ".json")); ok {
				out.Kind, out.CallID = TeamsKindPhoneLog, id
				return out, true
			}
		}
	case TeamsKeyTenant:
		out.Kind = TeamsKindInternal
		return out, true
	}
	return TeamsObjectKey{}, false
}

func teamsHosted(out TeamsObjectKey, msgSeg, contentSeg string) (TeamsObjectKey, bool) {
	msg, ok1 := teamsParseKeyID(msgSeg)
	content, ok2 := teamsParseKeyID(contentSeg)
	if !ok1 || !ok2 {
		return TeamsObjectKey{}, false
	}
	out.Kind, out.MessageID, out.ContentID = TeamsKindHosted, msg, content
	return out, true
}

func teamsVersion(out TeamsObjectKey, msgSeg, leaf string) (TeamsObjectKey, bool) {
	if !strings.HasSuffix(leaf, ".json") {
		return TeamsObjectKey{}, false
	}
	msg, ok1 := teamsParseKeyID(msgSeg)
	version, ok2 := teamsParseKeyID(strings.TrimSuffix(leaf, ".json"))
	if !ok1 || !ok2 {
		return TeamsObjectKey{}, false
	}
	out.Kind, out.MessageID, out.VersionID = TeamsKindMessageVersion, msg, version
	return out, true
}

func parseTeamsTeamKey(out TeamsObjectKey, parts []string) (TeamsObjectKey, bool) {
	leaf := parts[len(parts)-1]
	switch {
	case len(parts) == 1 && leaf == "_team.json":
		out.Kind = TeamsKindTeamMeta
		return out, true
	case len(parts) == 2 && parts[0] == TeamsSectionConfig:
		out.Kind, out.Section = TeamsKindConfig, TeamsSectionConfig
		return out, true
	case len(parts) >= 3 && parts[0] == TeamsSectionChannels:
		ch, ok := teamsParseKeyID(parts[1])
		if !ok {
			return TeamsObjectKey{}, false
		}
		out.ChannelID, out.Section = ch, TeamsSectionChannels
		inner := parts[2:]
		switch {
		case len(inner) == 1 && inner[0] == "_channel.json":
			out.Kind = TeamsKindChannelMeta
			return out, true
		case len(inner) == 2 && inner[0] == TeamsSectionConfig:
			out.Kind = TeamsKindConfig
			return out, true
		case len(inner) == 3 && inner[0] == teamsHostedDir:
			return teamsHosted(out, inner[1], inner[2])
		case len(inner) == 3 && inner[0] == teamsHistoryDir:
			return teamsVersion(out, inner[1], inner[2])
		case len(inner) == 4 && isTeamsDatePath(inner[:3]) && strings.HasSuffix(leaf, TeamsMessageExt):
			root, msg, found := strings.Cut(strings.TrimSuffix(leaf, TeamsMessageExt), teamsLeafSep)
			if !found {
				return TeamsObjectKey{}, false
			}
			rootID, ok1 := teamsParseKeyID(root)
			msgID, ok2 := teamsParseKeyID(msg)
			if !ok1 || !ok2 {
				return TeamsObjectKey{}, false
			}
			out.Kind, out.RootID, out.MessageID = TeamsKindChannelMessage, rootID, msgID
			return out, true
		}
	case len(parts) >= 2 && parts[0] == "channels":
		out.Kind = TeamsKindLegacyOther
		if ch, ok := teamsParseKeyID(parts[1]); ok && len(parts) >= 3 {
			out.ChannelID = ch
		}
		if len(parts) >= 4 && (parts[2] == "meta" || parts[2] == "data") && strings.HasSuffix(leaf, ".json") {
			if id, ok := teamsParseKeyID(strings.TrimSuffix(leaf, ".json")); ok {
				out.MessageID = id
				out.Kind = TeamsKindLegacyMeta
				if parts[2] == "data" {
					out.Kind = TeamsKindLegacyData
				}
			}
		}
		return out, true
	case parts[0] == "chats" || parts[0] == "meetings" || leaf == ".file_placeholder":
		out.Kind = TeamsKindLegacyOther
		return out, true
	}
	return TeamsObjectKey{}, false
}

func parseTeamsUserKey(out TeamsObjectKey, parts []string) (TeamsObjectKey, bool) {
	leaf := parts[len(parts)-1]
	out.Section = parts[0]
	switch parts[0] {
	case TeamsSectionChats:
		if len(parts) == 2 && strings.HasPrefix(leaf, teamsChatPointer) {
			if id, ok := teamsParseKeyID(strings.TrimPrefix(leaf, teamsChatPointer)); ok {
				out.Kind, out.ChatID = TeamsKindPointer, id
				return out, true
			}
		}
	case TeamsSectionMeetings:
		if len(parts) == 2 && leaf == "_coverage.json" {
			out.Kind = TeamsKindInternal
			return out, true
		}
		if len(parts) == 5 && isTeamsDatePath(parts[1:4]) && strings.HasPrefix(leaf, teamsMeetingPointer) {
			if id, ok := teamsParseKeyID(strings.TrimPrefix(leaf, teamsMeetingPointer)); ok {
				out.Kind, out.MeetingID = TeamsKindPointer, id
				return out, true
			}
		}
	case TeamsSectionCalls:
		if len(parts) == 5 && isTeamsDatePath(parts[1:4]) && strings.HasPrefix(leaf, teamsCallPointer) {
			if id, ok := teamsParseKeyID(strings.TrimPrefix(leaf, teamsCallPointer)); ok {
				out.Kind, out.CallID = TeamsKindPointer, id
				return out, true
			}
		}
	}
	return TeamsObjectKey{}, false
}

// IsTeamsRestoreKey reports whether a key is a restorable message: a channel or chat .teams
// object, or an old channel meta key not yet converted. Pointers, placeholders, _*.json,
// hosted objects, meetings and call logs are never restore items.
func IsTeamsRestoreKey(key string) bool {
	k, ok := ParseTeamsObjectKey(key)
	if !ok {
		return false
	}
	switch k.Kind {
	case TeamsKindChannelMessage, TeamsKindChatMessage, TeamsKindLegacyMeta:
		return true
	}
	return false
}

// TeamsMeetingID is the folder id of a meeting copy. shared = true only for a case the tenant
// checks confirmed mergeable: then every mailbox copy of one iCalUId shares the folder. Otherwise
// each copy gets its own folder. full is the whole sha256 of input, kept in _meeting.json for the
// collision check (TeamsMeetingFullID).
func TeamsMeetingID(iCalUID, mailboxUserID, eventID string, shared bool) (mid, input, full string) {
	iCalUID = strings.TrimSpace(iCalUID)
	kind := "p"
	input = iCalUID + "|" + strings.TrimSpace(mailboxUserID) + "|" + strings.TrimSpace(eventID)
	if shared && iCalUID != "" {
		kind, input = "s", iCalUID
	}
	sum := sha256.Sum256([]byte(input))
	full = hex.EncodeToString(sum[:])
	return kind + full[:32], input, full
}

// TeamsMeetingFullID is the folder id used when the short id's folder holds another identity.
func TeamsMeetingFullID(mid, full string) string {
	if mid == "" {
		return full
	}
	return mid[:1] + full
}

var (
	teamsErrEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	teamsErrURL    = regexp.MustCompile(`(?i)\b(?:https?|wss?)://\S+`)
	teamsErrGUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	teamsErrPhone  = regexp.MustCompile(`\+?\d[\d\s().\-]{6,}\d`)
	teamsErrQuoted = regexp.MustCompile(`"[^"]*"|'[^']*'`)
)

// SanitizeTeamsErrorText makes a Graph error message safe to store and show: e-mail addresses
// (and UPNs), URLs, GUIDs other than keep, phone numbers and quoted strings become placeholders,
// and the result is cut to 200 characters.
func SanitizeTeamsErrorText(s string, keep ...string) string {
	s = teamsErrURL.ReplaceAllString(s, "<url>")
	s = teamsErrEmail.ReplaceAllString(s, "<email>")
	s = teamsErrQuoted.ReplaceAllString(s, "<text>")
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[strings.ToLower(strings.TrimSpace(k))] = true
	}
	s = teamsErrGUID.ReplaceAllStringFunc(s, func(m string) string {
		if keepSet[strings.ToLower(m)] {
			return m
		}
		return "<id>"
	})
	s = teamsErrPhone.ReplaceAllString(s, "<phone>")
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 200 {
		r := []rune(s)
		s = string(r[:200])
	}
	return s
}

// Version decisions for an incoming copy of an item that may already be stored.
const (
	TeamsVersionWrite    = "write"
	TeamsVersionSkip     = "skip"
	TeamsVersionConflict = "version_conflict"
)

// TeamsVersion is the change marker of a stored or incoming item.
type TeamsVersion struct {
	Modified string
	ETag     string
}

// CompareTeamsVersion decides whether incoming replaces stored (nil = nothing stored). A newer
// lastModifiedDateTime wins; an older one never overwrites; equal times with a different etag keep
// the stored copy as a conflict; an incoming copy without a valid time never overwrites one that
// exists; a stored copy without metadata (old layout) is always replaced.
func CompareTeamsVersion(stored *TeamsVersion, incoming TeamsVersion) string {
	if stored == nil {
		return TeamsVersionWrite
	}
	in, inOK := ParseTeamsTime(incoming.Modified)
	if !inOK {
		return TeamsVersionSkip
	}
	st, stOK := ParseTeamsTime(stored.Modified)
	if !stOK {
		return TeamsVersionWrite
	}
	switch {
	case in.After(st):
		return TeamsVersionWrite
	case in.Before(st):
		return TeamsVersionSkip
	}
	a, b := strings.TrimSpace(stored.ETag), strings.TrimSpace(incoming.ETag)
	if a == "" || b == "" || a == b {
		return TeamsVersionSkip
	}
	return TeamsVersionConflict
}

// TeamsFileRef is a file or recording referenced by a Teams item. Teams never stores the bytes;
// BackupStatus says whether the OneDrive or SharePoint backup holds it.
type TeamsFileRef struct {
	Name            string `json:"name,omitempty"`
	DriveID         string `json:"drive_id,omitempty"`
	ItemID          string `json:"item_id,omitempty"`
	WebURL          string `json:"web_url,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	Owner           string `json:"owner,omitempty"`
	BackupStatus    string `json:"backup_status"`
	BackupKey       string `json:"backup_key,omitempty"`
	BackupBucket    string `json:"backup_bucket,omitempty"`
	OwnerJobID      uint   `json:"owner_job_id,omitempty"`
	SourceETag      string `json:"source_etag,omitempty"`
	BackupETag      string `json:"backup_etag,omitempty"`
	FirstSeenAt     string `json:"first_seen_at,omitempty"`
	CheckedAt       string `json:"checked_at,omitempty"`
	VerifiedAt      string `json:"verified_at,omitempty"`
}

// File backup statuses.
const (
	TeamsFileBackedUp          = "backed_up"
	TeamsFileBackedUpInBin     = "backed_up_in_bin"
	TeamsFileNotBackedUp       = "file_not_backed_up"
	TeamsFileOwnerNotBackedUp  = "owner_not_backed_up"
	TeamsFileDriveNotBackedUp  = "drive_not_backed_up"
	TeamsFileKeyUnverified     = "key_unverified"
	TeamsFileUnresolved        = "unresolved"
	TeamsFileBackupUnavailable = "backup_unavailable_resolving"
)

// TeamsBackupBlock is the storx_backup block added to every stored Teams item.
type TeamsBackupBlock struct {
	TenantID      string         `json:"tenant_id"`
	TeamID        string         `json:"team_id,omitempty"`
	ChannelID     string         `json:"channel_id,omitempty"`
	ChatID        string         `json:"chat_id,omitempty"`
	ReplyToID     string         `json:"reply_to_id,omitempty"`
	RemovedAt     string         `json:"removed_at,omitempty"`
	BackedUpAt    string         `json:"backed_up_at,omitempty"`
	Source        string         `json:"source,omitempty"`
	Hosted        []string       `json:"hosted,omitempty"`
	HostedMissing []string       `json:"hosted_missing,omitempty"`
	Files         []TeamsFileRef `json:"files,omitempty"`
	Conflict      string         `json:"version_conflict_at,omitempty"`
	ConvertedFrom string         `json:"converted_from,omitempty"`
	// Completion is set on call records: record, participants, sessions and segments, each
	// complete, incomplete or failed.
	Completion map[string]string `json:"completion,omitempty"`
	// HostedUnresolved lists hosted content links (for example custom emoji) that don't name a
	// message of the same chat or channel: never fetched, never listed as stored.
	HostedUnresolved []TeamsHostedUnresolved `json:"hosted_unresolved,omitempty"`
}

// TeamsHostedUnresolved is one hosted content link the backup could not tie to its message.
type TeamsHostedUnresolved struct {
	Link   string `json:"link"`
	Reason string `json:"reason"`
}

// Reasons of TeamsHostedUnresolved.
const (
	// TeamsHostedNotGraphLink: not a documented hostedContents $value route on the Graph host.
	TeamsHostedNotGraphLink = "not_graph_link"
	// TeamsHostedOtherConversation: owned by a message of another chat or channel.
	TeamsHostedOtherConversation = "other_conversation"
)

// ReadTeamsBackup returns the storx_backup block of a stored Teams object.
func ReadTeamsBackup(raw []byte) (TeamsBackupBlock, bool) {
	var doc struct {
		Block *TeamsBackupBlock `json:"storx_backup"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Block == nil {
		return TeamsBackupBlock{}, false
	}
	return *doc.Block, true
}

// PatchTeamsBackup sets the storx_backup block on a Graph JSON object. An empty BackedUpAt keeps
// the stored one.
func PatchTeamsBackup(raw []byte, block TeamsBackupBlock) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse teams object: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("parse teams object: not an object")
	}
	if block.BackedUpAt == "" {
		if prev, ok := ReadTeamsBackup(raw); ok {
			block.BackedUpAt = prev.BackedUpAt
		}
	}
	b, err := json.Marshal(block)
	if err != nil {
		return nil, err
	}
	doc["storx_backup"] = b
	return json.Marshal(doc)
}
