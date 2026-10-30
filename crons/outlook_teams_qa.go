package crons

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Teams meeting Q&A (meetings, webinars and town halls). getAllOnlineMeetingMessages has no
// filter and no delta: every listing is the tenant's whole Q&A, so it runs at most once a day and
// a message is rewritten only when it, its conversation (moderation, upvotes) or its reactions
// changed. Messages are kept under the meeting's folder; Graph can't write Q&A back, so they are
// download-only. A message deleted in Teams stays in the backup (a snapshot can't tell a
// deletion from a message the listing didn't return).

const (
	teamsQAEvery            = 24 * time.Hour
	teamsQAReactionsRecent  = 3 * 24 * time.Hour
	teamsQAReactionsPerRun  = 300
	teamsQABudgetShare      = 4
	teamsQAReactionsPending = "pending"
)

// teamsQAEnabled switches the Q&A listing on. Default off until permissions, tenant capability
// checks, failure handling and retrieval are verified in staging (TEAMS_QA_ENABLED=true|1|yes).
var teamsQAEnabled = teamsQAEnabledFromEnv()

func teamsQAEnabledFromEnv() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("TEAMS_QA_ENABLED")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// teamsQAState is {tenant}/tenant/{tenantId}/_state/qa.json. Conversations pins each Q&A
// conversation to the meeting folder it was first stored in.
type teamsQAState struct {
	PageLink      string               `json:"page_link,omitempty"`
	Dirty         bool                 `json:"dirty,omitempty"`
	Held          int                  `json:"held,omitempty"`
	HeldSince     *time.Time           `json:"held_since,omitempty"`
	DoneAt        *time.Time           `json:"done_at,omitempty"`
	Conversations map[string]string    `json:"conversations,omitempty"`
	SkipUntil     map[string]time.Time `json:"skip_until,omitempty"`
	Status        map[string]string    `json:"status,omitempty"`
}

func (s *teamsQAState) skipped(stream string, now time.Time) bool {
	return teamsStreamSkipped(s.SkipUntil, stream, now)
}

func (s *teamsQAState) block(stream, status string, until time.Time) {
	teamsStreamBlock(&s.SkipUntil, &s.Status, stream, status, until)
}

func (s *teamsQAState) unblock(stream string) {
	teamsStreamClear(s.SkipUntil, s.Status, stream)
}

// teamsQAMessage is the part of a Q&A message the backup reads.
type teamsQAMessage struct {
	ID                   string `json:"id"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	ReplyToID            string `json:"replyToId"`
	Conversation         *struct {
		ID              string `json:"id"`
		ModerationState string `json:"moderationState"`
		UpvoteCount     *int   `json:"upvoteCount"`
		OnlineMeetingID string `json:"onlineMeetingId"`
		Organizer       struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"organizer"`
	} `json:"conversation"`
}

// teamsQARun is the per-run memory of the Q&A listing.
type teamsQARun struct {
	st            *teamsQAState
	reactionsLeft int
	metaDone      map[string]bool
	orgMeetings   map[string]map[string]string
}

