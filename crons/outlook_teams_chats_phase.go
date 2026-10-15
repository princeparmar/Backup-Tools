package crons

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Chats, meetings and calls phases of the Teams tenant job, the item retry phase and the file
// contract. Every listing follows the paging contract of the teams phase: a page's items are
// stored or recorded before its cursor moves, and a round or window closes only after its last
// page with nothing held.

const (
	teamsCallsRetention   = 30 * 24 * time.Hour
	teamsCallsWarnAfter   = 25 * 24 * time.Hour
	teamsCallsOverlap     = 2 * 24 * time.Hour
	teamsPhoneOverlap     = 2 * 24 * time.Hour
	teamsPhoneChunk       = 88 * 24 * time.Hour // plus the 2-day overlap stays within Graph's 90 days
	teamsPhoneLookback    = 365 * 24 * time.Hour
	teamsCalendarBack     = 2 * 365 * 24 * time.Hour
	teamsCalendarAhead    = 365 * 24 * time.Hour
	teamsCalendarRollover = 182 * 24 * time.Hour
	teamsCalendarRestart  = 30 * 24 * time.Hour
	teamsAttendanceWindow = 30 * 24 * time.Hour
	teamsAttendanceEvery  = 24 * time.Hour
	teamsSeriesOverlap    = 2 * time.Hour
	teamsMaxFileRefs      = 20000
	teamsFileVerifyValid  = 7 * 24 * time.Hour
	teamsFileEveryRun     = 30 * 24 * time.Hour
	teamsFileRecheckUntil = 90 * 24 * time.Hour
	teamsPaidAPIBackoff   = 30 * 24 * time.Hour
	teamsNotFoundPending  = "not_found_unconfirmed"
)

// teamsUserState is {tenant}/tenant/{tenantId}/_state/users/{userId}.json.
type teamsUserState struct {
	ChatsDeltaLink   string     `json:"chats_delta_link,omitempty"`
	ChatsPageLink    string     `json:"chats_page_link,omitempty"`
	ChatsListedAt    *time.Time `json:"chats_listed_at,omitempty"`
	ChatsDirty       bool       `json:"chats_dirty,omitempty"`
	ChatsHeld        int        `json:"chats_held,omitempty"`
	ChatsHeldSince   *time.Time `json:"chats_held_since,omitempty"`
	HistoryDone      bool       `json:"history_done,omitempty"`
	HistoryUpper     *time.Time `json:"history_upper,omitempty"`
	HistoryPageLink  string     `json:"history_page_link,omitempty"`
	HistoryDirty     bool       `json:"history_dirty,omitempty"`
	HistoryHeld      int        `json:"history_held,omitempty"`
	HistoryHeldSince *time.Time `json:"history_held_since,omitempty"`
	SweepFrom        *time.Time `json:"sweep_from,omitempty"`
	SweepTo          *time.Time `json:"sweep_to,omitempty"`
	SweepPageLink    string     `json:"sweep_page_link,omitempty"`
	SweepDirty       bool       `json:"sweep_dirty,omitempty"`
	SweepHeld        int        `json:"sweep_held,omitempty"`
	SweepHeldSince   *time.Time `json:"sweep_held_since,omitempty"`
	// Retained is the user's retained chat messages listing (earlier versions of edited messages).
	Retained teamsRetainedState `json:"retained"`

	Transcripts    teamsListingState    `json:"transcripts,omitempty"`
	Recordings     teamsListingState    `json:"recordings,omitempty"`
	CalendarRanges []teamsCalendarRange `json:"calendar_ranges,omitempty"`
	CoveredFrom    *time.Time           `json:"covered_from,omitempty"`
	CoverageHash   string               `json:"coverage_hash,omitempty"`

	EventMids      map[string]string                 `json:"event_mids,omitempty"`
	Pointers       map[string]string                 `json:"pointers,omitempty"`
	OnlineMeetings map[string]string                 `json:"online_meetings,omitempty"`
	Attendance     map[string]teamsPendingAttendance `json:"attendance,omitempty"`

	SkipUntil      map[string]time.Time `json:"skip_until,omitempty"`
	Status         map[string]string    `json:"status,omitempty"`
	Scope          string               `json:"scope,omitempty"`
	ScopeChangedAt *time.Time           `json:"scope_changed_at,omitempty"`
}

// teamsListingState is an organizer's transcript or recording metadata listing (delta).
type teamsListingState struct {
	DeltaLink string     `json:"delta_link,omitempty"`
	PageLink  string     `json:"page_link,omitempty"`
	Dirty     bool       `json:"dirty,omitempty"`
	Held      int        `json:"held,omitempty"`
	HeldSince *time.Time `json:"held_since,omitempty"`
}

// teamsCalendarRange is one fixed calendarView range; its delta link is only used with it.
type teamsCalendarRange struct {
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	DeltaLink string     `json:"delta_link,omitempty"`
	PageLink  string     `json:"page_link,omitempty"`
	Dirty     bool       `json:"dirty,omitempty"`
	Held      int        `json:"held,omitempty"`
	HeldSince *time.Time `json:"held_since,omitempty"`
	Done      bool       `json:"done,omitempty"`
}

