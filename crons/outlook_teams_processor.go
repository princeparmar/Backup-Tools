package crons

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"storj.io/uplink"
)

// Teams is one outlook_teams job per tenant (resource_type tenant, resource_id = tenant id). A run
// walks phases calls, retries, teams, chats and meetings, each with a reserved share of the run
// budget, checkpointing after every page or resource so a crash or a budget stop resumes where it
// left off. Old per-team jobs (resource_type team) are merged into the tenant job and paused.

type outlookTeamsProcessor struct{}

func NewOutlookTeamsProcessor() *outlookTeamsProcessor {
	return &outlookTeamsProcessor{}
}

func (p *outlookTeamsProcessor) Run(input ProcessorInput) error {
	return runOutlookTeamsAutosync(input)
}

const (
	teamsBucket = satellite.ReserveBucket_OutlookTeams

	teamsRunBudget          = 50 * time.Minute
	teamsResourceCap        = 10 * time.Minute
	teamsNowMargin          = 2 * time.Minute
	teamsWindowOverlap      = time.Hour
	teamsReconcileEvery     = 7
	teamsReconcileSpan      = 30 * 24 * time.Hour
	teamsMaxFailures        = 5000
	teamsMaxMemoryBytes     = 2 << 20
	teamsMaxOpenWindows     = 50
	teamsReleaseReplays     = 10
	teamsReleaseAge         = 72 * time.Hour
	teamsItemMaxAttempts    = 10
	teamsWriteAttempts      = 3
	teamsHeartbeatEvery     = 2 * time.Minute
	teamsOwnCheckEvery      = 30 * time.Second
	teamsPermanentBackoff   = 24 * time.Hour
	teamsMaxConsecutive429  = 20
	teamsCallsLagBorrow     = 5 * 24 * time.Hour
	teamsConfigEvery        = 24 * time.Hour
	teamsDeselectedKeep     = 90 * 24 * time.Hour
	teamsPermanentKeep      = 90 * 24 * time.Hour
	teamsMergeKeep          = 30 * 24 * time.Hour
	teamsMergedJobMessage   = repo.TeamsMergedJobMessage
	teamsWaitingMergeReason = "Waiting to be merged into the Teams tenant job"
	teamsWarningPrefix      = "Teams backup finished with warnings"
)

// Phases of a tenant run, in order.
const (
	teamsPhaseCalls    = "calls"
	teamsPhaseRetries  = "retries"
	teamsPhaseTeams    = "teams"
	teamsPhaseChats    = "chats"
	teamsPhaseMeetings = "meetings"
)

// teamsPhaseOrder is the full tenant run (calls + meetings included). Graph cannot restore
// calendar, calls, or recordings into Teams, so production uses teamsRestorablePhaseOrder.
// Keep runCallsPhase / runMeetingsPhase so this list can be switched back later.
var teamsPhaseOrder = []string{teamsPhaseCalls, teamsPhaseRetries, teamsPhaseTeams, teamsPhaseChats, teamsPhaseMeetings}

// teamsRestorablePhaseOrder backs up channel and chat messages only.
var teamsRestorablePhaseOrder = []string{teamsPhaseRetries, teamsPhaseTeams, teamsPhaseChats}

// Per-resource statuses ("Data integrity rules, 16").
const (
	teamsComplete            = "complete"
	teamsCompleteWithRetries = "complete_with_retries"
	teamsCompleteWithGaps    = "complete_with_gaps"
	teamsIncomplete          = "incomplete"
	teamsBlocked             = "blocked"
	teamsSourceDeleted       = "source_deleted"
	teamsDeselected          = "deselected"
)

// Failure entry kinds.
const (
	teamsFailItem   = "item"
	teamsFailStream = "stream"
	teamsFailWindow = "window"

	teamsPermanentFailure = "permanent_failure"
	teamsStoredFallback   = "stored_fallback"
)

// teamsPhaseBudgets splits the run budget: calls 15%, retries 15%, teams 25%, chats 25%,
// meetings 20%. When the calls high-water mark lags more than 5 days (call records expire after
// 30), calls borrow another 15% from teams, chats and meetings in proportion to their shares.
func teamsPhaseBudgets(total time.Duration, callsLagging bool) map[string]time.Duration {
	shares := map[string]float64{
		teamsPhaseCalls: 0.15, teamsPhaseRetries: 0.15, teamsPhaseTeams: 0.25, teamsPhaseChats: 0.25, teamsPhaseMeetings: 0.20,
	}
	if callsLagging {
		const borrow = 0.15
		rest := shares[teamsPhaseTeams] + shares[teamsPhaseChats] + shares[teamsPhaseMeetings]
		for _, p := range []string{teamsPhaseTeams, teamsPhaseChats, teamsPhaseMeetings} {
			shares[p] -= borrow * shares[p] / rest
		}
		shares[teamsPhaseCalls] += borrow
	}
	out := make(map[string]time.Duration, len(shares))
	for p, s := range shares {
		out[p] = time.Duration(float64(total) * s)
	}
	return out
}

// teamsCallsLagging reports whether calls borrow budget: no calls high-water mark yet, or one
// more than 5 days behind.
func teamsCallsLagging(calls *repo.TeamsWindowState, now time.Time) bool {
	return calls == nil || calls.HighWater == nil || now.Sub(*calls.HighWater) > teamsCallsLagBorrow
}

// Seams (overridden in tests).
var (
	teamsPageFn       = outlook.FetchTeamsPage
	teamsJSONFn       = outlook.FetchTeamsJSON
	teamsBytesFn      = outlook.FetchTeamsBytes
	teamsNowFn        = time.Now
	teamsWriteBackoff = time.Second
)

// teamsStore is the outlook-teams bucket and its synced_objects index for one job.
type teamsStore interface {
	put(ctx context.Context, key string, data []byte, meta map[string]string) error
	stat(ctx context.Context, key string) (map[string]string, error)
	download(ctx context.Context, key string) ([]byte, error)
	remove(ctx context.Context, key string) error
	listKeys(ctx context.Context) ([]string, error)
}

// errTeamsNotFound marks a stat or download of an object that does not exist.
var errTeamsNotFound = errors.New("teams object not found")

type satelliteTeamsStore struct {
	input ProcessorInput
	storx string
}

func (s *satelliteTeamsStore) bucket() string { return satellite.BucketForAccess(s.storx, teamsBucket) }

// put uploads through the shared helper (keeps StorX grant recovery), then confirms the index row,
// which the helper does not report: a missing row is created again, and a row that still can't be
// written makes the write fail.
func (s *satelliteTeamsStore) put(ctx context.Context, key string, data []byte, meta map[string]string) error {
	if data == nil {
		data = []byte{}
	}
	if err := handler.UploadObjectWithMetadataAndSync(ctx, s.input.Database, s.storx, teamsBucket, key, data, meta, s.input.Job.UserID, s.input.StorxRecovery); err != nil {
		return err
	}
	rows := s.input.Database.SyncedObjectRepo
	if _, err := rows.GetSyncedObjectByBucketAndKey(s.bucket(), key); err == nil {
		return nil
	}
	if err := rows.CreateSyncedObject(s.input.Job.UserID, s.bucket(), key, "outlook", "outlook_teams"); err != nil {
		return fmt.Errorf("index %s: %w", key, err)
	}
	return nil
}

func (s *satelliteTeamsStore) stat(ctx context.Context, key string) (map[string]string, error) {
	obj, err := satellite.StatObject(ctx, s.storx, teamsBucket, key)
	if err != nil {
		if errors.Is(err, uplink.ErrObjectNotFound) {
			return nil, errTeamsNotFound
		}
		return nil, err
	}
	return map[string]string(obj.Custom), nil
}

func (s *satelliteTeamsStore) download(ctx context.Context, key string) ([]byte, error) {
	b, err := satellite.DownloadObject(ctx, s.storx, teamsBucket, key)
	if err != nil && errors.Is(err, uplink.ErrObjectNotFound) {
		return nil, errTeamsNotFound
	}
	return b, err
}

// remove deletes the object, then its index row; either error is returned.
func (s *satelliteTeamsStore) remove(ctx context.Context, key string) error {
	if err := satellite.DeleteObject(ctx, s.storx, teamsBucket, key); err != nil && !errors.Is(err, uplink.ErrObjectNotFound) {
		return err
	}
	return s.input.Database.SyncedObjectRepo.DeleteSyncedObject(s.bucket(), key)
}

func (s *satelliteTeamsStore) listKeys(context.Context) ([]string, error) {
	rows, err := s.input.Database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(s.input.Job.UserID, s.bucket(), "", "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ObjectKey)
	}
	return out, nil
}

// teamsSelection is the job's scope, read once at run start. Empty sets mean all.
type teamsSelection struct {
	TeamIDs    map[string]bool
	ChannelIDs map[string]map[string]bool
	UserIDs    map[string]bool
}

func teamsSelectionFromJob(job *repo.CronJobListingDB) teamsSelection {
	sel := teamsSelection{TeamIDs: map[string]bool{}, ChannelIDs: map[string]map[string]bool{}, UserIDs: map[string]bool{}}
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return sel
	}
	in := *job.InputData.Json()
	add := func(set map[string]bool, ids []string) {
		for _, id := range ids {
			set[id] = true
		}
	}
	add(sel.TeamIDs, repo.InputStringList(in["team_ids"]))
	add(sel.UserIDs, repo.InputStringList(in["user_ids"]))
	if raw, ok := in["team_channel_ids"].(map[string]interface{}); ok {
		for team, v := range raw {
			if ids := repo.InputStringList(v); len(ids) > 0 {
				sel.ChannelIDs[team] = map[string]bool{}
				add(sel.ChannelIDs[team], ids)
			}
		}
	}
	if job.ResourceType != repo.ResourceTypeTenant {
		if team := repo.JobTeamsTeamID(job); team != "" {
			sel.TeamIDs = map[string]bool{team: true}
			if ids := repo.InputStringList(in["channel_ids"]); len(ids) > 0 {
				sel.ChannelIDs[team] = map[string]bool{}
				add(sel.ChannelIDs[team], ids)
			}
		}
	}
	return sel
}

func (s teamsSelection) teamSelected(id string) bool { return len(s.TeamIDs) == 0 || s.TeamIDs[id] }
func (s teamsSelection) userSelected(id string) bool { return len(s.UserIDs) == 0 || s.UserIDs[id] }

func (s teamsSelection) channelSelected(teamID, channelID string) bool {
	set := s.ChannelIDs[teamID]
	return len(set) == 0 || set[channelID]
}

// teamsStatusDoc is {tenant}/tenant/{tenantId}/_status.json: what the vault shows per phase,
// resource and feature. Progress only, no backup content.
type teamsStatusDoc struct {
	TenantID     string                         `json:"tenant_id"`
	UpdatedAt    string                         `json:"updated_at"`
	Phases       map[string]teamsPhaseStatus    `json:"phases,omitempty"`
	Resources    map[string]teamsResourceStatus `json:"resources,omitempty"`
	Teams        []teamsTeamSummary             `json:"teams,omitempty"`
	Features     map[string]teamsFeatureStatus  `json:"features,omitempty"`
	Gaps         []teamsGap                     `json:"gaps,omitempty"`
	SkippedItems []teamsSkippedItem             `json:"skipped_items,omitempty"`
	Warnings     []string                       `json:"warnings,omitempty"`
	// Counters surfaced for ops/UI; durable across checkpoints via _status.json.
	IncomingChannelSkipped int `json:"incoming_channel_skipped,omitempty"`
	FailuresCapped         int `json:"failures_capped,omitempty"`
	StoredFallback         int `json:"stored_fallback,omitempty"`
}

type teamsPhaseStatus struct {
	Status     string         `json:"status"`
	StartedAt  string         `json:"started_at,omitempty"`
	FinishedAt string         `json:"finished_at,omitempty"`
	Counts     map[string]int `json:"counts,omitempty"`
}

type teamsResourceStatus struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	Code      string `json:"code,omitempty"`
	Scope     string `json:"scope,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

type teamsTeamSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// teamsFeatureStatus keeps capability (the endpoint answered) apart from coverage (the content
// is proven complete); "backed up" is shown only for ok + verified.
type teamsFeatureStatus struct {
	CapabilityStatus string `json:"capability_status"`
	CoverageStatus   string `json:"coverage_status"`
	Code             string `json:"code,omitempty"`
	CheckedAt        string `json:"checked_at"`
}

type teamsGap struct {
	Kind       string `json:"kind"`
	Resource   string `json:"resource,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	RecordedAt string `json:"recorded_at"`
}