// syncMeetingQA runs the daily Q&A listing within a quarter of the meetings budget. Returns false
// while the listing is unfinished.
func (r *teamsRun) syncMeetingQA(deadline time.Time) bool {
	if !teamsQAEnabled || r.singleTeam != "" || !r.consented(outlook.TeamsFeatureQA) {
		return true
	}
	key := outlook.TeamsQAStateKey(r.tenantPrefix)
	st := &teamsQAState{}
	r.loadState(key, st)
	save := func() error { return r.saveState(key, st) }
	now := r.now()
	if st.skipped("qa", now) || (st.PageLink == "" && !st.Dirty && st.DoneAt != nil && now.Sub(*st.DoneAt) < teamsQAEvery) {
		return true
	}
	if _, err := r.scopeUsers(); err != nil {
		code := outlook.ClassifyTeamsError(err)
		r.setFeature(outlook.TeamsFeatureQA, code, code)
		return false
	}
	if st.Conversations == nil {
		st.Conversations = map[string]string{}
	}
	if left := deadline.Sub(now); left > 0 {
		deadline = now.Add(left / teamsQABudgetShare)
	}
	q := &teamsQARun{st: st, reactionsLeft: teamsQAReactionsPerRun, metaDone: map[string]bool{}, orgMeetings: map[string]map[string]string{}}
	outcome, _, err := r.pageThrough(teamsPager{
		pageLink: &st.PageLink, first: outlook.TeamsQAMessagesURL(), dirty: &st.Dirty, deadline: deadline,
		streamKey: "qa:stream:tenant:" + r.tenant,
		stream:    repo.TeamsFailure{Phase: "qa", Kind: teamsFailStream, ResourceType: "tenant", ResourceID: r.tenant},
		save:      save,
		item: func(raw json.RawMessage) (string, repo.TeamsFailure, error, bool) {
			key, f, err := r.qaItem(q, raw)
			return key, f, err, false
		},
	})
	defer func() { _ = save() }()
	status, _ := r.pageStatus(st, "qa", outlook.TeamsFeatureQA, outcome, err, func() { st.PageLink = "" })
	if outcome != teamsPageDone {
		return status == teamsBlocked
	}
	closed, gaps := r.endRound(teamsRound{&st.Dirty, &st.Held, &st.HeldSince, "qa:stream:tenant:" + r.tenant}, r.failureKeysFor("qa", r.tenant, ""))
	if !closed {
		return false
	}
	done := r.now()
	st.DoneAt = &done
	r.setFeature(outlook.TeamsFeatureQA, outlook.TeamsStatusOK, "")
	if gaps {
		r.count(teamsCompleteWithGaps)
	}
	return true
}

func (r *teamsRun) qaItem(q *teamsQARun, raw json.RawMessage) (string, repo.TeamsFailure, error) {
	var m teamsQAMessage
	if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" || m.Conversation == nil || m.Conversation.ID == "" {
		return "", repo.TeamsFailure{}, nil
	}
	organizer := m.Conversation.Organizer.User.ID
	if organizer == "" || !r.userScope[organizer] {
		return "", repo.TeamsFailure{}, nil
	}
	f := repo.TeamsFailure{Phase: "qa", Kind: teamsFailItem, ResourceType: "tenant", ResourceID: r.tenant,
		ItemIDs: map[string]string{"conversation_id": m.Conversation.ID, "message_id": m.ID, "organizer_id": organizer}}
	return "qa:item:" + m.Conversation.ID + ":" + m.ID, f, r.storeQAMessage(q, raw, m)
}

// qaMeetingFolder returns the meeting folder of a Q&A conversation: the folder it was first stored
// in, else the organizer's calendar meeting with that online meeting id, else an artifact-only
// folder shared with the meeting's transcript and recording listings.
func (r *teamsRun) qaMeetingFolder(q *teamsQARun, organizer, convID, onlineMeetingID string) string {
	if mid := q.st.Conversations[convID]; mid != "" {
		return mid
	}
	mid := ""
	if onlineMeetingID != "" {
		known, ok := q.orgMeetings[organizer]
		if !ok {
			st, _ := r.loadUserState(organizer)
			known = st.OnlineMeetings
			q.orgMeetings[organizer] = known
		}
		mid = known[onlineMeetingID]
		if mid == "" {
			mid, _, _ = outlook.TeamsMeetingID("", organizer, "onlineMeeting:"+onlineMeetingID, false)
		}
	} else {
		mid, _, _ = outlook.TeamsMeetingID("", organizer, "qaConversation:"+convID, false)
	}
	q.st.Conversations[convID] = mid
	return mid
}

// teamsQAConversationSig is the change marker of the conversation a message carries.
func teamsQAConversationSig(m teamsQAMessage) string {
	up := ""
	if m.Conversation.UpvoteCount != nil {
		up = strconv.Itoa(*m.Conversation.UpvoteCount)
	}
	return m.Conversation.ModerationState + "|" + up
}