// teamsPendingAttendance is an organizer's meeting whose attendance is still being collected.
type teamsPendingAttendance struct {
	JoinURL         string     `json:"join_url"`
	End             time.Time  `json:"end"`
	OnlineMeetingID string     `json:"online_meeting_id,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
}

func (s *teamsUserState) skipped(stream string, now time.Time) bool {
	until, ok := s.SkipUntil[stream]
	return ok && now.Before(until)
}

func (s *teamsUserState) block(stream, status string, until time.Time) {
	if s.SkipUntil == nil {
		s.SkipUntil = map[string]time.Time{}
	}
	if s.Status == nil {
		s.Status = map[string]string{}
	}
	s.SkipUntil[stream], s.Status[stream] = until, status
}

func (s *teamsUserState) unblock(stream string) {
	delete(s.SkipUntil, stream)
	delete(s.Status, stream)
}

// teamsRound is the hold state of one round or window.
type teamsRound struct {
	dirty     *bool
	held      *int
	heldSince **time.Time
	// stream is the pager's streamKey.
	stream string
}

// endRound closes a round that finished its last page. A round with a failed item that can't be
// fetched alone, or a refused nextLink, is replayed instead; after 10 replays over 3 days it is
// released and its items (or its stream) become gaps. Returns closed and whether gaps were released.
func (r *teamsRun) endRound(rd teamsRound, failureKeys func() []string) (bool, bool) {
	if !*rd.dirty {
		*rd.held, *rd.heldSince = 0, nil
		return true, false
	}
	*rd.dirty = false
	*rd.held++
	if *rd.heldSince == nil {
		t := r.now()
		*rd.heldSince = &t
	}
	if r.releaseRound(*rd.held, *rd.heldSince, failureKeys(), rd.stream) {
		*rd.held, *rd.heldSince = 0, nil
		return true, true
	}
	return false, false
}

// failureKeysFor lists item entries of one phase and resource (and source, when set).
func (r *teamsRun) failureKeysFor(phase, resourceID, source string) func() []string {
	return func() []string {
		var out []string
		for k, f := range r.mem.TeamsFailures {
			if f.Phase == phase && f.ResourceID == resourceID && (source == "" || f.ItemIDs["source"] == source) {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
}

// teamsPager walks one listing under the paging contract.
type teamsPager struct {
	pageLink  *string
	first     string
	dirty     *bool
	deadline  time.Time
	streamKey string
	stream    repo.TeamsFailure
	save      func() error
	// item stores one row. key "" = nothing to track; lookup = the row can be fetched alone.
	item func(raw json.RawMessage) (key string, f repo.TeamsFailure, err error, lookup bool)
}

const (
	teamsPageDone = iota
	teamsPageStopped
	teamsPageFailed
)

// pageThrough returns the outcome, the delta link of the last page and the request error. A
// budget stop keeps the next (not yet requested) page; a failed request restarts from the
// stream's start unless replaying a nextLink is verified. A nextLink the page guard rejects ends
// the round dirty with no delta link: the caller's endRound replays it from the stream's start
// and releases it as a gap past the hold limit, so it can't stall on one page. A rejected
// deltaLink is recorded and not returned, so the caller keeps its previous one. A link off the
// Graph host is never requested.
func (r *teamsRun) pageThrough(p teamsPager) (int, string, error) {
	u := *p.pageLink
	if u == "" {
		u = p.first
	}
	guard := outlook.NewTeamsPageGuard(u)
	if !guard.Valid() {
		err := fmt.Errorf("%w: link not on the Graph host", outlook.ErrTeamsPaging)
		r.fail(p.streamKey, p.stream, err, false)
		if *p.pageLink == "" {
			return teamsPageFailed, "", err
		}
		*p.pageLink, *p.dirty = "", true
		_ = p.save()
		return teamsPageDone, "", nil
	}
	rows := 0
	for {
		if r.stopped() || !r.now().Before(p.deadline) {
			*p.pageLink = u
			_ = p.save()
			return teamsPageStopped, "", nil
		}
		page, err := r.page(u)
		if err != nil {
			r.fail(p.streamKey, p.stream, err, false)
			if outlook.TeamsVerified(outlook.TeamsCheckReplayNextLink) {
				*p.pageLink = u
			} else {
				*p.pageLink = ""
			}
			_ = p.save()
			return teamsPageFailed, "", err
		}
		for _, raw := range page.Items {
			rows++
			key, f, ierr, lookup := p.item(raw)
			if r.stopErr != nil {
				*p.pageLink = u
				return teamsPageStopped, "", r.stopErr
			}
			if key == "" {
				continue
			}
			if ierr != nil {
				recorded := r.fail(key, f, ierr, lookup)
				if !lookup || !recorded {
					*p.dirty = true
				}
				continue
			}
			r.clearFailure(key)
		}
		if page.NextLink == "" {
			*p.pageLink = ""
			r.count("rows")
			if rows == 0 {
				r.count("empty")
			}
			if page.DeltaLink != "" {
				if err := guard.Delta(page.DeltaLink); err != nil {
					r.fail(p.streamKey, p.stream, err, false)
					return teamsPageDone, "", nil
				}
			}
			r.clearFailure(p.streamKey)
			return teamsPageDone, page.DeltaLink, nil
		}
		if err := guard.Next(page.NextLink); err != nil {
			r.fail(p.streamKey, p.stream, err, false)
			*p.pageLink, *p.dirty = "", true
			_ = p.save()
			return teamsPageDone, "", nil
		}
		*p.pageLink = page.NextLink
		if err := p.save(); err != nil {
			*p.pageLink = u
			return teamsPageFailed, "", err
		}
		if err := r.checkpoint(); err != nil && r.stopErr != nil {
			return teamsPageStopped, "", r.stopErr
		}
		u = page.NextLink
	}
}

// scopeUsers lists the tenant's member users in scope, once per run.
func (r *teamsRun) scopeUsers() ([]teamsUser, error) {
	if r.usersLoaded {
		return r.users, r.usersErr
	}
	r.usersLoaded = true
	items, err := r.getAll(outlook.TeamsTenantUsersURL())
	if err != nil {
		r.usersErr = err
		return nil, err
	}
	r.userScope = map[string]bool{}
	for _, raw := range items {
		var u teamsUser
		if json.Unmarshal(raw, &u) != nil || u.ID == "" || strings.EqualFold(u.UserType, "Guest") || !r.sel.userSelected(u.ID) {
			continue
		}
		r.users = append(r.users, u)
		r.userScope[u.ID] = true
	}
	sort.Slice(r.users, func(i, j int) bool { return r.users[i].ID < r.users[j].ID })
	return r.users, nil
}

func (r *teamsRun) userIDs(users []teamsUser) []string {
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	return ids
}

func (r *teamsRun) loadUserState(userID string) (*teamsUserState, func() error) {
	key := outlook.TeamsUserStateKey(r.tenantPrefix, userID)
	st := &teamsUserState{}
	r.loadState(key, st)
	if st.Scope != "" {
		st.Scope, st.ScopeChangedAt = "", nil
	}
	return st, func() error { return r.saveState(key, st) }
}

// usersPhase runs fn for every user in scope, rotating and resuming like the teams phase.
func (r *teamsRun) usersPhase(phase, feature string, deadline time.Time, fn func(userID string, deadline time.Time) (string, string)) bool {
	if r.singleTeam != "" {
		return true
	}
	users, err := r.scopeUsers()
	if err != nil {
		status := outlook.ClassifyTeamsError(err)
		r.setFeature(feature, status, status)
		r.fail(phase+":stream:tenant:"+r.tenant, repo.TeamsFailure{Phase: phase, Kind: teamsFailStream, ResourceType: "tenant", ResourceID: r.tenant}, err, false)
		return false
	}
	r.clearFailure(phase + ":stream:tenant:" + r.tenant)
	if phase == teamsPhaseChats {
		r.markOutOfScope("users", r.userScope)
	}
	return r.eachResource(phase, r.userIDs(users), deadline, func(id string, capDeadline time.Time) {
		status, code := fn(id, capDeadline)
		r.setResource("user", id, phase, status, code)
	})
}

func teamsWorst(a, b string) string {
	rank := map[string]int{teamsComplete: 0, teamsCompleteWithRetries: 1, teamsCompleteWithGaps: 2, teamsIncomplete: 3, teamsBlocked: 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// teamsStreamBlocker is a state that backs off its streams (a user's state, the Q&A state).
type teamsStreamBlocker interface {
	block(stream, status string, until time.Time)
	unblock(stream string)
}

// pageStatus turns a pager outcome into a resource status, blocking the stream for a day on a
// permanent error and resetting its delta on a resync.
func (r *teamsRun) pageStatus(st teamsStreamBlocker, stream, feature string, outcome int, err error, reset func()) (string, string) {
	switch outcome {
	case teamsPageStopped:
		return teamsIncomplete, ""
	case teamsPageFailed:
		code := outlook.ClassifyTeamsError(err)
		switch {
		case code == outlook.TeamsStatusResync:
			reset()
			logger.Info(r.ctx, "teams delta restarted", logger.String("stream", stream))
		case code == outlook.TeamsStatusPaidAPI:
			st.block(stream, code, r.now().Add(teamsPaidAPIBackoff))
			r.setFeature(feature, code, code)
			return teamsBlocked, code
		case outlook.IsTeamsPermanentStatus(code):
			st.block(stream, code, r.now().Add(teamsPermanentBackoff))
			r.setFeature(feature, code, code)
			return teamsBlocked, code
		}
		return teamsIncomplete, code
	}
	st.unblock(stream)
	return teamsComplete, ""
}

// runChatsPhase backs up each user's chats: chat list, delta, one-time history and the sweep.
func (r *teamsRun) runChatsPhase(deadline time.Time) bool {
	if !outlook.TeamsVerified(outlook.TeamsCheckDeltaOldEdits) {
		r.setFeature("teams_chat_old_edits", outlook.TeamsStatusOK, "")
	}
	if !r.consented(outlook.TeamsFeatureChats) {
		return true
	}
	return r.usersPhase(teamsPhaseChats, "teams_chat_backup", deadline, r.syncUserChats)
}

func (r *teamsRun) syncUserChats(userID string, deadline time.Time) (string, string) {
	st, save := r.loadUserState(userID)
	defer func() { _ = save() }()
	if st.skipped("chats", r.now()) {
		return teamsBlocked, st.Status["chats"]
	}
	status, code := teamsComplete, ""
	if err := r.syncUserChatList(userID, st); err != nil {
		c := outlook.ClassifyTeamsError(err)
		if outlook.IsTeamsPermanentStatus(c) {
			st.block("chats", c, r.now().Add(teamsPermanentBackoff))
			r.setFeature("teams_chat_backup", c, c)
			return teamsBlocked, c
		}
		status, code = teamsIncomplete, c
	}
	if r.stopped() {
		return teamsIncomplete, code
	}

	first := st.ChatsDeltaLink
	if first == "" {
		first = outlook.TeamsUserChatDeltaURL(userID)
	}
	outcome, delta, err := r.pageThrough(teamsPager{
		pageLink: &st.ChatsPageLink, first: first, dirty: &st.ChatsDirty, deadline: deadline,
		streamKey: "chats:stream:user:" + userID,
		stream:    repo.TeamsFailure{Phase: "chats", Kind: teamsFailStream, ResourceType: "user", ResourceID: userID},
		save:      save,
		item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
			return r.chatItem(userID, "delta", raw)
		},
	})
	s, c := r.pageStatus(st, "chats", "teams_chat_backup", outcome, err, func() { st.ChatsDeltaLink, st.ChatsPageLink = "", "" })
	if s == teamsBlocked {
		return s, c
	}
	status = teamsWorst(status, s)
	if outcome == teamsPageDone {
		closed, gaps := r.endRound(teamsRound{&st.ChatsDirty, &st.ChatsHeld, &st.ChatsHeldSince, "chats:stream:user:" + userID}, r.failureKeysFor("chats", userID, "delta"))
		switch {
		case !closed:
			status = teamsWorst(status, teamsIncomplete)
		case delta != "":
			st.ChatsDeltaLink = delta
		}
		if gaps {
			status = teamsWorst(status, teamsCompleteWithGaps)
		}
		r.setFeature("teams_chat_backup", outlook.TeamsStatusOK, "")
	}
	if r.stopped() || !r.now().Before(deadline) {
		return teamsWorst(status, teamsIncomplete), code
	}
	if s := r.syncRetained(&st.Retained, teamsChatRetainedScope(userID), deadline, save); s == teamsIncomplete || s == teamsCompleteWithGaps {
		status = teamsWorst(status, s)
	}
	if r.stopped() || !r.now().Before(deadline) {
		return teamsWorst(status, teamsIncomplete), code
	}

	if !st.HistoryDone {
		if st.HistoryUpper == nil {
			t := r.to
			st.HistoryUpper = &t
		}
		outcome, _, err := r.pageThrough(teamsPager{
			pageLink: &st.HistoryPageLink, first: outlook.TeamsUserChatMessagesURL(userID, time.Time{}, *st.HistoryUpper),
			dirty: &st.HistoryDirty, deadline: deadline,
			streamKey: "chat_history:stream:user:" + userID,
			stream:    repo.TeamsFailure{Phase: "chat_history", Kind: teamsFailStream, ResourceType: "user", ResourceID: userID},
			save:      save,
			item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
				return r.chatItem(userID, "history", raw)
			},
		})
		s, _ := r.pageStatus(st, "chat_history", "teams_chat_backup", outcome, err, func() { st.HistoryPageLink = "" })
		status = teamsWorst(status, s)
		if outcome == teamsPageDone {
			closed, gaps := r.endRound(teamsRound{&st.HistoryDirty, &st.HistoryHeld, &st.HistoryHeldSince, "chat_history:stream:user:" + userID}, r.failureKeysFor("chats", userID, "history"))
			st.HistoryDone = closed
			if !closed {
				status = teamsWorst(status, teamsIncomplete)
			}
			if gaps {
				status = teamsWorst(status, teamsCompleteWithGaps)
			}
		}
		return status, code
	}

	if !outlook.TeamsVerified(outlook.TeamsCheckDeltaOldEdits) {
		if st.SweepFrom == nil {
			st.SweepFrom = st.HistoryUpper
		}
		if st.SweepPageLink == "" {
			t := r.to
			st.SweepTo = &t
		}
		outcome, _, err := r.pageThrough(teamsPager{
			pageLink: &st.SweepPageLink, first: outlook.TeamsUserChatMessagesURL(userID, teamsWindowFrom(*st.SweepFrom), *st.SweepTo),
			dirty: &st.SweepDirty, deadline: deadline,
			streamKey: "chat_history:stream:user:" + userID,
			stream:    repo.TeamsFailure{Phase: "chat_history", Kind: teamsFailStream, ResourceType: "user", ResourceID: userID},
			save:      save,
			item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
				return r.chatItem(userID, "sweep", raw)
			},
		})
		s, _ := r.pageStatus(st, "chat_sweep", "teams_chat_backup", outcome, err, func() { st.SweepPageLink = "" })
		status = teamsWorst(status, s)
		if outcome == teamsPageDone {
			closed, gaps := r.endRound(teamsRound{&st.SweepDirty, &st.SweepHeld, &st.SweepHeldSince, "chat_history:stream:user:" + userID}, r.failureKeysFor("chats", userID, "sweep"))
			if closed {
				st.SweepFrom = st.SweepTo
			} else {
				status = teamsWorst(status, teamsIncomplete)
			}
			if gaps {
				status = teamsWorst(status, teamsCompleteWithGaps)
			}
		}
	}
	return status, code
}

// chatItem stores one chat message found by a user's delta, history or sweep.
func (r *teamsRun) chatItem(userID, source string, raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
	var probe struct {
		ID      string          `json:"id"`
		ChatID  string          `json:"chatId"`
		Removed json.RawMessage `json:"@removed"`
	}
	if json.Unmarshal(raw, &probe) == nil && len(probe.Removed) > 0 {
		failure := repo.TeamsFailure{Phase: "chats", Kind: teamsFailItem, ResourceType: "user", ResourceID: userID,
			ItemIDs: map[string]string{"chat_id": probe.ChatID, "message_id": probe.ID, "source": source}}
		if probe.ID == "" {
			sum := sha1.Sum(raw)
			id := hex.EncodeToString(sum[:8])
			return "chats:deletion:unparsed:" + id, failure, fmt.Errorf("chat deletion marker has no message id"), false
		}
		if err := r.removeChatMessage(probe.ChatID, probe.ID); err != nil {
			return "chats:deletion:" + probe.ChatID + ":" + probe.ID, failure, err, false
		}
		return "", repo.TeamsFailure{}, nil, false
	}
	var head teamsMessageHead
	if err := json.Unmarshal(raw, &head); err != nil || head.ID == "" || head.ChatID == "" {
		sum := sha1.Sum(raw)
		id := hex.EncodeToString(sum[:8])
		return "chats:item:unparsed:" + id, repo.TeamsFailure{Phase: "chats", Kind: teamsFailItem, ResourceType: "user", ResourceID: userID,
			ItemIDs: map[string]string{"source": source}}, fmt.Errorf("unparseable chat message"), false
	}
	key := "chats:item:" + head.ChatID + ":" + head.ID
	f := repo.TeamsFailure{Phase: "chats", Kind: teamsFailItem, ResourceType: "user", ResourceID: userID,
		ItemIDs: map[string]string{"chat_id": head.ChatID, "message_id": head.ID, "source": source}}
	_, err := r.storeChatMessage(raw, head)
	return key, f, err, outlook.TeamsVerified(outlook.TeamsCheckLookupChatMessage)
}

func (r *teamsRun) removeChatMessage(chatID, messageID string) error {
	var matches []string
	for key := range r.keys {
		parsed, ok := outlook.ParseTeamsObjectKey(key)
		if !ok || parsed.Kind != outlook.TeamsKindChatMessage || parsed.MessageID != messageID {
			continue
		}
		if chatID == "" || parsed.ResourceID == chatID {
			matches = append(matches, key)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	if chatID == "" && len(matches) > 1 {
		return fmt.Errorf("chat deletion marker %s matches multiple chats", messageID)
	}
	deletedAt := outlook.TeamsUTC(r.now())
	for _, key := range matches {
		if err := r.applyRemoval(key, deletedAt, outlook.TeamsVersion{}); err != nil {
			return err
		}
		parsed, _ := outlook.ParseTeamsObjectKey(key)
		r.clearFailure("chats:item:" + parsed.ResourceID + ":" + messageID)
		r.count("removed")
	}
	return nil
}

func (r *teamsRun) storeChatMessage(raw json.RawMessage, head teamsMessageHead) (string, error) {
	chatPrefix := outlook.TeamsChatPrefix(r.tenant, head.ChatID)
	return r.storeMessage(raw, head, teamsMessageTarget{
		key: outlook.TeamsChatMessageKey(chatPrefix, head.ID, head.CreatedDateTime),
		hostedKey: func(ref outlook.TeamsHostedRef) (string, bool) {
			if ref.ChatID == "" || ref.ChatID != head.ChatID {
				return "", false
			}
			return outlook.TeamsChatHostedKey(chatPrefix, ref.MessageID, ref.ContentID), true
		},
		block: outlook.TeamsBackupBlock{TenantID: r.tenant, ChatID: head.ChatID, ReplyToID: head.ReplyToID, Source: "chats"},
	})
}

// syncUserChatList writes each chat's _chat.json once per run (members, topic, pins) and keeps
// members' pointers in step: a new member gets a pointer, a removed one keeps it with left-at.
func (r *teamsRun) syncUserChatList(userID string, st *teamsUserState) error {
	items, err := r.getAll(outlook.TeamsUserChatsURL(userID))
	if err != nil {
		return err
	}
	since := time.Time{}
	if st.ChatsListedAt != nil {
		since = st.ChatsListedAt.Add(-teamsWindowOverlap)
	}
	var firstErr error
	for _, raw := range items {
		var c struct {
			ID          string `json:"id"`
			LastUpdated string `json:"lastUpdatedDateTime"`
			Members     []struct {
				UserID string `json:"userId"`
			} `json:"members"`
		}
		if json.Unmarshal(raw, &c) != nil || c.ID == "" {
			continue
		}
		chatPrefix := outlook.TeamsChatPrefix(r.tenant, c.ID)
		if err := r.putPointer(outlook.TeamsUserChatPointerKey(outlook.TeamsUserPrefix(r.tenant, userID), c.ID), chatPrefix, nil); err != nil && firstErr == nil {
			firstErr = err
		}
		if r.chatsSeen[c.ID] {
			continue
		}
		r.chatsSeen[c.ID] = true
		metaKey := outlook.TeamsChatMetaKey(chatPrefix)
		if t, ok := outlook.ParseTeamsTime(c.LastUpdated); ok && r.keys[metaKey] && t.Before(since) {
			continue
		}
		members := make([]string, 0, len(c.Members))
		for _, m := range c.Members {
			if m.UserID != "" {
				members = append(members, m.UserID)
			}
		}
		if err := r.writeChatMeta(c.ID, chatPrefix, raw, members); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		t := r.to
		st.ChatsListedAt = &t
	}
	return firstErr
}

func (r *teamsRun) writeChatMeta(chatID, chatPrefix string, raw json.RawMessage, members []string) error {
	metaKey := outlook.TeamsChatMetaKey(chatPrefix)
	var previous []string
	if r.keys[metaKey] {
		if old, err := r.store.download(r.ctx, metaKey); err == nil {
			var doc struct {
				Members []struct {
					UserID string `json:"userId"`
				} `json:"members"`
			}
			if json.Unmarshal(old, &doc) == nil {
				for _, m := range doc.Members {
					previous = append(previous, m.UserID)
				}
			}
		}
	}
	pinsStatus := outlook.TeamsStatusOK
	pins, err := r.getAll(outlook.TeamsChatPinnedURL(chatID))
	if err != nil {
		pinsStatus = outlook.ClassifyTeamsError(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	pb, _ := json.Marshal(pins)
	ps, _ := json.Marshal(pinsStatus)
	doc["pinned_messages"], doc["pins_status"] = pb, ps
	body, _ := json.Marshal(doc)
	body, err = outlook.PatchTeamsBackup(body, outlook.TeamsBackupBlock{TenantID: r.tenant, ChatID: chatID, BackedUpAt: outlook.TeamsUTC(r.now())})
	if err != nil {
		return err
	}
	if err := r.put(metaKey, body, nil); err != nil {
		return err
	}
	current := map[string]bool{}
	for _, id := range members {
		current[id] = true
		if r.userScope[id] {
			if err := r.putPointer(outlook.TeamsUserChatPointerKey(outlook.TeamsUserPrefix(r.tenant, id), chatID), chatPrefix, nil); err != nil {
				return err
			}
		}
	}
	for _, id := range previous {
		if current[id] || !r.userScope[id] {
			continue
		}
		left := map[string]string{outlook.TeamsMetaLeftAt: outlook.TeamsUTC(r.now())}
		if err := r.put(outlook.TeamsUserChatPointerKey(outlook.TeamsUserPrefix(r.tenant, id), chatID), nil,
			teamsPointerMeta(chatPrefix, left)); err != nil {
			return err
		}
	}
	return nil
}

func teamsPointerMeta(target string, extra map[string]string) map[string]string {
	meta := map[string]string{outlook.TeamsMetaPhysical: target}
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

// putPointer writes a zero-byte pointer unless it exists (always when extra metadata is given).
func (r *teamsRun) putPointer(key, target string, extra map[string]string) error {
	if r.keys[key] && extra == nil {
		return nil
	}
	return r.put(key, nil, teamsPointerMeta(target, extra))
}

// teamsEvent is the part of a calendar event the meetings phase reads.
type teamsEvent struct {
	ID                    string          `json:"id"`
	Removed               json.RawMessage `json:"@removed"`
	ICalUID               string          `json:"iCalUId"`
	Type                  string          `json:"type"`
	SeriesMasterID        string          `json:"seriesMasterId"`
	OriginalStart         string          `json:"originalStart"`
	ChangeKey             string          `json:"changeKey"`
	LastModified          string          `json:"lastModifiedDateTime"`
	IsOnlineMeeting       bool            `json:"isOnlineMeeting"`
	OnlineMeetingProvider string          `json:"onlineMeetingProvider"`
	IsOrganizer           bool            `json:"isOrganizer"`
	IsCancelled           bool            `json:"isCancelled"`
	OnlineMeeting         *struct {
		JoinURL string `json:"joinUrl"`
	} `json:"onlineMeeting"`
	Organizer *struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"organizer"`
	ResponseStatus *struct {
		Response string `json:"response"`
	} `json:"responseStatus"`
	Start *struct {
		DateTime string `json:"dateTime"`
	} `json:"start"`
	End *struct {
		DateTime string `json:"dateTime"`
	} `json:"end"`
}

