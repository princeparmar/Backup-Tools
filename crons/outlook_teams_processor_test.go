package crons

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const teamsTestTenant = "11111111-2222-3333-4444-555555555555"

func TestOutlookTeamsProcessorRegistered(t *testing.T) {
	if processorMap["outlook_teams"] == nil {
		t.Fatal("outlook_teams processor must be registered")
	}
}

// memTeamsStore is an in-memory outlook-teams bucket with its index.
type memTeamsStore struct {
	objs   map[string][]byte
	meta   map[string]map[string]string
	putErr func(key string, attempt int) error
	puts   map[string]int
}

func newMemTeamsStore() *memTeamsStore {
	return &memTeamsStore{objs: map[string][]byte{}, meta: map[string]map[string]string{}, puts: map[string]int{}}
}

func (s *memTeamsStore) put(_ context.Context, key string, data []byte, meta map[string]string) error {
	s.puts[key]++
	if s.putErr != nil {
		if err := s.putErr(key, s.puts[key]); err != nil {
			return err
		}
	}
	s.objs[key] = append([]byte(nil), data...)
	m := map[string]string{}
	for k, v := range meta {
		m[k] = v
	}
	s.meta[key] = m
	return nil
}

func (s *memTeamsStore) stat(_ context.Context, key string) (map[string]string, error) {
	m, ok := s.meta[key]
	if !ok {
		return nil, errTeamsNotFound
	}
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out, nil
}

func (s *memTeamsStore) download(_ context.Context, key string) ([]byte, error) {
	b, ok := s.objs[key]
	if !ok {
		return nil, errTeamsNotFound
	}
	return b, nil
}

func (s *memTeamsStore) remove(_ context.Context, key string) error {
	delete(s.objs, key)
	delete(s.meta, key)
	return nil
}

func (s *memTeamsStore) listKeys(context.Context) ([]string, error) {
	out := make([]string, 0, len(s.objs))
	for k := range s.objs {
		out = append(out, k)
	}
	return out, nil
}

func newTeamsTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "teams.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&repo.CronJobListingDB{}, &repo.TaskListingDB{}); err != nil {
		t.Fatal(err)
	}
	return &db.PostgresDb{DB: gdb, CronJobRepo: repo.NewCronJobRepository(gdb)}
}

func createTeamsJob(t *testing.T, store *db.PostgresDb, cred uint, resourceType, resourceID string, input map[string]interface{}) *repo.CronJobListingDB {
	t.Helper()
	job, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "teams-"+resourceID, "outlook_teams", "daily", cred,
		repo.MicrosoftJobIdentity{TenantID: teamsTestTenant, ResourceType: resourceType, ResourceID: resourceID}, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Model(&repo.CronJobListingDB{}).Where("id = ?", job.ID).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	job.Active = true
	return job
}

// newTestTeamsRun builds a run on an in-memory store; Graph calls go through the seams.
func newTestTeamsRun(t *testing.T, store *memTeamsStore, dbs *db.PostgresDb, job *repo.CronJobListingDB) *teamsRun {
	t.Helper()
	if dbs == nil {
		dbs = newTeamsTestDB(t)
	}
	if job == nil {
		job = createTeamsJob(t, dbs, 1, repo.ResourceTypeTenant, teamsTestTenant, nil)
	}
	now := teamsNowFn().UTC()
	r := &teamsRun{
		ctx: context.Background(), input: ProcessorInput{Job: job, Database: dbs},
		store: store, token: "tok", tenant: teamsTestTenant, tenantPrefix: outlook.TeamsTenantPrefix(teamsTestTenant),
		mem: &job.TaskMemory, start: now, deadline: now.Add(teamsRunBudget), to: now.Add(-teamsNowMargin),
		status: &teamsStatusDoc{}, userScope: map[string]bool{}, chatsSeen: map[string]bool{},
		seriesSeen: map[string]string{}, seriesSubjects: map[string]string{}, midChecked: map[string]string{}, icalMids: map[string][]string{},
	}
	if err := r.loadKeys(); err != nil {
		t.Fatal(err)
	}
	r.sel = teamsSelectionFromJob(job)
	return r
}

// fakeGraph serves pages and documents by exact URL or by URL substring. A strict fake answers
// any other URL with 404 and records it in unknown. eventual lists the byte fetches sent with
// ConsistencyLevel: eventual.
type fakeGraph struct {
	exact    map[string]outlook.TeamsPage
	pages    map[string]outlook.TeamsPage
	docs     map[string][]byte
	errs     map[string]error
	calls    []string
	strict   bool
	unknown  []string
	eventual []string
}

func (g *fakeGraph) find(u string) (string, bool) {
	for k := range g.errs {
		if strings.Contains(u, k) {
			return k, true
		}
	}
	return "", false
}

func (g *fakeGraph) install(t *testing.T) {
	t.Helper()
	pp, pj, pb, pw := teamsPageFn, teamsJSONFn, teamsBytesFn, teamsWriteBackoff
	teamsWriteBackoff = 0
	teamsPageFn = func(_ context.Context, _ string, u string) (outlook.TeamsPage, error) {
		g.calls = append(g.calls, u)
		if k, ok := g.find(u); ok {
			return outlook.TeamsPage{}, g.errs[k]
		}
		if p, ok := g.exact[u]; ok {
			return p, nil
		}
		for k, p := range g.pages {
			if strings.Contains(u, k) {
				return p, nil
			}
		}
		if g.strict {
			g.unknown = append(g.unknown, u)
			return outlook.TeamsPage{}, &outlook.TeamsGraphError{Status: 404, Code: "NotFound"}
		}
		return outlook.TeamsPage{}, nil
	}
	teamsJSONFn = func(_ context.Context, _ string, u string) ([]byte, error) {
		g.calls = append(g.calls, u)
		if k, ok := g.find(u); ok {
			return nil, g.errs[k]
		}
		for k, b := range g.docs {
			if strings.Contains(u, k) {
				return b, nil
			}
		}
		if g.strict {
			g.unknown = append(g.unknown, u)
		}
		return nil, &outlook.TeamsGraphError{Status: 404, Code: "NotFound"}
	}
	teamsBytesFn = func(ctx context.Context, tok, u string, eventual bool) ([]byte, error) {
		if eventual {
			g.eventual = append(g.eventual, u)
		}
		return teamsJSONFn(ctx, tok, u)
	}
	t.Cleanup(func() { teamsPageFn, teamsJSONFn, teamsBytesFn, teamsWriteBackoff = pp, pj, pb, pw })
}

func teamsMsg(id, replyTo, channel, modified string, extra map[string]interface{}) json.RawMessage {
	m := map[string]interface{}{
		"id": id, "etag": "e-" + modified, "createdDateTime": "2026-10-01T10:00:00.000Z", "lastModifiedDateTime": modified,
		"channelIdentity": map[string]string{"teamId": "team-1", "channelId": channel},
		"body":            map[string]string{"content": "hello"},
	}
	if replyTo != "" {
		m["replyToId"] = replyTo
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func TestTeamsRestorablePhaseOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "production skips calls and meetings", got: teamsRestorablePhaseOrder, want: []string{teamsPhaseRetries, teamsPhaseTeams, teamsPhaseChats}},
		{name: "full order kept for later", got: teamsPhaseOrder, want: []string{teamsPhaseCalls, teamsPhaseRetries, teamsPhaseTeams, teamsPhaseChats, teamsPhaseMeetings}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if len(tt.got) != len(tt.want) {
				t.Fatalf("got %v want %v", tt.got, tt.want)
			}
			for i := range tt.want {
				if tt.got[i] != tt.want[i] {
					t.Fatalf("got %v want %v", tt.got, tt.want)
				}
			}
		})
	}
}

func TestTeamsPhaseBudgets(t *testing.T) {
	total := 100 * time.Minute
	b := teamsPhaseBudgets(total, false)
	want := map[string]time.Duration{teamsPhaseCalls: 15 * time.Minute, teamsPhaseRetries: 15 * time.Minute,
		teamsPhaseTeams: 25 * time.Minute, teamsPhaseChats: 25 * time.Minute, teamsPhaseMeetings: 20 * time.Minute}
	for p, w := range want {
		if b[p] != w {
			t.Fatalf("%s: got %v want %v", p, b[p], w)
		}
	}
	lag := teamsPhaseBudgets(total, true)
	if lag[teamsPhaseCalls] != 30*time.Minute || lag[teamsPhaseRetries] != 15*time.Minute {
		t.Fatalf("borrowing: calls %v retries %v", lag[teamsPhaseCalls], lag[teamsPhaseRetries])
	}
	var sum time.Duration
	for _, d := range lag {
		sum += d
	}
	if diff := sum - total; diff < -time.Second || diff > time.Second {
		t.Fatalf("borrowed budgets sum to %v", sum)
	}
	if !(lag[teamsPhaseTeams] < b[teamsPhaseTeams] && lag[teamsPhaseMeetings] < b[teamsPhaseMeetings]) {
		t.Fatal("teams and meetings must give time to calls")
	}
	if r1, r2 := float64(lag[teamsPhaseTeams])/float64(lag[teamsPhaseMeetings]), 25.0/20.0; r1 < r2-0.01 || r1 > r2+0.01 {
		t.Fatalf("borrowing must keep proportions, got %v", r1)
	}
}

func TestTeamsCallsLagging(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	recent, old := now.Add(-24*time.Hour), now.Add(-6*24*time.Hour)
	cases := []struct {
		state *repo.TeamsWindowState
		want  bool
	}{
		{nil, true},
		{&repo.TeamsWindowState{}, true},
		{&repo.TeamsWindowState{HighWater: &recent}, false},
		{&repo.TeamsWindowState{HighWater: &old}, true},
	}
	for i, c := range cases {
		if got := teamsCallsLagging(c.state, now); got != c.want {
			t.Fatalf("case %d: got %v", i, got)
		}
	}
}

