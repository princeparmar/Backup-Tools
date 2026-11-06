package crons

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// teamsHistoryEvery is how often a team's or user's retained messages are listed again. Each round
// lists them all: Graph doesn't document whether a retained copy's lastModifiedDateTime changes
// when it is copied, so a date filter could miss a version copied after the last round.
const teamsHistoryEvery = 24 * time.Hour

// teamsRetainedState is one getAllRetainedMessages listing (a team's channels or a user's chats).
type teamsRetainedState struct {
	PageLink  string     `json:"page_link,omitempty"`
	Dirty     bool       `json:"dirty,omitempty"`
	Held      int        `json:"held,omitempty"`
	HeldSince *time.Time `json:"held_since,omitempty"`
	ListedAt  *time.Time `json:"listed_at,omitempty"`
	SkipUntil *time.Time `json:"skip_until,omitempty"`
	Status    string     `json:"status,omitempty"`
}

// teamsRetainedScope is what one listing covers: a team's channels (teamPrefix set) or the chats
// of a user.
type teamsRetainedScope struct {
	feature, resourceType, id, url string
	teamPrefix                     string
}

func teamsChannelRetainedScope(teamID, teamPrefix string) teamsRetainedScope {
	return teamsRetainedScope{feature: outlook.TeamsFeatureChannelHistory, resourceType: "team", id: teamID,
		url: outlook.TeamsChannelRetainedMessagesURL(teamID), teamPrefix: teamPrefix}
}

func teamsChatRetainedScope(userID string) teamsRetainedScope {
	return teamsRetainedScope{feature: outlook.TeamsFeatureChatHistory, resourceType: "user", id: userID,
		url: outlook.TeamsUserRetainedChatMessagesURL(userID)}
}

// syncRetained lists retained messages and stores each earlier version under its own history key.
// It runs only with the application role shown by the token, at most once a day per listing, and
// resumes an interrupted or held round first. A refusal that holds for the whole listing
// (permission, licence, policy, bad request, not found) backs it off for a day, a paid-API answer for 30
// days, without a failure entry; other errors keep one stream failure and are retried. A version
// that can't be stored holds the round for replay with one failure entry for the listing.
// Returns "" when nothing was attempted or the listing is backed off.
func (r *teamsRun) syncRetained(rs *teamsRetainedState, scope teamsRetainedScope, deadline time.Time, save func() error) string {
	if r.historyNotConsented == nil {
		r.historyNotConsented = outlook.TeamsHistoryNotConsented(r.token)
	}
	if _, denied := r.historyNotConsented[scope.feature]; denied {
		return ""
	}
	now := r.now()
	if rs.SkipUntil != nil && now.Before(*rs.SkipUntil) {
		return ""
	}
	if rs.PageLink == "" && !rs.Dirty && rs.Held == 0 && rs.ListedAt != nil && now.Sub(*rs.ListedAt) < teamsHistoryEvery {
		return ""
	}
	streamKey := "history:stream:" + scope.resourceType + ":" + scope.id
	windowKey := "history:window:" + scope.resourceType + ":" + scope.id
	window := repo.TeamsFailure{Phase: "history", Kind: teamsFailWindow, ResourceType: scope.resourceType, ResourceID: scope.id}
	outcome, _, err := r.pageThrough(teamsPager{
		pageLink: &rs.PageLink, first: scope.url, dirty: &rs.Dirty, deadline: deadline,
		streamKey: streamKey,
		stream:    repo.TeamsFailure{Phase: "history", Kind: teamsFailStream, ResourceType: scope.resourceType, ResourceID: scope.id},
		save:      save,
		item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
			if err := r.storeRetainedVersion(scope, raw); err != nil {
				return windowKey, window, err, false
			}
			return "", repo.TeamsFailure{}, nil, false
		},
	})
	switch outcome {
	case teamsPageStopped:
		return teamsIncomplete
	case teamsPageFailed:
		code := outlook.ClassifyTeamsError(err)
		r.setFeature(scope.feature, code, code)
		if code == outlook.TeamsStatusPaidAPI || code == outlook.TeamsStatusNotFound || outlook.IsTeamsPermanentStatus(code) {
			backoff := teamsPermanentBackoff
			if code == outlook.TeamsStatusPaidAPI {
				backoff = teamsPaidAPIBackoff
			}
			until := now.Add(backoff)
			rs.SkipUntil, rs.Status, rs.PageLink = &until, code, ""
			r.clearFailure(streamKey)
			return ""
		}
		return teamsIncomplete
	}
	closed, gaps := r.endRound(teamsRound{&rs.Dirty, &rs.Held, &rs.HeldSince, streamKey}, func() []string { return []string{windowKey} })
	if !closed {
		return teamsIncomplete
	}
	r.clearFailure(windowKey)
	rs.ListedAt, rs.SkipUntil, rs.Status = &now, nil, ""
	r.setFeature(scope.feature, outlook.TeamsStatusOK, "")
	if gaps {
		return teamsCompleteWithGaps
	}
	return teamsComplete
}

// storeRetainedVersion writes one retained message version once under its history key. The
// current message object, its hosted contents and its files are never touched or fetched.
func (r *teamsRun) storeRetainedVersion(scope teamsRetainedScope, raw json.RawMessage) error {
	var head teamsMessageHead
	if err := json.Unmarshal(raw, &head); err != nil || head.ID == "" {
		return fmt.Errorf("unparseable retained message")
	}
	version := outlook.TeamsMessageVersionID(head.ETag, head.LastModifiedDateTime, raw)
	block := outlook.TeamsBackupBlock{TenantID: r.tenant, ReplyToID: head.ReplyToID, RemovedAt: head.DeletedDateTime,
		Source: "getAllRetainedMessages", BackedUpAt: outlook.TeamsUTC(r.now())}
	var key string
	if scope.teamPrefix != "" {
		if head.ChannelIdentity == nil || head.ChannelIdentity.ChannelID == "" {
			return fmt.Errorf("retained channel message %s without channelIdentity", head.ID)
		}
		ch := head.ChannelIdentity.ChannelID
		if !strings.EqualFold(head.ChannelIdentity.TeamID, scope.id) || !r.sel.channelSelected(scope.id, ch) || r.sharedIncoming[scope.id][ch] {
			r.count("history_out_of_scope")
			return nil
		}
		key = outlook.TeamsChannelVersionKey(scope.teamPrefix, ch, head.ID, version)
		block.TeamID, block.ChannelID = scope.id, ch
	} else {
		if head.ChatID == "" {
			return fmt.Errorf("retained chat message %s without chatId", head.ID)
		}
		key = outlook.TeamsChatVersionKey(outlook.TeamsChatPrefix(r.tenant, head.ChatID), head.ID, version)
		block.ChatID = head.ChatID
	}
	if r.keys[key] {
		r.count("history_skipped")
		return nil
	}
	body, err := outlook.PatchTeamsBackup(raw, block)
	if err != nil {
		return err
	}
	meta := map[string]string{
		outlook.TeamsMetaMessageID: head.ID,
		outlook.TeamsMetaETag:      head.ETag,
		outlook.TeamsMetaModified:  head.LastModifiedDateTime,
	}
	if head.ReplyToID != "" {
		meta[outlook.TeamsMetaReplyToID] = head.ReplyToID
	}
	if err := r.put(key, body, meta); err != nil {
		return err
	}
	r.count("history_written")
	return nil
}