func (ev teamsEvent) isTeamsMeeting() bool {
	return ev.IsOnlineMeeting && strings.EqualFold(ev.OnlineMeetingProvider, "teamsForBusiness")
}

func (ev teamsEvent) joinURL() string {
	if ev.OnlineMeeting == nil {
		return ""
	}
	return ev.OnlineMeeting.JoinURL
}

func (ev teamsEvent) startTime() (time.Time, bool) {
	if ev.Start == nil {
		return time.Time{}, false
	}
	return outlook.ParseTeamsTime(ev.Start.DateTime)
}

func (ev teamsEvent) endTime() (time.Time, bool) {
	if ev.End == nil {
		return time.Time{}, false
	}
	return outlook.ParseTeamsTime(ev.End.DateTime)
}

func (ev teamsEvent) kind() string {
	switch ev.Type {
	case "occurrence", "exception":
		return ev.Type
	case "seriesMaster":
		return "series"
	}
	return "single"
}

// meetingShared reports whether this copy may share one folder with other mailboxes' copies:
// only for a case the tenant checks confirmed (all off in the safe build).
func meetingShared(ev teamsEvent) bool {
	if strings.TrimSpace(ev.ICalUID) == "" {
		return false
	}
	switch ev.Type {
	case "occurrence":
		return outlook.TeamsVerified(outlook.TeamsCheckMergeOccurrence)
	case "exception":
		return outlook.TeamsVerified(outlook.TeamsCheckMergeException)
	case "seriesMaster":
		return outlook.TeamsVerified(outlook.TeamsCheckMergeOccurrence)
	}
	return outlook.TeamsVerified(outlook.TeamsCheckMergeSingleMeeting)
}

// runMeetingsPhase backs up the tenant's meeting Q&A, then each user's Teams meetings: calendar
// ranges, meeting folders, the organizer's attendance and the transcript/recording metadata
// listings.
func (r *teamsRun) runMeetingsPhase(deadline time.Time) bool {
	qaDone := r.syncMeetingQA(deadline)
	if !r.consented(outlook.TeamsFeatureCalendar) {
		return qaDone
	}
	done := r.usersPhase(teamsPhaseMeetings, "teams_meetings_backup", deadline, r.syncUserMeetings)
	r.writeReconcile()
	return done && qaDone
}

func (r *teamsRun) syncUserMeetings(userID string, deadline time.Time) (string, string) {
	st, save := r.loadUserState(userID)
	defer func() { _ = save() }()
	for _, m := range []*map[string]string{&st.EventMids, &st.Pointers, &st.OnlineMeetings} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	if st.Attendance == nil {
		st.Attendance = map[string]teamsPendingAttendance{}
	}
	status, code := teamsComplete, ""
	if st.skipped("calendar", r.now()) {
		status, code = teamsBlocked, st.Status["calendar"]
	} else {
		r.planCalendarRanges(st)
		for i := range st.CalendarRanges {
			s, c := r.syncCalendarRange(userID, st, &st.CalendarRanges[i], deadline, save)
			status = teamsWorst(status, s)
			if c != "" {
				code = c
			}
			if s == teamsBlocked || r.stopped() {
				break
			}
		}
		if n := len(st.CalendarRanges); n > 1 && st.CalendarRanges[n-1].Done {
			for _, old := range st.CalendarRanges[:n-1] {
				if st.CoveredFrom == nil || old.Start.Before(*st.CoveredFrom) {
					t := old.Start
					st.CoveredFrom = &t
				}
			}
			st.CalendarRanges = st.CalendarRanges[n-1:]
		}
		r.writeMeetingCoverage(userID, st)
	}
	if r.stopped() || !r.now().Before(deadline) {
		return teamsWorst(status, teamsIncomplete), code
	}
	if !r.syncAttendance(userID, st, deadline) {
		status = teamsWorst(status, teamsIncomplete)
	}
	for _, kind := range []string{"transcripts", "recordings"} {
		if r.stopped() || !r.now().Before(deadline) {
			return teamsWorst(status, teamsIncomplete), code
		}
		if s := r.syncArtifactListing(userID, st, kind, deadline, save); s == teamsIncomplete || s == teamsCompleteWithGaps {
			status = teamsWorst(status, s)
		}
	}
	return status, code
}

// planCalendarRanges starts the first range (2 years back, 1 year ahead) and rolls over to a new
// range when the newest one ends within 6 months.
func (r *teamsRun) planCalendarRanges(st *teamsUserState) {
	now := r.now()
	if len(st.CalendarRanges) == 0 {
		st.CalendarRanges = []teamsCalendarRange{{Start: now.Add(-teamsCalendarBack), End: now.Add(teamsCalendarAhead)}}
		return
	}
	if last := st.CalendarRanges[len(st.CalendarRanges)-1]; last.End.Sub(now) < teamsCalendarRollover {
		st.CalendarRanges = append(st.CalendarRanges, teamsCalendarRange{Start: now.Add(-teamsCalendarRestart), End: now.Add(teamsCalendarAhead)})
	}
}

func (r *teamsRun) syncCalendarRange(userID string, st *teamsUserState, rg *teamsCalendarRange, deadline time.Time, save func() error) (string, string) {
	first := rg.DeltaLink
	if first == "" {
		first = outlook.TeamsCalendarViewDeltaURL(userID, rg.Start, rg.End)
	}
	outcome, delta, err := r.pageThrough(teamsPager{
		pageLink: &rg.PageLink, first: first, dirty: &rg.Dirty, deadline: deadline,
		streamKey: "calendar:stream:user:" + userID,
		stream:    repo.TeamsFailure{Phase: "calendar", Kind: teamsFailStream, ResourceType: "user", ResourceID: userID},
		save:      save,
		item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
			return r.calendarItem(userID, st, rg, raw)
		},
	})
	status, code := r.pageStatus(st, "calendar", "teams_calendar", outcome, err, func() { rg.DeltaLink, rg.PageLink = "", "" })
	if outcome != teamsPageDone {
		return status, code
	}
	closed, gaps := r.endRound(teamsRound{&rg.Dirty, &rg.Held, &rg.HeldSince, "calendar:stream:user:" + userID}, r.failureKeysFor("calendar", userID, ""))
	if !closed {
		return teamsIncomplete, ""
	}
	if delta != "" {
		rg.DeltaLink = delta
	}
	rg.Done = true
	r.setFeature("teams_calendar", outlook.TeamsStatusOK, "")
	if gaps {
		return teamsCompleteWithGaps, ""
	}
	return teamsComplete, ""
}

func (r *teamsRun) calendarItem(userID string, st *teamsUserState, rg *teamsCalendarRange, raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
	var ev teamsEvent
	if err := json.Unmarshal(raw, &ev); err != nil || ev.ID == "" {
		return "", repo.TeamsFailure{}, nil, false
	}
	key := "calendar:item:" + userID + ":" + ev.ID
	f := repo.TeamsFailure{Phase: "calendar", Kind: teamsFailItem, ResourceType: "user", ResourceID: userID, ItemIDs: map[string]string{"event_id": ev.ID}}
	lookup := outlook.TeamsVerified(outlook.TeamsCheckLookupEvent)
	if len(ev.Removed) > 0 {
		return key, f, r.removeEvent(userID, st, rg, ev.ID), lookup
	}
	if !ev.isTeamsMeeting() {
		return "", f, nil, false
	}
	return key, f, r.storeMeetingEvent(userID, st, raw, ev), lookup
}

// resolveMid applies the collision check: a folder whose stored identity differs from this
// copy's is never written; the copy then uses the full-hash folder id.
func (r *teamsRun) resolveMid(mid, input, full string) string {
	if v, ok := r.midChecked[mid+"|"+full]; ok {
		return v
	}
	out := mid
	metaKey := outlook.TeamsMeetingMetaKey(outlook.TeamsMeetingPrefix(r.tenant, mid))
	if r.keys[metaKey] {
		if raw, err := r.store.download(r.ctx, metaKey); err == nil {
			var doc struct {
				Identity struct {
					Full string `json:"full"`
				} `json:"identity"`
			}
			if json.Unmarshal(raw, &doc) == nil && doc.Identity.Full != "" && doc.Identity.Full != full {
				out = outlook.TeamsMeetingFullID(mid, full)
				logger.Warn(r.ctx, "teams meeting folder id collision; using the full id", logger.String("mid", mid))
			}
		}
	}
	r.midChecked[mid+"|"+full] = out
	return out
}

// meetingIdentity returns the folder id of one mailbox copy plus its identity record.
func (r *teamsRun) meetingIdentity(userID string, ev teamsEvent) (string, map[string]interface{}) {
	shared := meetingShared(ev)
	mid, input, full := outlook.TeamsMeetingID(ev.ICalUID, userID, ev.ID, shared)
	mid = r.resolveMid(mid, input, full)
	identity := map[string]interface{}{"input": input, "full": full, "kind": "separate"}
	switch {
	case shared:
		identity["kind"] = "shared"
	case strings.TrimSpace(ev.ICalUID) == "":
		identity["reason"] = "no_ical_uid"
	default:
		identity["reason"] = "case_unverified"
	}
	return mid, identity
}

