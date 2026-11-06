package handler

import (
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

func sharePointManualFileKey(tenant string) string {
	return outlook.SharePointIDBasedDataKey(outlook.ResourceKeyPrefix(tenant, "site", "site-1"), "item-1", "file.docx", "2026-10-01T10:00:00Z")
}

func callMicrosoftManualRestore(t *testing.T, handle echo.HandlerFunc, token, headerTenant string, keys []string) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(keys)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("ACCESS_TOKEN", "grant")
	req.Header.Set("Authorization", "Bearer "+token)
	if headerTenant != "" {
		req.Header.Set("MICROSOFT_TENANT_ID", headerTenant)
	}
	rec := httptest.NewRecorder()
	if err := handle(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return rec.Code, rec.Body.String()
}

func identityCheck(t *testing.T, token, headerTenant string, keys []string) (string, int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if headerTenant != "" {
		req.Header.Set("MICROSOFT_TENANT_ID", headerTenant)
	}
	tenant, err := checkMicrosoftManualRestoreKeys(echo.New().NewContext(req, httptest.NewRecorder()), token, keys)
	if err == nil {
		return tenant, 0, ""
	}
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("want HTTPError, got %T %v", err, err)
	}
	return "", he.Code, fmt.Sprint(he.Message)
}

func TestMicrosoftManualRestoreIdentity(t *testing.T) {
	work := teamsHandlerTenant
	other := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	teamsKey := teamsHandlerChannelKey(work, "100")
	sharePointKey := sharePointManualFileKey(work)
	personal := teamsHandlerJWT(t, map[string]interface{}{
		"tid": outlook.MSATenantID, "oid": "personal-1", "idtyp": "user",
		"scp":                "ChannelMessage.Send ChatMessage.Send Files.ReadWrite.All Sites.ReadWrite.All",
		"preferred_username": "bob@outlook.com",
	})
	alice := teamsHandlerJWT(t, map[string]interface{}{
		"tid": work, "oid": "alice-1", "idtyp": "user",
		"scp":                "ChannelMessage.Send ChatMessage.Send Files.ReadWrite.All Sites.ReadWrite.All",
		"preferred_username": "alice@contoso.com",
	})
	otherEmployee := teamsHandlerJWT(t, map[string]interface{}{
		"tid": work, "oid": "bob-1", "idtyp": "user",
		"scp":                "ChannelMessage.Send ChatMessage.Send Files.ReadWrite.All Sites.ReadWrite.All",
		"preferred_username": "bob@contoso.com",
	})
	otherTenant := teamsHandlerJWT(t, map[string]interface{}{
		"tid": other, "oid": "other-1", "idtyp": "user",
		"scp":                "ChannelMessage.Send ChatMessage.Send Files.ReadWrite.All Sites.ReadWrite.All",
		"preferred_username": "erin@fabrikam.com",
	})

	cases := []struct {
		name         string
		token        string
		headerTenant string
		keys         []string
		wantTenant   string
		wantCode     int
		wantMsg      string
	}{
		{"Teams personal Microsoft account", personal, work, []string{teamsKey}, "", http.StatusForbidden, msrestore.ManualRestorePersonalAccount},
		{"SharePoint personal Microsoft account", personal, work, []string{sharePointKey}, "", http.StatusForbidden, msrestore.ManualRestorePersonalAccount},
		{"Teams user from another tenant", otherTenant, "", []string{teamsKey}, "", http.StatusForbidden, msrestore.ManualRestoreOtherOrganization},
		{"SharePoint user from another tenant", otherTenant, "", []string{sharePointKey}, "", http.StatusForbidden, msrestore.ManualRestoreOtherOrganization},
		{"Teams selected tenant is not the signed-in tenant", otherTenant, work, []string{teamsKey}, "", http.StatusForbidden, msrestore.ManualRestoreWrongSelectedOrg},
		{"SharePoint selected tenant is not the signed-in tenant", otherTenant, work, []string{sharePointKey}, "", http.StatusForbidden, msrestore.ManualRestoreWrongSelectedOrg},
		{"Teams same-tenant other Outlook employee is allowed", otherEmployee, work, []string{teamsKey}, work, 0, ""},
		{"SharePoint same-tenant other Outlook employee is allowed", otherEmployee, work, []string{sharePointKey}, work, 0, ""},
		{"Teams original employee is allowed", alice, work, []string{teamsKey}, work, 0, ""},
		{"SharePoint original employee is allowed", alice, work, []string{sharePointKey}, work, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTenant, gotCode, gotMsg := identityCheck(t, tc.token, tc.headerTenant, tc.keys)
			if gotTenant != tc.wantTenant || gotCode != tc.wantCode || gotMsg != tc.wantMsg {
				t.Fatalf("got tenant=%q code=%d msg=%q, want tenant=%q code=%d msg=%q",
					gotTenant, gotCode, gotMsg, tc.wantTenant, tc.wantCode, tc.wantMsg)
			}
		})
	}
}

