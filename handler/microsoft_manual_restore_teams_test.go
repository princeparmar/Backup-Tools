package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	msrestore "github.com/StorX2-0/Backup-Tools/restore/microsoft"
	"github.com/labstack/echo/v4"
)

const teamsHandlerTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func teamsHandlerJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func teamsHandlerChannelKey(tenant, id string) string {
	return outlook.TeamsChannelMessageKey(outlook.TeamsTeamPrefix(tenant, "team-1"), "19:ch@thread.tacv2", id, id, "2026-10-01T10:00:00Z")
}

func callTeamsRestore(t *testing.T, handle echo.HandlerFunc, token string, keys []string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(keys)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("ACCESS_TOKEN", "grant")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	if err := handle(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return rec.Code, rec.Body.String()
}

// Every case below is refused before the handler reads keys from the vault, calls /me or writes
// to Graph.
func TestTeamsManualRestoreRefusesBeforeAnyWrite(t *testing.T) {
	appOnly := teamsHandlerJWT(t, map[string]interface{}{"tid": teamsHandlerTenant, "idtyp": "app", "roles": []string{"ChannelMessage.Read.All"}})
	rolesOnly := teamsHandlerJWT(t, map[string]interface{}{"tid": teamsHandlerTenant, "roles": []string{"Teamwork.Migrate.All"}})
	channelOnly := teamsHandlerJWT(t, map[string]interface{}{"tid": teamsHandlerTenant, "idtyp": "user", "scp": "ChannelMessage.Send User.Read"})
	delegated := teamsHandlerJWT(t, map[string]interface{}{"tid": teamsHandlerTenant, "idtyp": "user", "scp": "ChannelMessage.Send ChatMessage.Send"})
	key := teamsHandlerChannelKey(teamsHandlerTenant, "100")
	foreign := teamsHandlerChannelKey("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "100")

	cases := []struct {
		name   string
		handle echo.HandlerFunc
		token  string
		keys   []string
		code   int
		msg    string
	}{
		{"app-only token, channel endpoint", HandleTeamsDownloadAndInsert, appOnly, []string{key}, http.StatusForbidden, "organization connections"},
		{"app-only token, chat endpoint", HandleTeamsChatsDownloadAndInsert, appOnly, []string{key}, http.StatusForbidden, "organization connections"},
		{"roles without scp, channel endpoint", HandleTeamsDownloadAndInsert, rolesOnly, []string{key}, http.StatusForbidden, "organization connections"},
		{"delegated token without ChatMessage.Send", HandleTeamsChatsDownloadAndInsert, channelOnly, []string{key}, http.StatusForbidden, "ChatMessage.Send"},
		{"key of another tenant", HandleTeamsDownloadAndInsert, delegated, []string{key, foreign}, http.StatusForbidden, "belongs to tenant"},
		{"key that is not tenant scoped", HandleTeamsDownloadAndInsert, delegated, []string{"teams/old-layout/100.json"}, http.StatusBadRequest, "not a tenant-scoped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callTeamsRestore(t, tc.handle, tc.token, tc.keys)
			if code != tc.code || !strings.Contains(body, tc.msg) {
				t.Fatalf("got %d %s", code, body)
			}
		})
	}
}

func TestRestoreTeamsManualKeysReportsEachKey(t *testing.T) {
	delegated := teamsHandlerJWT(t, map[string]interface{}{"tid": teamsHandlerTenant, "idtyp": "user", "scp": "ChannelMessage.Send ChatMessage.Send"})
	deleted := teamsHandlerChannelKey(teamsHandlerTenant, "100")
	system := teamsHandlerChannelKey(teamsHandlerTenant, "101")
	foreign := teamsHandlerChannelKey("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "102")
	chat := outlook.TeamsChatMessageKey(outlook.TeamsChatPrefix(teamsHandlerTenant, "19:chat@thread.v2"), "400", "2026-10-01T11:00:00Z")
	meeting := outlook.TeamsMeetingEventKey(outlook.TeamsMeetingPrefix(teamsHandlerTenant, "p-abc"))
	invalid := teamsHandlerTenant + "/team/team-1/not-a-teams-key"
	stored := func(id, messageType, extra string) string {
		return fmt.Sprintf(`{"id":%q,"messageType":%q,"body":{"content":""},`+
			`"channelIdentity":{"teamId":"team-1","channelId":"19:ch@thread.tacv2"},`+
			`"storx_backup":{"tenant_id":%q,"team_id":"team-1","channel_id":"19:ch@thread.tacv2"}%s}`, id, messageType, teamsHandlerTenant, extra)
	}
	objects := map[string]string{
		deleted: stored("100", "message", `,"deletedDateTime":"2026-10-02T00:00:00Z"`),
		system:  stored("101", "systemEventMessage", ""),
	}
	session := &msrestore.TeamsRestoreSession{AccessGrant: "grant", Token: delegated, TenantID: teamsHandlerTenant,
		Download: func(_ context.Context, key string) ([]byte, error) {
			raw, ok := objects[key]
			if !ok {
				t.Errorf("unexpected download of %s", key)
				return nil, fmt.Errorf("not found")
			}
			return []byte(raw), nil
		}}
	result := restoreTeamsManualKeys(context.Background(), session, []string{deleted, system, foreign, chat, meeting, invalid}, outlook.TeamsKindChannelMessage)

	if len(result.ProcessedIDs) != 0 {
		t.Fatalf("nothing here is written: %+v", result.ProcessedIDs)
	}
	if strings.Join(result.SkippedIDs, ",") != deleted {
		t.Fatalf("skipped: %v", result.SkippedIDs)
	}
	if len(result.FailedIDs) != 5 || len(result.Items) != 6 {
		t.Fatalf("failed %v items %+v", result.FailedIDs, result.Items)
	}
	want := map[string]string{
		deleted: msrestore.TeamsSkipped, system: msrestore.TeamsUnsupported, foreign: msrestore.TeamsFailed,
		chat: msrestore.TeamsUnsupported, meeting: msrestore.TeamsUnsupported, invalid: msrestore.TeamsFailed,
	}
	for _, it := range result.Items {
		if it.Status != want[it.Key] {
			t.Fatalf("%s: got %s (%s), want %s", it.Key, it.Status, it.Detail, want[it.Key])
		}
		if it.Detail == "" {
			t.Fatalf("%s: every outcome explains itself", it.Key)
		}
	}
	if result.Message != teamsRepostNotice {
		t.Fatalf("notice: %q", result.Message)
	}
}