func TestTeamsSelectionFromJob(t *testing.T) {
	dbs := newTeamsTestDB(t)
	tenant := createTeamsJob(t, dbs, 1, repo.ResourceTypeTenant, teamsTestTenant, map[string]interface{}{
		"team_ids": []interface{}{"t1", "t2"}, "user_ids": []interface{}{"u9"},
		"team_channel_ids": map[string]interface{}{"t1": []interface{}{"c1"}, "t2": []interface{}{}},
	})
	sel := teamsSelectionFromJob(tenant)
	if !sel.teamSelected("t1") || sel.teamSelected("t3") || !sel.userSelected("u9") || sel.userSelected("u1") {
		t.Fatalf("selection: %+v", sel)
	}
	if !sel.channelSelected("t1", "c1") || sel.channelSelected("t1", "c2") || !sel.channelSelected("t2", "any") {
		t.Fatalf("channels: %+v", sel.ChannelIDs)
	}
	old := createTeamsJob(t, dbs, 1, repo.ResourceTypeTeam, "t5", map[string]interface{}{"team_id": "t5", "channel_ids": []interface{}{"c5"}})
	osel := teamsSelectionFromJob(old)
	if !osel.teamSelected("t5") || osel.teamSelected("t1") || !osel.channelSelected("t5", "c5") || osel.channelSelected("t5", "c6") {
		t.Fatalf("old job selection: %+v", osel)
	}
	if all := teamsSelectionFromJob(createTeamsJob(t, dbs, 2, repo.ResourceTypeTenant, "other", nil)); !all.teamSelected("x") || !all.userSelected("y") {
		t.Fatal("empty selection means all")
	}
}

func fakeJWT(claims map[string]string) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func TestTeamsCheckTenantPinsTokenAndAuth(t *testing.T) {
	good := fakeJWT(map[string]string{"oid": "o1", "tid": teamsTestTenant})
	if err := teamsCheckTenant(teamsTestTenant, &microsoftJobAuth{AccessToken: good, TenantID: teamsTestTenant}); err != nil {
		t.Fatalf("matching tenant: %v", err)
	}
	if err := teamsCheckTenant(teamsTestTenant, &microsoftJobAuth{AccessToken: good, TenantID: "other"}); err == nil {
		t.Fatal("resolved tenant mismatch must stop the run")
	}
	bad := fakeJWT(map[string]string{"oid": "o1", "tid": "99999999-0000-0000-0000-000000000000"})
	if err := teamsCheckTenant(teamsTestTenant, &microsoftJobAuth{AccessToken: bad}); err == nil {
		t.Fatal("token tid mismatch must stop the run")
	}
}

func TestTeamsPutRefusesForeignTenantAndLongKeys(t *testing.T) {
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	foreign := outlook.TeamsTeamMetaKey(outlook.TeamsTeamPrefix("99999999-0000-0000-0000-000000000000", "team-1"))
	if err := r.put(foreign, []byte("{}"), nil); err == nil {
		t.Fatal("key of another tenant must be refused")
	}
	long := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1") + "/" + strings.Repeat("x", 1100)
	if err := r.put(long, []byte("{}"), nil); err == nil {
		t.Fatal("over-long key must be refused")
	}
	if len(store.objs) != 0 {
		t.Fatal("nothing may be written")
	}
}

func TestTeamsPutRetriesThenStopsOnStorageFull(t *testing.T) {
	(&fakeGraph{}).install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	key := outlook.TeamsTeamMetaKey(outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
	store.putErr = func(_ string, attempt int) error {
		if attempt < 3 {
			return errors.New("transient")
		}
		return nil
	}
	if err := r.put(key, []byte("{}"), nil); err != nil || store.puts[key] != 3 || !r.keys[key] {
		t.Fatalf("third attempt should succeed: err %v puts %d", err, store.puts[key])
	}
	store.putErr = func(string, int) error { return errors.New("always") }
	other := key + "x"
	if err := r.put(other, []byte("{}"), nil); err == nil || store.puts[other] != teamsWriteAttempts {
		t.Fatalf("write must fail after %d attempts, got %d", teamsWriteAttempts, store.puts[other])
	}
}

func TestTeamsChannelWindowStoresRoutesAndCloses(t *testing.T) {
	g := &fakeGraph{
		pages: map[string]outlook.TeamsPage{"getAllMessages": {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{
				"body": map[string]string{"content": `<img src="https://graph.microsoft.com/v1.0/teams/team-1/channels/ch-a/messages/100/hostedContents/img1/$value">`},
			}),
			teamsMsg("101", "100", "ch-a", "2026-10-02T10:01:00.000Z", nil),
			teamsMsg("200", "", "ch-b", "2026-10-02T11:00:00.000Z", nil),
		}}},
		docs: map[string][]byte{"hostedContents/img1/$value": []byte("PNG")},
	}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	st := &teamsTeamState{FullListing: true}
	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, r.deadline); got != teamsComplete {
		t.Fatalf("status %s", got)
	}
	root := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "100", "2026-10-01T10:00:00.000Z")
	reply := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "101", "2026-10-01T10:00:00.000Z")
	other := outlook.TeamsChannelMessageKey(prefix, "ch-b", "200", "200", "2026-10-01T10:00:00.000Z")
	hosted := outlook.TeamsChannelHostedKey(prefix, "ch-a", "100", "img1")
	for _, k := range []string{root, reply, other, hosted} {
		if _, ok := store.objs[k]; !ok {
			t.Fatalf("missing %s; have %v", k, keysOf(store.objs))
		}
	}
	if store.meta[reply][outlook.TeamsMetaReplyToID] != "100" || store.meta[root][outlook.TeamsMetaModified] != "2026-10-02T10:00:00.000Z" {
		t.Fatalf("metadata: %v / %v", store.meta[reply], store.meta[root])
	}
	block, ok := outlook.ReadTeamsBackup(store.objs[root])
	if !ok || block.ChannelID != "ch-a" || len(block.Hosted) != 1 || block.TenantID != teamsTestTenant {
		t.Fatalf("storx_backup: %+v", block)
	}
	if len(st.Windows) != 0 || st.HighWater == nil || st.FullListing {
		t.Fatalf("window must close: %+v", st)
	}
	for _, k := range keysOf(store.objs) {
		if strings.Contains(k, "hello") {
			t.Fatalf("message text in a key: %s", k)
		}
	}
}

const teamsTestHostedURL = "https://graph.microsoft.com/v1.0/teams/team-1/channels/ch-a/messages/100/hostedContents/img1/$value"

// A hosted content download that fails doesn't stop the message: text, metadata and inline
// reactions are stored, the content is listed as missing (never as stored), the item failure is
// recorded and holds the window, and the replay stores the content and clears the failure.
func TestTeamsHostedFailureStoresMessageAndRecoversOnReplay(t *testing.T) {
	g := &fakeGraph{strict: true,
		pages: map[string]outlook.TeamsPage{"/teams/team-1/channels/getAllMessages?$filter": {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{
				"body":      map[string]string{"content": `<p>caption</p><img src="` + teamsTestHostedURL + `">`},
				"reactions": []map[string]string{{"reactionType": "like"}},
			}),
		}}},
		errs: map[string]error{"hostedContents/img1/$value": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}},
	}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	key := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "100", "2026-10-01T10:00:00.000Z")
	hk := outlook.TeamsChannelHostedKey(prefix, "ch-a", "100", "img1")
	failKey := "channels:item:team-1:ch-a:100:100"
	st := &teamsTeamState{FullListing: true}

	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, r.deadline); got != teamsIncomplete {
		t.Fatalf("status %s", got)
	}
	body, ok := store.objs[key]
	if !ok || !strings.Contains(string(body), "caption") {
		t.Fatalf("message must be stored despite the hosted failure; have %v", keysOf(store.objs))
	}
	if got := storedReactionTypes(t, body); len(got) != 1 || got[0] != "like" {
		t.Fatalf("inline reactions = %v", got)
	}
	if store.meta[key][outlook.TeamsMetaModified] != "2026-10-02T10:00:00.000Z" || store.meta[key][outlook.TeamsMetaHostedMissing] != "1" {
		t.Fatalf("message metadata: %v", store.meta[key])
	}
	block, _ := outlook.ReadTeamsBackup(body)
	if len(block.Hosted) != 0 || len(block.HostedMissing) != 1 || block.HostedMissing[0] != hk {
		t.Fatalf("failed content must be listed as missing, not stored: %+v", block)
	}
	if _, ok := store.objs[hk]; ok {
		t.Fatal("no hosted object may exist for a failed download")
	}
	f, ok := r.mem.TeamsFailures[failKey]
	if !ok || f.HTTPStatus != 503 || !strings.Contains(f.ErrorMessage, "hosted content") {
		t.Fatalf("hosted failure must be recorded on the item: %+v", r.mem.TeamsFailures)
	}
	if len(st.Windows) != 1 || st.Windows[0].Held != 1 || st.HighWater != nil {
		t.Fatalf("window must be held for the replay: %+v", st.Windows)
	}

	delete(g.errs, "hostedContents/img1/$value")
	g.docs = map[string][]byte{"hostedContents/img1/$value": []byte("PNG")}
	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, r.deadline); got != teamsComplete {
		t.Fatalf("replay status %s; failures %+v", got, r.mem.TeamsFailures)
	}
	if string(store.objs[hk]) != "PNG" {
		t.Fatalf("hosted content must be stored on the replay; have %v", keysOf(store.objs))
	}
	block, _ = outlook.ReadTeamsBackup(store.objs[key])
	if len(block.Hosted) != 1 || block.Hosted[0] != hk || len(block.HostedMissing) != 0 {
		t.Fatalf("recovered content must move to hosted: %+v", block)
	}
	if _, ok := store.meta[key][outlook.TeamsMetaHostedMissing]; ok || store.meta[key][outlook.TeamsMetaReactions] == "" {
		t.Fatalf("missing marker must clear, other metadata kept: %v", store.meta[key])
	}
	if !strings.Contains(string(store.objs[key]), "caption") {
		t.Fatal("message body must be kept")
	}
	if _, ok := r.mem.TeamsFailures[failKey]; ok || len(st.Windows) != 0 || st.HighWater == nil {
		t.Fatalf("failure must clear and the window close: %+v %+v", r.mem.TeamsFailures, st.Windows)
	}
	if len(g.unknown) != 0 {
		t.Fatalf("unexpected Graph requests: %v", g.unknown)
	}
}