type teamsSkippedItem struct {
	Phase     string            `json:"phase"`
	Key       string            `json:"key"`
	IDs       map[string]string `json:"ids,omitempty"`
	FirstCode string            `json:"first_code,omitempty"`
	LastCode  string            `json:"last_code,omitempty"`
	Since     string            `json:"since"`
	Released  string            `json:"released"`
	Status    string            `json:"status"`
}

type teamsTeamInfo struct {
	ID          string
	DisplayName string
}

type teamsUser struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	UPN         string `json:"userPrincipalName"`
	Mail        string `json:"mail"`
	UserType    string `json:"userType"`
}

// teamsRun is one run of a Teams job.
type teamsRun struct {
	ctx          context.Context
	input        ProcessorInput
	store        teamsStore
	token        string
	tenant       string
	tenantPrefix string
	mem          *repo.TaskMemory
	start        time.Time
	deadline     time.Time
	to           time.Time
	sel          teamsSelection
	singleTeam   string
	notConsented map[string]string

	// configNotConsented holds the config parts (see outlook.TeamsConfigPartRoles) without their role.
	configNotConsented map[string]string
	// attendanceNotConsented holds the attendance requests (see outlook.TeamsAttendancePermissions)
	// the token can't make.
	attendanceNotConsented map[string]string
	// historyNotConsented holds the retained-history features (outlook.TeamsHistoryRoles) the
	// token can't use, with the reason.
	historyNotConsented map[string]string
	// sharedIncoming holds, per team, the shared channels another team hosts (incomingChannels):
	// their messages belong to the host team and are never stored under this one.
	sharedIncoming map[string]map[string]bool

	keys    map[string]bool
	status  *teamsStatusDoc
	counts  map[string]int
	halt    string
	stopErr error

	failuresFull   bool
	failuresCapped int
	consecutive429 int
	refreshedAuth  bool
	ownCheckedAt   time.Time
	lastBeat       time.Time
	beatMu         sync.Mutex

	checkpointFn func(repo.TaskMemory) error
	ownsFn       func() (bool, error)
	heartbeatFn  func() error
	refreshFn    func() (string, error)

	users       []teamsUser
	userScope   map[string]bool
	usersLoaded bool
	usersErr    error

	chatsSeen      map[string]bool
	seriesSeen     map[string]string
	seriesSubjects map[string]string
	midChecked     map[string]string
	icalMids       map[string][]string
	files          *teamsFileResolver
}

func runOutlookTeamsAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)
	if input.Job.ResourceType != repo.ResourceTypeTenant {
		err = runOutlookTeamsTeamJob(ctx, input)
		return err
	}
	r, err := newTeamsRun(ctx, input, "")
	if err != nil {
		return err
	}
	err = r.run(teamsRestorablePhaseOrder)
	return err
}

// runOutlookTeamsTeamJob runs an old per-team job. Once a tenant job exists for the same
// connection it waits to be merged (the tenant job imports and pauses it); until then it backs
// up its own team with the tenant layout so nothing is lost or duplicated by the merge.
func runOutlookTeamsTeamJob(ctx context.Context, input ProcessorInput) error {
	job := input.Job
	teamID := repo.JobTeamsTeamID(job)
	if teamID == "" {
		return fmt.Errorf("team_id is required on job for teams backup")
	}
	tenantJob, err := input.Database.CronJobRepo.FindMicrosoftResourceJob(job.UserID, job.TenantID, repo.ResourceTypeTenant,
		strings.ToLower(strings.TrimSpace(job.TenantID)), "outlook_teams", job.SyncType)
	if err == nil && tenantJob != nil && tenantJob.Active && repo.JobCredentialID(tenantJob) == repo.JobCredentialID(job) {
		job.Message, job.MessageStatus = teamsWaitingMergeReason, repo.JobMessageStatusWarning
		return nil
	}
	r, err := newTeamsRun(ctx, input, teamID)
	if err != nil {
		return err
	}
	return r.run([]string{teamsPhaseTeams})
}

func newTeamsRun(ctx context.Context, input ProcessorInput, singleTeam string) (*teamsRun, error) {
	tenant := strings.ToLower(strings.TrimSpace(input.Job.TenantID))
	if tenant == "" || outlook.TeamsTenantPrefix(tenant) == "" {
		return nil, fmt.Errorf("microsoft job %d has no tenant identity", input.Job.ID)
	}
	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return nil, err
	}
	if err := teamsCheckTenant(tenant, auth); err != nil {
		return nil, err
	}
	drainMicrosoftWebhooks(input, auth.StorxToken, "teams")
	now := teamsNowFn().UTC()
	r := &teamsRun{
		ctx:          ctx,
		input:        input,
		store:        &satelliteTeamsStore{input: input, storx: auth.StorxToken},
		token:        auth.AccessToken,
		tenant:       tenant,
		tenantPrefix: outlook.TeamsTenantPrefix(tenant),
		mem:          &input.Job.TaskMemory,
		start:        now,
		deadline:     now.Add(teamsRunBudget),
		to:           now.Add(-teamsNowMargin),
		singleTeam:   singleTeam,
	}
	taskID := uint(0)
	if input.Task != nil {
		taskID = input.Task.ID
	}
	jobs := input.Database.CronJobRepo
	r.checkpointFn = func(mem repo.TaskMemory) error { return jobs.CheckpointTaskMemory(input.Job.ID, taskID, mem) }
	r.ownsFn = func() (bool, error) { return jobs.TaskOwnsJob(input.Job.ID, taskID) }
	r.heartbeatFn = input.HeartBeatFunc
	r.refreshFn = func() (string, error) {
		a, err := microsoftJobAccessToken(input)
		if err != nil {
			return "", err
		}
		if err := teamsCheckTenant(tenant, a); err != nil {
			return "", err
		}
		return a.AccessToken, nil
	}
	return r, nil
}

// teamsCheckTenant pins the run to the job's tenant: the resolved tenant and the token's tid
// claim must both be the job's tenant before anything is written.
func teamsCheckTenant(tenant string, auth *microsoftJobAuth) error {
	if t := strings.ToLower(strings.TrimSpace(auth.TenantID)); t != "" && t != tenant {
		return fmt.Errorf("microsoft token is for tenant %s, job is for %s", t, tenant)
	}
	if c, err := outlook.IdentityClaimsFromTokens("", auth.AccessToken); err == nil && c.TenantID != "" && c.TenantID != tenant {
		return fmt.Errorf("microsoft token tid %s does not match job tenant %s", c.TenantID, tenant)
	}
	return nil
}

func (r *teamsRun) now() time.Time { return teamsNowFn().UTC() }

func (r *teamsRun) userID() string { return r.input.Job.UserID }

// stopped reports a hard stop (ownership lost, auth, storage full) or a soft one (budget, throttling).
func (r *teamsRun) stopped() bool {
	return r.stopErr != nil || r.halt != "" || r.ctx.Err() != nil
}

func (r *teamsRun) count(name string) {
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[name]++
}

func (r *teamsRun) warn(msg string) {
	for _, w := range r.status.Warnings {
		if w == msg {
			return
		}
	}
	r.status.Warnings = append(r.status.Warnings, msg)
}

// beat sends the task heartbeat at most every 30 seconds; a background ticker covers long Graph
// waits (MissedHeartbeatForTask fails a task after 10 minutes).
func (r *teamsRun) beat() {
	r.beatMu.Lock()
	defer r.beatMu.Unlock()
	if r.heartbeatFn == nil || time.Since(r.lastBeat) < 30*time.Second {
		return
	}
	r.lastBeat = time.Now()
	if err := r.heartbeatFn(); err != nil {
		logger.Warn(r.ctx, "teams heartbeat failed", logger.ErrorField(err))
	}
}

func (r *teamsRun) startHeartbeat() (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(teamsHeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				r.beatMu.Lock()
				r.lastBeat = time.Time{}
				r.beatMu.Unlock()
				r.beat()
			}
		}
	}()
	return func() { close(done) }
}

// owns checks, at most every 30 seconds, that this run's task is still the job's running task.
func (r *teamsRun) owns() bool {
	if r.stopErr != nil {
		return false
	}
	if r.ownsFn == nil || time.Since(r.ownCheckedAt) < teamsOwnCheckEvery {
		return true
	}
	ok, err := r.ownsFn()
	r.ownCheckedAt = time.Now()
	if err != nil {
		logger.Warn(r.ctx, "teams ownership check failed", logger.ErrorField(err))
		return true
	}
	if !ok {
		r.stopErr = repo.ErrJobOwnershipLost
	}
	return ok
}

// checkpoint saves task_memory only while the run still owns the job.
func (r *teamsRun) checkpoint() error {
	if r.stopErr != nil {
		return r.stopErr
	}
	r.beat()
	r.compactMemory()
	if teamsMemorySize(r.mem) > teamsMaxMemoryBytes {
		r.failuresFull = true
		r.warn("Teams progress data is near its size limit; new failures hold their streams instead")
	}
	if r.checkpointFn == nil {
		return nil
	}
	if err := r.checkpointFn(*r.mem); err != nil {
		if errors.Is(err, repo.ErrJobOwnershipLost) {
			r.stopErr = err
		}
		logger.Warn(r.ctx, "teams checkpoint failed", logger.ErrorField(err))
		return err
	}
	return nil
}

// teamsMemorySize is the JSON size of the Teams part of task_memory.
func teamsMemorySize(mem *repo.TaskMemory) int {
	b, _ := json.Marshal(struct {
		A, B, C, D, E, F, G interface{}
	}{mem.TeamsCursor, mem.TeamsCalls, mem.TeamsPstn, mem.TeamsDirect, mem.TeamsFailures, mem.TeamsMerge, mem.TeamsLastRun})
	return len(b)
}

// compactMemory moves permanent failures older than 90 days to _status.json and drops merge
// entries 30 days after their job was paused.
func (r *teamsRun) compactMemory() {
	now := r.now()
	for k, f := range r.mem.TeamsFailures {
		if f.Status == teamsPermanentFailure && now.Sub(f.LastAt) > teamsPermanentKeep {
			r.addSkipped(k, f, now)
			delete(r.mem.TeamsFailures, k)
		}
	}
	if m := r.mem.TeamsMerge; m != nil {
		for id, at := range m.Paused {
			if now.Sub(at) > teamsMergeKeep {
				delete(m.Paused, id)
				delete(m.Imported, id)
			}
		}
	}
}