func TestTeamsAndSharePointManualRestoreIdentityHTTP(t *testing.T) {
	work := teamsHandlerTenant
	other := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	teamsKey := teamsHandlerChannelKey(work, "100")
	sharePointKey := sharePointManualFileKey(work)
	personal := teamsHandlerJWT(t, map[string]interface{}{
		"tid": outlook.MSATenantID, "oid": "personal-1", "idtyp": "user",
		"scp": "ChannelMessage.Send ChatMessage.Send Files.ReadWrite.All Sites.ReadWrite.All",
	})
	otherTenant := teamsHandlerJWT(t, map[string]interface{}{
		"tid": other, "oid": "other-1", "idtyp": "user",
		"scp": "ChannelMessage.Send ChatMessage.Send Files.ReadWrite.All Sites.ReadWrite.All",
	})

	cases := []struct {
		name         string
		handle       echo.HandlerFunc
		token        string
		headerTenant string
		keys         []string
		code         int
		msg          string
	}{
		{"Teams personal account", HandleTeamsDownloadAndInsert, personal, work, []string{teamsKey}, http.StatusForbidden, "personal Microsoft account"},
		{"SharePoint personal account", HandleSharePointDownloadAndInsert, personal, work, []string{sharePointKey}, http.StatusForbidden, "personal Microsoft account"},
		{"Teams other tenant user", HandleTeamsDownloadAndInsert, otherTenant, "", []string{teamsKey}, http.StatusForbidden, "another Microsoft organization"},
		{"SharePoint other tenant user", HandleSharePointDownloadAndInsert, otherTenant, "", []string{sharePointKey}, http.StatusForbidden, "another Microsoft organization"},
		{"Teams chat personal account", HandleTeamsChatsDownloadAndInsert, personal, work, []string{teamsKey}, http.StatusForbidden, "personal Microsoft account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := callMicrosoftManualRestore(t, tc.handle, tc.token, tc.headerTenant, tc.keys)
			if code != tc.code || !strings.Contains(body, tc.msg) {
				t.Fatalf("got %d %s", code, body)
			}
			if strings.Contains(body, work) || strings.Contains(body, other) || strings.Contains(strings.ToLower(body), outlook.MSATenantID) {
				t.Fatalf("identity error should not dump tenant ids: %s", body)
			}
		})
	}
}

func TestMicrosoftManualRestoreActivityMessage(t *testing.T) {
	cases := []struct {
		name       string
		loginID    string
		phase      string
		itemCount  int
		result     *DownloadResult
		restoreErr error
		want       string
	}{
		{
			name:       "personal account denied",
			loginID:    "bob@outlook.com",
			phase:      "failed",
			restoreErr: echo.NewHTTPError(http.StatusForbidden, msrestore.ManualRestorePersonalAccount),
			want:       "Restore for bob@outlook.com failed: " + msrestore.ManualRestorePersonalAccount,
		},
		{
			name:       "other tenant denied",
			loginID:    "erin@fabrikam.com",
			phase:      "failed",
			restoreErr: echo.NewHTTPError(http.StatusForbidden, msrestore.ManualRestoreOtherOrganization),
			want:       "Restore for erin@fabrikam.com failed: " + msrestore.ManualRestoreOtherOrganization,
		},
		{
			name:   "Teams Graph 403 from another employee",
			phase:  "completed",
			result: &DownloadResult{ProcessedIDs: nil, FailedIDs: []string{"k1"}, Message: msrestore.ManualRestoreTeamsNoAccess},
			want:   "Restore for this Microsoft account completed. 0 succeeded, 1 failed. " + msrestore.ManualRestoreTeamsNoAccess,
		},
		{
			name:   "SharePoint Graph 403 from another employee",
			phase:  "completed",
			result: &DownloadResult{ProcessedIDs: nil, FailedIDs: []string{"k1"}, Message: msrestore.ManualRestoreSharePointNoAccess},
			want:   "Restore for this Microsoft account completed. 0 succeeded, 1 failed. " + msrestore.ManualRestoreSharePointNoAccess,
		},
		{
			name:   "SharePoint success stays generic",
			phase:  "completed",
			result: &DownloadResult{ProcessedIDs: []string{"k1"}, FailedIDs: nil, Message: "sharepoint restore processed"},
			want:   "Restore for this Microsoft account completed. 1 succeeded, 0 failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := microsoftManualRestoreActivityMessage(tc.loginID, tc.phase, tc.itemCount, tc.result, tc.restoreErr)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