func (r *teamsRun) storeQAMessage(q *teamsQARun, raw json.RawMessage, m teamsQAMessage) error {
	c := m.Conversation
	organizer := c.Organizer.User.ID
	mid := r.qaMeetingFolder(q, organizer, c.ID, c.OnlineMeetingID)
	prefix := outlook.TeamsMeetingPrefix(r.tenant, mid)
	key := outlook.TeamsMeetingQAKey(prefix, c.ID, m.ID)

	var stored map[string]string
	if r.keys[key] {
		meta, err := r.store.stat(r.ctx, key)
		switch {
		case errors.Is(err, errTeamsNotFound):
			if rerr := r.remove(key); rerr != nil {
				return rerr
			}
		case err != nil:
			return err
		default:
			stored = meta
		}
	}
	changed := stored == nil || stored[outlook.TeamsMetaModified] != m.LastModifiedDateTime
	if stored != nil && changed {
		v := outlook.CompareTeamsVersion(&outlook.TeamsVersion{Modified: stored[outlook.TeamsMetaModified]}, outlook.TeamsVersion{Modified: m.LastModifiedDateTime})
		if v != outlook.TeamsVersionWrite {
			r.count(v)
			return nil
		}
	}

	now := r.now()
	created, _ := outlook.ParseTeamsTime(m.CreatedDateTime)
	reactSig := ""
	if stored != nil {
		reactSig = stored[outlook.TeamsMetaReactions]
	}
	var reactions json.RawMessage
	if changed || reactSig == teamsQAReactionsPending || reactSig == "" || now.Sub(created) < teamsQAReactionsRecent {
		switch {
		case q.reactionsLeft > 0:
			q.reactionsLeft--
			items, err := r.getAll(outlook.TeamsQAReactionsURL(c.ID, m.ID))
			if err != nil {
				if !outlook.IsTeamsPermanentStatus(outlook.ClassifyTeamsError(err)) {
					return fmt.Errorf("q&a reactions: %w", err)
				}
				q.reactionsLeft = 0
				if changed || reactSig == "" {
					reactSig = teamsQAReactionsPending
				}
				break
			}
			if items == nil {
				items = []json.RawMessage{}
			}
			reactions, _ = json.Marshal(items)
			sum := sha1.Sum(reactions)
			reactSig = hex.EncodeToString(sum[:])
		case changed || reactSig == "":
			reactSig = teamsQAReactionsPending
		}
	}

	convSig := teamsQAConversationSig(m)
	if stored != nil && !changed && stored[outlook.TeamsMetaETag] == convSig && stored[outlook.TeamsMetaReactions] == reactSig {
		r.count("skipped")
		return nil
	}
	if stored != nil && !changed && reactions == nil {
		// Only the conversation changed: keep the stored message and its reactions.
		prev, err := r.store.download(r.ctx, key)
		if err != nil {
			return err
		}
		var doc struct {
			Reactions json.RawMessage `json:"reactions"`
		}
		_ = json.Unmarshal(prev, &doc)
		reactions = doc.Reactions
	}
	body := []byte(raw)
	if reactions != nil {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		doc["reactions"] = reactions
		body, _ = json.Marshal(doc)
	}
	body, err := outlook.PatchTeamsBackup(body, outlook.TeamsBackupBlock{
		TenantID: r.tenant, ReplyToID: m.ReplyToID, Source: "getAllOnlineMeetingMessages", BackedUpAt: outlook.TeamsUTC(now),
	})
	if err != nil {
		return err
	}
	meta := map[string]string{
		outlook.TeamsMetaMessageID: m.ID,
		outlook.TeamsMetaModified:  m.LastModifiedDateTime,
		outlook.TeamsMetaETag:      convSig,
		outlook.TeamsMetaReactions: reactSig,
	}
	if m.ReplyToID != "" {
		meta[outlook.TeamsMetaReplyToID] = m.ReplyToID
	}
	if err := r.put(key, body, meta); err != nil {
		r.storeFallback(key, body)
		return err
	}
	r.count("written")
	if q.metaDone[prefix] {
		return nil
	}
	q.metaDone[prefix] = true
	return r.updateMeetingMeta(prefix, func(doc map[string]interface{}) {
		if _, ok := doc["mid"]; !ok {
			doc["mid"], doc["kind"], doc["calendar_status"] = mid, "single", "not_found"
			doc["identity"] = map[string]interface{}{"kind": "separate", "reason": "artifact_only"}
		}
		if c.OnlineMeetingID != "" {
			doc["online_meeting_id"] = c.OnlineMeetingID
		}
		doc["qa_status"] = outlook.TeamsStatusOK
		teamsMeetingDefaults(doc)
	})
}