// Replies and chat messages keep their text when a hosted download or the hosted write fails.
func TestTeamsHostedFailureNeverDropsReplyOrChatMessage(t *testing.T) {
	g := &fakeGraph{strict: true, errs: map[string]error{
		"hostedContents/r-img/$value": &outlook.TeamsGraphError{Status: 404, Code: "NotFound"},
	}, docs: map[string][]byte{"hostedContents/c-img/$value": []byte("JPG")}}
	g.install(t)
	store := newMemTeamsStore()
	store.putErr = func(key string, _ int) error {
		if strings.Contains(key, "/hosted/") {
			return errors.New("write refused")
		}
		return nil
	}
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")

	reply := teamsMsg("101", "100", "ch-a", "2026-10-02T10:01:00.000Z", map[string]interface{}{
		"body": map[string]string{"content": `reply text <img src="https://graph.microsoft.com/v1.0/teams/team-1/channels/ch-a/messages/100/replies/101/hostedContents/r-img/$value">`},
	})
	_, err := r.storeChannelMessage("team-1", prefix, reply)
	var ge *outlook.TeamsGraphError
	if err == nil || !errors.As(err, &ge) || ge.Status != 404 {
		t.Fatalf("reply must report the hosted failure, got %v", err)
	}
	replyKey := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "101", "2026-10-01T10:00:00.000Z")
	if !strings.Contains(string(store.objs[replyKey]), "reply text") || store.meta[replyKey][outlook.TeamsMetaReplyToID] != "100" {
		t.Fatalf("reply must be stored: %s %v", store.objs[replyKey], store.meta[replyKey])
	}

	chatRaw := json.RawMessage(`{"id":"chat-msg","chatId":"chat-1","createdDateTime":"2026-10-01T10:00:00Z","lastModifiedDateTime":"2026-10-02T10:00:00Z",` +
		`"body":{"content":"chat text <img src=\"https://graph.microsoft.com/v1.0/chats/chat-1/messages/chat-msg/hostedContents/c-img/$value\">"},` +
		`"reactions":[{"reactionType":"heart"}]}`)
	if _, err := r.storeChatMessage(chatRaw, mustHead(t, chatRaw)); err == nil || !strings.Contains(err.Error(), "hosted content") {
		t.Fatalf("chat message must report the hosted write failure, got %v", err)
	}
	chatKey := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(teamsTestTenant, "chat-1"), "chat-msg", "2026-10-01T10:00:00Z")
	if !strings.Contains(string(store.objs[chatKey]), "chat text") || store.meta[chatKey][outlook.TeamsMetaHostedMissing] != "1" {
		t.Fatalf("chat message must be stored with the content missing: %s %v", store.objs[chatKey], store.meta[chatKey])
	}
	if got := storedReactionTypes(t, store.objs[chatKey]); len(got) != 1 || got[0] != "heart" {
		t.Fatalf("chat reactions = %v", got)
	}
	for k := range store.objs {
		if strings.Contains(k, "/hosted/") {
			t.Fatalf("no hosted object may be stored: %s", k)
		}
	}
	if len(g.unknown) != 0 {
		t.Fatalf("unexpected Graph requests: %v", g.unknown)
	}
}

// Hosted contents referenced from attachment cards (adaptive card images, code snippets) are
// stored like inline images; no other attachment URL is requested.
func TestTeamsAttachmentHostedContentIsStored(t *testing.T) {
	card := `{"type":"AdaptiveCard","body":[{"type":"Image","url":"https://graph.microsoft.com/v1.0/teams/team-1/channels/ch-a/messages/300/hostedContents/card1/$value"}]}`
	snippet := `{"codeSnippetUrl":"https:\/\/graph.microsoft.com\/v1.0\/teams\/team-1\/channels\/ch-a\/messages\/300\/hostedContents\/snip1\/$value"}`
	g := &fakeGraph{strict: true, docs: map[string][]byte{
		"messages/300/hostedContents/card1/$value": []byte("CARD"),
		"messages/300/hostedContents/snip1/$value": []byte("CODE"),
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	raw := teamsMsg("300", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{
		"attachments": []map[string]interface{}{
			{"id": "a1", "contentType": "application/vnd.microsoft.card.adaptive", "content": card},
			{"id": "a2", "contentType": "application/vnd.microsoft.card.codesnippet", "content": snippet},
			{"id": "a3", "contentType": "application/vnd.microsoft.card.thumbnail", "content": map[string]string{"title": "object content"}},
		},
	})
	if _, err := r.storeChannelMessage("team-1", prefix, raw); err != nil {
		t.Fatal(err)
	}
	key := outlook.TeamsChannelMessageKey(prefix, "ch-a", "300", "300", "2026-10-01T10:00:00.000Z")
	block, _ := outlook.ReadTeamsBackup(store.objs[key])
	want := []string{outlook.TeamsChannelHostedKey(prefix, "ch-a", "300", "card1"), outlook.TeamsChannelHostedKey(prefix, "ch-a", "300", "snip1")}
	if len(block.Hosted) != 2 || block.Hosted[0] != want[0] || block.Hosted[1] != want[1] || len(block.HostedMissing) != 0 {
		t.Fatalf("attachment hosted contents: %+v", block)
	}
	if string(store.objs[want[0]]) != "CARD" || string(store.objs[want[1]]) != "CODE" {
		t.Fatal("attachment hosted bytes must be stored")
	}
	if _, ok := store.meta[key][outlook.TeamsMetaHostedMissing]; ok {
		t.Fatalf("nothing missing: %v", store.meta[key])
	}
	if len(g.calls) != 2 || len(g.unknown) != 0 {
		t.Fatalf("only the two hosted contents may be requested: %v %v", g.calls, g.unknown)
	}
}

// A hosted content that keeps failing is released as a gap after the hold limit; the message stays
// stored with the content still listed as missing.
func TestTeamsHostedFailureReleasedAsGapKeepsMessage(t *testing.T) {
	g := &fakeGraph{strict: true,
		pages: map[string]outlook.TeamsPage{"/teams/team-1/channels/getAllMessages?$filter": {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{
				"body": map[string]string{"content": `<p>caption</p><img src="` + teamsTestHostedURL + `">`},
			}),
		}}},
		errs: map[string]error{"hostedContents/img1/$value": &outlook.TeamsGraphError{Status: 404, Code: "NotFound"}},
	}
	g.install(t)
	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	prevNow := teamsNowFn
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prevNow })
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	key := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "100", "2026-10-01T10:00:00.000Z")
	st := &teamsTeamState{FullListing: true}
	for i := 1; i <= teamsReleaseReplays-1; i++ {
		if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, clock.Add(time.Hour)); got != teamsIncomplete {
			t.Fatalf("replay %d: %s", i, got)
		}
		clock = clock.Add(12 * time.Hour)
	}
	if store.puts[key] != 1 {
		t.Fatalf("unchanged replays must not rewrite the message, puts %d", store.puts[key])
	}
	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, clock.Add(time.Hour)); got != teamsCompleteWithGaps {
		t.Fatalf("after the hold limit: %s", got)
	}
	gap := false
	for _, s := range r.status.SkippedItems {
		gap = gap || (s.Key == "channels:item:team-1:ch-a:100:100" && s.IDs["message_id"] == "100")
	}
	if !gap {
		t.Fatalf("hosted failure must be listed as a gap: %+v", r.status.SkippedItems)
	}
	block, _ := outlook.ReadTeamsBackup(store.objs[key])
	if !strings.Contains(string(store.objs[key]), "caption") || len(block.HostedMissing) != 1 || store.meta[key][outlook.TeamsMetaHostedMissing] != "1" {
		t.Fatalf("message must stay stored with the content missing: %+v %v", block, store.meta[key])
	}
}

const teamsTestGraph = "https://graph.microsoft.com/v1.0"

func teamsChatTestMsg(t *testing.T, chatID, id, modified string, extra map[string]interface{}) (json.RawMessage, teamsMessageHead) {
	t.Helper()
	m := map[string]interface{}{"id": id, "chatId": chatID, "createdDateTime": "2026-10-01T10:00:00Z", "lastModifiedDateTime": modified,
		"body": map[string]string{"content": "text of " + id}}
	for k, v := range extra {
		m[k] = v
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw, mustHead(t, raw)
}

func teamsCustomReaction(link string) []map[string]interface{} {
	return []map[string]interface{}{{"reactionType": "custom", "displayName": "microsoft_teams", "reactionContentUrl": link,
		"createdDateTime": "2026-10-02T10:00:00Z", "user": map[string]interface{}{"user": map[string]string{"id": "u1"}}}}
}

// teamsBodyOnly is a stored message without its storx_backup block.
func teamsBodyOnly(t *testing.T, raw []byte) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "storx_backup")
	out, _ := json.Marshal(m)
	return string(out)
}