// storeMeetingEvent writes one Teams meeting copy: event.json (version rule by changeKey),
// _meeting.json, the user's pointer, the series master once, and the organizer's attendance.
func (r *teamsRun) storeMeetingEvent(userID string, st *teamsUserState, raw json.RawMessage, ev teamsEvent) error {
	seriesMid := ""
	if ev.SeriesMasterID != "" {
		mid, err := r.storeSeriesMaster(userID, st, ev.SeriesMasterID)
		if err != nil {
			return fmt.Errorf("series master: %w", err)
		}
		seriesMid = mid
	}
	mid, identity := r.meetingIdentity(userID, ev)
	prefix := outlook.TeamsMeetingPrefix(r.tenant, mid)
	if err := r.writeEventJSON(prefix, raw, ev, seriesMid); err != nil {
		return err
	}
	start, _ := ev.startTime()
	end, _ := ev.endTime()
	organizer := ""
	if ev.Organizer != nil {
		organizer = ev.Organizer.EmailAddress.Address
	}
	err := r.updateMeetingMeta(prefix, func(doc map[string]interface{}) {
		doc["mid"], doc["identity"], doc["kind"] = mid, identity, ev.kind()
		doc["ical_uid"], doc["join_url"], doc["organizer"] = ev.ICalUID, ev.joinURL(), organizer
		doc["start"], doc["end"], doc["is_cancelled"] = outlook.TeamsUTC(start), outlook.TeamsUTC(end), ev.IsCancelled
		doc["calendar_status"] = outlook.TeamsStatusOK
		if seriesMid != "" {
			doc["series_mid"] = seriesMid
		}
		ids, _ := doc["event_ids"].(map[string]interface{})
		if ids == nil {
			ids = map[string]interface{}{}
		}
		ids[userID] = ev.ID
		doc["event_ids"] = ids
		teamsMeetingDefaults(doc)
	})
	if err != nil {
		return err
	}
	role, response := "attendee", ""
	if ev.IsOrganizer {
		role = "organizer"
	}
	if ev.ResponseStatus != nil {
		response = ev.ResponseStatus.Response
	}
	if err := r.movePointer(st, mid, outlook.TeamsUserMeetingPointerKey(outlook.TeamsUserPrefix(r.tenant, userID), mid, outlook.TeamsUTC(start)),
		prefix, map[string]string{outlook.TeamsMetaRole: role, outlook.TeamsMetaResponse: response}); err != nil {
		return err
	}
	st.EventMids[ev.ID] = mid
	if ev.ICalUID != "" {
		r.icalMids[ev.ICalUID] = appendUnique(r.icalMids[ev.ICalUID], mid)
	}
	if ev.IsOrganizer && ev.joinURL() != "" && !end.IsZero() && r.now().Sub(end) < teamsAttendanceWindow {
		p := st.Attendance[mid]
		p.JoinURL, p.End = ev.joinURL(), end
		st.Attendance[mid] = p
	}
	return nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func teamsMeetingDefaults(doc map[string]interface{}) {
	if _, ok := doc["transcript_content_status"]; !ok {
		doc["transcript_content_status"] = outlook.TeamsStatusPaidAPI
	}
	for _, k := range []string{"transcript_metadata_status", "recording_metadata_status", "attendance_status"} {
		if _, ok := doc[k]; !ok {
			doc[k] = "not_verified"
		}
	}
}

func (r *teamsRun) writeEventJSON(prefix string, raw json.RawMessage, ev teamsEvent, seriesMid string) error {
	key := outlook.TeamsMeetingEventKey(prefix)
	incoming := outlook.TeamsVersion{Modified: ev.LastModified, ETag: ev.ChangeKey}
	if r.keys[key] {
		meta, err := r.store.stat(r.ctx, key)
		if err != nil && !errors.Is(err, errTeamsNotFound) {
			return err
		}
		if err == nil {
			stored := &outlook.TeamsVersion{Modified: meta[outlook.TeamsMetaModified], ETag: meta[outlook.TeamsMetaChangeKey]}
			if v := outlook.CompareTeamsVersion(stored, incoming); v != outlook.TeamsVersionWrite {
				r.count(v)
				return nil
			}
		}
	}
	body, err := outlook.PatchTeamsBackup(raw, outlook.TeamsBackupBlock{TenantID: r.tenant, Source: "calendarView", BackedUpAt: outlook.TeamsUTC(r.now())})
	if err != nil {
		return err
	}
	if seriesMid != "" {
		var doc map[string]json.RawMessage
		if json.Unmarshal(body, &doc) == nil {
			b, _ := json.Marshal(seriesMid)
			doc["series_mid"] = b
			body, _ = json.Marshal(doc)
		}
	}
	return r.put(key, body, map[string]string{
		outlook.TeamsMetaModified: ev.LastModified, outlook.TeamsMetaChangeKey: ev.ChangeKey, outlook.TeamsMetaETag: ev.ChangeKey,
	})
}

// updateMeetingMeta reads, changes and (when changed) writes a meeting's _meeting.json.
func (r *teamsRun) updateMeetingMeta(prefix string, change func(map[string]interface{})) error {
	key := outlook.TeamsMeetingMetaKey(prefix)
	doc := map[string]interface{}{}
	var before []byte
	if r.keys[key] {
		raw, err := r.store.download(r.ctx, key)
		if err != nil && !errors.Is(err, errTeamsNotFound) {
			return err
		}
		if err == nil {
			_ = json.Unmarshal(raw, &doc)
			delete(doc, "storx_backup")
			before, _ = json.Marshal(doc)
		}
	}
	change(doc)
	after, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if before != nil && string(before) == string(after) {
		return nil
	}
	body, err := outlook.PatchTeamsBackup(after, outlook.TeamsBackupBlock{TenantID: r.tenant, BackedUpAt: outlook.TeamsUTC(r.now())})
	if err != nil {
		return err
	}
	return r.put(key, body, nil)
}

// movePointer writes a user's meeting pointer and, when the meeting moved to another date,
// removes the old pointer only after the new one is written.
func (r *teamsRun) movePointer(st *teamsUserState, mid, key, target string, meta map[string]string) error {
	if err := r.put(key, nil, teamsPointerMeta(target, meta)); err != nil {
		return err
	}
	if old := st.Pointers[mid]; old != "" && old != key && r.keys[old] {
		if err := r.remove(old); err != nil {
			return err
		}
	}
	st.Pointers[mid] = key
	return nil
}

// storeSeriesMaster stores a recurring series master once per user and run (kind series).
func (r *teamsRun) storeSeriesMaster(userID string, st *teamsUserState, masterID string) (string, error) {
	seen := userID + "|" + masterID
	if mid, ok := r.seriesSeen[seen]; ok {
		return mid, nil
	}
	raw, err := r.getJSON(outlook.TeamsUserEventURL(userID, masterID))
	if err != nil {
		return "", err
	}
	var ev teamsEvent
	if err := json.Unmarshal(raw, &ev); err != nil || ev.ID == "" {
		return "", fmt.Errorf("unparseable series master")
	}
	ev.Type = "seriesMaster"
	mid, identity := r.meetingIdentity(userID, ev)
	prefix := outlook.TeamsMeetingPrefix(r.tenant, mid)
	if err := r.writeEventJSON(prefix, raw, ev, ""); err != nil {
		return "", err
	}
	if err := r.updateMeetingMeta(prefix, func(doc map[string]interface{}) {
		doc["mid"], doc["identity"], doc["kind"], doc["ical_uid"], doc["join_url"] = mid, identity, "series", ev.ICalUID, ev.joinURL()
		doc["calendar_status"] = outlook.TeamsStatusOK
		ids, _ := doc["event_ids"].(map[string]interface{})
		if ids == nil {
			ids = map[string]interface{}{}
		}
		ids[userID] = ev.ID
		doc["event_ids"] = ids
		teamsMeetingDefaults(doc)
	}); err != nil {
		return "", err
	}
	st.EventMids[ev.ID] = mid
	r.seriesSeen[seen] = mid
	return mid, nil
}

// removeEvent applies a calendar @removed entry: only an event whose stored start lies inside the
// range is marked removed (the delta also lists events that left the range).
func (r *teamsRun) removeEvent(userID string, st *teamsUserState, rg *teamsCalendarRange, eventID string) error {
	mid := st.EventMids[eventID]
	if mid == "" {
		return nil
	}
	prefix := outlook.TeamsMeetingPrefix(r.tenant, mid)
	metaKey := outlook.TeamsMeetingMetaKey(prefix)
	if !r.keys[metaKey] {
		return nil
	}
	raw, err := r.store.download(r.ctx, metaKey)
	if err != nil {
		return err
	}
	var doc struct {
		Start     string `json:"start"`
		RemovedAt string `json:"removed_at"`
	}
	_ = json.Unmarshal(raw, &doc)
	start, ok := outlook.ParseTeamsTime(doc.Start)
	if !ok || start.Before(rg.Start) || !start.Before(rg.End) || doc.RemovedAt != "" {
		return nil
	}
	now := outlook.TeamsUTC(r.now())
	evKey := outlook.TeamsMeetingEventKey(prefix)
	if r.keys[evKey] {
		if err := r.applyRemoval(evKey, now, outlook.TeamsVersion{}); err != nil {
			return err
		}
	}
	if err := r.updateMeetingMeta(prefix, func(d map[string]interface{}) { d["removed_at"] = now }); err != nil {
		return err
	}
	if ptr := st.Pointers[mid]; ptr != "" && r.keys[ptr] {
		meta, err := r.store.stat(r.ctx, ptr)
		if err == nil {
			meta[outlook.TeamsMetaRemovedAt] = now
			return r.put(ptr, nil, meta)
		}
	}
	return nil
}

// writeMeetingCoverage writes MEETINGS/_coverage.json: the period this user's calendar covers.
func (r *teamsRun) writeMeetingCoverage(userID string, st *teamsUserState) {
	ranges := make([]map[string]interface{}, 0, len(st.CalendarRanges))
	from := st.CoveredFrom
	for _, rg := range st.CalendarRanges {
		ranges = append(ranges, map[string]interface{}{"start": outlook.TeamsUTC(rg.Start), "end": outlook.TeamsUTC(rg.End), "complete": rg.Done})
		if rg.Done && (from == nil || rg.Start.Before(*from)) {
			t := rg.Start
			from = &t
		}
	}
	doc := map[string]interface{}{
		"user_id": userID, "ranges": ranges,
		"not_covered": []string{"outside_ranges", "deleted_before_first_backup", "no_calendar_event", "meet_now"},
	}
	if from != nil {
		doc["covered_from"] = outlook.TeamsUTC(*from)
	}
	b, _ := json.Marshal(doc)
	h := teamsHash(b)
	key := outlook.TeamsUserMeetingCoverageKey(outlook.TeamsUserPrefix(r.tenant, userID))
	if h == st.CoverageHash && r.keys[key] {
		return
	}
	if r.put(key, b, nil) == nil {
		st.CoverageHash = h
	}
}

// syncAttendance collects attendance reports for the organizer's ended meetings, at most once a
// day for 30 days after the meeting. Each request runs only when the token holds its permission
// (outlook.TeamsAttendancePermissions); a meeting that needs a request without it stays pending
// for a later run. A refusal that holds for all the organizer's meetings (permission, consent,
// application access policy, licence) backs that request off for the organizer, without a
// failure entry per meeting; any other error is an item failure retried on the next run.
func (r *teamsRun) syncAttendance(userID string, st *teamsUserState, deadline time.Time) bool {
	now := r.now()
	if !r.consented(outlook.TeamsFeatureAttendance) || !r.attendanceAllowed(outlook.TeamsAttendanceReports) || st.skipped("attendance", now) {
		return true
	}
	lookup := r.attendanceAllowed(outlook.TeamsAttendanceLookup) && !st.skipped("attendance_lookup", now)
	deny := func(stream, req, code string) bool {
		switch {
		case code == outlook.TeamsStatusPaidAPI:
			st.block(stream, code, now.Add(teamsPaidAPIBackoff))
		case teamsAccessDenied(code):
			st.block(stream, code, now.Add(teamsPermanentBackoff))
		default:
			return false
		}
		r.setFeature(teamsAttendanceFeature(req), code, code)
		return true
	}
	mids := make([]string, 0, len(st.Attendance))
	for mid := range st.Attendance {
		mids = append(mids, mid)
	}
	sort.Strings(mids)
	ok := true
	for _, mid := range mids {
		if r.stopped() || !now.Before(deadline) {
			return false
		}
		p := st.Attendance[mid]
		if now.Sub(p.End) > teamsAttendanceWindow {
			delete(st.Attendance, mid)
			continue
		}
		if now.Before(p.End) || (p.CheckedAt != nil && now.Sub(*p.CheckedAt) < teamsAttendanceEvery) {
			continue
		}
		prefix := outlook.TeamsMeetingPrefix(r.tenant, mid)
		meetingKey := "attendance:item:" + userID + ":" + mid
		meetingFailure := func(source string) repo.TeamsFailure {
			return repo.TeamsFailure{Phase: "attendance", Kind: teamsFailItem, ResourceType: "user", ResourceID: userID,
				ItemIDs: map[string]string{"mid": mid, "source": source}}
		}
		if p.OnlineMeetingID == "" {
			if !lookup {
				continue
			}
			items, err := r.getAll(outlook.TeamsOnlineMeetingByJoinURL(userID, p.JoinURL))
			if err != nil {
				code := outlook.ClassifyTeamsError(err)
				if deny("attendance_lookup", outlook.TeamsAttendanceLookup, code) {
					r.clearFailure(meetingKey)
					_ = r.updateMeetingMeta(prefix, func(d map[string]interface{}) { d["attendance_status"], d["online_meeting_status"] = code, code })
					lookup = false
					continue
				}
				r.setFeature(teamsAttendanceFeature(outlook.TeamsAttendanceLookup), code, code)
				r.fail(meetingKey, meetingFailure(outlook.TeamsAttendanceLookup), err, false)
				ok = false
				continue
			}
			st.unblock("attendance_lookup")
			r.setFeature(teamsAttendanceFeature(outlook.TeamsAttendanceLookup), outlook.TeamsStatusOK, "")
			var om struct {
				ID string `json:"id"`
			}
			if len(items) > 0 {
				_ = json.Unmarshal(items[0], &om)
			}
			if om.ID == "" {
				r.clearFailure(meetingKey)
				_ = r.updateMeetingMeta(prefix, func(d map[string]interface{}) {
					d["attendance_status"], d["online_meeting_status"] = "not_found", "not_found"
				})
				p.CheckedAt = &now
				st.Attendance[mid] = p
				continue
			}
			if err := r.storeOnlineMeeting(userID, prefix, items[0]); err != nil {
				r.fail(meetingKey, meetingFailure("online_meeting"), err, false)
				ok = false
				continue
			}
			r.clearFailure(meetingKey)
			p.OnlineMeetingID = om.ID
			st.OnlineMeetings[om.ID] = mid
			st.Attendance[mid] = p
		}
		reports, err := r.getAll(outlook.TeamsAttendanceReportsURL(userID, p.OnlineMeetingID))
		if err != nil {
			code := outlook.ClassifyTeamsError(err)
			if deny("attendance", outlook.TeamsAttendanceReports, code) {
				r.clearFailure(meetingKey)
				_ = r.updateMeetingMeta(prefix, func(d map[string]interface{}) { d["attendance_status"] = code })
				return ok
			}
			r.setFeature(outlook.TeamsFeatureAttendance, code, code)
			r.fail(meetingKey, meetingFailure(outlook.TeamsAttendanceReports), err, false)
			ok = false
			continue
		}
		st.unblock("attendance")
		r.clearFailure(meetingKey)
		complete := true
		for _, rr := range reports {
			var rep struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(rr, &rep) != nil || rep.ID == "" {
				continue
			}
			key := outlook.TeamsMeetingAttendanceKey(prefix, rep.ID)
			if r.keys[key] {
				continue
			}
			failKey := "attendance:item:" + userID + ":" + p.OnlineMeetingID + ":" + rep.ID
			if err := r.storeAttendanceReport(userID, p.OnlineMeetingID, rep.ID, key); err != nil {
				complete = false
				if deny("attendance", outlook.TeamsAttendanceReports, outlook.ClassifyTeamsError(err)) {
					r.clearFailure(failKey)
					return ok
				}
				r.fail(failKey, repo.TeamsFailure{Phase: "attendance", Kind: teamsFailItem, ResourceType: "user", ResourceID: userID,
					ItemIDs: map[string]string{"meeting_id": p.OnlineMeetingID, "report_id": rep.ID, "mid": mid}}, err,
					outlook.TeamsVerified(outlook.TeamsCheckLookupAttendance))
				continue
			}
			r.clearFailure(failKey)
		}
		if complete {
			p.CheckedAt = &now
			r.setFeature("teams_meeting_attendance", outlook.TeamsStatusOK, "")
		}
		st.Attendance[mid] = p
		omid := p.OnlineMeetingID
		_ = r.updateMeetingMeta(prefix, func(d map[string]interface{}) {
			d["online_meeting_id"], d["attendance_status"], d["attendance_checked_at"] = omid, outlook.TeamsStatusOK, outlook.TeamsUTC(now)
		})
	}
	return ok
}

// teamsOnlineMeetingSecrets are the onlineMeeting fields never stored: the join passcode and the
// invite text that carries it, and the retired live-event report stream.
var teamsOnlineMeetingSecrets = []string{"joinInformation", "attendeeReport"}

// teamsOnlineMeeting is the part of an onlineMeeting the backup checks.
type teamsOnlineMeeting struct {
	ID           string `json:"id"`
	Participants *struct {
		Organizer *struct {
			Identity *struct {
				User *struct {
					ID       string `json:"id"`
					TenantID string `json:"tenantId"`
				} `json:"user"`
			} `json:"identity"`
		} `json:"organizer"`
	} `json:"participants"`
	ChatInfo *struct {
		ThreadID  string `json:"threadId"`
		MessageID string `json:"messageId"`
	} `json:"chatInfo"`
}

// organizer returns the organizer's user and tenant ids when Graph sent them.
func (m teamsOnlineMeeting) organizer() (string, string) {
	if m.Participants == nil || m.Participants.Organizer == nil || m.Participants.Organizer.Identity == nil || m.Participants.Organizer.Identity.User == nil {
		return "", ""
	}
	u := m.Participants.Organizer.Identity.User
	return u.ID, u.TenantID
}

// chatLink returns the meeting chat id from chatInfo: a meeting chat has no channel message
// (messageId empty or "0"); a channel meeting's threadId is its channel, not a chat.
func (m teamsOnlineMeeting) chatLink() (string, string) {
	switch {
	case m.ChatInfo == nil || strings.TrimSpace(m.ChatInfo.ThreadID) == "":
		return "", "not_provided"
	case m.ChatInfo.MessageID != "" && m.ChatInfo.MessageID != "0":
		return "", "channel_meeting"
	}
	return m.ChatInfo.ThreadID, outlook.TeamsStatusOK
}

// storeOnlineMeeting writes the organizer's onlineMeeting (as returned by the join-URL lookup,
// without passcode or invite text) to online_meeting.json when it changed, and records its status
// and meeting chat link in _meeting.json. An object whose organizer is another user or tenant is
// not stored.
func (r *teamsRun) storeOnlineMeeting(organizerID, prefix string, raw json.RawMessage) error {
	var m teamsOnlineMeeting
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if uid, tid := m.organizer(); (uid != "" && uid != organizerID) || (tid != "" && tid != r.tenant) {
		return r.updateMeetingMeta(prefix, func(d map[string]interface{}) { d["online_meeting_status"] = "organizer_mismatch" })
	}
	for _, k := range teamsOnlineMeetingSecrets {
		delete(doc, k)
	}
	if settings, ok := doc["joinMeetingIdSettings"]; ok {
		var s map[string]json.RawMessage
		if json.Unmarshal(settings, &s) == nil && s != nil {
			delete(s, "passcode")
			doc["joinMeetingIdSettings"], _ = json.Marshal(s)
		}
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	key := outlook.TeamsMeetingOnlineMeetingKey(prefix)
	same := false
	if r.keys[key] {
		old, err := r.store.download(r.ctx, key)
		if err != nil && !errors.Is(err, errTeamsNotFound) {
			return err
		}
		var stored map[string]json.RawMessage
		if err == nil && json.Unmarshal(old, &stored) == nil {
			delete(stored, "storx_backup")
			b, _ := json.Marshal(stored)
			same = string(b) == string(body)
		}
	}
	if !same {
		patched, err := outlook.PatchTeamsBackup(body, outlook.TeamsBackupBlock{TenantID: r.tenant, Source: "onlineMeetings", BackedUpAt: outlook.TeamsUTC(r.now())})
		if err != nil {
			return err
		}
		if err := r.put(key, patched, nil); err != nil {
			return err
		}
	}
	chatID, link := m.chatLink()
	return r.updateMeetingMeta(prefix, func(d map[string]interface{}) {
		d["online_meeting_id"], d["online_meeting_status"], d["chat_link_status"] = m.ID, outlook.TeamsStatusOK, link
		delete(d, "chat_id")
		delete(d, "chat_prefix")
		if chatID != "" {
			d["chat_id"], d["chat_prefix"] = chatID, outlook.TeamsChatPrefix(r.tenant, chatID)
		}
	})
}

func (r *teamsRun) storeAttendanceReport(organizerID, meetingID, reportID, key string) error {
	raw, err := r.getJSON(outlook.TeamsAttendanceReportURL(organizerID, meetingID, reportID))
	if err != nil {
		return err
	}
	body, err := outlook.PatchTeamsBackup(raw, outlook.TeamsBackupBlock{TenantID: r.tenant, Source: "attendanceReports", BackedUpAt: outlook.TeamsUTC(r.now())})
	if err != nil {
		return err
	}
	return r.put(key, body, nil)
}

// syncArtifactListing reads an organizer's transcript or recording metadata (never content) and
// records it in the meeting's _meeting.json. A meeting found only here gets a folder with
// calendar_status not_found.
func (r *teamsRun) syncArtifactListing(userID string, st *teamsUserState, kind string, deadline time.Time, save func() error) string {
	ls, first, feature := &st.Transcripts, outlook.TeamsAllTranscriptsDeltaURL(userID), "teams_meeting_transcripts"
	statusField := "transcript_metadata_status"
	if kind == "recordings" {
		ls, first, feature = &st.Recordings, outlook.TeamsAllRecordingsDeltaURL(userID), "teams_meeting_recordings"
		statusField = "recording_metadata_status"
	}
	if !r.consented(feature) || st.skipped(kind, r.now()) {
		return teamsComplete
	}
	if ls.DeltaLink != "" {
		if outlook.NewTeamsPageGuard(first).Delta(ls.DeltaLink) == nil {
			first = ls.DeltaLink
		} else {
			ls.DeltaLink = ""
		}
	}
	windowKey := kind + "_meta:window:" + userID
	outcome, delta, err := r.pageThrough(teamsPager{
		pageLink: &ls.PageLink, first: first, dirty: &ls.Dirty, deadline: deadline,
		streamKey: kind + "_meta:stream:user:" + userID,
		stream:    repo.TeamsFailure{Phase: kind + "_meta", Kind: teamsFailStream, ResourceType: "user", ResourceID: userID},
		save:      save,
		item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
			err := r.storeArtifact(userID, st, kind, statusField, raw)
			return windowKey, repo.TeamsFailure{Phase: kind + "_meta", Kind: teamsFailWindow, ResourceType: "user", ResourceID: userID}, err, false
		},
	})
	status, _ := r.pageStatus(st, kind, feature, outcome, err, func() { ls.DeltaLink, ls.PageLink = "", "" })
	if outcome != teamsPageDone {
		return status
	}
	closed, gaps := r.endRound(teamsRound{&ls.Dirty, &ls.Held, &ls.HeldSince, kind + "_meta:stream:user:" + userID}, func() []string { return []string{windowKey} })
	if !closed {
		return teamsIncomplete
	}
	if delta != "" {
		ls.DeltaLink = delta
	}
	r.setFeature(feature, outlook.TeamsStatusOK, "")
	if gaps {
		return teamsCompleteWithGaps
	}
	return teamsComplete
}

func (r *teamsRun) storeArtifact(userID string, st *teamsUserState, kind, statusField string, raw json.RawMessage) error {
	var row map[string]interface{}
	if err := json.Unmarshal(raw, &row); err != nil {
		return err
	}
	if _, removed := row["@removed"]; removed {
		return nil
	}
	id, _ := row["id"].(string)
	meetingID, _ := row["meetingId"].(string)
	if id == "" || meetingID == "" {
		return fmt.Errorf("artifact row without id or meetingId")
	}
	for _, k := range []string{"transcriptContentUrl", "recordingContentUrl", "content", "metadataContent"} {
		delete(row, k)
	}
	mid := st.OnlineMeetings[meetingID]
	artifactOnly := false
	if mid == "" {
		mid, _, _ = outlook.TeamsMeetingID("", userID, "onlineMeeting:"+meetingID, false)
		artifactOnly = true
	}
	prefix := outlook.TeamsMeetingPrefix(r.tenant, mid)
	if kind == "recordings" {
		row["file"] = outlook.TeamsFileRef{BackupStatus: outlook.TeamsFileUnresolved, CheckedAt: outlook.TeamsUTC(r.now())}
	}
	return r.updateMeetingMeta(prefix, func(doc map[string]interface{}) {
		if artifactOnly {
			if _, ok := doc["mid"]; !ok {
				doc["mid"], doc["kind"], doc["calendar_status"] = mid, "single", "not_found"
				doc["identity"] = map[string]interface{}{"kind": "separate", "reason": "artifact_only"}
			}
		}
		doc["online_meeting_id"] = meetingID
		doc[statusField] = outlook.TeamsStatusOK
		list, _ := doc[kind].([]interface{})
		for i, item := range list {
			if m, ok := item.(map[string]interface{}); ok && m["id"] == id {
				list[i] = row
				doc[kind] = list
				teamsMeetingDefaults(doc)
				return
			}
		}
		doc[kind] = append(list, row)
		teamsMeetingDefaults(doc)
	})
}

// writeReconcile lists, per meeting folder, the other folders holding a copy with the same
// iCalUId (kept apart until merging is confirmed for that case).
func (r *teamsRun) writeReconcile() {
	icals := make([]string, 0, len(r.icalMids))
	for ical, mids := range r.icalMids {
		if len(mids) > 1 {
			icals = append(icals, ical)
		}
	}
	sort.Strings(icals)
	for _, ical := range icals {
		for _, mid := range r.icalMids[ical] {
			key := outlook.TeamsReconcileMeetingKey(r.tenantPrefix, mid)
			candidates := map[string]bool{}
			if r.keys[key] {
				if raw, err := r.store.download(r.ctx, key); err == nil {
					var doc struct {
						Candidates []string `json:"candidates"`
					}
					if json.Unmarshal(raw, &doc) == nil {
						for _, c := range doc.Candidates {
							candidates[c] = true
						}
					}
				}
			}
			before := len(candidates)
			for _, other := range r.icalMids[ical] {
				if other != mid {
					candidates[other] = true
				}
			}
			if r.keys[key] && len(candidates) == before {
				continue
			}
			list := make([]string, 0, len(candidates))
			for c := range candidates {
				list = append(list, c)
			}
			sort.Strings(list)
			b, _ := json.Marshal(map[string]interface{}{"mid": mid, "ical_uid": ical, "reason": "case_unverified", "candidates": list, "updated_at": outlook.TeamsUTC(r.now())})
			_ = r.put(key, b, nil)
		}
	}
}

// runCallsPhase backs up call records, then PSTN and Direct Routing logs.
func (r *teamsRun) runCallsPhase(deadline time.Time) bool {
	if r.singleTeam != "" {
		return true
	}
	if _, err := r.scopeUsers(); err != nil && len(r.sel.UserIDs) > 0 {
		return false
	}
	done := r.syncCallRecords(deadline)
	for _, kind := range []string{outlook.TeamsKeyPstn, outlook.TeamsKeyDirectRouting} {
		if r.stopped() || !r.now().Before(deadline) {
			return false
		}
		done = r.syncPhoneLogs(kind, deadline) && done
	}
	return done
}

func (r *teamsRun) syncCallRecords(deadline time.Time) bool {
	if !r.consented(outlook.TeamsFeatureCallRecords) {
		return true
	}
	if r.mem.TeamsCalls == nil {
		r.mem.TeamsCalls = &repo.TeamsWindowState{}
	}
	ws := r.mem.TeamsCalls
	now := r.now()
	oldest := now.Add(-teamsCallsRetention)
	if ws.LastSuccessAt != nil && now.Sub(*ws.LastSuccessAt) > teamsCallsWarnAfter {
		r.warn("No successful calls backup for over 25 days; call records older than 30 days are lost if the calls phase doesn't run")
	}
	if ws.SkipUntil != nil && now.Before(*ws.SkipUntil) {
		r.setResource("calls", r.tenant, teamsPhaseCalls, teamsBlocked, ws.StatusCode)
		return true
	}
	if ws.HighWater != nil && ws.HighWater.Before(oldest) {
		r.addGap("calls_gap", "", *ws.HighWater, oldest)
	}
	kept := ws.Windows[:0]
	for _, w := range ws.Windows {
		if w.From.Before(oldest) {
			end := w.To
			if end.After(oldest) {
				end = oldest
			}
			r.addGap("calls_gap", "", w.From, end)
			if !w.To.After(oldest) {
				continue
			}
			w.From, w.PageLink = oldest, ""
		}
		kept = append(kept, w)
	}
	ws.Windows = kept
	r.planWindow(ws, oldest, 0)

	status := teamsComplete
	for i := 0; i < len(ws.Windows); {
		w := &ws.Windows[i]
		outcome, _, err := r.pageThrough(teamsPager{
			pageLink: &w.PageLink, first: outlook.TeamsCallRecordsURL(w.From.Add(-teamsCallsOverlap), w.To), dirty: &w.Dirty, deadline: deadline,
			streamKey: "call_records:stream:tenant:" + r.tenant,
			stream:    repo.TeamsFailure{Phase: "call_records", Kind: teamsFailStream, ResourceType: "tenant", ResourceID: r.tenant},
			save:      func() error { return nil },
			item:      r.callRecordItem,
		})
		if outcome == teamsPageFailed {
			code := outlook.ClassifyTeamsError(err)
			r.setFeature("teams_call_records", code, code)
			if outlook.IsTeamsPermanentStatus(code) {
				skip := now.Add(teamsPermanentBackoff)
				ws.SkipUntil, ws.StatusCode = &skip, code
				r.setResource("calls", r.tenant, teamsPhaseCalls, teamsBlocked, code)
				return true
			}
		}
		if outcome != teamsPageDone {
			r.setResource("calls", r.tenant, teamsPhaseCalls, teamsIncomplete, outlook.ClassifyTeamsError(err))
			return false
		}
		closed, gaps := r.endRound(teamsRound{&w.Dirty, &w.Held, &w.HeldSince, "call_records:stream:tenant:" + r.tenant}, func() []string {
			return append(r.failureKeysWithPrefix("call_records:item:"), r.failureKeysWithPrefix("call_details:item:")...)
		})
		if gaps {
			status = teamsWorst(status, teamsCompleteWithGaps)
		}
		if !closed {
			status = teamsWorst(status, teamsIncomplete)
			i++
			continue
		}
		if ws.HighWater == nil || w.To.After(*ws.HighWater) {
			hw := w.To
			ws.HighWater = &hw
		}
		ws.Windows = append(ws.Windows[:i], ws.Windows[i+1:]...)
	}
	if status != teamsIncomplete {
		ws.LastSuccessAt, ws.SkipUntil, ws.StatusCode = &now, nil, ""
		r.setFeature("teams_call_records", outlook.TeamsStatusOK, "")
	}
	r.setResource("calls", r.tenant, teamsPhaseCalls, status, "")
	return status != teamsIncomplete
}

// planWindow adds the next window [high water, run end) (split into chunks of at most chunk when
// chunk > 0) unless one already reaches the run end.
func (r *teamsRun) planWindow(ws *repo.TeamsWindowState, earliest time.Time, chunk time.Duration) {
	from := earliest
	if ws.HighWater != nil && ws.HighWater.After(from) {
		from = *ws.HighWater
	}
	for _, w := range ws.Windows {
		if !w.To.Before(r.to) {
			return
		}
		if w.To.After(from) {
			from = w.To
		}
	}
	for from.Before(r.to) {
		if len(ws.Windows) >= teamsMaxOpenWindows {
			r.warn("Too many held call or phone-log windows; newer records wait until they close")
			return
		}
		to := r.to
		if chunk > 0 && to.Sub(from) > chunk {
			to = from.Add(chunk)
		}
		ws.Windows = append(ws.Windows, repo.TeamsWindow{From: from, To: to})
		from = to
	}
}

// callRecordItem stores one call record with all its participants and sessions. A record
// already stored is fetched again only when its version or lastModifiedDateTime changed or it
// was stored incomplete.
func (r *teamsRun) callRecordItem(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
	var rec struct {
		ID           string          `json:"id"`
		Version      json.RawMessage `json:"version"`
		LastModified string          `json:"lastModifiedDateTime"`
		Start        string          `json:"startDateTime"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || rec.ID == "" {
		return "", repo.TeamsFailure{}, nil, false
	}
	version := strings.Trim(string(rec.Version), `"`)
	key := outlook.TeamsCallRecordKey(r.tenant, rec.ID, rec.Start)
	f := repo.TeamsFailure{Phase: "call_records", Kind: teamsFailItem, ResourceType: "tenant", ResourceID: r.tenant, ItemIDs: map[string]string{"call_record_id": rec.ID}}
	lookup := outlook.TeamsVerified(outlook.TeamsCheckLookupCallRecord)
	if r.keys[key] {
		if meta, err := r.store.stat(r.ctx, key); err == nil &&
			meta[outlook.TeamsMetaETag] == version && meta[outlook.TeamsMetaModified] == rec.LastModified && meta[outlook.TeamsMetaComplete] == "true" {
			r.count("skipped")
			return "call_records:item:" + rec.ID, f, nil, lookup
		}
	}
	complete, err := r.storeCallRecord(rec.ID, rec.Start, version, rec.LastModified)
	if err != nil {
		return "call_records:item:" + rec.ID, f, err, lookup
	}
	r.clearFailure("call_records:item:" + rec.ID)
	f.Phase = "call_details"
	if !complete {
		return "call_details:item:" + rec.ID, f, fmt.Errorf("call record details incomplete"), lookup
	}
	return "call_details:item:" + rec.ID, f, nil, lookup
}

func (r *teamsRun) storeCallRecord(id, start, version, modified string) (bool, error) {
	record, err := r.getJSON(outlook.TeamsCallRecordURL(id))
	if err != nil {
		return false, err
	}
	completion := map[string]string{"record": "complete", "participants": "complete", "sessions": "complete", "segments": "complete"}
	participants, perr := r.getAll(outlook.TeamsCallParticipantsURL(id))
	if perr != nil {
		completion["participants"] = teamsCallPartStatus(perr)
	}
	sessions, serr := r.getAll(outlook.TeamsCallSessionsURL(id))
	if serr != nil {
		completion["sessions"], completion["segments"] = teamsCallPartStatus(serr), teamsCallPartStatus(serr)
	} else {
		for _, s := range sessions {
			if strings.Contains(string(s), "segments@odata.nextLink") {
				completion["segments"] = "incomplete"
			}
		}
	}
	users := map[string]bool{}
	collect := func(raw []byte) {
		var p struct {
			Identity struct {
				User *struct {
					ID string `json:"id"`
				} `json:"user"`
			} `json:"identity"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Identity.User != nil && p.Identity.User.ID != "" {
			users[p.Identity.User.ID] = true
		}
	}
	for _, p := range participants {
		collect(p)
	}
	var head struct {
		Organizer json.RawMessage `json:"organizer_v2"`
		JoinURL   string          `json:"joinWebUrl"`
		// participants (identitySets) is Graph's documented fallback when participants_v2 is incomplete.
		Participants []struct {
			User *struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"participants"`
	}
	_ = json.Unmarshal(record, &head)
	if len(head.Organizer) > 0 {
		collect(head.Organizer)
	}
	for _, p := range head.Participants {
		if p.User != nil && p.User.ID != "" {
			users[p.User.ID] = true
		}
	}
	if len(r.sel.UserIDs) > 0 {
		inScope := false
		for u := range users {
			if r.userScope[u] {
				inScope = true
				break
			}
		}
		if !inScope {
			if perr != nil {
				// A selected user may be on a participant page that wasn't read.
				r.count("scope_unknown")
				return false, nil
			}
			r.count("out_of_scope")
			return true, nil
		}
	}
	complete := true
	for _, v := range completion {
		if v != "complete" {
			complete = false
		}
	}
	doc := map[string]interface{}{
		"record": json.RawMessage(record), "participants": participants, "sessions": sessions,
		"storx_backup": outlook.TeamsBackupBlock{TenantID: r.tenant, Source: "callRecords", BackedUpAt: outlook.TeamsUTC(r.now()), Completion: completion},
	}
	if head.JoinURL != "" && outlook.TeamsVerified(outlook.TeamsCheckCallJoinWebURL) {
		doc["meeting_link"] = map[string]string{"join_web_url": head.JoinURL, "matched_by": "join_web_url"}
	} else {
		doc["meeting_link"] = map[string]string{"status": "not_verified"}
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}
	// Pointers go first: a stored complete record is skipped on replay, so a pointer written
	// after it would never be retried.
	key := outlook.TeamsCallRecordKey(r.tenant, id, start)
	for u := range users {
		if r.userScope[u] {
			if err := r.putPointer(outlook.TeamsUserCallPointerKey(outlook.TeamsUserPrefix(r.tenant, u), id, start), key, nil); err != nil {
				return false, err
			}
		}
	}
	if err := r.put(key, body, map[string]string{
		outlook.TeamsMetaETag: version, outlook.TeamsMetaModified: modified, outlook.TeamsMetaComplete: strconv.FormatBool(complete),
	}); err != nil {
		return false, err
	}
	r.count("written")
	return complete, nil
}

// teamsCallPartStatus is the completion of a call record part whose listing stopped: incomplete
// when a continuation link was refused by the page guard (never requested), failed otherwise.
func teamsCallPartStatus(err error) string {
	if errors.Is(err, outlook.ErrTeamsPaging) {
		return "incomplete"
	}
	return "failed"
}

// syncPhoneLogs backs up PSTN or Direct Routing rows in chunks; a chunk closes only after all its
// pages were stored, and a row that fails holds its chunk (no get-by-id exists).
func (r *teamsRun) syncPhoneLogs(kind string, deadline time.Time) bool {
	ws, phase, feature := &r.mem.TeamsPstn, "pstn", "teams_pstn_calls"
	if kind == outlook.TeamsKeyDirectRouting {
		ws, phase, feature = &r.mem.TeamsDirect, "direct_routing", "teams_direct_routing_calls"
	}
	if !r.consented(feature) {
		return true
	}
	if *ws == nil {
		*ws = &repo.TeamsWindowState{}
	}
	s := *ws
	now := r.now()
	if s.SkipUntil != nil && now.Before(*s.SkipUntil) {
		r.setResource(phase, r.tenant, teamsPhaseCalls, teamsBlocked, s.StatusCode)
		return true
	}
	r.planWindow(s, now.Add(-teamsPhoneLookback), teamsPhoneChunk)
	status, rows := teamsComplete, 0
	for i := 0; i < len(s.Windows); {
		w := &s.Windows[i]
		windowKey := phase + ":window:" + outlook.TeamsUTC(w.From)
		from := w.From
		outcome, _, err := r.pageThrough(teamsPager{
			pageLink: &w.PageLink, first: outlook.TeamsPhoneLogsURL(kind, w.From.Add(-teamsPhoneOverlap), w.To), dirty: &w.Dirty, deadline: deadline,
			streamKey: phase + ":stream:tenant:" + r.tenant,
			stream:    repo.TeamsFailure{Phase: phase, Kind: teamsFailStream, ResourceType: "tenant", ResourceID: r.tenant},
			save:      func() error { return nil },
			item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
				rows++
				err := r.storePhoneRow(kind, raw, from)
				return windowKey, repo.TeamsFailure{Phase: phase, Kind: teamsFailWindow, ResourceType: "tenant", ResourceID: r.tenant,
					ItemIDs: map[string]string{"from": outlook.TeamsUTC(from)}}, err, false
			},
		})
		if outcome == teamsPageFailed {
			code := outlook.ClassifyTeamsError(err)
			r.setFeature(feature, code, code)
			if outlook.IsTeamsPermanentStatus(code) {
				skip := now.Add(teamsPermanentBackoff)
				s.SkipUntil, s.StatusCode = &skip, code
				r.setResource(phase, r.tenant, teamsPhaseCalls, teamsBlocked, code)
				return true
			}
		}
		if outcome != teamsPageDone {
			r.setResource(phase, r.tenant, teamsPhaseCalls, teamsIncomplete, "")
			return false
		}
		closed, gaps := r.endRound(teamsRound{&w.Dirty, &w.Held, &w.HeldSince, phase + ":stream:tenant:" + r.tenant}, func() []string { return []string{windowKey} })
		if gaps {
			status = teamsWorst(status, teamsCompleteWithGaps)
			r.addGap(phase+"_gap", "", w.From, w.To)
		}
		if !closed {
			status = teamsWorst(status, teamsIncomplete)
			i++
			continue
		}
		if s.HighWater == nil || w.To.After(*s.HighWater) {
			hw := w.To
			s.HighWater = &hw
		}
		s.Windows = append(s.Windows[:i], s.Windows[i+1:]...)
	}
	if status != teamsIncomplete {
		s.LastSuccessAt, s.SkipUntil, s.StatusCode = &now, nil, ""
		capability := outlook.TeamsStatusOK
		if rows == 0 {
			capability = outlook.TeamsStatusOKEmpty
		}
		r.setFeature(feature, capability, "")
	}
	r.setResource(phase, r.tenant, teamsPhaseCalls, status, "")
	return status != teamsIncomplete
}

// teamsPhoneRowKey is the row id when it is proven stable, else a hash of the call's identity.
func teamsPhoneRowKey(id, callID, userID, start, callType string) string {
	if id != "" && outlook.TeamsVerified(outlook.TeamsCheckPstnRowIDStable) {
		return id
	}
	sum := sha1.Sum([]byte(callID + "|" + userID + "|" + start + "|" + callType))
	return hex.EncodeToString(sum[:])
}

func (r *teamsRun) storePhoneRow(kind string, raw json.RawMessage, chunkFrom time.Time) error {
	var row struct {
		ID       string `json:"id"`
		CallID   string `json:"callId"`
		UserID   string `json:"userId"`
		Start    string `json:"startDateTime"`
		CallType string `json:"callType"`
	}
	_ = json.Unmarshal(raw, &row)
	var key, rowKey string
	if _, ok := outlook.ParseTeamsTime(row.Start); !ok || (row.ID == "" && row.CallID == "") {
		key = outlook.TeamsPhoneLogUnparsedKey(r.tenant, kind, chunkFrom, raw)
	} else {
		rowKey = teamsPhoneRowKey(row.ID, row.CallID, row.UserID, row.Start, row.CallType)
		key = outlook.TeamsPhoneLogKey(r.tenant, kind, rowKey, row.Start)
	}
	if r.keys[key] {
		r.count("skipped")
	} else {
		body, err := outlook.PatchTeamsBackup(raw, outlook.TeamsBackupBlock{TenantID: r.tenant, Source: kind, BackedUpAt: outlook.TeamsUTC(r.now())})
		if err != nil {
			body = raw
		}
		if err := r.put(key, body, nil); err != nil {
			return err
		}
		r.count("written")
	}
	if rowKey != "" && row.UserID != "" && r.userScope[row.UserID] {
		return r.putPointer(outlook.TeamsUserCallPointerKey(outlook.TeamsUserPrefix(r.tenant, row.UserID), rowKey, row.Start), key, nil)
	}
	return nil
}

// runRetriesPhase retries failed items by their own lookup (only lookups the tenant checks
// verified), then re-checks file references.
func (r *teamsRun) runRetriesPhase(deadline time.Time) bool {
	keys := make([]string, 0, len(r.mem.TeamsFailures))
	for k, f := range r.mem.TeamsFailures {
		if f.Kind == teamsFailItem && teamsLookupVerified(f.Phase) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return r.mem.TeamsFailures[keys[i]].LastAt.Before(r.mem.TeamsFailures[keys[j]].LastAt)
	})
	done := true
	for _, k := range keys {
		if r.stopped() || !r.now().Before(deadline) {
			done = false
			break
		}
		f := r.mem.TeamsFailures[k]
		if f.SkipUntil != nil && r.now().Before(*f.SkipUntil) {
			continue
		}
		err := r.retryItem(f)
		if r.stopErr != nil {
			return false
		}
		if err == nil {
			r.clearFailure(k)
			r.count("retried")
			continue
		}
		r.fail(k, f, err, true)
		if outlook.ClassifyTeamsError(err) == outlook.TeamsStatusNotFound {
			f = r.mem.TeamsFailures[k]
			if f.Status != teamsPermanentFailure {
				f.Status = teamsNotFoundPending
			}
			r.mem.TeamsFailures[k] = f
		}
	}
	_ = r.checkpoint()
	if !r.recheckFiles(deadline) {
		done = false
	}
	return done
}

func teamsLookupVerified(phase string) bool {
	switch phase {
	case "channels":
		return outlook.TeamsVerified(outlook.TeamsCheckLookupChannelMessage)
	case "chats":
		return outlook.TeamsVerified(outlook.TeamsCheckLookupChatMessage)
	case "calendar":
		return outlook.TeamsVerified(outlook.TeamsCheckLookupEvent)
	case "call_records", "call_details":
		return outlook.TeamsVerified(outlook.TeamsCheckLookupCallRecord)
	case "attendance":
		return outlook.TeamsVerified(outlook.TeamsCheckLookupAttendance)
	}
	return false
}

func (r *teamsRun) retryItem(f repo.TeamsFailure) error {
	ids := f.ItemIDs
	switch f.Phase {
	case "channels":
		u := outlook.TeamsChannelMessageURL(ids["team_id"], ids["channel_id"], ids["message_id"])
		if ids["root_id"] != "" && ids["root_id"] != ids["message_id"] {
			u = outlook.TeamsChannelReplyURL(ids["team_id"], ids["channel_id"], ids["root_id"], ids["message_id"])
		}
		raw, err := r.getJSON(u)
		if err != nil {
			return err
		}
		_, err = r.storeChannelMessage(ids["team_id"], outlook.TeamsTeamPrefix(r.tenant, ids["team_id"]), raw)
		return err
	case "chats":
		raw, err := r.getJSON(outlook.TeamsChatMessageURL(ids["chat_id"], ids["message_id"]))
		if err != nil {
			return err
		}
		var head teamsMessageHead
		if err := json.Unmarshal(raw, &head); err != nil {
			return err
		}
		if head.ChatID == "" {
			head.ChatID = ids["chat_id"]
		}
		_, err = r.storeChatMessage(raw, head)
		return err
	case "calendar":
		raw, err := r.getJSON(outlook.TeamsUserEventURL(f.ResourceID, ids["event_id"]))
		if err != nil {
			return err
		}
		var ev teamsEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		if !ev.isTeamsMeeting() {
			return nil
		}
		st, save := r.loadUserState(f.ResourceID)
		for _, m := range []*map[string]string{&st.EventMids, &st.Pointers, &st.OnlineMeetings} {
			if *m == nil {
				*m = map[string]string{}
			}
		}
		if st.Attendance == nil {
			st.Attendance = map[string]teamsPendingAttendance{}
		}
		if err := r.storeMeetingEvent(f.ResourceID, st, raw, ev); err != nil {
			return err
		}
		return save()
	case "call_records", "call_details":
		raw, err := r.getJSON(outlook.TeamsCallRecordURL(ids["call_record_id"]))
		if err != nil {
			return err
		}
		_, _, ierr, _ := r.callRecordItem(raw)
		return ierr
	case "attendance":
		prefix := outlook.TeamsMeetingPrefix(r.tenant, ids["mid"])
		return r.storeAttendanceReport(f.ResourceID, ids["meeting_id"], ids["report_id"], outlook.TeamsMeetingAttendanceKey(prefix, ids["report_id"]))
	}
	return fmt.Errorf("no lookup for %s", f.Phase)
}

// File contract: Teams never stores file bytes. A shared file or recording is "backed up" only
// when the owning OneDrive, SharePoint or Groups job holds it under a key whose parsed item id
// and object metadata match (owner, drive and key all checked).

type teamsFilesDoc struct {
	UpdatedAt     string                  `json:"updated_at"`
	MissingOwners []teamsMissingOwner     `json:"missing_owners,omitempty"`
	Refs          map[string]teamsFileReg `json:"refs,omitempty"`
}

type teamsMissingOwner struct {
	Service string `json:"service"`
	ID      string `json:"id"`
	Files   int    `json:"files"`
}

// teamsFileReg is one referenced file and the Teams objects that reference it.
type teamsFileReg struct {
	Ref     outlook.TeamsFileRef `json:"ref"`
	Objects []string             `json:"objects,omitempty"`
}

// Owner-backup reads of the file contract (overridden in tests).
var (
	teamsOwnerKeysFn = func(ctx context.Context, in ProcessorInput, storx, bucket, prefix, userID, method string) (map[string]bool, error) {
		return handler.GetSyncedObjectsWithPrefix(ctx, in.Database, storx, bucket, prefix, userID, "outlook", method, in.StorxRecovery)
	}
	teamsOwnerStatFn = func(ctx context.Context, storx, bucket, key string) (map[string]string, error) {
		obj, err := satellite.StatObject(ctx, storx, bucket, key)
		if err != nil {
			return nil, err
		}
		return obj.Custom, nil
	}
)

type teamsOwnerIndex struct {
	job     *repo.CronJobListingDB
	storx   string
	bucket  string
	prefix  string
	service string
	items   map[string]string
	legacy  []string
}

type teamsFileResolver struct {
	r       *teamsRun
	cache   map[string]outlook.TeamsFileRef
	owners  map[string]*teamsOwnerIndex
	sites   []repo.CronJobListingDB
	drives  map[string]string
	driveOf map[string]string
	doc     *teamsFilesDoc
	dirty   bool
}

func (r *teamsRun) fileResolver() *teamsFileResolver {
	if r.files == nil {
		f := &teamsFileResolver{r: r, cache: map[string]outlook.TeamsFileRef{}, owners: map[string]*teamsOwnerIndex{},
			drives: map[string]string{}, driveOf: map[string]string{}, doc: &teamsFilesDoc{}}
		r.loadState(outlook.TeamsFilesCoverageKey(r.tenantPrefix), f.doc)
		if f.doc.Refs == nil {
			f.doc.Refs = map[string]teamsFileReg{}
		}
		r.files = f
	}
	return r.files
}

// messageFiles resolves the files and recordings a message references.
func (r *teamsRun) messageFiles(head teamsMessageHead, objectKey string) []outlook.TeamsFileRef {
	var urls []string
	for _, a := range head.Attachments {
		if strings.EqualFold(a.ContentType, "reference") && a.ContentURL != "" {
			urls = append(urls, a.ContentURL)
		}
	}
	if head.EventDetail != nil && head.EventDetail.CallRecordingURL != "" {
		urls = append(urls, head.EventDetail.CallRecordingURL)
	}
	if len(urls) == 0 {
		return nil
	}
	f := r.fileResolver()
	refs := make([]outlook.TeamsFileRef, 0, len(urls))
	for _, u := range urls {
		ref, ok := f.cache[u]
		if !ok {
			ref = f.resolve(u)
			f.cache[u] = ref
		}
		ref.SourceMessageID = head.ID
		refs = append(refs, ref)
		f.register(ref, objectKey)
	}
	return refs
}

func teamsFileRegKey(ref outlook.TeamsFileRef) string {
	if ref.DriveID != "" && ref.ItemID != "" {
		return ref.DriveID + ":" + ref.ItemID
	}
	sum := sha1.Sum([]byte(ref.WebURL))
	return "url:" + hex.EncodeToString(sum[:])
}

func (f *teamsFileResolver) register(ref outlook.TeamsFileRef, objectKey string) {
	k := teamsFileRegKey(ref)
	reg, ok := f.doc.Refs[k]
	if !ok && len(f.doc.Refs) >= teamsMaxFileRefs {
		f.r.warn("Too many Teams file references to re-check; older ones are checked when opened")
		return
	}
	if ok && reg.Ref.FirstSeenAt != "" {
		ref.FirstSeenAt = reg.Ref.FirstSeenAt
	}
	reg.Ref = ref
	if objectKey != "" {
		reg.Objects = appendUnique(reg.Objects, objectKey)
	}
	f.doc.Refs[k] = reg
	f.dirty = true
}

// resolve runs the five checks for one file link.
func (f *teamsFileResolver) resolve(webURL string) outlook.TeamsFileRef {
	now := outlook.TeamsUTC(f.r.now())
	ref := outlook.TeamsFileRef{WebURL: webURL, FirstSeenAt: now, CheckedAt: now, BackupStatus: outlook.TeamsFileUnresolved}
	raw, err := f.r.getJSON(outlook.TeamsSharedDriveItemURL(webURL))
	if err != nil {
		return ref
	}
	var item struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		ETag   string `json:"eTag"`
		Parent struct {
			DriveID   string `json:"driveId"`
			DriveType string `json:"driveType"`
			SiteID    string `json:"siteId"`
		} `json:"parentReference"`
	}
	if json.Unmarshal(raw, &item) != nil || item.ID == "" || item.Parent.DriveID == "" {
		return ref
	}
	ref.Name, ref.DriveID, ref.ItemID, ref.SourceETag = item.Name, item.Parent.DriveID, item.ID, item.ETag
	f.verify(&ref, item.Parent.DriveType, item.Parent.SiteID)
	return ref
}

