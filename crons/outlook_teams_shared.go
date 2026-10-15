package crons

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// teamsSharedEvery is how often a team's hosted shared channels are listed again. The per-channel
// listing has no date filter, so each round reads every post and reply; unchanged ones are skipped
// by the version rule without a write.
const teamsSharedEvery = 24 * time.Hour

// teamsFeatureSharedChannels reports the shared-channel listing. It uses the channel messages
// feature's roles (Channel.ReadBasic.All, ChannelMessage.Read.All), so it adds no consent.
const teamsFeatureSharedChannels = "teams_shared_channel_messages"

// teamsSharedState is one round over a team's hosted shared channels: the channel being listed
// with its next page, the channels done this round, and the channels that refused this round.
type teamsSharedState struct {
	Channel   string            `json:"channel,omitempty"`
	PageLink  string            `json:"page_link,omitempty"`
	Done      []string          `json:"done,omitempty"`
	Refused   map[string]string `json:"refused,omitempty"`
	Dirty     bool              `json:"dirty,omitempty"`
	Held      int               `json:"held,omitempty"`
	HeldSince *time.Time        `json:"held_since,omitempty"`
	ListedAt  *time.Time        `json:"listed_at,omitempty"`
}

// teamsSharedChannels splits a team's shared channels: hosted are the ones this team hosts in this
// tenant (listed per channel), incoming the ones another team hosts. resolved is false when the
// incoming channels couldn't be read; then no shared channel is listed.
type teamsSharedChannels struct {
	hosted   []string
	incoming map[string]bool
	resolved bool
}

func teamsIsShared(ch teamsChannelInfo) bool {
	return strings.EqualFold(strings.TrimSpace(ch.MembershipType), "shared")
}