func (r *teamsRun) addSkipped(key string, f repo.TeamsFailure, now time.Time) {
	for _, s := range r.status.SkippedItems {
		if s.Key == key {
			return
		}
	}
	r.status.SkippedItems = append(r.status.SkippedItems, teamsSkippedItem{
		Phase: f.Phase, Key: key, IDs: f.ItemIDs, FirstCode: f.ErrorCode, LastCode: f.ErrorCode,
		Since: outlook.TeamsUTC(f.Since), Released: outlook.TeamsUTC(now), Status: cmpOr(f.Status, teamsPermanentFailure),
	})
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// graph wraps a Graph call with one token refresh on 401, the throttling stop and auth stop.
func (r *teamsRun) graph(call func(token string) error) error {
	err := call(r.token)
	status := outlook.ClassifyTeamsError(err)
	if status == outlook.TeamsStatusUnauthorized && !r.refreshedAuth && r.refreshFn != nil {
		r.refreshedAuth = true
		if tok, rerr := r.refreshFn(); rerr == nil {
			r.token = tok
			err = call(r.token)
			status = outlook.ClassifyTeamsError(err)
		}
	}
	switch status {
	case outlook.TeamsStatusUnauthorized:
		r.stopErr = fmt.Errorf("microsoft authorization failed; reconnect the tenant: %w", err)
	case outlook.TeamsStatusThrottled:
		r.consecutive429++
		if r.consecutive429 >= teamsMaxConsecutive429 {
			r.halt = "throttled"
		}
	case outlook.TeamsStatusOK:
		r.consecutive429 = 0
	}
	return err
}

func (r *teamsRun) page(u string) (outlook.TeamsPage, error) {
	var p outlook.TeamsPage
	err := r.graph(func(tok string) error {
		var e error
		p, e = teamsPageFn(r.ctx, tok, u)
		return e
	})
	return p, err
}

func (r *teamsRun) getJSON(u string) ([]byte, error) {
	var b []byte
	err := r.graph(func(tok string) error {
		var e error
		b, e = teamsJSONFn(r.ctx, tok, u)
		return e
	})
	return b, err
}

func (r *teamsRun) getAll(u string) ([]json.RawMessage, error) {
	guard := outlook.NewTeamsPageGuard(u)
	var out []json.RawMessage
	for next := u; next != ""; {
		p, err := r.page(next)
		if err != nil {
			return out, err
		}
		out = append(out, p.Items...)
		if p.NextLink == "" {
			break
		}
		if err := guard.Next(p.NextLink); err != nil {
			return out, err
		}
		next = p.NextLink
	}
	return out, nil
}

func (r *teamsRun) getBytes(u string, eventual bool) ([]byte, error) {
	var b []byte
	err := r.graph(func(tok string) error {
		var e error
		b, e = teamsBytesFn(r.ctx, tok, u, eventual)
		return e
	})
	return b, err
}

// loadKeys reads the job's synced keys once per run (the duplicate index).
func (r *teamsRun) loadKeys() error {
	if r.keys != nil {
		return nil
	}
	keys, err := r.store.listKeys(r.ctx)
	if err != nil {
		return fmt.Errorf("load teams index: %w", err)
	}
	r.keys = make(map[string]bool, len(keys))
	for _, k := range keys {
		r.keys[k] = true
	}
	return nil
}

func (r *teamsRun) keysWithPrefix(prefix string) []string {
	var out []string
	for k := range r.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// put writes one object: ownership checked, key in the job's tenant, write retried in memory,
// index row confirmed by the store. Storage full stops the run.
func (r *teamsRun) put(key string, data []byte, meta map[string]string) error {
	if r.stopErr != nil {
		return r.stopErr
	}
	if !r.owns() {
		return r.stopErr
	}
	if _, tid, _, _, _, ok := outlook.SplitResourceKey(key); !ok || tid != r.tenant {
		return fmt.Errorf("refusing key outside tenant %s: %s", r.tenant, key)
	}
	if !outlook.TeamsKeyFits(key) {
		return fmt.Errorf("key too long (%d bytes)", len(key))
	}
	var err error
	for attempt := 0; attempt < teamsWriteAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-r.ctx.Done():
				return r.ctx.Err()
			case <-time.After(teamsWriteBackoff * time.Duration(attempt)):
			}
		}
		if err = r.store.put(r.ctx, key, data, meta); err == nil {
			r.keys[key] = true
			return nil
		}
		if shouldAbortOnItemError(err) {
			r.stopErr = wrapStorageAbort("outlook_teams", err)
			return r.stopErr
		}
	}
	return err
}

// remove deletes an object and its index row.
func (r *teamsRun) remove(key string) error {
	if !r.owns() {
		return r.stopErr
	}
	if err := r.store.remove(r.ctx, key); err != nil {
		return err
	}
	delete(r.keys, key)
	return nil
}

