package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	msrestore "github.com/StorX2-0/Backup-Tools/restore/microsoft"
	"github.com/labstack/echo/v4"
)

func TestRestoreTenantUserEmails(t *testing.T) {
	got := restoreTenantUserEmails([]outlook.DirectoryUser{
		{UPN: "sales@contoso.onmicrosoft.com", Mail: "Sales@contoso.com", AccountEnabled: true},
		{UPN: "admin@contoso.onmicrosoft.com", AccountEnabled: true},
		{UPN: "off@contoso.com", Mail: "off@contoso.com", AccountEnabled: false},
		{UPN: "guest_gmail.com#EXT#@contoso.onmicrosoft.com", Mail: "guest@gmail.com", AccountEnabled: true},
		{UPN: "sales2@contoso.onmicrosoft.com", Mail: "sales@contoso.com", AccountEnabled: true},
	})
	want := []string{"admin@contoso.onmicrosoft.com", "Sales@contoso.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("emails = %v, want %v (enabled members only, deduplicated, sorted)", got, want)
	}
}

func TestHandleMicrosoftRestoreWorkspaces(t *testing.T) {
	prevTenants, prevUsers := listRestoreOrgTenantsFn, listRestoreTenantUsersFn
	t.Cleanup(func() { listRestoreOrgTenantsFn, listRestoreTenantUsersFn = prevTenants, prevUsers })
	listRestoreOrgTenantsFn = func(context.Context, *db.PostgresDb, string) ([]msrestore.OrganizationTenant, error) {
		return []msrestore.OrganizationTenant{{TenantID: "t1", Name: "Contoso", DefaultDomain: "contoso.com",
			Domains: []string{"contoso.onmicrosoft.com", "contoso.com"}, Token: "app-t1"}}, nil
	}
	listRestoreTenantUsersFn = func(_ context.Context, token string) ([]outlook.DirectoryUser, error) {
		if token != "app-t1" {
			t.Fatalf("token %q", token)
		}
		return []outlook.DirectoryUser{
			{UPN: "ann@contoso.com", Mail: "ann@contoso.com", AccountEnabled: true},
			{UPN: "bob@contoso.com", Mail: "bob@contoso.com", AccountEnabled: true},
		}, nil
	}

	call := func(query string) (int, map[string]json.RawMessage) {
		t.Helper()
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/restore/workspaces?"+query, nil), rec)
		if err := handleMicrosoftRestoreWorkspaces(c, nil, "u1", c.QueryParam("domain")); err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	code, body := call("")
	var tenants []restoreWorkspaceTenant
	_ = json.Unmarshal(body["tenants"], &tenants)
	if code != http.StatusOK || string(body["workspaces"]) != `["contoso.com"]` || len(tenants) != 1 || len(tenants[0].Domains) != 2 {
		t.Fatalf("tabs: %d %s", code, body)
	}

	code, body = call("domain=contoso.com&login_id=ann@contoso.com")
	if code != http.StatusOK || string(body["mailboxes"]) != `["bob@contoso.com"]` {
		t.Fatalf("mailboxes: %d %s", code, body)
	}

	if code, _ = call("domain=fabrikam.com"); code != http.StatusNotFound {
		t.Fatalf("unknown organization: %d", code)
	}

	prevSites := listRestoreSharePointSites
	t.Cleanup(func() { listRestoreSharePointSites = prevSites })
	listRestoreSharePointSites = func(_ context.Context, token, search string, limit int) ([]outlook.SharePointSiteSummary, error) {
		if token != "app-t1" || search != "legal" || limit != 0 {
			t.Fatalf("sites query token=%q search=%q limit=%d", token, search, limit)
		}
		return []outlook.SharePointSiteSummary{{
			ID: "site-1", DisplayName: "Legal", WebURL: "https://contoso.sharepoint.com/sites/legal",
		}}, nil
	}
	code, body = call("domain=contoso.com&service=outlook_sharepoint&search=legal")
	if code != http.StatusOK || string(body["sites"]) != `[{"id":"site-1","name":"Legal","web_url":"https://contoso.sharepoint.com/sites/legal"}]` {
		t.Fatalf("sites: %d %s", code, body)
	}
	code, body = call("domain=contoso.com")
	if code != http.StatusOK || string(body["mailboxes"]) != `["ann@contoso.com","bob@contoso.com"]` {
		t.Fatalf("mailbox list must stay unchanged without service: %d %s", code, body)
	}
}