// verify runs steps 2-5 (owner job, drive identity, exact key, object metadata) on a resolved ref.
func (f *teamsFileResolver) verify(ref *outlook.TeamsFileRef, driveType, siteID string) {
	ref.BackupKey, ref.BackupBucket, ref.OwnerJobID, ref.BackupETag, ref.VerifiedAt = "", "", 0, "", ""
	var idx *teamsOwnerIndex
	switch driveType {
	case "business", "personal":
		owner := f.driveOwner(ref.DriveID)
		if owner == "" {
			ref.BackupStatus = outlook.TeamsFileUnresolved
			return
		}
		ref.Owner = "onedrive:" + owner
		in := f.r.input
		job, err := in.Database.CronJobRepo.FindMicrosoftResourceJob(in.Job.UserID, in.Job.TenantID, repo.ResourceTypeUser, owner, "outlook_onedrive", in.Job.SyncType)
		if err != nil {
			ref.BackupStatus = outlook.TeamsFileUnresolved
			return
		}
		if job == nil || !job.Active {
			ref.BackupStatus = outlook.TeamsFileOwnerNotBackedUp
			return
		}
		if f.userDrive(owner) != ref.DriveID {
			ref.BackupStatus = outlook.TeamsFileDriveNotBackedUp
			return
		}
		idx = f.ownerIndex("onedrive", job)
	default:
		ref.Owner = "sharepoint:" + siteID
		job, backsDrive, err := f.siteJob(ref.DriveID, siteID)
		if err != nil {
			ref.BackupStatus = outlook.TeamsFileUnresolved
			return
		}
		if job == nil {
			ref.BackupStatus = outlook.TeamsFileOwnerNotBackedUp
			return
		}
		ref.Owner = "sharepoint:" + job.ResourceID
		if !backsDrive {
			ref.BackupStatus = outlook.TeamsFileDriveNotBackedUp
			return
		}
		idx = f.ownerIndex("sharepoint", job)
	}
	if idx.storx == "" {
		ref.BackupStatus = outlook.TeamsFileKeyUnverified
		return
	}
	prefix := idx.prefix
	if idx.service == "sharepoint" && ref.DriveID != repo.JobSharePointDriveID(idx.job) {
		prefix = outlook.SharePointLibraryPrefix(idx.prefix, ref.DriveID)
	}
	key := idx.items[prefix+"|"+ref.ItemID]
	if key == "" && prefix == idx.prefix {
		for _, k := range idx.legacy {
			leaf := k[strings.LastIndex(k, "/")+1:]
			if strings.HasPrefix(leaf, ref.ItemID+"_") {
				key = k
				break
			}
		}
	}
	if key == "" {
		ref.BackupStatus = outlook.TeamsFileNotBackedUp
		return
	}
	meta, err := teamsOwnerStatFn(f.r.ctx, idx.storx, idx.bucket, key)
	if err != nil {
		ref.BackupStatus = outlook.TeamsFileKeyUnverified
		return
	}
	if meta[outlook.OneDriveMetaItemID] != ref.ItemID || (idx.service != "onedrive" && meta[outlook.OneDriveMetaDriveID] != ref.DriveID) {
		ref.BackupStatus = outlook.TeamsFileKeyUnverified
		return
	}
	ref.BackupStatus = outlook.TeamsFileBackedUp
	if k, ok := outlook.ParseOneDriveObjectKey(key); ok && k.Section == outlook.OneDriveSectionBin {
		ref.BackupStatus = outlook.TeamsFileBackedUpInBin
	}
	ref.BackupKey, ref.BackupBucket, ref.OwnerJobID = key, idx.bucket, idx.job.ID
	ref.BackupETag, ref.VerifiedAt = meta[outlook.OneDriveMetaETag], outlook.TeamsUTC(f.r.now())
}