func (r *teamsRun) loadState(key string, v interface{}) bool {
	if !r.keys[key] {
		return false
	}
	raw, err := r.store.download(r.ctx, key)
	if err != nil {
		if errors.Is(err, errTeamsNotFound) {
			_ = r.store.remove(r.ctx, key)
			delete(r.keys, key)
		}
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

func (r *teamsRun) saveState(key string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.put(key, b, nil)
}

// fail records a failed item, window or stream. The key ends in the item's own id. At the cap
// (5,000 entries or 2 MB) nothing new is added and false is returned: the caller then keeps its
// round or window open instead.
func (r *teamsRun) fail(key string, f repo.TeamsFailure, cause error, verifiedLookup bool) bool {
	if r.mem.TeamsFailures == nil {
		r.mem.TeamsFailures = map[string]repo.TeamsFailure{}
	}
	now := r.now()
	prev, exists := r.mem.TeamsFailures[key]
	if !exists && (len(r.mem.TeamsFailures) >= teamsMaxFailures || r.failuresFull) {
		r.failuresCapped++
		if r.status != nil {
			r.status.FailuresCapped = r.failuresCapped
			// Durable gap so capped drops are never silent; resource is the attempted failure key.
			r.addGap("failures_capped", key, r.now(), r.now())
		}
		r.warn("Too many failed Teams items; their streams are replayed instead")
		return false
	}
	f.Since, f.Attempts = now, 1
	if exists {
		f.Since, f.Attempts = prev.Since, prev.Attempts+1
		if f.Status == "" && prev.Status == teamsStoredFallback {
			f.Status = teamsStoredFallback
		}
	}
	f.LastAt = now
	status := outlook.ClassifyTeamsError(cause)
	var ge *outlook.TeamsGraphError
	if errors.As(cause, &ge) {
		f.HTTPStatus, f.ErrorCode = ge.Status, cmpOr(ge.InnerCode, ge.Code)
	} else if f.ErrorCode == "" {
		f.ErrorCode = status
	}
	keep := make([]string, 0, len(f.ItemIDs)+1)
	for _, id := range f.ItemIDs {
		keep = append(keep, id)
	}
	keep = append(keep, f.ResourceID)
	if cause != nil {
		f.ErrorMessage = outlook.SanitizeTeamsErrorText(cause.Error(), keep...)
	}
	backoff := time.Hour << min(f.Attempts-1, 5)
	if outlook.IsTeamsPermanentStatus(status) || backoff > teamsPermanentBackoff {
		backoff = teamsPermanentBackoff
	}
	skip := now.Add(backoff)
	f.SkipUntil = &skip
	if f.Kind == teamsFailItem && verifiedLookup && f.Attempts >= teamsItemMaxAttempts {
		f.Status = teamsPermanentFailure
	}
	r.mem.TeamsFailures[key] = f
	return true
}

func (r *teamsRun) clearFailure(key string) {
	delete(r.mem.TeamsFailures, key)
}

// releaseHold decides whether a held window or round is released: after 10 replays spread over at
// least 3 days its failed items become permanent failures recorded as gaps, so one poison item
// can't hold a stream forever.
func (r *teamsRun) releaseHold(held int, since *time.Time, failureKeys []string) bool {
	if held < teamsReleaseReplays || since == nil || r.now().Sub(*since) < teamsReleaseAge {
		return false
	}
	now := r.now()
	for _, k := range failureKeys {
		f, ok := r.mem.TeamsFailures[k]
		if !ok {
			continue
		}
		f.Status = teamsPermanentFailure
		r.mem.TeamsFailures[k] = f
		r.addSkipped(k, f, now)
	}
	return true
}

// releaseRound is releaseHold for a listing round: its stream entry (the only record of a refused
// nextLink) is released with the item entries, and a release with no entry at all (failures past
// the cap) is recorded as a gap of the stream, so held data is never dropped unrecorded.
func (r *teamsRun) releaseRound(held int, since *time.Time, failureKeys []string, streamKey string) bool {
	keys := append(append([]string(nil), failureKeys...), streamKey)
	if !r.releaseHold(held, since, keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := r.mem.TeamsFailures[k]; ok {
			return true
		}
	}
	r.addGap("released_round", streamKey, *since, r.now())
	return true
}

func (r *teamsRun) failureKeysWithPrefix(prefix string) []string {
	var out []string
	for k := range r.mem.TeamsFailures {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (r *teamsRun) setResource(kind, id, phase, status, code string) {
	if r.status.Resources == nil {
		r.status.Resources = map[string]teamsResourceStatus{}
	}
	key := kind + ":" + id + ":" + phase
	prev := r.status.Resources[key]
	r.status.Resources[key] = teamsResourceStatus{
		Type: kind, ID: id, Phase: phase, Status: status, Code: code, Scope: prev.Scope, UpdatedAt: outlook.TeamsUTC(r.now()),
	}
	r.count(status)
}

// consented reports whether the token holds every role the feature needs; a feature without
// them is recorded as not_consented and never called.
func (r *teamsRun) consented(feature string) bool {
	_, missing := r.notConsented[feature]
	return !missing
}

// loadConsent reads the token's roles: features and config parts without their role are skipped
// and reported as not consented.
func (r *teamsRun) loadConsent() {
	roles := outlook.TokenRoles(r.token)
	r.notConsented = outlook.TeamsFeaturesNotConsented(roles)
	for feature, role := range outlook.TeamsApplicationOnlyNotConsented(r.token) {
		if r.notConsented == nil {
			r.notConsented = map[string]string{}
		}
		r.notConsented[feature] = role
	}
	for feature, role := range r.notConsented {
		r.setFeature(feature, outlook.TeamsStatusNotConsented, "missing role "+role)
	}
	r.configNotConsented = outlook.TeamsConfigPartsNotConsented(roles)
	for part, role := range r.configNotConsented {
		r.setFeature(teamsConfigFeature(part), outlook.TeamsStatusNotConsented, "missing role "+role)
	}
	r.attendanceNotConsented = outlook.TeamsAttendanceNotConsented(r.token)
	for req, perm := range r.attendanceNotConsented {
		r.setFeature(teamsAttendanceFeature(req), outlook.TeamsStatusNotConsented, "missing permission "+perm)
	}
	r.historyNotConsented = outlook.TeamsHistoryNotConsented(r.token)
	for feature, reason := range r.historyNotConsented {
		r.setFeature(feature, outlook.TeamsStatusNotConsented, reason)
	}
}

func teamsConfigFeature(part string) string { return outlook.TeamsFeatureConfig + "_" + part }

// teamsAttendanceFeature is the feature an attendance request reports on: the reports on the
// attendance feature itself, the meeting lookup on its own.
func teamsAttendanceFeature(req string) string {
	if req == outlook.TeamsAttendanceReports {
		return outlook.TeamsFeatureAttendance
	}
	return outlook.TeamsFeatureAttendance + "_" + req
}

// attendanceAllowed reports whether the token can make an attendance request.
func (r *teamsRun) attendanceAllowed(req string) bool {
	_, missing := r.attendanceNotConsented[req]
	return !missing
}

// teamsAccessDenied reports statuses where Graph refuses a request for a whole scope (permission,
// consent, application access policy, licence), so it is reported, not retried per item.
func teamsAccessDenied(status string) bool {
	switch status {
	case outlook.TeamsStatusNotConsented, outlook.TeamsStatusNotApproved, outlook.TeamsStatusMissingPermission,
		outlook.TeamsStatusBlockedByPolicy, outlook.TeamsStatusDisabledByTenant, outlook.TeamsStatusNotLicensed:
		return true
	}
	return false
}

func (r *teamsRun) setFeature(name, capability, code string) {
	if r.status.Features == nil {
		r.status.Features = map[string]teamsFeatureStatus{}
	}
	prev, had := r.status.Features[name]
	if had && prev.CapabilityStatus == outlook.TeamsStatusOK && capability != outlook.TeamsStatusOK && capability != outlook.TeamsStatusOKEmpty &&
		prev.CheckedAt >= outlook.TeamsUTC(r.start) {
		return
	}
	coverage := "not_verified"
	switch capability {
	case outlook.TeamsStatusOK, outlook.TeamsStatusOKEmpty:
	case outlook.TeamsStatusPaidAPI:
		coverage = outlook.TeamsStatusPaidAPI
	default:
		coverage = "unavailable"
	}
	r.status.Features[name] = teamsFeatureStatus{CapabilityStatus: capability, CoverageStatus: coverage, Code: code, CheckedAt: outlook.TeamsUTC(r.now())}
}

func (r *teamsRun) addGap(kind, resource string, from, to time.Time) {
	for _, g := range r.status.Gaps {
		if g.Kind == kind && g.Resource == resource && g.From == outlook.TeamsUTC(from) {
			return
		}
	}
	r.status.Gaps = append(r.status.Gaps, teamsGap{Kind: kind, Resource: resource, From: outlook.TeamsUTC(from), To: outlook.TeamsUTC(to), RecordedAt: outlook.TeamsUTC(r.now())})
}

func (r *teamsRun) cursor() *repo.TeamsCursor {
	if r.mem.TeamsCursor == nil {
		r.mem.TeamsCursor = &repo.TeamsCursor{}
	}
	if r.mem.TeamsCursor.Positions == nil {
		r.mem.TeamsCursor.Positions = map[string]string{}
	}
	return r.mem.TeamsCursor
}

// eachResource walks ids (sorted) for a phase, resuming after the last finished id or, when the
// previous pass completed, starting at a position that rotates every run. Each resource gets at
// most teamsResourceCap; the cursor is checkpointed after each. done = the whole list was walked.
func (r *teamsRun) eachResource(phase string, ids []string, deadline time.Time, fn func(id string, capDeadline time.Time)) bool {
	sort.Strings(ids)
	n := len(ids)
	cur := r.cursor()
	start := 0
	if pos := cur.Positions[phase]; pos != "" {
		start = sort.SearchStrings(ids, pos)
		if start < n && ids[start] == pos {
			start++
		}
	} else if n > 0 {
		start = cur.Round % n
	}
	for k := 0; k < n; k++ {
		if r.stopped() || !r.now().Before(deadline) {
			return false
		}
		id := ids[(start+k)%n]
		capDeadline := r.now().Add(teamsResourceCap)
		if deadline.Before(capDeadline) {
			capDeadline = deadline
		}
		fn(id, capDeadline)
		if r.stopErr != nil {
			return false
		}
		cur.Phase, cur.Positions[phase] = phase, id
		if err := r.checkpoint(); err != nil && r.stopErr != nil {
			return false
		}
	}
	cur.Positions[phase] = ""
	return true
}

// run executes the phases in order, each within its budget slice (unused time passes on).
func (r *teamsRun) run(phases []string) error {
	stopBeat := r.startHeartbeat()
	defer stopBeat()
	r.beat()
	if err := r.loadKeys(); err != nil {
		return err
	}
	r.userScope, r.chatsSeen = map[string]bool{}, map[string]bool{}
	r.seriesSeen, r.seriesSubjects, r.midChecked, r.icalMids = map[string]string{}, map[string]string{}, map[string]string{}, map[string][]string{}
	r.status = &teamsStatusDoc{}
	r.loadState(outlook.TeamsStatusKey(r.tenantPrefix), r.status)
	r.status.TenantID, r.status.Warnings = r.tenant, nil
	r.status.IncomingChannelSkipped, r.status.FailuresCapped, r.status.StoredFallback = 0, 0, 0
	if r.status.Phases == nil {
		r.status.Phases = map[string]teamsPhaseStatus{}
	}

	if r.singleTeam == "" {
		r.mergeTeamJobs()
	}
	r.sel = teamsSelectionFromJob(r.input.Job)
	r.loadConsent()
	r.cursor().Round++

	budgets := teamsPhaseBudgets(teamsRunBudget, teamsCallsLagging(r.mem.TeamsCalls, r.now()))
	carry := time.Duration(0)
	for _, phase := range phases {
		if r.stopped() {
			break
		}
		slice := budgets[phase] + carry
		if len(phases) == 1 {
			slice = teamsRunBudget
		}
		phaseDeadline := r.now().Add(slice)
		if phaseDeadline.After(r.deadline) {
			phaseDeadline = r.deadline
		}
		r.counts = map[string]int{}
		ps := teamsPhaseStatus{StartedAt: outlook.TeamsUTC(r.now())}
		done := r.runPhase(phase, phaseDeadline)
		ps.FinishedAt, ps.Counts = outlook.TeamsUTC(r.now()), r.counts
		ps.Status = teamsComplete
		if !done {
			ps.Status = teamsIncomplete
		}
		r.status.Phases[phase] = ps
		carry = phaseDeadline.Sub(r.now())
		if carry < 0 {
			carry = 0
		}
		_ = r.checkpoint()
	}
	return r.finish()
}

func (r *teamsRun) runPhase(phase string, deadline time.Time) bool {
	switch phase {
	case teamsPhaseCalls:
		return r.runCallsPhase(deadline)
	case teamsPhaseRetries:
		return r.runRetriesPhase(deadline)
	case teamsPhaseTeams:
		return r.runTeamsPhase(deadline)
	case teamsPhaseChats:
		return r.runChatsPhase(deadline)
	case teamsPhaseMeetings:
		return r.runMeetingsPhase(deadline)
	}
	return true
}

// finish writes _status.json, records the run outcome, and sets a job warning only for
// required-content problems. Time-budget incomplete, optional call blocks, and expected
// limitations stay in _status.json so the next run can resume without spamming the job.
func (r *teamsRun) finish() error {
	now := r.now()
	r.status.UpdatedAt = outlook.TeamsUTC(now)
	r.status.FailuresCapped = r.failuresCapped
	if r.counts != nil {
		r.status.StoredFallback = r.counts[teamsStoredFallback]
	}
	statuses := map[string]int{}
	for _, res := range r.status.Resources {
		statuses[res.Status]++
	}
	if r.halt != "" {
		r.warn("Run stopped early (" + r.halt + "); the next run resumes")
	}
	r.saveFiles()
	if r.stopErr == nil {
		if b, err := json.Marshal(r.status); err == nil {
			if err := r.put(outlook.TeamsStatusKey(r.tenantPrefix), b, nil); err != nil {
				logger.Warn(r.ctx, "teams status write failed", logger.ErrorField(err))
			}
		}
	}
	r.mem.TeamsLastRun = &repo.TeamsRunSummary{FinishedAt: now, Statuses: statuses, Warnings: r.status.Warnings, Stopped: r.halt}
	_ = r.checkpoint()
	if job, err := r.input.Database.CronJobRepo.GetCronJobByID(r.input.Job.ID); err == nil && job != nil {
		r.input.Job.InputData = job.InputData
	}
	if r.stopErr != nil {
		return r.stopErr
	}
	if msg := teamsJobWarning(r.status.Resources, r.status.Warnings); msg != "" {
		r.input.Job.Message, r.input.Job.MessageStatus = msg, repo.JobMessageStatusWarning
	} else {
		r.input.Job.Message, r.input.Job.MessageStatus = "", ""
	}
	return nil
}

func teamsOptionalResource(res teamsResourceStatus) bool {
	if res.Phase == teamsPhaseCalls {
		return true
	}
	switch res.Type {
	case "calls", "pstn", "direct_routing", "direct-routing":
		return true
	}
	return false
}

func teamsResumeIncomplete(res teamsResourceStatus) bool {
	if res.Status != teamsIncomplete {
		return false
	}
	switch res.Code {
	case "", outlook.TeamsStatusThrottled:
		return true
	}
	return false
}

func teamsExpectedBlock(res teamsResourceStatus) bool {
	if res.Status != teamsBlocked {
		return false
	}
	if teamsOptionalResource(res) {
		return true
	}
	switch res.Code {
	case outlook.TeamsStatusPaidAPI, outlook.TeamsStatusNotConsented, outlook.TeamsStatusNotAvailable:
		return true
	}
	return false
}

func teamsExpectedStatusNote(msg string) bool {
	msg = strings.TrimSpace(msg)
	switch {
	case strings.HasPrefix(msg, "Run stopped early ("):
		return true
	case strings.HasPrefix(msg, "Messages of shared channels owned by another tenant"):
		return true
	case strings.HasPrefix(msg, "Some Teams files are in OneDrive or SharePoint"):
		return true
	case strings.HasPrefix(msg, "A team has too many held message windows"):
		return true
	case strings.HasPrefix(msg, "Too many held call or phone-log windows"):
		return true
	case strings.HasPrefix(msg, "No successful calls backup for over 25 days"):
		return true
	case strings.HasPrefix(msg, "Teams progress data is near its size limit"):
		return true
	case strings.HasPrefix(msg, "Too many failed Teams items"):
		return true
	case strings.HasPrefix(msg, "Too many Teams file references"):
		return true
	}
	return false
}

// teamsJobWarning is the job banner for required-content problems. Empty means the run succeeded
// (or only has resume/optional/expected notes) and the next run continues from checkpoints.
func teamsJobWarning(resources map[string]teamsResourceStatus, notes []string) string {
	var incomplete, blocked, gaps int
	for _, res := range resources {
		if teamsOptionalResource(res) {
			continue
		}
		switch res.Status {
		case teamsCompleteWithGaps:
			gaps++
		case teamsIncomplete:
			if !teamsResumeIncomplete(res) {
				incomplete++
			}
		case teamsBlocked:
			if !teamsExpectedBlock(res) {
				blocked++
			}
		}
	}
	var actionNotes []string
	for _, n := range notes {
		if !teamsExpectedStatusNote(n) {
			actionNotes = append(actionNotes, n)
		}
	}
	if incomplete == 0 && blocked == 0 && len(actionNotes) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	if incomplete > 0 {
		parts = append(parts, fmt.Sprintf("%d incomplete", incomplete))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	if gaps > 0 && (incomplete > 0 || blocked > 0 || len(actionNotes) > 0) {
		parts = append(parts, fmt.Sprintf("%d with gaps", gaps))
	}
	msg := teamsWarningPrefix
	if len(parts) > 0 {
		msg += ": " + strings.Join(parts, ", ")
	}
	if len(actionNotes) > 0 && len(parts) == 0 {
		msg += ": " + actionNotes[0]
	}
	return msg
}

// mergeTeamJobs imports old per-team jobs of the same connection into the tenant job's selection
// and pauses them, one recorded step at a time so a crash between steps repeats safely.
func (r *teamsRun) mergeTeamJobs() {
	jobs, err := r.input.Database.CronJobRepo.ListTeamJobsForMerge(r.input.Job)
	if err != nil {
		logger.Warn(r.ctx, "list team jobs for merge", logger.ErrorField(err))
		return
	}
	if len(jobs) == 0 {
		return
	}
	if r.mem.TeamsMerge == nil {
		r.mem.TeamsMerge = &repo.TeamsMergeState{}
	}
	m := r.mem.TeamsMerge
	if m.Imported == nil {
		m.Imported = map[string]repo.TeamsMergeImport{}
	}
	if m.Paused == nil {
		m.Paused = map[string]time.Time{}
	}
	for i := range jobs {
		old := &jobs[i]
		id := strconv.FormatUint(uint64(old.ID), 10)
		if _, done := m.Imported[id]; !done {
			teamID := cmpOr(repo.JobTeamsTeamID(old), old.ResourceID)
			if err := r.importTeamJob(old, teamID); err != nil {
				logger.Warn(r.ctx, "import team job", logger.Int("job_id", int(old.ID)), logger.ErrorField(err))
				continue
			}
			m.Imported[id] = repo.TeamsMergeImport{TeamID: teamID, ImportedAt: r.now()}
			if r.checkpoint() != nil {
				return
			}
		}
		if _, done := m.Paused[id]; !done {
			if old.Active {
				if err := r.input.Database.CronJobRepo.PauseJobs([]uint{old.ID}, teamsMergedJobMessage); err != nil {
					logger.Warn(r.ctx, "pause merged team job", logger.Int("job_id", int(old.ID)), logger.ErrorField(err))
					continue
				}
			}
			m.Paused[id] = r.now()
			if r.checkpoint() != nil {
				return
			}
		}
	}
	if job, err := r.input.Database.CronJobRepo.GetCronJobByID(r.input.Job.ID); err == nil && job != nil {
		r.input.Job.InputData = job.InputData
	}
}

// importTeamJob adds the old job's team and channels to the selection (union rules) and makes the
// team's next window a full listing, deduped by the index.
func (r *teamsRun) importTeamJob(old *repo.CronJobListingDB, teamID string) error {
	var channels []string
	if old.InputData != nil && old.InputData.Json() != nil {
		channels = repo.InputStringList((*old.InputData.Json())["channel_ids"])
	}
	err := r.input.Database.CronJobRepo.UpdateJobInputData(r.input.Job.ID, func(in map[string]interface{}) map[string]interface{} {
		return repo.TeamsSelectionAddTeam(in, teamID, channels)
	})
	if err != nil {
		return err
	}
	stateKey := outlook.TeamsTeamStateKey(r.tenantPrefix, teamID)
	var st teamsTeamState
	r.loadState(stateKey, &st)
	if st.HighWater == nil && !st.FullListing {
		st.FullListing = true
		return r.saveState(stateKey, &st)
	}
	return nil
}

// teamsTeamState is {tenant}/tenant/{tenantId}/_state/teams/{teamId}.json.
type teamsTeamState struct {
	Layout         int                          `json:"layout,omitempty"`
	Windows        []repo.TeamsWindow           `json:"windows,omitempty"`
	HighWater      *time.Time                   `json:"high_water,omitempty"`
	FullListing    bool                         `json:"full_listing,omitempty"`
	ClosedWindows  int                          `json:"closed_windows,omitempty"`
	LastReconcile  int                          `json:"last_reconcile,omitempty"`
	Channels       map[string]teamsChannelState `json:"channels,omitempty"`
	MetaHash       string                       `json:"meta_hash,omitempty"`
	ConfigAt       *time.Time                   `json:"config_at,omitempty"`
	Status         string                       `json:"status,omitempty"`
	StatusCode     string                       `json:"status_code,omitempty"`
	SkipUntil      *time.Time                   `json:"skip_until,omitempty"`
	Scope          string                       `json:"scope,omitempty"`
	ScopeChangedAt *time.Time                   `json:"scope_changed_at,omitempty"`
	LastSuccessAt  *time.Time                   `json:"last_success_at,omitempty"`
	Retained       teamsRetainedState           `json:"retained"`
	Shared         teamsSharedState             `json:"shared"`
}

// teamsChannelState is a channel's conversion progress from the old meta/data layout.
type teamsChannelState struct {
	MembershipType string `json:"membership_type,omitempty"`
	Hash           string `json:"hash,omitempty"`
	Converted      bool   `json:"converted,omitempty"`
	Verified       bool   `json:"verified,omitempty"`
	Cleaned        bool   `json:"cleaned,omitempty"`
}

func teamsHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// runTeamsPhase backs up every team in scope.
func (r *teamsRun) runTeamsPhase(deadline time.Time) bool {
	if !r.consented(outlook.TeamsFeatureChannelMessages) {
		return true
	}
	teams, err := r.listTeams()
	if err != nil {
		status := outlook.ClassifyTeamsError(err)
		r.setFeature("teams_channel_messages", status, status)
		r.fail("teams:stream:tenant:"+r.tenant, repo.TeamsFailure{Phase: "channels", Kind: teamsFailStream, ResourceType: "tenant", ResourceID: r.tenant}, err, false)
		return false
	}
	r.clearFailure("teams:stream:tenant:" + r.tenant)
	byID := map[string]teamsTeamInfo{}
	ids := make([]string, 0, len(teams))
	for _, t := range teams {
		byID[t.ID] = t
		ids = append(ids, t.ID)
	}
	if r.singleTeam == "" {
		r.status.Teams = r.status.Teams[:0]
		for _, id := range ids {
			r.status.Teams = append(r.status.Teams, teamsTeamSummary{ID: id, DisplayName: byID[id].DisplayName})
		}
		inScope := make(map[string]bool, len(ids))
		for _, id := range ids {
			inScope[id] = true
		}
		r.markOutOfScope("teams", inScope)
	}
	return r.eachResource(teamsPhaseTeams, ids, deadline, func(id string, capDeadline time.Time) {
		status, code := r.syncTeam(byID[id], capDeadline)
		r.setResource("team", id, teamsPhaseTeams, status, code)
	})
}

func (r *teamsRun) listTeams() ([]teamsTeamInfo, error) {
	if r.singleTeam != "" {
		return []teamsTeamInfo{{ID: r.singleTeam}}, nil
	}
	items, err := r.getAll(outlook.TeamsTenantTeamsURL())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []teamsTeamInfo
	for _, raw := range items {
		var g struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}
		if json.Unmarshal(raw, &g) != nil || g.ID == "" || !r.sel.teamSelected(g.ID) {
			continue
		}
		seen[g.ID] = true
		out = append(out, teamsTeamInfo{ID: g.ID, DisplayName: g.DisplayName})
	}
	for id := range r.sel.TeamIDs {
		if !seen[id] {
			out = append(out, teamsTeamInfo{ID: id})
		}
	}
	return out, nil
}

// markOutOfScope marks state objects of teams or users no longer in scope: deselected when a
// selection excludes them, source_deleted when "all" no longer lists them. Backups are never
// deleted; the state object itself is removed 90 days later.
func (r *teamsRun) markOutOfScope(kind string, inScope map[string]bool) {
	prefix := r.tenantPrefix + "/_state/" + kind + "/"
	selection := len(r.sel.TeamIDs) > 0
	if kind == "users" {
		selection = len(r.sel.UserIDs) > 0
	}
	scope := teamsDeselected
	if !selection {
		scope = teamsSourceDeleted
	}
	for _, key := range r.keysWithPrefix(prefix) {
		id, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json"))
		if err != nil {
			continue
		}
		if inScope[id] {
			continue
		}
		var st map[string]interface{}
		if !r.loadState(key, &st) {
			continue
		}
		cur, _ := st["scope"].(string)
		if cur == scope {
			if at, ok := st["scope_changed_at"].(string); ok {
				if t, ok := outlook.ParseTeamsTime(at); ok && r.now().Sub(t) > teamsDeselectedKeep {
					_ = r.remove(key)
				}
			}
			continue
		}
		st["scope"], st["scope_changed_at"] = scope, outlook.TeamsUTC(r.now())
		_ = r.saveState(key, st)
		r.setResource(strings.TrimSuffix(kind, "s"), id, kind, scope, "")
	}
}

type teamsChannelInfo struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	MembershipType string `json:"membershipType"`
	TenantID       string `json:"tenantId"`
	raw            json.RawMessage
}

type teamsSharedChannelCoverageEntry struct {
	ChannelID      string `json:"channel_id"`
	OwnerTenantID  string `json:"owner_tenant_id,omitempty"`
	OwnerScope     string `json:"owner_scope"`
	MessageStatus  string `json:"message_status"`
	CoverageReason string `json:"coverage_reason"`
}

// Message statuses of a shared channel in _team.json.
const (
	teamsSharedListed       = "listed_per_channel"
	teamsSharedAtHostTeam   = "stored_under_host_team"
	teamsSharedNotCollected = "not_collected"
)

// teamsSharedChannelCoverage says, per shared channel, whether this team's backup lists its
// messages. Only a channel this team hosts in this tenant is listed: app-only access to channel
// messages works only from the channel owner's tenant, and a channel another team hosts belongs to
// that team.
func teamsSharedChannelCoverage(channels []teamsChannelInfo, tenantID string, shared teamsSharedChannels) []teamsSharedChannelCoverageEntry {
	var out []teamsSharedChannelCoverageEntry
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	for _, channel := range channels {
		if !teamsIsShared(channel) {
			continue
		}
		ownerTenantID := strings.ToLower(strings.TrimSpace(channel.TenantID))
		entry := teamsSharedChannelCoverageEntry{ChannelID: channel.ID, OwnerTenantID: ownerTenantID,
			OwnerScope: "owner_tenant_unknown", MessageStatus: teamsSharedNotCollected, CoverageReason: "owner_tenant_unknown"}
		switch {
		case ownerTenantID == "":
		case ownerTenantID != tenantID:
			entry.OwnerScope, entry.CoverageReason = "incoming_cross_tenant", "app_only_access_requires_owner_tenant"
		case !shared.resolved:
			entry.OwnerScope, entry.CoverageReason = "same_tenant", "host_team_unresolved"
		case shared.incoming[channel.ID]:
			entry.OwnerScope, entry.MessageStatus, entry.CoverageReason = "same_tenant", teamsSharedAtHostTeam, "hosted_by_another_team"
		default:
			entry.OwnerScope, entry.MessageStatus, entry.CoverageReason = "same_tenant", teamsSharedListed, "getAllMessages_documents_public_and_private_only"
		}
		out = append(out, entry)
	}
	return out
}

func teamsSharedGaps(entries []teamsSharedChannelCoverageEntry) int {
	n := 0
	for _, e := range entries {
		if e.MessageStatus == teamsSharedNotCollected {
			n++
		}
	}
	return n
}

// syncTeam backs up one team: details, channels, config, messages, then the old-layout conversion.
func (r *teamsRun) syncTeam(team teamsTeamInfo, deadline time.Time) (string, string) {
	stateKey := outlook.TeamsTeamStateKey(r.tenantPrefix, team.ID)
	var st teamsTeamState
	r.loadState(stateKey, &st)
	if st.Scope != "" {
		st.Scope, st.ScopeChangedAt = "", nil
	}
	now := r.now()
	if st.SkipUntil != nil && now.Before(*st.SkipUntil) {
		return cmpOr(st.Status, teamsBlocked), st.StatusCode
	}
	teamPrefix := outlook.TeamsTeamPrefix(r.tenant, team.ID)
	streamKey := "channels:stream:team:" + team.ID
	streamFail := func(err error) (string, string) {
		status := outlook.ClassifyTeamsError(err)
		r.fail(streamKey, repo.TeamsFailure{Phase: "channels", Kind: teamsFailStream, ResourceType: "team", ResourceID: team.ID}, err, false)
		st.StatusCode = status
		result := teamsIncomplete
		switch {
		case status == outlook.TeamsStatusNotFound:
			result = teamsSourceDeleted
		case outlook.IsTeamsPermanentStatus(status):
			result = teamsBlocked
			skip := now.Add(teamsPermanentBackoff)
			st.SkipUntil = &skip
			r.setFeature("teams_channel_messages", status, status)
		}
		st.Status = result
		_ = r.saveState(stateKey, &st)
		return result, status
	}

	detail, err := r.getJSON(outlook.TeamsTeamURL(team.ID))
	if err != nil {
		return streamFail(err)
	}
	chItems, err := r.getAll(outlook.TeamsAllChannelsURL(team.ID))
	if err != nil {
		return streamFail(err)
	}
	channels := make([]teamsChannelInfo, 0, len(chItems))
	for _, raw := range chItems {
		var ch teamsChannelInfo
		if json.Unmarshal(raw, &ch) != nil || ch.ID == "" || !r.sel.channelSelected(team.ID, ch.ID) {
			continue
		}
		ch.raw = raw
		channels = append(channels, ch)
	}
	if st.Channels == nil {
		st.Channels = map[string]teamsChannelState{}
	}
	shared, sharedErr := r.resolveSharedChannels(team.ID, channels)
	if err := r.writeTeamMeta(&st, team, teamPrefix, detail, channels, shared, "", "", ""); err != nil {
		return streamFail(err)
	}
	if st.ConfigAt == nil || now.Sub(*st.ConfigAt) >= teamsConfigEvery {
		if r.syncTeamConfig(team.ID, teamPrefix, detail, teamsOwnTenantChannels(channels, r.tenant)) {
			st.ConfigAt = &now
		}
	}
	r.clearFailure(streamKey)
	st.SkipUntil, st.StatusCode = nil, ""

	status := r.syncTeamMessages(&st, team, teamPrefix, deadline)
	msgStatus, msgCode := status, st.StatusCode
	if status != teamsIncomplete && st.Layout < repo.TeamsLayoutFiles {
		if r.convertLegacyTeam(&st, teamPrefix, deadline) {
			st.Layout = repo.TeamsLayoutFiles
		} else {
			status = teamsIncomplete
		}
	}
	save := func() error { return r.saveState(stateKey, &st) }
	sharedStatus := ""
	if sharedErr != nil {
		sharedStatus = r.sharedLookupFailed(team.ID, sharedErr)
	} else if len(shared.hosted) > 0 {
		sharedStatus = r.syncSharedChannels(&st.Shared, team.ID, teamPrefix, shared.hosted, deadline, save)
	}
	if sharedStatus == teamsIncomplete || sharedStatus == teamsCompleteWithGaps {
		status = teamsWorst(status, sharedStatus)
	}
	if !r.stopped() && r.now().Before(deadline) {
		s := r.syncRetained(&st.Retained, teamsChannelRetainedScope(team.ID, teamPrefix), deadline, save)
		if s == teamsIncomplete || s == teamsCompleteWithGaps {
			status = teamsWorst(status, s)
		}
	}
	sharedCoverage := teamsSharedChannelCoverage(channels, r.tenant, shared)
	if n := teamsSharedGaps(sharedCoverage); n > 0 {
		for _, e := range sharedCoverage {
			if e.CoverageReason == "app_only_access_requires_owner_tenant" {
				r.status.IncomingChannelSkipped++
			}
		}
		r.warn("Messages of shared channels owned by another tenant, or whose host team couldn't be read, are not collected")
		if status == teamsComplete || status == teamsCompleteWithRetries {
			status = teamsCompleteWithGaps
		}
	}
	if status == teamsComplete || status == teamsCompleteWithGaps || status == teamsCompleteWithRetries {
		st.LastSuccessAt = &now
		r.setFeature("teams_channel_messages", outlook.TeamsStatusOK, "")
	}
	st.Status = status
	if err := r.writeTeamMeta(&st, team, teamPrefix, detail, channels, shared, msgStatus, msgCode, sharedStatus); err != nil {
		return streamFail(err)
	}
	_ = r.saveState(stateKey, &st)
	return status, ""
}

// teamsCoverageFromSync maps a collector result to a channel_coverage status. verified is used
// only after a complete successful window (no open gaps).
func teamsCoverageFromSync(status, code string) string {
	switch status {
	case "":
		return "not_verified"
	case teamsComplete, teamsCompleteWithRetries:
		return "verified"
	case teamsCompleteWithGaps, teamsIncomplete:
		return "incomplete"
	case teamsBlocked, teamsSourceDeleted:
		if outlook.IsTeamsPermanentStatus(code) || code == outlook.TeamsStatusNotConsented ||
			code == outlook.TeamsStatusMissingPermission || code == outlook.TeamsStatusBlockedByPolicy ||
			code == outlook.TeamsStatusDisabledByTenant || code == outlook.TeamsStatusNotFound ||
			status == teamsSourceDeleted {
			return "inaccessible"
		}
		return "failed"
	default:
		return "not_verified"
	}
}

// teamsBuildChannelCoverage sets standard/private from the getAllMessages window result and shared
// from shared-channel listing + per-channel gap rows. States stay distinguishable.
func teamsBuildChannelCoverage(channels []teamsChannelInfo, sharedCoverage []teamsSharedChannelCoverageEntry, msgStatus, msgCode, sharedStatus string) map[string]string {
	stdPriv := teamsCoverageFromSync(msgStatus, msgCode)
	coverage := map[string]string{"standard": stdPriv, "private": stdPriv, "shared": "not_verified"}
	switch {
	case teamsSharedGaps(sharedCoverage) > 0:
		coverage["shared"] = "incomplete"
	case sharedStatus != "":
		coverage["shared"] = teamsCoverageFromSync(sharedStatus, "")
		if coverage["shared"] == "verified" && teamsSharedGaps(sharedCoverage) > 0 {
			coverage["shared"] = "incomplete"
		}
	case len(sharedCoverage) == 0 && msgStatus != "" &&
		(msgStatus == teamsComplete || msgStatus == teamsCompleteWithRetries || msgStatus == teamsCompleteWithGaps):
		// No shared channels on this team: treat shared as verified once the team message window completed.
		if msgStatus == teamsComplete || msgStatus == teamsCompleteWithRetries {
			coverage["shared"] = "verified"
		} else {
			coverage["shared"] = "incomplete"
		}
	}
	return coverage
}

// writeTeamMeta writes _team.json and each channel's _channel.json when they changed.
// msgStatus/sharedStatus empty means coverage stays not_verified (pre-sync write).
func (r *teamsRun) writeTeamMeta(st *teamsTeamState, team teamsTeamInfo, teamPrefix string, detail []byte, channels []teamsChannelInfo, shared teamsSharedChannels, msgStatus, msgCode, sharedStatus string) error {
	sharedCoverage := teamsSharedChannelCoverage(channels, r.tenant, shared)
	coverage := teamsBuildChannelCoverage(channels, sharedCoverage, msgStatus, msgCode, sharedStatus)
	list := make([]json.RawMessage, 0, len(channels))
	for _, ch := range channels {
		list = append(list, ch.raw)
	}
	doc := map[string]interface{}{
		"team":                            json.RawMessage(detail),
		"channels":                        list,
		"channel_coverage":                coverage,
		"shared_channel_message_coverage": sharedCoverage,
		"storx_backup":                    outlook.TeamsBackupBlock{TenantID: r.tenant, TeamID: team.ID},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if h := teamsHash(b); h != st.MetaHash || !r.keys[outlook.TeamsTeamMetaKey(teamPrefix)] {
		if err := r.put(outlook.TeamsTeamMetaKey(teamPrefix), b, nil); err != nil {
			return err
		}
		st.MetaHash = h
	}
	for _, ch := range channels {
		cs := st.Channels[ch.ID]
		cs.MembershipType = ch.MembershipType
		key := outlook.TeamsChannelMetaKey(teamPrefix, ch.ID)
		body, err := outlook.PatchTeamsBackup(ch.raw, outlook.TeamsBackupBlock{TenantID: r.tenant, TeamID: team.ID, ChannelID: ch.ID})
		if err != nil {
			return err
		}
		if h := teamsHash(ch.raw); h != cs.Hash || !r.keys[key] {
			if err := r.put(key, body, map[string]string{"teams-channel-id": ch.ID}); err != nil {
				return err
			}
			cs.Hash = h
		}
		st.Channels[ch.ID] = cs
	}
	return nil
}

// syncTeamConfig stores settings, members, apps and tags of the team and members and tabs of each
// channel, each part on its own. A part without its role is skipped, and one Graph refuses for
// the tenant (teamsAccessDenied) is reported on its feature without a failure entry. true when no
// part failed for another reason.
func (r *teamsRun) syncTeamConfig(teamID, teamPrefix string, detail []byte, channels []teamsChannelInfo) bool {
	ok := true
	store := func(key, failKey string, value interface{}) {
		doc := map[string]interface{}{"value": value, "storx_backup": outlook.TeamsBackupBlock{TenantID: r.tenant, TeamID: teamID, BackedUpAt: outlook.TeamsUTC(r.now())}}
		b, _ := json.Marshal(doc)
		if err := r.put(key, b, nil); err != nil {
			ok = false
			r.fail(failKey, repo.TeamsFailure{Phase: "team_config", Kind: teamsFailItem, ResourceType: "team", ResourceID: teamID}, err, true)
			return
		}
		r.clearFailure(failKey)
	}
	fetch := func(part, u, failKey string, ids map[string]string) ([]json.RawMessage, bool) {
		if _, missing := r.configNotConsented[part]; missing {
			r.clearFailure(failKey)
			return nil, false
		}
		items, err := r.getAll(u)
		if err == nil {
			r.setFeature(teamsConfigFeature(part), outlook.TeamsStatusOK, "")
			return items, true
		}
		if code := outlook.ClassifyTeamsError(err); teamsAccessDenied(code) {
			r.clearFailure(failKey)
			r.setFeature(teamsConfigFeature(part), code, code)
			return nil, false
		}
		ok = false
		r.fail(failKey, repo.TeamsFailure{Phase: "team_config", Kind: teamsFailItem, ResourceType: "team", ResourceID: teamID, ItemIDs: ids}, err, true)
		return nil, false
	}
	store(outlook.TeamsTeamConfigKey(teamPrefix, "settings"), "team_config:item:"+teamID+":settings", json.RawMessage(detail))
	parts := outlook.TeamsTeamConfigURLs(teamID)
	for _, part := range []string{"members", "apps", "tags"} {
		failKey := "team_config:item:" + teamID + ":" + part
		if items, got := fetch(part, parts[part], failKey, map[string]string{"part": part}); got {
			store(outlook.TeamsTeamConfigKey(teamPrefix, part), failKey, items)
		}
	}
	for _, ch := range channels {
		urls := outlook.TeamsChannelConfigURLs(teamID, ch.ID)
		for _, part := range []string{"members", "tabs"} {
			failKey := "team_config:item:" + teamID + ":channel_" + part + ":" + ch.ID
			if items, got := fetch("channel_"+part, urls[part], failKey, map[string]string{"channel_id": ch.ID, "part": part}); got {
				store(outlook.TeamsChannelConfigKey(teamPrefix, ch.ID, part), failKey, items)
			}
		}
	}
	status := outlook.TeamsStatusOK
	if !ok {
		status = outlook.TeamsStatusError
	}
	r.setFeature("teams_config", status, "")
	return ok
}

// syncTeamMessages runs the team's open getAllMessages windows plus a new one up to the run's
// end time. A window closes only after its last page with no failed item; a held window is
// replayed on its own while newer windows continue from the high-water mark.
func (r *teamsRun) syncTeamMessages(st *teamsTeamState, team teamsTeamInfo, teamPrefix string, deadline time.Time) string {
	r.planTeamWindow(st)
	status := teamsComplete
	for i := 0; i < len(st.Windows); {
		w := &st.Windows[i]
		closed, result := r.runTeamWindow(st, w, team, teamPrefix, deadline)
		if result == teamsIncomplete || result == teamsBlocked {
			return result
		}
		if result == teamsCompleteWithGaps {
			status = teamsCompleteWithGaps
		}
		if closed {
			if st.HighWater == nil || w.To.After(*st.HighWater) {
				hw := w.To
				st.HighWater = &hw
			}
			st.ClosedWindows++
			st.FullListing = false
			st.Windows = append(st.Windows[:i], st.Windows[i+1:]...)
			continue
		}
		status = teamsIncomplete
		i++
	}
	return status
}

// planTeamWindow adds the next window [high water, run end) unless one already ends there. Every
// 7th closed window the new one reaches back 30 days to catch late-visible changes.
func (r *teamsRun) planTeamWindow(st *teamsTeamState) {
	from := time.Time{}
	if !st.FullListing && st.HighWater != nil {
		from = *st.HighWater
	}
	for _, w := range st.Windows {
		if !w.To.Before(r.to) {
			return
		}
		if w.To.After(from) && !st.FullListing {
			from = w.To
		}
	}
	if len(st.Windows) >= teamsMaxOpenWindows {
		r.warn("A team has too many held message windows; newer messages wait until they close")
		return
	}
	if !from.IsZero() && st.ClosedWindows-st.LastReconcile >= teamsReconcileEvery {
		if back := r.to.Add(-teamsReconcileSpan); back.Before(from) {
			from = back
		}
		st.LastReconcile = st.ClosedWindows
	}
	st.Windows = append(st.Windows, repo.TeamsWindow{From: from, To: r.to})
}

func teamsWindowFrom(from time.Time) time.Time {
	if from.IsZero() {
		return from
	}
	return from.Add(-teamsWindowOverlap)
}

// runTeamWindow processes one window page by page (paging contract): items are stored or recorded
// before PageLink moves; the window closes only after its last page.
func (r *teamsRun) runTeamWindow(st *teamsTeamState, w *repo.TeamsWindow, team teamsTeamInfo, teamPrefix string, deadline time.Time) (bool, string) {
	stateKey := outlook.TeamsTeamStateKey(r.tenantPrefix, team.ID)
	pageURL := w.PageLink
	if pageURL == "" {
		pageURL = outlook.TeamsChannelAllMessagesURL(team.ID, teamsWindowFrom(w.From), w.To)
	}
	guard := outlook.NewTeamsPageGuard(pageURL)
	failPrefix := "channels:item:" + team.ID + ":"
	streamKey := "channels:stream:team:" + team.ID
	for {
		if r.stopped() || !r.now().Before(deadline) {
			w.PageLink = pageURL
			return false, teamsIncomplete
		}
		page, err := r.page(pageURL)
		if err != nil {
			status := outlook.ClassifyTeamsError(err)
			r.fail(streamKey, repo.TeamsFailure{Phase: "channels", Kind: teamsFailStream, ResourceType: "team", ResourceID: team.ID}, err, false)
			if !outlook.TeamsVerified(outlook.TeamsCheckReplayNextLink) {
				w.PageLink = ""
			}
			if outlook.IsTeamsPermanentStatus(status) {
				st.StatusCode = status
				return false, teamsBlocked
			}
			return false, teamsIncomplete
		}
		lookup := outlook.TeamsVerified(outlook.TeamsCheckLookupChannelMessage)
		for _, raw := range page.Items {
			ids, err := r.storeChannelMessage(team.ID, teamPrefix, raw)
			if r.stopErr != nil {
				return false, teamsIncomplete
			}
			key := failPrefix + ids["channel_id"] + ":" + ids["root_id"] + ":" + ids["message_id"]
			if err != nil {
				recorded := r.fail(key, repo.TeamsFailure{Phase: "channels", Kind: teamsFailItem, ResourceType: "team", ResourceID: team.ID, ItemIDs: ids}, err, lookup)
				if !lookup || !recorded {
					w.Dirty = true
				}
				continue
			}
			r.clearFailure(key)
		}
		// A rejected nextLink ends the round dirty: the window is replayed from its start and, past
		// the hold limit, released with the paging failure listed as a gap.
		last, pagingFailed := page.NextLink == "", false
		if !last {
			if err := guard.Next(page.NextLink); err != nil {
				st.StatusCode = outlook.TeamsStatusPaging
				r.fail(streamKey, repo.TeamsFailure{Phase: "channels", Kind: teamsFailStream, ResourceType: "team", ResourceID: team.ID}, err, false)
				w.Dirty, last, pagingFailed = true, true, true
			}
		}
		if last {
			if w.Dirty {
				w.Held++
				if w.HeldSince == nil {
					t := r.now()
					w.HeldSince = &t
				}
				w.PageLink, w.Dirty = "", false
				if !pagingFailed {
					r.clearFailure(streamKey)
				}
				if r.releaseRound(w.Held, w.HeldSince, r.failureKeysWithPrefix(failPrefix), streamKey) {
					return true, teamsCompleteWithGaps
				}
				_ = r.saveState(stateKey, st)
				return false, teamsIncomplete
			}
			r.clearFailure(streamKey)
			return true, teamsComplete
		}
		w.PageLink = page.NextLink
		if err := r.saveState(stateKey, st); err != nil {
			w.PageLink = pageURL
			return false, teamsIncomplete
		}
		if err := r.checkpoint(); err != nil && r.stopErr != nil {
			return false, teamsIncomplete
		}
		pageURL = page.NextLink
	}
}

// teamsMessageHead is the part of a Graph chatMessage the backup reads.
type teamsMessageHead struct {
	ID                   string `json:"id"`
	ReplyToID            string `json:"replyToId"`
	ETag                 string `json:"etag"`
	ChatID               string `json:"chatId"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	LastEditedDateTime   string `json:"lastEditedDateTime"`
	DeletedDateTime      string `json:"deletedDateTime"`
	ChannelIdentity      *struct {
		TeamID    string `json:"teamId"`
		ChannelID string `json:"channelId"`
	} `json:"channelIdentity"`
	From *struct {
		User *struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"from"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Attachments []struct {
		ID          string `json:"id"`
		ContentType string `json:"contentType"`
		ContentURL  string `json:"contentUrl"`
		Name        string `json:"name"`
		// Content is a card as a JSON string (Graph may also send an object), kept raw.
		Content json.RawMessage `json:"content"`
	} `json:"attachments"`
	EventDetail *struct {
		CallRecordingURL string `json:"callRecordingUrl"`
	} `json:"eventDetail"`
	Reactions json.RawMessage `json:"reactions"`
}

var teamsHostedLink = regexp.MustCompile(`(?i)(?:https?://[^\s"'<>]*?/)?hostedContents/[^/"'?\s<>]+/\$value`)

// teamsHostedLinks returns the hosted content links a message references: in its body (inline
// images, custom emoji), its attachments' card content (card images, code snippets) and its
// reactions' reactionContentUrl (custom reactions). Links are returned whole so the content is
// fetched from the message each one names, which isn't always this message.
func teamsHostedLinks(head teamsMessageHead) []string {
	sources := []string{head.Body.Content}
	for _, a := range head.Attachments {
		var card string
		if json.Unmarshal(a.Content, &card) != nil {
			card = string(a.Content)
		}
		sources = append(sources, strings.ReplaceAll(card, `\/`, "/"))
	}
	seen := map[string]bool{}
	var out []string
	add := func(link string) {
		if link = strings.TrimSpace(link); link != "" && !seen[link] {
			seen[link] = true
			out = append(out, link)
		}
	}
	for _, s := range sources {
		for _, link := range teamsHostedLink.FindAllString(s, -1) {
			add(link)
		}
	}
	var reactions []struct {
		ReactionContentURL string `json:"reactionContentUrl"`
	}
	if json.Unmarshal(head.Reactions, &reactions) == nil {
		for _, re := range reactions {
			add(re.ReactionContentURL)
		}
	}
	return out
}

// teamsMessageTarget is where and how one message is stored. hostedKey maps a hosted content
// reference to its key under the message that owns it; false when that message isn't in this
// chat or channel.
type teamsMessageTarget struct {
	key       string
	hostedKey func(ref outlook.TeamsHostedRef) (string, bool)
	block     outlook.TeamsBackupBlock
	legacy    []string
}

// storeChannelMessage stores one getAllMessages item in its channel section. ids always carries
// channel_id, root_id and message_id (for the failure key).
func (r *teamsRun) storeChannelMessage(teamID, teamPrefix string, raw json.RawMessage) (map[string]string, error) {
	return r.storeChannelMessageFrom(teamID, teamPrefix, raw, "getAllMessages")
}

// storeChannelMessageFrom is storeChannelMessage for a message read from source. A message of a
// channel another team hosts is not stored under this team.
func (r *teamsRun) storeChannelMessageFrom(teamID, teamPrefix string, raw json.RawMessage, source string) (map[string]string, error) {
	var head teamsMessageHead
	if err := json.Unmarshal(raw, &head); err != nil || head.ID == "" {
		return map[string]string{"message_id": "unparsed"}, fmt.Errorf("unparseable channel message")
	}
	ch := ""
	if head.ChannelIdentity != nil {
		ch = head.ChannelIdentity.ChannelID
	}
	root := cmpOr(head.ReplyToID, head.ID)
	ids := map[string]string{"team_id": teamID, "channel_id": ch, "root_id": root, "message_id": head.ID}
	if ch == "" {
		return ids, fmt.Errorf("channel message without channelIdentity")
	}
	if !r.sel.channelSelected(teamID, ch) {
		return ids, nil
	}
	if r.sharedIncoming[teamID][ch] {
		r.count("incoming_channel_skipped")
		return ids, nil
	}
	var legacy []string
	for _, k := range []string{
		outlook.TeamsIDBasedMetaKey(teamPrefix, ch, head.ID, head.CreatedDateTime),
		outlook.TeamsIDBasedDataKey(teamPrefix, ch, head.ID, head.CreatedDateTime),
	} {
		if r.keys[k] {
			legacy = append(legacy, k)
		}
	}
	_, err := r.storeMessage(raw, head, teamsMessageTarget{
		key: outlook.TeamsChannelMessageKey(teamPrefix, ch, root, head.ID, head.CreatedDateTime),
		hostedKey: func(ref outlook.TeamsHostedRef) (string, bool) {
			if ref.ChatID != "" || !strings.EqualFold(ref.TeamID, teamID) || ref.ChannelID != ch {
				return "", false
			}
			return outlook.TeamsChannelHostedKey(teamPrefix, ch, ref.MessageID, ref.ContentID), true
		},
		block:  outlook.TeamsBackupBlock{TenantID: r.tenant, TeamID: teamID, ChannelID: ch, ReplyToID: head.ReplyToID, Source: source},
		legacy: legacy,
	})
	return ids, err
}

// teamsInlineReactions returns the message's own reactions array (Graph v1.0 has no per-message
// reactions listing); [] when the field is missing or null.
func teamsInlineReactions(message map[string]json.RawMessage) json.RawMessage {
	r := bytes.TrimSpace(message["reactions"])
	if len(r) == 0 || bytes.Equal(r, []byte("null")) {
		return json.RawMessage("[]")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, r); err != nil {
		return r
	}
	return buf.Bytes()
}

// storeMessage applies the version rule and writes one message object with its inline images.
// Outcomes: written, skipped, removed (deletion applied as metadata), version_conflict,
// updated_reactions, updated_hosted. A hosted content that can't be stored doesn't stop the
// message: it is written with the content listed as missing and an error is returned so the
// caller records the item and replays it; an unchanged replay retries only the missing contents.
func (r *teamsRun) storeMessage(raw json.RawMessage, head teamsMessageHead, t teamsMessageTarget) (string, error) {
	var stored *outlook.TeamsVersion
	storedRemoved := ""
	storedReactions := ""
	storedHostedMissing := ""
	if r.keys[t.key] {
		meta, err := r.store.stat(r.ctx, t.key)
		switch {
		case errors.Is(err, errTeamsNotFound):
			if rerr := r.remove(t.key); rerr != nil {
				return "", rerr
			}
		case err != nil:
			return "", err
		default:
			stored = &outlook.TeamsVersion{Modified: meta[outlook.TeamsMetaModified], ETag: meta[outlook.TeamsMetaETag]}
			storedRemoved = meta[outlook.TeamsMetaRemovedAt]
			storedReactions = meta[outlook.TeamsMetaReactions]
			storedHostedMissing = meta[outlook.TeamsMetaHostedMissing]
		}
	}
	incoming := outlook.TeamsVersion{Modified: head.LastModifiedDateTime, ETag: head.ETag}
	if stored != nil && head.DeletedDateTime != "" && storedRemoved == "" {
		if err := r.applyRemoval(t.key, head.DeletedDateTime, incoming); err != nil {
			return "", err
		}
		r.count("removed")
		return "removed", r.cleanupLegacy(t)
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", err
	}
	reactionJSON := teamsInlineReactions(message)
	reactionSig := teamsHash(reactionJSON)
	switch outlook.CompareTeamsVersion(stored, incoming) {
	case outlook.TeamsVersionSkip:
		if stored == nil {
			return "skipped", nil
		}
		outcome := "skipped"
		var hostedErr error
		reactionsChanged := storedReactions != reactionSig
		var res *teamsHostedResult
		if storedHostedMissing != "" || reactionsChanged {
			got, err := r.storeHosted(head, t)
			if r.stopErr != nil {
				return "", r.stopErr
			}
			res, hostedErr = &got, err
		}
		if reactionsChanged {
			if err := r.updateMessageReactions(t.key, reactionJSON, reactionSig, res); err != nil {
				return "", err
			}
			r.count("reaction_updated")
			outcome = "updated_reactions"
		} else if res != nil {
			if n, _ := strconv.Atoi(storedHostedMissing); len(res.missing) != n {
				if uerr := r.updateMessageHosted(t.key, *res); uerr != nil {
					return "", uerr
				}
				if len(res.missing) == 0 {
					r.count("hosted_recovered")
					outcome = "updated_hosted"
				}
			}
		}
		if outcome == "skipped" {
			r.count("skipped")
		}
		if err := r.cleanupLegacy(t); err != nil {
			return outcome, err
		}
		return outcome, hostedErr
	case outlook.TeamsVersionConflict:
		r.count(outlook.TeamsVersionConflict)
		return outlook.TeamsVersionConflict, nil
	}
	message["reactions"] = reactionJSON
	raw, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	block := t.block
	res, hostedErr := r.storeHosted(head, t)
	if r.stopErr != nil {
		return "", r.stopErr
	}
	block.Hosted, block.HostedMissing, block.HostedUnresolved = res.hosted, res.missing, res.unresolved
	block.Files = r.messageFiles(head, t.key)
	block.RemovedAt = head.DeletedDateTime
	block.BackedUpAt = outlook.TeamsUTC(r.now())
	body, err := outlook.PatchTeamsBackup(raw, block)
	if err != nil {
		return "", err
	}
	meta := map[string]string{
		outlook.TeamsMetaMessageID: head.ID,
		outlook.TeamsMetaETag:      head.ETag,
		outlook.TeamsMetaModified:  head.LastModifiedDateTime,
		outlook.TeamsMetaReactions: reactionSig,
	}
	if head.ReplyToID != "" {
		meta[outlook.TeamsMetaReplyToID] = head.ReplyToID
	}
	if head.DeletedDateTime != "" {
		meta[outlook.TeamsMetaRemovedAt] = head.DeletedDateTime
	}
	teamsSetHostedMissing(meta, res.missing)
	if err := r.put(t.key, body, meta); err != nil {
		r.storeFallback(t.key, body)
		return "", err
	}
	r.count("written")
	if err := r.cleanupLegacy(t); err != nil {
		return "written", err
	}
	return "written", hostedErr
}

// teamsHostedResult is where a message's hosted contents stand: stored keys, keys still missing
// (retried on replay) and links that can't be tied to a message of the same chat or channel.
type teamsHostedResult struct {
	hosted, missing []string
	unresolved      []outlook.TeamsHostedUnresolved
}

// storeHosted stores the hosted contents a message references that aren't stored yet. Each link
// is fetched only from the message it names and only when that message is in the same chat or
// channel, and stored under that message's key (stored once however many messages reference
// it); any other link is listed as unresolved, never fetched. ConsistencyLevel: eventual is sent
// for this message's own contents when it was edited or deleted. A missing content returns an
// error for the caller's failure entry but never stops the message itself from being stored.
func (r *teamsRun) storeHosted(head teamsMessageHead, t teamsMessageTarget) (teamsHostedResult, error) {
	var res teamsHostedResult
	edited := head.LastEditedDateTime != "" || head.DeletedDateTime != ""
	seen := map[string]bool{}
	var first error
	for _, link := range teamsHostedLinks(head) {
		ref, ok := outlook.ParseTeamsHostedRef(link)
		reason, hk := outlook.TeamsHostedNotGraphLink, ""
		if ok {
			reason = outlook.TeamsHostedOtherConversation
			hk, ok = t.hostedKey(ref)
		}
		if !ok {
			res.unresolved = append(res.unresolved, outlook.TeamsHostedUnresolved{Link: link, Reason: reason})
			continue
		}
		if seen[hk] {
			continue
		}
		seen[hk] = true
		if r.keys[hk] {
			res.hosted = append(res.hosted, hk)
			continue
		}
		b, ferr := r.getBytes(ref.ValueURL(), edited && ref.MessageID == head.ID)
		if ferr == nil {
			ferr = r.put(hk, b, map[string]string{outlook.TeamsMetaMessageID: ref.MessageID})
		}
		if r.stopErr != nil {
			return res, r.stopErr
		}
		if ferr != nil {
			res.missing = append(res.missing, hk)
			if first == nil {
				first = ferr
			}
			continue
		}
		res.hosted = append(res.hosted, hk)
	}
	if len(res.unresolved) > 0 {
		r.count("hosted_unresolved")
	}
	if len(res.missing) > 0 {
		r.count("hosted_missing")
		return res, fmt.Errorf("hosted content: %d of %d not stored: %w", len(res.missing), len(seen), first)
	}
	return res, nil
}

// updateMessageHosted records on a stored message which hosted contents are now stored, missing
// or unresolved, keeping its body and other metadata.
func (r *teamsRun) updateMessageHosted(key string, res teamsHostedResult) error {
	raw, err := r.store.download(r.ctx, key)
	if err != nil {
		return err
	}
	block, _ := outlook.ReadTeamsBackup(raw)
	block.Hosted, block.HostedMissing, block.HostedUnresolved = res.hosted, res.missing, res.unresolved
	body, err := outlook.PatchTeamsBackup(raw, block)
	if err != nil {
		return err
	}
	meta, err := r.store.stat(r.ctx, key)
	if err != nil {
		return err
	}
	teamsSetHostedMissing(meta, res.missing)
	return r.put(key, body, meta)
}

func teamsSetHostedMissing(meta map[string]string, missing []string) {
	if len(missing) == 0 {
		delete(meta, outlook.TeamsMetaHostedMissing)
	} else {
		meta[outlook.TeamsMetaHostedMissing] = strconv.Itoa(len(missing))
	}
}

// updateMessageReactions replaces a stored message's reactions, and its hosted lists when res is
// given (a new custom reaction can reference new hosted content).
func (r *teamsRun) updateMessageReactions(key string, reactions json.RawMessage, signature string, res *teamsHostedResult) error {
	raw, err := r.store.download(r.ctx, key)
	if err != nil {
		return err
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	message["reactions"] = reactions
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if block, ok := outlook.ReadTeamsBackup(raw); ok {
		if res != nil {
			block.Hosted, block.HostedMissing, block.HostedUnresolved = res.hosted, res.missing, res.unresolved
		}
		body, err = outlook.PatchTeamsBackup(body, block)
		if err != nil {
			return err
		}
	}
	meta, err := r.store.stat(r.ctx, key)
	if err != nil {
		return err
	}
	meta[outlook.TeamsMetaReactions] = signature
	if res != nil {
		teamsSetHostedMissing(meta, res.missing)
	}
	return r.put(key, body, meta)
}

// storeFallback keeps an item whose normal key could not be written under the ids-only _failed/
// key, so its content isn't lost while the item is retried.
func (r *teamsRun) storeFallback(key string, body []byte) {
	if r.stopErr != nil {
		return
	}
	if err := r.put(outlook.TeamsFailedKey(r.tenantPrefix, key), body, map[string]string{"original-key": key}); err == nil {
		r.count(teamsStoredFallback)
	}
}

// applyRemoval records a deletion on the stored copy (removed-at) without replacing its body.
func (r *teamsRun) applyRemoval(key, deletedAt string, v outlook.TeamsVersion) error {
	raw, err := r.store.download(r.ctx, key)
	if err != nil {
		return err
	}
	block, _ := outlook.ReadTeamsBackup(raw)
	block.RemovedAt = deletedAt
	body, err := outlook.PatchTeamsBackup(raw, block)
	if err != nil {
		return err
	}
	meta, err := r.store.stat(r.ctx, key)
	if err != nil {
		return err
	}
	meta[outlook.TeamsMetaRemovedAt] = deletedAt
	if v.Modified != "" {
		meta[outlook.TeamsMetaModified], meta[outlook.TeamsMetaETag] = v.Modified, v.ETag
	}
	return r.put(key, body, meta)
}

// cleanupLegacy removes the old meta/data pair of a message once its new object is verified by
// reading it back.
func (r *teamsRun) cleanupLegacy(t teamsMessageTarget) error {
	if len(t.legacy) == 0 {
		return nil
	}
	if err := r.verifyObject(t.key); err != nil {
		return err
	}
	for _, k := range t.legacy {
		if err := r.remove(k); err != nil {
			return err
		}
	}
	return nil
}

// verifyObject reads an object back and checks it parses with its storx_backup block and id.
func (r *teamsRun) verifyObject(key string) error {
	raw, err := r.store.download(r.ctx, key)
	if err != nil {
		return fmt.Errorf("verify %s: %w", key, err)
	}
	var doc struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.ID == "" {
		return fmt.Errorf("verify %s: object does not parse", key)
	}
	if _, ok := outlook.ReadTeamsBackup(raw); !ok {
		return fmt.Errorf("verify %s: storx_backup missing", key)
	}
	if k, ok := outlook.ParseTeamsObjectKey(key); ok && k.MessageID != "" && k.MessageID != doc.ID {
		return fmt.Errorf("verify %s: id mismatch", key)
	}
	return nil
}

// convertLegacyTeam converts old meta/data objects that have no new object yet, channel by
// channel: write the new object from the old copy, verify it by reading it back, then remove the
// old objects. Old hosted images are copied the same way. Index objects (replies/, attachments/,
// _channels.json) go only after every channel is cleaned. true when the team is fully converted.
func (r *teamsRun) convertLegacyTeam(st *teamsTeamState, teamPrefix string, deadline time.Time) bool {
	type legacyMsg struct{ meta, data string }
	byChannel := map[string]map[string]*legacyMsg{}
	hosted := map[string][]string{}
	var others []string
	for _, key := range r.keysWithPrefix(teamPrefix + "/channels/") {
		k, ok := outlook.ParseTeamsObjectKey(key)
		if !ok {
			continue
		}
		switch k.Kind {
		case outlook.TeamsKindLegacyMeta, outlook.TeamsKindLegacyData:
			if byChannel[k.ChannelID] == nil {
				byChannel[k.ChannelID] = map[string]*legacyMsg{}
			}
			m := byChannel[k.ChannelID][k.MessageID]
			if m == nil {
				m = &legacyMsg{}
				byChannel[k.ChannelID][k.MessageID] = m
			}
			if k.Kind == outlook.TeamsKindLegacyMeta {
				m.meta = key
			} else {
				m.data = key
			}
		default:
			parts := strings.Split(strings.TrimPrefix(key, teamPrefix+"/channels/"), "/")
			if len(parts) == 4 && parts[1] == "hosted" {
				hosted[k.ChannelID] = append(hosted[k.ChannelID], key)
				continue
			}
			others = append(others, key)
		}
	}
	current := map[string]string{}
	for _, key := range r.keysWithPrefix(teamPrefix + "/" + outlook.TeamsSectionChannels + "/") {
		if k, ok := outlook.ParseTeamsObjectKey(key); ok && k.Kind == outlook.TeamsKindChannelMessage {
			current[k.ChannelID+"|"+k.MessageID] = key
		}
	}
	channelIDs := make([]string, 0, len(byChannel)+len(hosted))
	for ch := range byChannel {
		channelIDs = append(channelIDs, ch)
	}
	for ch := range hosted {
		if byChannel[ch] == nil {
			channelIDs = append(channelIDs, ch)
		}
	}
	sort.Strings(channelIDs)
	if st.Channels == nil {
		st.Channels = map[string]teamsChannelState{}
	}
	for _, ch := range channelIDs {
		cs := st.Channels[ch]
		msgIDs := make([]string, 0, len(byChannel[ch]))
		for id := range byChannel[ch] {
			msgIDs = append(msgIDs, id)
		}
		sort.Strings(msgIDs)
		for _, id := range msgIDs {
			if r.stopped() || !r.now().Before(deadline) {
				return false
			}
			m := byChannel[ch][id]
			newKey := current[ch+"|"+id]
			if newKey == "" {
				var err error
				newKey, err = r.convertLegacyMessage(teamPrefix, ch, id, m.meta, m.data)
				if err != nil {
					logger.Warn(r.ctx, "teams conversion kept old objects", logger.String("message_id", id), logger.ErrorField(err))
					return false
				}
			}
			if err := r.verifyObject(newKey); err != nil {
				return false
			}
			for _, old := range []string{m.meta, m.data} {
				if old != "" {
					if err := r.remove(old); err != nil {
						return false
					}
				}
			}
		}
		cs.Converted, cs.Verified = true, true
		for _, old := range hosted[ch] {
			parts := strings.Split(strings.TrimPrefix(old, teamPrefix+"/channels/"), "/")
			msg, _ := url.PathUnescape(parts[2])
			content, _ := url.PathUnescape(parts[3])
			newKey := outlook.TeamsChannelHostedKey(teamPrefix, ch, msg, content)
			if !r.keys[newKey] {
				b, err := r.store.download(r.ctx, old)
				if err != nil || r.put(newKey, b, map[string]string{outlook.TeamsMetaMessageID: msg}) != nil {
					return false
				}
			}
			if _, err := r.store.stat(r.ctx, newKey); err != nil {
				return false
			}
			if err := r.remove(old); err != nil {
				return false
			}
		}
		cs.Cleaned = true
		st.Channels[ch] = cs
		_ = r.saveState(outlook.TeamsTeamStateKey(r.tenantPrefix, teamPrefixTeamID(teamPrefix)), st)
	}
	for _, old := range others {
		if err := r.remove(old); err != nil {
			return false
		}
	}
	if r.keys[teamPrefix+"/.file_placeholder"] {
		_ = r.remove(teamPrefix + "/.file_placeholder")
	}
	return true
}

func teamPrefixTeamID(teamPrefix string) string {
	_, _, _, id, _, _ := outlook.SplitResourceKey(teamPrefix + "/x")
	return id
}

// convertLegacyMessage writes the new object of one old message from its data copy (the Graph
// message) or, without one, from its meta record.
func (r *teamsRun) convertLegacyMessage(teamPrefix, channelID, messageID, metaKey, dataKey string) (string, error) {
	var raw []byte
	var err error
	if dataKey != "" {
		raw, err = r.store.download(r.ctx, dataKey)
	}
	var meta outlook.TeamsCronBackupMeta
	if metaKey != "" {
		if b, merr := r.store.download(r.ctx, metaKey); merr == nil {
			_ = json.Unmarshal(b, &meta)
		}
	}
	var head teamsMessageHead
	if err != nil || len(raw) == 0 || json.Unmarshal(raw, &head) != nil || head.ID == "" {
		if metaKey == "" {
			return "", fmt.Errorf("old message has no readable copy")
		}
		doc := map[string]interface{}{
			"id":                   messageID,
			"subject":              meta.Subject,
			"createdDateTime":      meta.CreatedDateTime,
			"lastModifiedDateTime": meta.LastModifiedDateTime,
			"from":                 map[string]interface{}{"user": map[string]string{"displayName": meta.From}},
			"channelIdentity":      map[string]string{"teamId": meta.TeamID, "channelId": channelID},
		}
		if meta.RemovedFromTeams {
			doc["deletedDateTime"] = cmpOr(meta.DeletedAt, meta.UpdatedAt)
		}
		raw, _ = json.Marshal(doc)
		_ = json.Unmarshal(raw, &head)
	}
	created := cmpOr(head.CreatedDateTime, meta.CreatedDateTime)
	root := cmpOr(head.ReplyToID, head.ID)
	key := outlook.TeamsChannelMessageKey(teamPrefix, channelID, root, head.ID, created)
	block := outlook.TeamsBackupBlock{
		TenantID: r.tenant, TeamID: cmpOr(meta.TeamID, teamPrefixTeamID(teamPrefix)), ChannelID: channelID, ReplyToID: head.ReplyToID,
		RemovedAt: head.DeletedDateTime, BackedUpAt: outlook.TeamsUTC(r.now()), ConvertedFrom: cmpOr(dataKey, metaKey), Source: "conversion",
	}
	body, err := outlook.PatchTeamsBackup(raw, block)
	if err != nil {
		return "", err
	}
	md := map[string]string{
		outlook.TeamsMetaMessageID: head.ID,
		outlook.TeamsMetaETag:      head.ETag,
		outlook.TeamsMetaModified:  cmpOr(head.LastModifiedDateTime, meta.LastModifiedDateTime),
	}
	if head.ReplyToID != "" {
		md[outlook.TeamsMetaReplyToID] = head.ReplyToID
	}
	if head.DeletedDateTime != "" {
		md[outlook.TeamsMetaRemovedAt] = head.DeletedDateTime
	}
	if err := r.put(key, body, md); err != nil {
		return "", err
	}
	return key, nil
}