// teamsOwnTenantChannels drops channels owned by another tenant: the app-only token reads a
// channel only from its owner's tenant, and that tenant's data isn't this backup's.
func teamsOwnTenantChannels(channels []teamsChannelInfo, tenantID string) []teamsChannelInfo {
	out := make([]teamsChannelInfo, 0, len(channels))
	for _, ch := range channels {
		if owner := strings.TrimSpace(ch.TenantID); owner != "" && !strings.EqualFold(owner, tenantID) {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// resolveSharedChannels reads the team's incoming channels when it lists any shared channel. A
// shared channel is hosted here when its tenantId is this tenant and it isn't incoming.
func (r *teamsRun) resolveSharedChannels(teamID string, channels []teamsChannelInfo) (teamsSharedChannels, error) {
	out := teamsSharedChannels{incoming: map[string]bool{}}
	if r.sharedIncoming == nil {
		r.sharedIncoming = map[string]map[string]bool{}
	}
	delete(r.sharedIncoming, teamID)
	any := false
	for _, ch := range channels {
		any = any || teamsIsShared(ch)
	}
	if !any {
		out.resolved = true
		return out, nil
	}
	items, err := r.getAll(outlook.TeamsIncomingChannelsURL(teamID))
	if err != nil {
		return out, err
	}
	for _, raw := range items {
		var c struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &c) == nil && c.ID != "" {
			out.incoming[c.ID] = true
		}
	}
	out.resolved = true
	r.sharedIncoming[teamID] = out.incoming
	r.clearFailure(teamsSharedStreamKey(teamID))
	for _, ch := range channels {
		if teamsIsShared(ch) && !out.incoming[ch.ID] && strings.EqualFold(strings.TrimSpace(ch.TenantID), r.tenant) {
			out.hosted = append(out.hosted, ch.ID)
		}
	}
	sort.Strings(out.hosted)
	return out, nil
}

func teamsSharedStreamKey(teamID string) string { return "shared:stream:team:" + teamID }

func teamsSharedStream(teamID string) repo.TeamsFailure {
	return repo.TeamsFailure{Phase: "channels", Kind: teamsFailStream, ResourceType: "team", ResourceID: teamID,
		ItemIDs: map[string]string{"source": "shared_channels"}}
}

// sharedLookupFailed records an incomingChannels error: a refusal is reported on the feature
// without a failure entry (the channels show as host_team_unresolved), any other error keeps one
// stream entry and the team incomplete.
func (r *teamsRun) sharedLookupFailed(teamID string, err error) string {
	code := outlook.ClassifyTeamsError(err)
	r.setFeature(teamsFeatureSharedChannels, code, code)
	if code == outlook.TeamsStatusNotFound || outlook.IsTeamsPermanentStatus(code) {
		r.clearFailure(teamsSharedStreamKey(teamID))
		return ""
	}
	r.fail(teamsSharedStreamKey(teamID), teamsSharedStream(teamID), err, false)
	return teamsIncomplete
}

// syncSharedChannels lists each shared channel the team hosts (root posts with replies) under the
// paging contract, at most once a day. A channel that refuses (permission, policy, not found) is
// skipped for the round without a failure entry and makes the round complete with gaps; any other
// listing error keeps one stream entry and resumes the round on the next run. A post that can't be
// stored holds the round for replay with one failure entry for the team. Returns "" when no round
// was due.
func (r *teamsRun) syncSharedChannels(ss *teamsSharedState, teamID, teamPrefix string, hosted []string, deadline time.Time, save func() error) string {
	now := r.now()
	fresh := ss.Channel == "" && len(ss.Done) == 0
	if fresh && !ss.Dirty && ss.Held == 0 && ss.ListedAt != nil && now.Sub(*ss.ListedAt) < teamsSharedEvery {
		return ""
	}
	if fresh {
		ss.Refused = nil
	}
	streamKey := teamsSharedStreamKey(teamID)
	windowKey := "shared:window:team:" + teamID
	window := repo.TeamsFailure{Phase: "channels", Kind: teamsFailWindow, ResourceType: "team", ResourceID: teamID,
		ItemIDs: map[string]string{"source": "shared_channels"}}
	done := map[string]bool{}
	for _, ch := range ss.Done {
		done[ch] = true
	}
	for _, ch := range hosted {
		if done[ch] {
			continue
		}
		if r.stopped() || !r.now().Before(deadline) {
			_ = save()
			return teamsIncomplete
		}
		if ss.Channel != ch {
			ss.Channel, ss.PageLink = ch, ""
		}
		channelID := ch
		outcome, _, err := r.pageThrough(teamsPager{
			pageLink: &ss.PageLink, first: outlook.TeamsChannelRootsURL(teamID, channelID), dirty: &ss.Dirty, deadline: deadline,
			streamKey: streamKey, stream: teamsSharedStream(teamID), save: save,
			item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
				if err := r.storeSharedThread(teamID, teamPrefix, channelID, raw); err != nil {
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
			if code != outlook.TeamsStatusNotFound && !outlook.IsTeamsPermanentStatus(code) {
				return teamsIncomplete
			}
			r.clearFailure(streamKey)
			r.setFeature(teamsFeatureSharedChannels, code, code)
			if ss.Refused == nil {
				ss.Refused = map[string]string{}
			}
			ss.Refused[ch] = code
		}
		ss.Done = append(ss.Done, ch)
		ss.Channel, ss.PageLink = "", ""
		_ = save()
	}
	ss.Done, ss.Channel, ss.PageLink = nil, "", ""
	closed, released := r.endRound(teamsRound{&ss.Dirty, &ss.Held, &ss.HeldSince, streamKey}, func() []string { return []string{windowKey} })
	if !closed {
		return teamsIncomplete
	}
	r.clearFailure(windowKey)
	ss.ListedAt = &now
	if len(ss.Refused) > 0 || released {
		return teamsCompleteWithGaps
	}
	r.setFeature(teamsFeatureSharedChannels, outlook.TeamsStatusOK, "")
	return teamsComplete
}

// storeSharedThread stores a root post of a shared channel listing and its replies: the expanded
// ones, then the further reply pages, or every reply page when the listing didn't expand them.
// Reply pages are followed under their own page guard.
func (r *teamsRun) storeSharedThread(teamID, teamPrefix, channelID string, raw json.RawMessage) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("unparseable shared channel post")
	}
	var head teamsMessageHead
	if err := json.Unmarshal(raw, &head); err != nil || head.ID == "" {
		return fmt.Errorf("unparseable shared channel post")
	}
	expanded, hasReplies := root["replies"]
	var next string
	if v, ok := root["replies@odata.nextLink"]; ok {
		_ = json.Unmarshal(v, &next)
	}
	for k := range root {
		if k == "replies" || strings.HasPrefix(k, "replies@") {
			delete(root, k)
		}
	}
	body, err := json.Marshal(root)
	if err != nil {
		return err
	}
	if err := r.storeSharedMessage(teamID, teamPrefix, channelID, head.ID, body); err != nil {
		return err
	}
	var replies []json.RawMessage
	if hasReplies && string(expanded) != "null" {
		if err := json.Unmarshal(expanded, &replies); err != nil {
			return fmt.Errorf("unparseable replies of post %s", head.ID)
		}
	}
	for _, reply := range replies {
		if err := r.storeSharedMessage(teamID, teamPrefix, channelID, head.ID, reply); err != nil {
			return err
		}
	}
	first := outlook.TeamsChannelRootRepliesURL(teamID, channelID, head.ID)
	guard := outlook.NewTeamsPageGuard(first)
	link := next
	switch {
	case link != "":
		if err := guard.Next(link); err != nil {
			return err
		}
	case !hasReplies:
		link = first
	}
	for link != "" {
		if r.stopErr != nil {
			return r.stopErr
		}
		page, err := r.page(link)
		if err != nil {
			return err
		}
		for _, reply := range page.Items {
			if err := r.storeSharedMessage(teamID, teamPrefix, channelID, head.ID, reply); err != nil {
				return err
			}
		}
		if page.NextLink == "" {
			return nil
		}
		if err := guard.Next(page.NextLink); err != nil {
			return err
		}
		link = page.NextLink
	}
	return nil
}

// storeSharedMessage stores one post or reply of the listed channel; a message naming another
// team, channel or thread is refused so nothing is stored under the wrong owner.
func (r *teamsRun) storeSharedMessage(teamID, teamPrefix, channelID, rootID string, raw json.RawMessage) error {
	var head teamsMessageHead
	if err := json.Unmarshal(raw, &head); err != nil || head.ID == "" {
		return fmt.Errorf("unparseable shared channel message")
	}
	if head.ChannelIdentity == nil || head.ChannelIdentity.ChannelID != channelID ||
		(head.ChannelIdentity.TeamID != "" && !strings.EqualFold(head.ChannelIdentity.TeamID, teamID)) {
		return fmt.Errorf("shared channel message %s doesn't name channel %s of team %s", head.ID, channelID, teamID)
	}
	if head.ID != rootID && head.ReplyToID != rootID {
		return fmt.Errorf("reply %s isn't in thread %s", head.ID, rootID)
	}
	_, err := r.storeChannelMessageFrom(teamID, teamPrefix, raw, "channel_messages")
	return err
}