func (f *teamsFileResolver) driveOwner(driveID string) string {
	if v, ok := f.driveOf[driveID]; ok {
		return v
	}
	var d struct {
		Owner struct {
			User *struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"owner"`
	}
	owner := ""
	if raw, err := f.r.getJSON(outlook.TeamsDriveURL(driveID)); err == nil && json.Unmarshal(raw, &d) == nil && d.Owner.User != nil {
		owner = d.Owner.User.ID
	}
	f.driveOf[driveID] = owner
	return owner
}

func (f *teamsFileResolver) userDrive(userID string) string {
	if v, ok := f.drives[userID]; ok {
		return v
	}
	var d struct {
		ID string `json:"id"`
	}
	if raw, err := f.r.getJSON(outlook.TeamsUserDriveURL(userID)); err == nil {
		_ = json.Unmarshal(raw, &d)
	}
	f.drives[userID] = d.ID
	return d.ID
}

// siteJob finds the active SharePoint job that backs up driveID: the site's default library or a
// library the job has backed up. Without one, a job of the file's site is returned with
// backsDrive false. siteID is the bare site collection id or the composite host,site,web id.
func (f *teamsFileResolver) siteJob(driveID, siteID string) (job *repo.CronJobListingDB, backsDrive bool, err error) {
	if f.sites == nil {
		in := f.r.input
		rows, err := in.Database.CronJobRepo.ListMicrosoftScopeJobs(in.Job.UserID, in.Job.TenantID, repo.ResourceTypeSite, in.Job.SyncType)
		if err != nil {
			return nil, false, err
		}
		f.sites = []repo.CronJobListingDB{}
		for _, j := range rows {
			if j.Method == "outlook_sharepoint" && j.Active {
				f.sites = append(f.sites, j)
			}
		}
	}
	want := sharePointSiteGUID(siteID)
	for i := range f.sites {
		j := &f.sites[i]
		if lib, ok := j.TaskMemory.SharePointLibraries[driveID]; repo.JobSharePointDriveID(j) == driveID || (ok && lib.RemovedAt == "") {
			return j, true, nil
		}
		if job == nil && want != "" && sharePointSiteGUID(j.ResourceID) == want {
			job = j
		}
	}
	return job, false, nil
}

// sharePointSiteGUID is the site collection id of a bare or composite (host,site,web) site id.
func sharePointSiteGUID(id string) string {
	parts := strings.Split(strings.TrimSpace(id), ",")
	switch len(parts) {
	case 1:
		return strings.ToLower(parts[0])
	case 3:
		return strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return ""
}

// ownerIndex indexes the synced keys of an owner job by library prefix and item id (read only,
// once per run). storx stays empty when the backup could not be listed.
func (f *teamsFileResolver) ownerIndex(service string, job *repo.CronJobListingDB) *teamsOwnerIndex {
	k := fmt.Sprintf("%s:%d", service, job.ID)
	if idx, ok := f.owners[k]; ok {
		return idx
	}
	idx := &teamsOwnerIndex{job: job, service: service, items: map[string]string{}}
	f.owners[k] = idx
	method, bucket := "outlook_onedrive", satellite.ReserveBucket_OutlookOneDrive
	if service == "sharepoint" {
		method, bucket = "outlook_sharepoint", satellite.ReserveBucket_OutlookSharePoint
	}
	in := f.r.input
	storx := in.Database.CronJobRepo.ResolvedStorxToken(job)
	prefix, err := microsoftJobKeyPrefix(job)
	if storx == "" || err != nil {
		return idx
	}
	keys, err := teamsOwnerKeysFn(f.r.ctx, in, storx, bucket, prefix+"/", job.UserID, method)
	if err != nil {
		logger.Warn(f.r.ctx, "teams file check: list owner backup", logger.String("service", service), logger.ErrorField(err))
		return idx
	}
	idx.storx, idx.bucket, idx.prefix = storx, bucket, prefix
	for key := range keys {
		if p, ok := outlook.ParseOneDriveObjectKey(key); ok {
			if !p.IsFolder && p.ItemID != "" {
				idx.items[p.Prefix+"|"+p.ItemID] = key
			}
			continue
		}
		if p, ok := outlook.ParseOneDriveLegacyKey(key); ok && p.IsMeta {
			idx.legacy = append(idx.legacy, key)
		}
	}
	return idx
}

// recheckFiles re-checks file references on the one schedule: not backed up, every run for 30
// days then weekly until 90 days; backed up, weekly until 90 days. Oldest check first. Objects
// that reference a file whose status changed get their storx_backup.files updated.
func (r *teamsRun) recheckFiles(deadline time.Time) bool {
	f := r.fileResolver()
	now := r.now()
	type due struct {
		key     string
		checked time.Time
	}
	var list []due
	for k, reg := range f.doc.Refs {
		first, _ := outlook.ParseTeamsTime(reg.Ref.FirstSeenAt)
		checked, _ := outlook.ParseTeamsTime(reg.Ref.CheckedAt)
		age, since := now.Sub(first), now.Sub(checked)
		if age > teamsFileRecheckUntil {
			continue
		}
		backed := reg.Ref.BackupStatus == outlook.TeamsFileBackedUp || reg.Ref.BackupStatus == outlook.TeamsFileBackedUpInBin
		if (backed || age > teamsFileEveryRun) && since < teamsFileVerifyValid {
			continue
		}
		list = append(list, due{k, checked})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].checked.Before(list[j].checked) })
	for _, d := range list {
		if r.stopped() || !r.now().Before(deadline) {
			return false
		}
		reg := f.doc.Refs[d.key]
		fresh := f.resolve(reg.Ref.WebURL)
		fresh.FirstSeenAt, fresh.SourceMessageID = reg.Ref.FirstSeenAt, reg.Ref.SourceMessageID
		changed := fresh.BackupStatus != reg.Ref.BackupStatus || fresh.BackupKey != reg.Ref.BackupKey
		reg.Ref = fresh
		f.doc.Refs[d.key] = reg
		f.dirty = true
		if changed {
			for _, obj := range reg.Objects {
				if err := r.patchFileRef(obj, fresh); err != nil {
					logger.Warn(r.ctx, "teams file status update", logger.ErrorField(err))
				}
			}
		}
	}
	return true
}

func (r *teamsRun) patchFileRef(key string, ref outlook.TeamsFileRef) error {
	if !r.keys[key] {
		return nil
	}
	raw, err := r.store.download(r.ctx, key)
	if err != nil {
		return err
	}
	block, ok := outlook.ReadTeamsBackup(raw)
	if !ok {
		return nil
	}
	for i, old := range block.Files {
		if old.WebURL == ref.WebURL || (old.ItemID != "" && old.ItemID == ref.ItemID && old.DriveID == ref.DriveID) {
			ref.SourceMessageID = old.SourceMessageID
			block.Files[i] = ref
		}
	}
	body, err := outlook.PatchTeamsBackup(raw, block)
	if err != nil {
		return err
	}
	meta, err := r.store.stat(r.ctx, key)
	if err != nil {
		return err
	}
	return r.put(key, body, meta)
}

// saveFiles writes _files_coverage.json: the reference registry and, per owner without an active
// backup job, how many referenced files it holds.
func (r *teamsRun) saveFiles() {
	if r.files == nil || !r.files.dirty || r.stopErr != nil {
		return
	}
	counts := map[string]int{}
	for _, reg := range r.files.doc.Refs {
		if reg.Ref.BackupStatus == outlook.TeamsFileOwnerNotBackedUp && reg.Ref.Owner != "" {
			counts[reg.Ref.Owner]++
		}
	}
	owners := make([]teamsMissingOwner, 0, len(counts))
	for owner, n := range counts {
		service, id, _ := strings.Cut(owner, ":")
		owners = append(owners, teamsMissingOwner{Service: service, ID: id, Files: n})
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].Service+owners[i].ID < owners[j].Service+owners[j].ID })
	r.files.doc.MissingOwners = owners
	r.files.doc.UpdatedAt = outlook.TeamsUTC(r.now())
	if len(owners) > 0 {
		r.warn("Some Teams files are in OneDrive or SharePoint locations without a backup job")
	}
	if err := r.saveState(outlook.TeamsFilesCoverageKey(r.tenantPrefix), r.files.doc); err == nil {
		r.files.dirty = false
	}
}
