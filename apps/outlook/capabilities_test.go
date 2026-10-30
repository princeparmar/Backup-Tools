package outlook

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type capabilityGraph struct {
	mu    sync.Mutex
	paths []string
}

func (g *capabilityGraph) serve(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, r.URL.Path)
		g.mu.Unlock()
		handler(w, r)
	}))
	prev := graphBaseURL
	graphBaseURL = srv.URL
	t.Cleanup(func() {
		graphBaseURL = prev
		srv.Close()
	})
}

func (g *capabilityGraph) called(substr string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.paths {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

func TestEvaluateCapabilities_missingRoleVsNotProvisioned(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"value":[{"id":"u1"}]}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"MailboxNotEnabledForRESTAPI","message":"no mailbox"}}`))
		case strings.HasSuffix(r.URL.Path, "/drive"):
			_, _ = w.Write([]byte(`{"id":"d1"}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied"}}`))
		}
	})

	res := EvaluateCapabilities(context.Background(), CapabilityInput{
		AccessToken:   "app-token",
		GrantedRoles:  []string{"User.Read.All", "Mail.Read", "Files.Read.All"},
		SampleUserIDs: []string{"u1"},
	})

	if !res.Capabilities[CapabilityListUsers] || !res.Capabilities[CapabilityOneDrive] {
		t.Fatalf("list_users/onedrive should be true: %v", res.Capabilities)
	}
	if res.Capabilities[CapabilityMail] || res.Errors[CapabilityMail].Code != CapabilityErrNotProvisioned {
		t.Fatalf("mail: want not_provisioned, got %v %+v", res.Capabilities[CapabilityMail], res.Errors[CapabilityMail])
	}
	calErr := res.Errors[CapabilityCalendar]
	if res.Capabilities[CapabilityCalendar] || calErr.Code != CapabilityErrMissingRole || calErr.Role != "Calendars.Read" {
		t.Fatalf("calendar: want missing_role Calendars.Read, got %+v", calErr)
	}
	if g.called("/calendars") {
		t.Fatal("missing role must not trigger a Graph probe")
	}
	if !res.Probed {
		t.Fatal("expected probed result with sample users")
	}
}

func TestEvaluateCapabilities_supersetRoleAndTemporaryKeepsPrevious(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	// The retry loop backs off on 429; a short deadline turns that into a temporary error quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	res := EvaluateCapabilities(ctx, CapabilityInput{
		AccessToken:  "app-token",
		GrantedRoles: []string{"Directory.Read.All"},
		Previous:     map[string]bool{CapabilityListUsers: true},
	})
	if res.Errors[CapabilityListUsers].Code != CapabilityErrTemporary || !res.Capabilities[CapabilityListUsers] {
		t.Fatalf("temporary failure must keep previous=true: %v %+v", res.Capabilities[CapabilityListUsers], res.Errors[CapabilityListUsers])
	}
}