// ConsistencyLevel: eventual goes on the hosted fetches of an edited or deleted message's own
// content only; unedited messages and content owned by another message are fetched without it.
func TestTeamsHostedEventualHeaderForEditedOrDeletedMessages(t *testing.T) {
	g := &fakeGraph{strict: true, docs: map[string][]byte{
		"messages/plain/hostedContents/p1/$value":  []byte("P"),
		"messages/edited/hostedContents/e1/$value": []byte("E"),
		"messages/owner/hostedContents/o1/$value":  []byte("O"),
		"messages/gone/hostedContents/d1/$value":   []byte("D"),
		"messages/500/hostedContents/c1/$value":    []byte("C"),
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	img := func(msg, cid string) string {
		return `<img src="` + teamsTestGraph + `/chats/chat-1/messages/` + msg + `/hostedContents/` + cid + `/$value">`
	}
	for _, m := range []struct {
		id    string
		extra map[string]interface{}
	}{
		{"plain", map[string]interface{}{"body": map[string]string{"content": img("plain", "p1")}}},
		{"edited", map[string]interface{}{"lastEditedDateTime": "2026-10-02T11:00:00Z",
			"body": map[string]string{"content": img("edited", "e1") + `<customemoji source="` + teamsTestGraph + `/chats/chat-1/messages/owner/hostedContents/o1/$value"></customemoji>`}}},
		{"gone", map[string]interface{}{"deletedDateTime": "2026-10-02T12:00:00Z", "body": map[string]string{"content": img("gone", "d1")}}},
	} {
		raw, head := teamsChatTestMsg(t, "chat-1", m.id, "2026-10-02T12:00:00Z", m.extra)
		if _, err := r.storeChatMessage(raw, head); err != nil {
			t.Fatalf("%s: %v", m.id, err)
		}
	}
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	post := teamsMsg("500", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{"lastEditedDateTime": "2026-10-02T10:00:00.000Z",
		"body": map[string]string{"content": `<img src="` + teamsTestGraph + `/teams/team-1/channels/ch-a/messages/500/hostedContents/c1/$value">`}})
	if _, err := r.storeChannelMessage("team-1", prefix, post); err != nil {
		t.Fatal(err)
	}
	want := []string{"messages/edited/hostedContents/e1/$value", "messages/gone/hostedContents/d1/$value", "messages/500/hostedContents/c1/$value"}
	if len(g.eventual) != len(want) {
		t.Fatalf("eventual fetches = %v, want %v", g.eventual, want)
	}
	for i, w := range want {
		if !strings.HasSuffix(g.eventual[i], w) {
			t.Fatalf("eventual fetches = %v, want %v", g.eventual, want)
		}
	}
	if len(g.calls) != 5 || len(g.unknown) != 0 {
		t.Fatalf("calls %v unknown %v", g.calls, g.unknown)
	}
}

// A custom reaction's reactionContentUrl naming the reacted message itself (Graph's documented
// shape) is fetched from that URL and stored under that message; the reaction JSON is kept.
func TestTeamsCustomReactionContentIsStoredUnderItsMessage(t *testing.T) {
	chat := "19:bcf84b15c2994a909770f7d05bc4fe16@thread.v2"
	msg, cid := "1706763669648", "aWQ9eCx0eXBlPTE="
	link := teamsTestGraph + "/chats/" + chat + "/messages/" + msg + "/hostedContents/" + cid + "/$value"
	g := &fakeGraph{strict: true, docs: map[string][]byte{"/messages/" + msg + "/hostedContents/" + cid + "/$value": []byte("EMOJI")}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	raw, head := teamsChatTestMsg(t, chat, msg, "2026-10-02T10:00:00Z", map[string]interface{}{"reactions": teamsCustomReaction(link)})
	if _, err := r.storeChatMessage(raw, head); err != nil {
		t.Fatal(err)
	}
	chatPrefix := outlook.TeamsChatPrefix(teamsTestTenant, chat)
	hk := outlook.TeamsChatHostedKey(chatPrefix, msg, cid)
	key := outlook.TeamsChatMessageKey(chatPrefix, msg, "2026-10-01T10:00:00Z")
	if string(store.objs[hk]) != "EMOJI" || store.meta[hk][outlook.TeamsMetaMessageID] != msg {
		t.Fatalf("emoji content must be stored under its message: %v %v", keysOf(store.objs), store.meta[hk])
	}
	block, _ := outlook.ReadTeamsBackup(store.objs[key])
	if len(block.Hosted) != 1 || block.Hosted[0] != hk || len(block.HostedMissing) != 0 || len(block.HostedUnresolved) != 0 {
		t.Fatalf("block: %+v", block)
	}
	if got := storedReactionTypes(t, store.objs[key]); len(got) != 1 || got[0] != "custom" || !strings.Contains(string(store.objs[key]), "reactionContentUrl") {
		t.Fatalf("reaction JSON must be kept: %s", store.objs[key])
	}
	if len(g.calls) != 1 || len(g.unknown) != 0 {
		t.Fatalf("only the emoji content may be requested: %v %v", g.calls, g.unknown)
	}
}

// A body customemoji whose source is another message of the same chat (and a reply image owned
// by the thread root) is fetched from the owner and stored once under the owner's key; it is never
// requested under the referencing message's id.
func TestTeamsHostedContentOfAnotherMessageIsStoredUnderItsOwner(t *testing.T) {
	g := &fakeGraph{strict: true, docs: map[string][]byte{
		"/chats/chat-1/messages/owner/hostedContents/emo1/$value":           []byte("EMO"),
		"/teams/team-1/channels/ch-a/messages/100/hostedContents/r1/$value": []byte("ROOT"),
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	emoji := `<customemoji alt="party" source="` + teamsTestGraph + `/chats/chat-1/messages/owner/hostedContents/emo1/$value"></customemoji>`
	chatPrefix := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	ownerKey := outlook.TeamsChatHostedKey(chatPrefix, "owner", "emo1")
	for _, id := range []string{"a", "b"} {
		raw, head := teamsChatTestMsg(t, "chat-1", id, "2026-10-02T10:00:00Z", map[string]interface{}{"body": map[string]string{"content": "hi " + emoji}})
		if _, err := r.storeChatMessage(raw, head); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		block, _ := outlook.ReadTeamsBackup(store.objs[outlook.TeamsChatMessageKey(chatPrefix, id, "2026-10-01T10:00:00Z")])
		if len(block.Hosted) != 1 || block.Hosted[0] != ownerKey || len(block.HostedMissing) != 0 {
			t.Fatalf("%s block: %+v", id, block)
		}
	}
	if string(store.objs[ownerKey]) != "EMO" || store.meta[ownerKey][outlook.TeamsMetaMessageID] != "owner" {
		t.Fatalf("emoji must be stored under its owner: %v", keysOf(store.objs))
	}

	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	reply := teamsMsg("101", "100", "ch-a", "2026-10-02T10:01:00.000Z", map[string]interface{}{
		"body": map[string]string{"content": `<img src="` + teamsTestGraph + `/teams/team-1/channels/ch-a/messages/100/hostedContents/r1/$value">`},
	})
	if _, err := r.storeChannelMessage("team-1", prefix, reply); err != nil {
		t.Fatal(err)
	}
	rootKey := outlook.TeamsChannelHostedKey(prefix, "ch-a", "100", "r1")
	if string(store.objs[rootKey]) != "ROOT" || store.meta[rootKey][outlook.TeamsMetaMessageID] != "100" {
		t.Fatalf("root content must be stored under the root: %v", keysOf(store.objs))
	}
	for k := range store.objs {
		if strings.Contains(k, "/hosted/") && k != ownerKey && k != rootKey {
			t.Fatalf("content stored under the wrong message: %s", k)
		}
	}
	if len(g.calls) != 2 || len(g.unknown) != 0 {
		t.Fatalf("each content must be requested once from its owner: %v %v", g.calls, g.unknown)
	}
}

// Links outside the message's own chat or channel, off the Graph host or version, or not on a
// documented route are listed as unresolved with a reason: never requested, never stored, never
// counted as missing, and the message is stored without a failure.
func TestTeamsHostedLinkOutsideTheConversationIsNeverFetched(t *testing.T) {
	g := &fakeGraph{strict: true}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	other := map[string]string{
		teamsTestGraph + "/chats/chat-2/messages/x/hostedContents/c1/$value":                 outlook.TeamsHostedOtherConversation,
		teamsTestGraph + "/teams/team-1/channels/ch-a/messages/100/hostedContents/c2/$value": outlook.TeamsHostedOtherConversation,
		"https://evil.example/v1.0/chats/chat-1/messages/m/hostedContents/c3/$value":         outlook.TeamsHostedNotGraphLink,
		"https://graph.microsoft.com/beta/chats/chat-1/messages/m/hostedContents/c4/$value":  outlook.TeamsHostedNotGraphLink,
		"hostedContents/c5/$value": outlook.TeamsHostedNotGraphLink,
		teamsTestGraph + "/chats/chat-1/messages/m/../../chat-2/messages/x/hostedContents/c6/$value": outlook.TeamsHostedNotGraphLink,
	}
	body := ""
	for link := range other {
		body += `<img src="` + link + `">`
	}
	reaction := teamsTestGraph + "/chats/chat-3/messages/y/hostedContents/c7/$value"
	other[reaction] = outlook.TeamsHostedOtherConversation
	raw, head := teamsChatTestMsg(t, "chat-1", "m", "2026-10-02T10:00:00Z", map[string]interface{}{
		"body": map[string]string{"content": body}, "reactions": teamsCustomReaction(reaction)})
	if _, err := r.storeChatMessage(raw, head); err != nil {
		t.Fatalf("unresolved links must not fail the message: %v", err)
	}
	key := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(teamsTestTenant, "chat-1"), "m", "2026-10-01T10:00:00Z")
	block, _ := outlook.ReadTeamsBackup(store.objs[key])
	if len(block.Hosted) != 0 || len(block.HostedMissing) != 0 || len(block.HostedUnresolved) != len(other) {
		t.Fatalf("block: %+v", block)
	}
	for _, u := range block.HostedUnresolved {
		if other[u.Link] != u.Reason {
			t.Fatalf("%s: reason %q, want %q", u.Link, u.Reason, other[u.Link])
		}
	}
	if _, ok := store.meta[key][outlook.TeamsMetaHostedMissing]; ok {
		t.Fatalf("unresolved links are not missing content: %v", store.meta[key])
	}

	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	post := teamsMsg("200", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{"body": map[string]string{"content": `` +
		`<img src="` + teamsTestGraph + `/teams/team-2/channels/ch-a/messages/200/hostedContents/t1/$value">` +
		`<img src="` + teamsTestGraph + `/teams/team-1/channels/ch-b/messages/200/hostedContents/t2/$value">` +
		`<img src="` + teamsTestGraph + `/chats/chat-1/messages/200/hostedContents/t3/$value">`}})
	if _, err := r.storeChannelMessage("team-1", prefix, post); err != nil {
		t.Fatal(err)
	}
	block, _ = outlook.ReadTeamsBackup(store.objs[outlook.TeamsChannelMessageKey(prefix, "ch-a", "200", "200", "2026-10-01T10:00:00.000Z")])
	if len(block.Hosted) != 0 || len(block.HostedMissing) != 0 || len(block.HostedUnresolved) != 3 {
		t.Fatalf("channel block: %+v", block)
	}
	for k := range store.objs {
		if strings.Contains(k, "/hosted/") {
			t.Fatalf("no hosted object may be stored: %s", k)
		}
	}
	if len(g.calls) != 0 || len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("nothing may be requested or failed: %v %+v", g.calls, r.mem.TeamsFailures)
	}
}

// A custom reaction whose content fails keeps the message and reaction JSON stored with the
// content missing; the replay fetches the content and patches only the backup block, and a later
// unchanged replay neither requests nor writes anything.
func TestTeamsFailedCustomReactionContentKeepsMessageAndReplayRecovers(t *testing.T) {
	link := teamsTestGraph + "/chats/chat-1/messages/m/hostedContents/emo/$value"
	g := &fakeGraph{strict: true, errs: map[string]error{"messages/m/hostedContents/emo/$value": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	raw, head := teamsChatTestMsg(t, "chat-1", "m", "2026-10-02T10:00:00Z", map[string]interface{}{"reactions": teamsCustomReaction(link)})
	chatPrefix := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	key := outlook.TeamsChatMessageKey(chatPrefix, "m", "2026-10-01T10:00:00Z")
	hk := outlook.TeamsChatHostedKey(chatPrefix, "m", "emo")

	if _, err := r.storeChatMessage(raw, head); err == nil || !strings.Contains(err.Error(), "hosted content") {
		t.Fatalf("emoji failure must be reported, got %v", err)
	}
	first := store.objs[key]
	if got := storedReactionTypes(t, first); len(got) != 1 || got[0] != "custom" || !strings.Contains(string(first), "text of m") {
		t.Fatalf("message and reaction JSON must be stored: %s", first)
	}
	block, _ := outlook.ReadTeamsBackup(first)
	if len(block.Hosted) != 0 || len(block.HostedMissing) != 1 || block.HostedMissing[0] != hk || store.meta[key][outlook.TeamsMetaHostedMissing] != "1" {
		t.Fatalf("emoji must be listed as missing, not stored: %+v %v", block, store.meta[key])
	}
	if _, ok := store.objs[hk]; ok {
		t.Fatal("no object may exist for a failed emoji")
	}

	delete(g.errs, "messages/m/hostedContents/emo/$value")
	g.docs = map[string][]byte{"messages/m/hostedContents/emo/$value": []byte("EMO")}
	outcome, err := r.storeChatMessage(raw, head)
	if err != nil || outcome != "updated_hosted" {
		t.Fatalf("replay: %s %v", outcome, err)
	}
	block, _ = outlook.ReadTeamsBackup(store.objs[key])
	if string(store.objs[hk]) != "EMO" || len(block.Hosted) != 1 || block.Hosted[0] != hk || len(block.HostedMissing) != 0 {
		t.Fatalf("emoji must be recovered: %+v", block)
	}
	if _, ok := store.meta[key][outlook.TeamsMetaHostedMissing]; ok {
		t.Fatalf("missing marker must clear: %v", store.meta[key])
	}
	if teamsBodyOnly(t, store.objs[key]) != teamsBodyOnly(t, first) || store.puts[key] != 2 {
		t.Fatalf("replay must only patch the backup block, puts %d", store.puts[key])
	}

	calls := len(g.calls)
	if outcome, err := r.storeChatMessage(raw, head); err != nil || outcome != "skipped" || store.puts[key] != 2 || len(g.calls) != calls {
		t.Fatalf("unchanged replay: %s %v puts %d calls %v", outcome, err, store.puts[key], g.calls[calls:])
	}
	if len(g.unknown) != 0 {
		t.Fatalf("unexpected Graph requests: %v", g.unknown)
	}
}

// A custom reaction added to an unchanged message brings new hosted content: it is fetched with
// the reaction update, kept missing when it fails and recovered on the next replay.
func TestTeamsNewCustomReactionOnUnchangedMessageFetchesItsContent(t *testing.T) {
	link := teamsTestGraph + "/chats/chat-1/messages/m/hostedContents/emo/$value"
	g := &fakeGraph{strict: true, errs: map[string]error{"messages/m/hostedContents/emo/$value": &outlook.TeamsGraphError{Status: 503, Code: "ServiceUnavailable"}}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	chatPrefix := outlook.TeamsChatPrefix(teamsTestTenant, "chat-1")
	key := outlook.TeamsChatMessageKey(chatPrefix, "m", "2026-10-01T10:00:00Z")
	hk := outlook.TeamsChatHostedKey(chatPrefix, "m", "emo")

	raw, head := teamsChatTestMsg(t, "chat-1", "m", "2026-10-02T10:00:00Z", nil)
	if _, err := r.storeChatMessage(raw, head); err != nil {
		t.Fatal(err)
	}
	raw, head = teamsChatTestMsg(t, "chat-1", "m", "2026-10-02T10:00:00Z", map[string]interface{}{"reactions": teamsCustomReaction(link)})
	if _, err := r.storeChatMessage(raw, head); err == nil || !strings.Contains(err.Error(), "hosted content") {
		t.Fatalf("emoji failure must be reported, got %v", err)
	}
	block, _ := outlook.ReadTeamsBackup(store.objs[key])
	if got := storedReactionTypes(t, store.objs[key]); len(got) != 1 || got[0] != "custom" {
		t.Fatalf("new reaction must be stored: %v", got)
	}
	if len(block.HostedMissing) != 1 || block.HostedMissing[0] != hk || store.meta[key][outlook.TeamsMetaHostedMissing] != "1" {
		t.Fatalf("emoji must be listed as missing: %+v %v", block, store.meta[key])
	}

	delete(g.errs, "messages/m/hostedContents/emo/$value")
	g.docs = map[string][]byte{"messages/m/hostedContents/emo/$value": []byte("EMO")}
	if outcome, err := r.storeChatMessage(raw, head); err != nil || outcome != "updated_hosted" {
		t.Fatalf("replay: %s %v", outcome, err)
	}
	block, _ = outlook.ReadTeamsBackup(store.objs[key])
	if string(store.objs[hk]) != "EMO" || len(block.Hosted) != 1 || len(block.HostedMissing) != 0 {
		t.Fatalf("emoji must be recovered: %+v", block)
	}
	if store.puts[key] != 3 || len(g.unknown) != 0 {
		t.Fatalf("puts %d unknown %v", store.puts[key], g.unknown)
	}
}

func TestTeamsSharedChannelsAreReportedAsCoverageGaps(t *testing.T) {
	localShared, _ := json.Marshal(map[string]string{
		"id": "shared-local", "membershipType": "shared", "tenantId": teamsTestTenant,
	})
	incomingShared, _ := json.Marshal(map[string]string{
		"id": "shared-incoming", "membershipType": "shared", "tenantId": "22222222-3333-4444-5555-666666666666",
	})
	g := &fakeGraph{
		docs: map[string][]byte{"/teams/team-1": []byte(`{"id":"team-1"}`)},
		pages: map[string]outlook.TeamsPage{
			"/allChannels":   {Items: []json.RawMessage{localShared, incomingShared}},
			"getAllMessages": {},
		},
	}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	status, _ := r.syncTeam(teamsTeamInfo{ID: "team-1"}, r.deadline)
	if status != teamsCompleteWithGaps {
		t.Fatalf("team status = %s, want %s", status, teamsCompleteWithGaps)
	}
	key := outlook.TeamsTeamMetaKey(outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
	var doc struct {
		Coverage map[string]string                 `json:"channel_coverage"`
		Shared   []teamsSharedChannelCoverageEntry `json:"shared_channel_message_coverage"`
	}
	if err := json.Unmarshal(store.objs[key], &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Coverage["shared"] != "incomplete" || len(doc.Shared) != 2 {
		t.Fatalf("shared channel coverage: %+v", doc)
	}
	if doc.Coverage["standard"] != "verified" || doc.Coverage["private"] != "verified" {
		t.Fatalf("standard/private coverage after complete window: %+v", doc.Coverage)
	}
	if r.status.IncomingChannelSkipped != 1 {
		t.Fatalf("incoming_channel_skipped = %d, want 1", r.status.IncomingChannelSkipped)
	}
	scopes := map[string]string{}
	want := map[string]string{"shared-local": teamsSharedListed, "shared-incoming": teamsSharedNotCollected}
	for _, entry := range doc.Shared {
		if entry.MessageStatus != want[entry.ChannelID] {
			t.Fatalf("shared channel status: %+v", entry)
		}
		scopes[entry.ChannelID] = entry.OwnerScope
	}
	if scopes["shared-local"] != "same_tenant" || scopes["shared-incoming"] != "incoming_cross_tenant" {
		t.Fatalf("owner scopes: %v", scopes)
	}
}

// Channel getAllMessages deletions use deletedDateTime (not @removed). Chat delta deletions use
// @removed (see TestTeamsChatDeletionMarkerPreservesStoredMessage). The two fields are not
// interchangeable across collectors.
func TestTeamsChannelVersionRuleAndDeletion(t *testing.T) {
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{"getAllMessages": {Items: []json.RawMessage{
		teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
	}}}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	if _, err := r.storeChannelMessage("team-1", prefix, teamsMsg("100", "", "ch-a", "2026-10-03T10:00:00.000Z", map[string]interface{}{"body": map[string]string{"content": "newer"}})); err != nil {
		t.Fatal(err)
	}
	key := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "100", "2026-10-01T10:00:00.000Z")
	if _, err := r.storeChannelMessage("team-1", prefix, teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", map[string]interface{}{"body": map[string]string{"content": "older"}})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(store.objs[key]), "newer") {
		t.Fatal("an older copy must never overwrite a newer one")
	}
	if _, err := r.storeChannelMessage("team-1", prefix, teamsMsg("100", "", "ch-a", "2026-10-03T10:00:00.000Z", map[string]interface{}{
		"deletedDateTime": "2026-10-04T00:00:00.000Z", "body": map[string]string{"content": ""},
	})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(store.objs[key]), "newer") || store.meta[key][outlook.TeamsMetaRemovedAt] == "" {
		t.Fatalf("deletion must set removed-at and keep the body: %s %v", store.objs[key], store.meta[key])
	}
}

func storedReactionTypes(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		Reactions []struct {
			Type string `json:"reactionType"`
		} `json:"reactions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, r := range doc.Reactions {
		out = append(out, r.Type)
	}
	return out
}

// Reactions come from the message's own reactions property: Graph v1.0 has no per-message
// reactions listing, and the strict fake fails any request the backup isn't expected to make.
func TestTeamsInlineReactionsArePersistedWithoutReactionRequests(t *testing.T) {
	g := &fakeGraph{strict: true}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	like := []map[string]interface{}{{"reactionType": "like", "createdDateTime": "2026-10-02T10:00:00Z", "user": map[string]interface{}{"user": map[string]string{"id": "u1"}}}}

	root := teamsMsg("root", "", "ch-a", "2026-10-02T10:00:00Z", map[string]interface{}{"reactions": like})
	reply := teamsMsg("reply", "root", "ch-a", "2026-10-02T10:01:00Z", map[string]interface{}{"reactions": []map[string]string{{"reactionType": "heart"}}})
	bare := teamsMsg("bare", "", "ch-a", "2026-10-02T10:02:00Z", nil)
	null := teamsMsg("null", "", "ch-a", "2026-10-02T10:03:00Z", map[string]interface{}{"reactions": nil})
	odd := teamsMsg("odd", "", "ch-a", "2026-10-02T10:04:00Z", map[string]interface{}{"reactions": map[string]string{"unexpected": "shape"}})
	for _, raw := range []json.RawMessage{root, reply, bare, null, odd} {
		if _, err := r.storeChannelMessage("team-1", prefix, raw); err != nil {
			t.Fatal(err)
		}
	}
	chatRaw := json.RawMessage(`{"id":"chat-msg","chatId":"chat-1","createdDateTime":"2026-10-01T10:00:00Z","lastModifiedDateTime":"2026-10-02T10:00:00Z","body":{"content":"chat body"},"reactions":[{"reactionType":"laugh","user":{"user":{"id":"u2"}}}]}`)
	var chatHead teamsMessageHead
	if err := json.Unmarshal(chatRaw, &chatHead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.storeChatMessage(chatRaw, chatHead); err != nil {
		t.Fatal(err)
	}

	key := func(root, id string) string {
		return outlook.TeamsChannelMessageKey(prefix, "ch-a", root, id, "2026-10-01T10:00:00.000Z")
	}
	chatKey := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(teamsTestTenant, "chat-1"), "chat-msg", "2026-10-01T10:00:00Z")
	want := map[string]string{key("root", "root"): "like", key("root", "reply"): "heart", chatKey: "laugh", key("bare", "bare"): "", key("null", "null"): ""}
	for k, reaction := range want {
		body, ok := store.objs[k]
		if !ok {
			t.Fatalf("missing %s; have %v", k, keysOf(store.objs))
		}
		got := storedReactionTypes(t, body)
		if (reaction == "" && len(got) != 0) || (reaction != "" && (len(got) != 1 || got[0] != reaction)) {
			t.Fatalf("%s reactions = %v, want %q", k, got, reaction)
		}
		if store.meta[k][outlook.TeamsMetaReactions] == "" {
			t.Fatalf("%s reaction signature missing", k)
		}
		if !strings.Contains(string(body), "body") {
			t.Fatalf("%s body missing: %s", k, body)
		}
	}
	if !strings.Contains(string(store.objs[key("root", "root")]), `"createdDateTime":"2026-10-02T10:00:00Z","reactionType":"like"`) {
		t.Fatalf("inline reaction must be kept whole: %s", store.objs[key("root", "root")])
	}
	if _, ok := store.objs[key("odd", "odd")]; !ok || !strings.Contains(string(store.objs[key("odd", "odd")]), "unexpected") {
		t.Fatal("an unexpected reactions value must not stop the message body from being stored")
	}
	if !strings.Contains(string(store.objs[key("bare", "bare")]), `"reactions":[]`) {
		t.Fatalf("a message without reactions is stored with an empty list: %s", store.objs[key("bare", "bare")])
	}

	// Same version, reactions changed inline: only the reactions are refreshed.
	rootChanged := teamsMsg("root", "", "ch-a", "2026-10-02T10:00:00Z", map[string]interface{}{"reactions": []map[string]string{{"reactionType": "surprised"}}})
	if got, err := r.storeMessage(rootChanged, mustHead(t, rootChanged), teamsMessageTarget{key: key("root", "root")}); err != nil || got != "updated_reactions" {
		t.Fatalf("reaction refresh: %s %v", got, err)
	}
	if got := storedReactionTypes(t, store.objs[key("root", "root")]); len(got) != 1 || got[0] != "surprised" {
		t.Fatalf("refreshed reactions = %v", got)
	}

	if len(g.unknown) != 0 {
		t.Fatalf("unexpected Graph requests: %v", g.unknown)
	}
	for _, c := range g.calls {
		if strings.Contains(c, "/reactions") {
			t.Fatalf("per-message reactions request made: %s", c)
		}
	}
}

func mustHead(t *testing.T, raw json.RawMessage) teamsMessageHead {
	t.Helper()
	var h teamsMessageHead
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTeamsStrictFakeRejectsUnknownURLs(t *testing.T) {
	g := &fakeGraph{strict: true}
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	reactions := "https://graph.microsoft.com/v1.0/chats/chat-1/messages/m1/reactions"
	if _, err := r.getAll(reactions); outlook.ClassifyTeamsError(err) != outlook.ClassifyTeamsError(&outlook.TeamsGraphError{Status: 404, Code: "NotFound"}) {
		t.Fatalf("unknown page URL must fail, got %v", err)
	}
	if _, err := r.getJSON(reactions); err == nil {
		t.Fatal("unknown document URL must fail")
	}
	if len(g.unknown) != 2 || g.unknown[0] != reactions {
		t.Fatalf("unknown URLs must be recorded: %v", g.unknown)
	}
}

func TestTeamsHeldWindowReplaysThenReleasesAsGap(t *testing.T) {
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{"getAllMessages": {Items: []json.RawMessage{
		teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
		teamsMsg("poison", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
	}}}}
	g.install(t)
	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	prevNow := teamsNowFn
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prevNow })

	store := newMemTeamsStore()
	store.putErr = func(key string, _ int) error {
		if strings.Contains(key, "poison") {
			return errors.New("write refused")
		}
		return nil
	}
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	st := &teamsTeamState{FullListing: true}
	for i := 1; i <= teamsReleaseReplays-1; i++ {
		if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, clock.Add(time.Hour)); got != teamsIncomplete {
			t.Fatalf("replay %d: %s", i, got)
		}
		if len(st.Windows) != 1 || st.Windows[0].Held != i || st.HighWater != nil {
			t.Fatalf("replay %d: window must stay held: %+v", i, st.Windows)
		}
		clock = clock.Add(12 * time.Hour)
	}
	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, clock.Add(time.Hour)); got != teamsCompleteWithGaps {
		t.Fatalf("after 10 replays over 3+ days: %s", got)
	}
	if len(st.Windows) != 0 || st.HighWater == nil {
		t.Fatalf("released window must close: %+v", st)
	}
	if len(r.status.SkippedItems) == 0 || r.status.SkippedItems[0].IDs["message_id"] != "poison" {
		t.Fatalf("released item must be listed as a gap: %+v", r.status.SkippedItems)
	}
	fallback := false
	for k := range store.objs {
		if strings.Contains(k, "/_failed/") {
			fallback = true
		}
	}
	if !fallback {
		t.Fatal("an item read but not writable is kept under its ids-only fallback key")
	}
}

// A rejected nextLink (here a loop) holds the window, replays it from its start instead of saving
// the same page, and after the hold limit releases it with the paging failure listed as a gap.
func TestTeamsPagingLoopHoldsThenReleasesAsGap(t *testing.T) {
	loop := "https://graph.microsoft.com/v1.0/teams/team-1/channels/getAllMessages?$skiptoken=same"
	g := &fakeGraph{pages: map[string]outlook.TeamsPage{"getAllMessages": {Items: []json.RawMessage{
		teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
	}, NextLink: loop}}}
	g.install(t)
	clock := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	prevNow := teamsNowFn
	teamsNowFn = func() time.Time { return clock }
	t.Cleanup(func() { teamsNowFn = prevNow })

	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	st := &teamsTeamState{FullListing: true}
	for i := 1; i <= teamsReleaseReplays-1; i++ {
		g.calls = nil
		if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, clock.Add(time.Hour)); got != teamsIncomplete {
			t.Fatalf("replay %d: status %s", i, got)
		}
		if len(st.Windows) != 1 || st.HighWater != nil || st.Windows[0].PageLink != "" || st.Windows[0].Held != i {
			t.Fatalf("replay %d: window must be held and restart from its start: %+v", i, st.Windows)
		}
		if len(g.calls) == 0 || strings.Contains(g.calls[0], "skiptoken") {
			t.Fatalf("replay %d must start from the window's first page: %v", i, g.calls)
		}
		f, ok := r.mem.TeamsFailures["channels:stream:team:team-1"]
		if !ok || f.ErrorCode != outlook.TeamsStatusPaging {
			t.Fatalf("paging error must be recorded on the stream: %+v", r.mem.TeamsFailures)
		}
		clock = clock.Add(12 * time.Hour)
	}
	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, clock.Add(time.Hour)); got != teamsCompleteWithGaps {
		t.Fatalf("after the hold limit: %s", got)
	}
	if len(st.Windows) != 0 || st.HighWater == nil {
		t.Fatalf("released window must close: %+v", st)
	}
	listed := false
	for _, s := range r.status.SkippedItems {
		listed = listed || s.Key == "channels:stream:team:team-1"
	}
	if !listed {
		t.Fatalf("paging failure must be listed as a gap: %+v", r.status.SkippedItems)
	}
}

// Channel history follows the nextLink exactly as documented for getAllMessages (OData key form,
// lower-case function name), stores both pages and closes the window.
func TestTeamsChannelHistoryFollowsDocumentedNextLink(t *testing.T) {
	next := "https://graph.microsoft.com/v1.0/teams('team-1')/channels/getallMessages?$skiptoken=U2tpcFZhbHVl%3d%3d&$filter=lastModifiedDateTime+gt+2019-11-01T00%3a00%3a00Z"
	g := &fakeGraph{strict: true, pages: map[string]outlook.TeamsPage{
		"/teams/team-1/channels/getAllMessages?$filter": {Items: []json.RawMessage{
			teamsMsg("100", "", "ch-a", "2026-10-02T10:00:00.000Z", nil),
		}, NextLink: next},
		"$skiptoken=U2tpcFZhbHVl%3d%3d": {Items: []json.RawMessage{
			teamsMsg("101", "100", "ch-a", "2026-10-02T10:01:00.000Z", nil),
		}},
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	st := &teamsTeamState{FullListing: true}
	if got := r.syncTeamMessages(st, teamsTeamInfo{ID: "team-1"}, prefix, r.deadline); got != teamsComplete {
		t.Fatalf("status %s; failures %+v; unknown %v", got, r.mem.TeamsFailures, g.unknown)
	}
	for _, id := range []string{"100", "101"} {
		if _, ok := store.objs[outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", id, "2026-10-01T10:00:00.000Z")]; !ok {
			t.Fatalf("page message %s missing; have %v", id, keysOf(store.objs))
		}
	}
	if len(st.Windows) != 0 || st.HighWater == nil {
		t.Fatalf("window must close: %+v", st)
	}
	if len(g.calls) != 2 || g.calls[1] != next {
		t.Fatalf("second request must be the nextLink unchanged: %v", g.calls)
	}
	if _, ok := r.mem.TeamsFailures["channels:stream:team:team-1"]; ok || len(g.unknown) != 0 {
		t.Fatalf("no failures or unknown requests: %+v %v", r.mem.TeamsFailures, g.unknown)
	}
}

func TestTeamsFailureCapHoldsInsteadOfGrowing(t *testing.T) {
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	r.mem.TeamsFailures = map[string]repo.TeamsFailure{}
	for i := 0; i < teamsMaxFailures; i++ {
		r.mem.TeamsFailures[fmt.Sprintf("chats:item:c:%d", i)] = repo.TeamsFailure{Kind: teamsFailItem}
	}
	if r.fail("chats:item:c:new", repo.TeamsFailure{Phase: "chats", Kind: teamsFailItem}, errors.New("x"), true) {
		t.Fatal("a full failure list must not grow")
	}
	if r.failuresCapped != 1 || r.status.FailuresCapped != 1 {
		t.Fatalf("failures capped counter: run=%d status=%d", r.failuresCapped, r.status.FailuresCapped)
	}
	if len(r.status.Gaps) != 1 || r.status.Gaps[0].Kind != "failures_capped" || r.status.Gaps[0].Resource != "chats:item:c:new" {
		t.Fatalf("capped drop must be a durable gap: %+v", r.status.Gaps)
	}
	if !r.fail("chats:item:c:1", repo.TeamsFailure{Phase: "chats", Kind: teamsFailItem}, errors.New("x"), true) {
		t.Fatal("an existing entry is still updated")
	}
	if r.mem.TeamsFailures["chats:item:c:1"].Attempts != 1 || len(r.mem.TeamsFailures) != teamsMaxFailures {
		t.Fatalf("entries: %d", len(r.mem.TeamsFailures))
	}
	if size := teamsMemorySize(r.mem); size > teamsMaxMemoryBytes {
		t.Fatalf("task memory %d bytes over the limit", size)
	}
}

func TestTeamsChannelCoverageStates(t *testing.T) {
	if got := teamsCoverageFromSync(teamsComplete, ""); got != "verified" {
		t.Fatalf("complete → verified, got %s", got)
	}
	if got := teamsCoverageFromSync(teamsCompleteWithGaps, ""); got != "incomplete" {
		t.Fatalf("gaps → incomplete, got %s", got)
	}
	if got := teamsCoverageFromSync(teamsBlocked, outlook.TeamsStatusMissingPermission); got != "inaccessible" {
		t.Fatalf("missing permission → inaccessible, got %s", got)
	}
	if got := teamsCoverageFromSync(teamsBlocked, outlook.TeamsStatusThrottled); got != "failed" {
		t.Fatalf("throttled block → failed, got %s", got)
	}
	cov := teamsBuildChannelCoverage(nil, nil, teamsComplete, "", "")
	if cov["standard"] != "verified" || cov["private"] != "verified" || cov["shared"] != "verified" {
		t.Fatalf("no shared channels after complete window: %+v", cov)
	}
}

func TestTeamsFailureErrorTextSanitizedAndBackedOff(t *testing.T) {
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	err := &outlook.TeamsGraphError{Status: 403, Code: "Forbidden", Message: "denied for bob@contoso.com on https://x/y"}
	r.fail("chats:item:c:m", repo.TeamsFailure{Phase: "chats", Kind: teamsFailItem, ResourceID: "u1"}, err, false)
	f := r.mem.TeamsFailures["chats:item:c:m"]
	if strings.Contains(f.ErrorMessage, "bob@") || strings.Contains(f.ErrorMessage, "https://") || f.HTTPStatus != 403 {
		t.Fatalf("failure: %+v", f)
	}
	if f.SkipUntil == nil || f.SkipUntil.Sub(f.LastAt) != teamsPermanentBackoff {
		t.Fatalf("permanent status backs off a day: %+v", f.SkipUntil)
	}
}

func TestTeamsCheckpointStopsWhenOwnershipLost(t *testing.T) {
	dbs := newTeamsTestDB(t)
	job := createTeamsJob(t, dbs, 1, repo.ResourceTypeTenant, teamsTestTenant, nil)
	task := &repo.TaskListingDB{CronJobID: job.ID, Status: repo.TaskStatusRunning}
	if err := dbs.DB.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, dbs, job)
	r.checkpointFn = func(mem repo.TaskMemory) error { return dbs.CronJobRepo.CheckpointTaskMemory(job.ID, task.ID, mem) }
	r.ownsFn = func() (bool, error) { return dbs.CronJobRepo.TaskOwnsJob(job.ID, task.ID) }
	r.cursor().Round = 7
	if err := r.checkpoint(); err != nil {
		t.Fatalf("owning run checkpoints: %v", err)
	}
	if err := dbs.DB.Model(task).Update("status", "failed").Error; err != nil {
		t.Fatal(err)
	}
	r.cursor().Round = 8
	if err := r.checkpoint(); !errors.Is(err, repo.ErrJobOwnershipLost) || !errors.Is(r.stopErr, repo.ErrJobOwnershipLost) {
		t.Fatalf("lost ownership: %v / %v", err, r.stopErr)
	}
	saved, err := dbs.CronJobRepo.GetCronJobByID(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.TaskMemory.TeamsCursor == nil || saved.TaskMemory.TeamsCursor.Round != 7 {
		t.Fatalf("stale checkpoint must not land: %+v", saved.TaskMemory.TeamsCursor)
	}
	key := outlook.TeamsTeamMetaKey(outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"))
	if err := r.put(key, []byte("{}"), nil); err == nil || len(store.objs) != 0 {
		t.Fatal("a run that lost ownership writes nothing")
	}
}

func TestTeamsMergeImportsThenPausesOnlySameConnection(t *testing.T) {
	(&fakeGraph{}).install(t)
	dbs := newTeamsTestDB(t)
	tenantJob := createTeamsJob(t, dbs, 1, repo.ResourceTypeTenant, teamsTestTenant, map[string]interface{}{"team_ids": []interface{}{"t0"}})
	same := createTeamsJob(t, dbs, 1, repo.ResourceTypeTeam, "t1", map[string]interface{}{"team_id": "t1", "channel_ids": []interface{}{"c1"}})
	otherCred := createTeamsJob(t, dbs, 2, repo.ResourceTypeTeam, "t2", map[string]interface{}{"team_id": "t2"})
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, dbs, tenantJob)

	// Crash after the import was recorded, before the pause.
	r.mem.TeamsMerge = &repo.TeamsMergeState{Imported: map[string]repo.TeamsMergeImport{}}
	r.mergeTeamJobs()
	r.mergeTeamJobs()

	got, _ := dbs.CronJobRepo.GetCronJobByID(tenantJob.ID)
	sel := teamsSelectionFromJob(got)
	if !sel.teamSelected("t0") || !sel.teamSelected("t1") || sel.teamSelected("t2") || !sel.channelSelected("t1", "c1") || sel.channelSelected("t1", "c9") {
		t.Fatalf("selection after merge: %+v", sel)
	}
	paused, _ := dbs.CronJobRepo.GetCronJobByID(same.ID)
	untouched, _ := dbs.CronJobRepo.GetCronJobByID(otherCred.ID)
	if paused.Active || paused.Message != teamsMergedJobMessage || !untouched.Active {
		t.Fatalf("pause: same %v %q, other active %v", paused.Active, paused.Message, untouched.Active)
	}
	id := fmt.Sprint(same.ID)
	if _, ok := r.mem.TeamsMerge.Imported[id]; !ok {
		t.Fatal("import must be recorded")
	}
	if _, ok := r.mem.TeamsMerge.Paused[id]; !ok {
		t.Fatal("pause must be recorded")
	}
	var st teamsTeamState
	if !r.loadState(outlook.TeamsTeamStateKey(r.tenantPrefix, "t1"), &st) || !st.FullListing {
		t.Fatal("a merged team starts with a full listing")
	}
}

func TestTeamsLegacyConversionVerifiesBeforeRemoving(t *testing.T) {
	(&fakeGraph{}).install(t)
	store := newMemTeamsStore()
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")
	created := "2026-09-01T08:00:00.000Z"
	metaKey := outlook.TeamsIDBasedMetaKey(prefix, "ch-a", "100", created)
	dataKey := outlook.TeamsIDBasedDataKey(prefix, "ch-a", "100", created)
	store.objs[metaKey] = []byte(`{"team_id":"team-1","channel_id":"ch-a","message_id":"100"}`)
	store.objs[dataKey] = teamsMsg("100", "", "ch-a", created, nil)
	store.meta[metaKey], store.meta[dataKey] = map[string]string{}, map[string]string{}
	r := newTestTeamsRun(t, store, nil, nil)
	st := &teamsTeamState{}
	if !r.convertLegacyTeam(st, prefix, r.deadline) {
		t.Fatal("conversion should finish")
	}
	newKey := outlook.TeamsChannelMessageKey(prefix, "ch-a", "100", "100", "2026-10-01T10:00:00.000Z")
	if _, ok := store.objs[newKey]; !ok {
		t.Fatalf("new object missing; have %v", keysOf(store.objs))
	}
	if _, ok := store.objs[metaKey]; ok {
		t.Fatal("old meta must be removed after verification")
	}
	block, _ := outlook.ReadTeamsBackup(store.objs[newKey])
	if block.ConvertedFrom != dataKey {
		t.Fatalf("converted_from: %+v", block)
	}

	// A new object that doesn't verify keeps the old copies.
	store2 := newMemTeamsStore()
	store2.objs[metaKey], store2.meta[metaKey] = []byte(`{}`), map[string]string{}
	store2.putErr = func(string, int) error { return errors.New("down") }
	r2 := newTestTeamsRun(t, store2, nil, nil)
	if r2.convertLegacyTeam(&teamsTeamState{}, prefix, r2.deadline) {
		t.Fatal("failed conversion must not report done")
	}
	if _, ok := store2.objs[metaKey]; !ok {
		t.Fatal("old objects stay when the new one isn't written")
	}
}

func TestTeamsContentURLsNeverRequested(t *testing.T) {
	_, err := outlook.FetchTeamsJSON(context.Background(), "tok",
		"https://graph.microsoft.com/v1.0/users/u/onlineMeetings/m/transcripts/t1/content")
	if !errors.Is(err, outlook.ErrTeamsContentAPI) {
		t.Fatalf("content URL must be refused, got %v", err)
	}
}

func teamsRolesToken(roles ...string) string {
	claims, _ := json.Marshal(map[string]interface{}{"tid": teamsTestTenant, "roles": roles})
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(claims) + ".sig"
}

func teamsConfigPage(id string) outlook.TeamsPage {
	return outlook.TeamsPage{Items: []json.RawMessage{json.RawMessage(`{"id":"` + id + `"}`)}}
}

func teamsCalled(g *fakeGraph, part string) bool {
	for _, c := range g.calls {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}

// A config part whose role isn't granted is never requested, adds no failure entry (an old one
// is cleared) and is reported as not consented; the other parts are stored.
func TestTeamsConfigPartWithoutRoleIsSkippedWithoutFailure(t *testing.T) {
	g := &fakeGraph{strict: true, pages: map[string]outlook.TeamsPage{
		"/teams/team-1/members":               teamsConfigPage("m1"),
		"/teams/team-1/installedApps":         teamsConfigPage("a1"),
		"/teams/team-1/tags":                  teamsConfigPage("t1"),
		"/teams/team-1/channels/ch-a/members": teamsConfigPage("cm1"),
		"/teams/team-1/channels/ch-a/tabs":    teamsConfigPage("tab1"),
	}}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.token = teamsRolesToken("Team.ReadBasic.All", "Channel.ReadBasic.All", "TeamworkTag.Read.All", "Group.Read.All")
	r.loadConsent()
	r.mem.TeamsFailures = map[string]repo.TeamsFailure{"team_config:item:team-1:members": {Phase: "team_config", Kind: teamsFailItem}}
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-1")

	if !r.syncTeamConfig("team-1", prefix, []byte(`{"id":"team-1"}`), []teamsChannelInfo{{ID: "ch-a"}}) {
		t.Fatal("a part without its role must not fail the config")
	}
	if teamsCalled(g, "/members") || len(g.unknown) != 0 {
		t.Fatalf("members requested without TeamMember.Read.All / ChannelMember.Read.All: %v", g.calls)
	}
	for _, k := range []string{
		outlook.TeamsTeamConfigKey(prefix, "settings"), outlook.TeamsTeamConfigKey(prefix, "apps"),
		outlook.TeamsTeamConfigKey(prefix, "tags"), outlook.TeamsChannelConfigKey(prefix, "ch-a", "tabs"),
	} {
		if _, ok := store.objs[k]; !ok {
			t.Fatalf("missing %s; have %v", k, keysOf(store.objs))
		}
	}
	for _, k := range []string{outlook.TeamsTeamConfigKey(prefix, "members"), outlook.TeamsChannelConfigKey(prefix, "ch-a", "members")} {
		if _, ok := store.objs[k]; ok {
			t.Fatalf("%s written without its role", k)
		}
	}
	if len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("no failure entry for a part without its role: %v", r.mem.TeamsFailures)
	}
	for part, role := range map[string]string{"members": "TeamMember.Read.All", "channel_members": "ChannelMember.Read.All"} {
		got := r.status.Features[teamsConfigFeature(part)]
		if got.CapabilityStatus != outlook.TeamsStatusNotConsented || got.Code != "missing role "+role {
			t.Fatalf("%s: %+v", part, got)
		}
	}
	for _, f := range []string{"teams_config", teamsConfigFeature("apps"), teamsConfigFeature("tags"), teamsConfigFeature("channel_tabs")} {
		if got := r.status.Features[f]; got.CapabilityStatus != outlook.TeamsStatusOK {
			t.Fatalf("%s: %+v", f, got)
		}
	}
}

// A part Graph refuses for the tenant (403) is reported once on its feature and adds no failure
// entry per team or channel, so it can't fill the failure map; the other parts are stored.
func TestTeamsConfigDeniedPartAddsNoFailurePerChannel(t *testing.T) {
	forbidden := &outlook.TeamsGraphError{Status: 403, Code: "Forbidden"}
	g := &fakeGraph{strict: true,
		pages: map[string]outlook.TeamsPage{"/members": teamsConfigPage("m1"), "/tags": teamsConfigPage("t1")},
		errs:  map[string]error{"/tabs": forbidden, "/installedApps": forbidden},
	}
	g.install(t)
	store := newMemTeamsStore()
	r := newTestTeamsRun(t, store, nil, nil)
	r.loadConsent()
	channels := make([]teamsChannelInfo, 50)
	for i := range channels {
		channels[i] = teamsChannelInfo{ID: fmt.Sprintf("ch-%d", i)}
	}
	for _, team := range []string{"team-1", "team-2"} {
		if !r.syncTeamConfig(team, outlook.TeamsTeamPrefix(teamsTestTenant, team), []byte(`{"id":"`+team+`"}`), channels) {
			t.Fatalf("%s: a refused part must not fail the config (it isn't retried per team)", team)
		}
	}
	if len(r.mem.TeamsFailures) != 0 {
		t.Fatalf("refused parts must not add failure entries: %d", len(r.mem.TeamsFailures))
	}
	for _, part := range []string{"apps", "channel_tabs"} {
		if got := r.status.Features[teamsConfigFeature(part)]; got.CapabilityStatus != outlook.TeamsStatusMissingPermission {
			t.Fatalf("%s: %+v", part, got)
		}
	}
	prefix := outlook.TeamsTeamPrefix(teamsTestTenant, "team-2")
	for _, k := range []string{outlook.TeamsTeamConfigKey(prefix, "members"), outlook.TeamsTeamConfigKey(prefix, "tags"),
		outlook.TeamsChannelConfigKey(prefix, "ch-49", "members")} {
		if _, ok := store.objs[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	if len(g.unknown) != 0 {
		t.Fatalf("unknown Graph URLs: %v", g.unknown)
	}
}

// Any other error keeps the per-part failure entry and fails the config, so it is retried.
func TestTeamsConfigOtherErrorStillRecordsFailure(t *testing.T) {
	g := &fakeGraph{strict: true,
		pages: map[string]outlook.TeamsPage{"/members": teamsConfigPage("m1"), "/installedApps": teamsConfigPage("a1")},
		errs:  map[string]error{"/tags": &outlook.TeamsGraphError{Status: 404, Code: "NotFound"}},
	}
	g.install(t)
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	r.loadConsent()
	if r.syncTeamConfig("team-1", outlook.TeamsTeamPrefix(teamsTestTenant, "team-1"), []byte(`{"id":"team-1"}`), nil) {
		t.Fatal("a failed part must fail the config")
	}
	f, ok := r.mem.TeamsFailures["team_config:item:team-1:tags"]
	if !ok || f.Phase != "team_config" || f.ItemIDs["part"] != "tags" {
		t.Fatalf("failure entry: %+v (%v)", f, r.mem.TeamsFailures)
	}
	if len(r.mem.TeamsFailures) != 1 {
		t.Fatalf("only the failed part: %v", r.mem.TeamsFailures)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTeamsJobWarning(t *testing.T) {
	res := func(kind, phase, status, code string) teamsResourceStatus {
		return teamsResourceStatus{Type: kind, Phase: phase, Status: status, Code: code}
	}
	tests := []struct {
		name      string
		resources map[string]teamsResourceStatus
		notes     []string
		want      string
	}{
		{
			name: "all complete is success",
			resources: map[string]teamsResourceStatus{
				"team": res("team", teamsPhaseTeams, teamsComplete, ""),
				"user": res("user", teamsPhaseChats, teamsComplete, ""),
			},
		},
		{
			name: "budget incomplete and optional calls block are not a job warning",
			resources: map[string]teamsResourceStatus{
				"t1": res("team", teamsPhaseTeams, teamsComplete, ""),
				"u1": res("user", teamsPhaseChats, teamsComplete, ""),
				"u2": res("user", teamsPhaseChats, teamsComplete, ""),
				"u3": res("user", teamsPhaseChats, teamsComplete, ""),
				"u4": res("user", teamsPhaseMeetings, teamsComplete, ""),
				"u5": res("user", teamsPhaseMeetings, teamsComplete, ""),
				"u6": res("user", teamsPhaseMeetings, teamsComplete, ""),
				"u7": res("user", teamsPhaseMeetings, teamsCompleteWithRetries, ""),
				"u8": res("user", teamsPhaseChats, teamsIncomplete, ""),
				"c1": res("calls", teamsPhaseCalls, teamsBlocked, outlook.TeamsStatusMissingPermission),
			},
			notes: []string{"Some Teams files are in OneDrive or SharePoint locations without a backup job"},
		},
		{
			name: "throttled incomplete resumes without a job warning",
			resources: map[string]teamsResourceStatus{
				"u1": res("user", teamsPhaseChats, teamsIncomplete, outlook.TeamsStatusThrottled),
			},
		},
		{
			name: "shared-channel limitation is not a job warning",
			resources: map[string]teamsResourceStatus{
				"t1": res("team", teamsPhaseTeams, teamsCompleteWithGaps, ""),
			},
			notes: []string{"Messages of shared channels owned by another tenant, or whose host team couldn't be read, are not collected"},
		},
		{
			name: "required chat blocked is a job warning",
			resources: map[string]teamsResourceStatus{
				"u1": res("user", teamsPhaseChats, teamsBlocked, outlook.TeamsStatusMissingPermission),
			},
			want: teamsWarningPrefix + ": 1 blocked",
		},
		{
			name: "team paging error is a job warning",
			resources: map[string]teamsResourceStatus{
				"t1": res("team", teamsPhaseTeams, teamsIncomplete, outlook.TeamsStatusPaging),
			},
			want: teamsWarningPrefix + ": 1 incomplete",
		},
		{
			name:  "unexpected status note is a job warning",
			notes: []string{"Graph returned an unexpected tenant error"},
			want:  teamsWarningPrefix + ": Graph returned an unexpected tenant error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := teamsJobWarning(tc.resources, tc.notes)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestTeamsFinishClearsExpectedWarning(t *testing.T) {
	r := newTestTeamsRun(t, newMemTeamsStore(), nil, nil)
	r.input.Job.Message = "Teams backup finished with warnings: 8 complete, 1 incomplete, 1 blocked"
	r.input.Job.MessageStatus = repo.JobMessageStatusWarning
	r.status.Resources = map[string]teamsResourceStatus{
		"team:1:teams":  {Type: "team", Phase: teamsPhaseTeams, Status: teamsComplete},
		"user:1:chats":  {Type: "user", Phase: teamsPhaseChats, Status: teamsComplete},
		"user:2:chats":  {Type: "user", Phase: teamsPhaseChats, Status: teamsIncomplete},
		"calls:t:calls": {Type: "calls", Phase: teamsPhaseCalls, Status: teamsBlocked, Code: outlook.TeamsStatusMissingPermission},
	}
	r.warn("Some Teams files are in OneDrive or SharePoint locations without a backup job")
	if err := r.finish(); err != nil {
		t.Fatal(err)
	}
	if r.input.Job.Message != "" || r.input.Job.MessageStatus != "" {
		t.Fatalf("expected job warning cleared, got %q (%s)", r.input.Job.Message, r.input.Job.MessageStatus)
	}
}