func TestEvaluateCapabilities_noSamplesIsRolesOnly(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[{"id":"x"}]}`))
	})
	res := EvaluateCapabilities(context.Background(), CapabilityInput{
		AccessToken:  "app-token",
		GrantedRoles: []string{"User.Read.All", "Mail.Read"},
	})
	if res.Probed {
		t.Fatal("no samples must be a roles-only evaluation")
	}
	if !res.Capabilities[CapabilityMail] {
		t.Fatal("mail role granted without samples should be provisionally true")
	}
	if g.called("/messages") {
		t.Fatal("no per-user probe expected without samples")
	}
}

func TestTeamsChannelFeatureRequiresChannelDiscoveryRole(t *testing.T) {
	missing := TeamsFeaturesNotConsented([]string{"Team.ReadBasic.All", "ChannelMessage.Read.All"})
	if missing[TeamsFeatureChannelMessages] != "Channel.ReadBasic.All" {
		t.Fatalf("channel backup missing role = %q, want Channel.ReadBasic.All", missing[TeamsFeatureChannelMessages])
	}
	granted := TeamsFeaturesNotConsented([]string{"Team.ReadBasic.All", "Channel.ReadBasic.All", "ChannelMessage.Read.All"})
	if _, ok := granted[TeamsFeatureChannelMessages]; ok {
		t.Fatalf("channel backup should be consented with discovery and message roles: %v", granted)
	}
}

// Each config part needs its own documented role; the team and channel discovery roles cover none.
func TestTeamsConfigPartsNeedTheirOwnRoles(t *testing.T) {
	if TeamsConfigPartsNotConsented(nil) != nil {
		t.Fatal("a token without roles must not block config parts")
	}
	missing := TeamsConfigPartsNotConsented([]string{"Team.ReadBasic.All", "Channel.ReadBasic.All"})
	if len(missing) != len(TeamsConfigPartRoles) {
		t.Fatalf("discovery roles must not cover config parts: %v", missing)
	}
	for part, role := range TeamsConfigPartRoles {
		if missing[part] != role {
			t.Fatalf("%s: missing %q, want %q", part, missing[part], role)
		}
	}
	if got := TeamsConfigPartsNotConsented([]string{"TeamMember.Read.All", "TeamsAppInstallation.ReadForTeam.All",
		"TeamworkTag.Read.All", "ChannelMember.Read.All", "TeamsTab.Read.All"}); len(got) != 0 {
		t.Fatalf("least-privileged roles: %v", got)
	}
	if got := TeamsConfigPartsNotConsented([]string{"TeamMember.ReadWrite.All", "TeamsAppInstallation.Read.All",
		"TeamworkTag.ReadWrite.All", "ChannelMember.ReadWrite.All", "TeamsTab.ReadWrite.All"}); len(got) != 0 {
		t.Fatalf("documented higher roles: %v", got)
	}
	got := TeamsConfigPartsNotConsented([]string{"Group.Read.All"})
	if _, ok := got["apps"]; ok {
		t.Fatalf("Group.Read.All is documented for installed apps: %v", got)
	}
	if _, ok := got["channel_tabs"]; ok {
		t.Fatalf("Group.Read.All is documented for tabs: %v", got)
	}
	for _, part := range []string{"members", "tags", "channel_members"} {
		if _, ok := got[part]; !ok {
			t.Fatalf("%s: Group.Read.All is not documented for it: %v", part, got)
		}
	}
}

func attendanceTestToken(claims string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString([]byte(claims)) + ".sig"
}

// The meeting lookup needs OnlineMeetings.Read.All (app) or OnlineMeetings.Read (delegated); the
// attendance reports need OnlineMeetingArtifact.Read.All in both. Neither covers the other, and a
// delegated token is checked by its scopes, never treated as consented for lacking roles.
func TestTeamsAttendanceRequestsNeedTheirOwnPermission(t *testing.T) {
	lookupMissing := map[string]string{TeamsAttendanceLookup: "OnlineMeetings.Read.All"}
	reportsMissing := map[string]string{TeamsAttendanceReports: "OnlineMeetingArtifact.Read.All"}
	cases := []struct {
		name, token string
		want        map[string]string
	}{
		{"opaque token", "opaque", nil},
		{"app: artifacts only", attendanceTestToken(`{"roles":["OnlineMeetingArtifact.Read.All"]}`), lookupMissing},
		{"app: meetings only", attendanceTestToken(`{"roles":["OnlineMeetings.Read.All"]}`), reportsMissing},
		{"app: both", attendanceTestToken(`{"roles":["onlinemeetings.read.all","OnlineMeetingArtifact.Read.All"]}`), map[string]string{}},
		{"app: higher meetings role", attendanceTestToken(`{"roles":["OnlineMeetings.ReadWrite.All","OnlineMeetingArtifact.Read.All"]}`), map[string]string{}},
		{"app: RSC chat role is not the reports role", attendanceTestToken(`{"roles":["OnlineMeetings.Read.All","OnlineMeetingArtifact.Read.Chat"]}`), reportsMissing},
		{"app: no roles claim", attendanceTestToken(`{"tid":"t"}`), map[string]string{TeamsAttendanceLookup: "OnlineMeetings.Read.All", TeamsAttendanceReports: "OnlineMeetingArtifact.Read.All"}},
		{"delegated: meetings only", attendanceTestToken(`{"scp":"User.Read OnlineMeetings.Read"}`), reportsMissing},
		{"delegated: both", attendanceTestToken(`{"scp":"OnlineMeetings.ReadWrite OnlineMeetingArtifact.Read.All"}`), map[string]string{}},
		{"delegated: app names are not scopes", attendanceTestToken(`{"scp":"OnlineMeetings.Read.All OnlineMeetingArtifact.Read.All"}`), map[string]string{TeamsAttendanceLookup: "OnlineMeetings.Read"}},
		{"delegated: no scopes", attendanceTestToken(`{"scp":""}`), map[string]string{TeamsAttendanceLookup: "OnlineMeetings.Read", TeamsAttendanceReports: "OnlineMeetingArtifact.Read.All"}},
	}
	for _, c := range cases {
		got := TeamsAttendanceNotConsented(c.token)
		if (got == nil) != (c.want == nil) || len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
		for req, perm := range c.want {
			if got[req] != perm {
				t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
			}
		}
	}
}

// getAllRetainedMessages is application-only: ChannelMessage.Read.All for channels, Chat.Read.All
// (or Chat.ReadWrite.All) for chats. A delegated or unreadable token never counts as consented,
// and the history roles don't change TeamsFeatureRoles.
func TestTeamsHistoryNeedsApplicationRoles(t *testing.T) {
	ch, chat := TeamsFeatureChannelHistory, TeamsFeatureChatHistory
	cases := []struct {
		name, token string
		denied      []string
	}{
		{"opaque token", "opaque", []string{ch, chat}},
		{"app: both", attendanceTestToken(`{"roles":["ChannelMessage.Read.All","Chat.Read.All"]}`), nil},
		{"app: higher chat role", attendanceTestToken(`{"roles":["ChannelMessage.Read.All","Chat.ReadWrite.All"]}`), nil},
		{"app: channels only", attendanceTestToken(`{"roles":["ChannelMessage.Read.All"]}`), []string{chat}},
		{"app: chats only", attendanceTestToken(`{"roles":["Chat.Read.All"]}`), []string{ch}},
		{"delegated", attendanceTestToken(`{"scp":"ChannelMessage.Read.All Chat.Read"}`), []string{ch, chat}},
	}
	for _, c := range cases {
		got := TeamsHistoryNotConsented(c.token)
		if len(got) != len(c.denied) {
			t.Fatalf("%s: got %v, want %v denied", c.name, got, c.denied)
		}
		for _, f := range c.denied {
			if got[f] == "" {
				t.Fatalf("%s: %s must be denied with a reason, got %v", c.name, f, got)
			}
		}
	}
	if _, ok := TeamsFeatureRoles[ch]; ok {
		t.Fatal("history must not add consent requirements")
	}
	if _, ok := TeamsFeatureRoles[chat]; ok {
		t.Fatal("history must not add consent requirements")
	}
}

func TestCallRecordsDoesNotCoverPstnPermission(t *testing.T) {
	cases := []struct {
		name     string
		granted  []string
		feature  string
		wantMiss string
	}{
		{"call records with its role", []string{"CallRecords.Read.All"}, TeamsFeatureCallRecords, ""},
		{"pstn needs its own role", []string{"CallRecords.Read.All"}, TeamsFeaturePSTN, "CallRecord-PstnCalls.Read.All"},
		{"direct routing needs its own role", []string{"CallRecords.Read.All"}, TeamsFeatureDirectRouting, "CallRecord-PstnCalls.Read.All"},
		{"pstn granted", []string{"CallRecord-PstnCalls.Read.All"}, TeamsFeaturePSTN, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TeamsFeaturesNotConsented(tc.granted)[tc.feature]
			if got != tc.wantMiss {
				t.Fatalf("got %q want %q", got, tc.wantMiss)
			}
		})
	}
}

func TestDelegatedTokenCannotConsentCallRecords(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantLen int
	}{
		{"opaque", "opaque", 0},
		{"app with call records", attendanceTestToken(`{"roles":["CallRecords.Read.All"]}`), 0},
		{"delegated user", attendanceTestToken(`{"scp":"User.Read Calendars.Read CallRecords.Read.All"}`), 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TeamsApplicationOnlyNotConsented(tc.token)
			if len(got) != tc.wantLen {
				t.Fatalf("got %v want %d features", got, tc.wantLen)
			}
			if tc.wantLen > 0 && got[TeamsFeatureCallRecords] != "CallRecords.Read.All" {
				t.Fatalf("call records: %v", got)
			}
		})
	}
}
