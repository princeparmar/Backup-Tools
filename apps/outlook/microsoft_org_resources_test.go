package outlook

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestListTenantUserRoles(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/directoryRoles"):
			_, _ = w.Write([]byte(`{"value":[
				{"id":"r1","displayName":"Global Administrator"},
				{"id":"r2","displayName":"Exchange Administrator"}
			]}`))
		case strings.Contains(r.URL.Path, "/directoryRoles/r1/members"):
			_, _ = w.Write([]byte(`{"value":[{"@odata.type":"#microsoft.graph.user","id":"admin-oid"},{"@odata.type":"#microsoft.graph.servicePrincipal","id":"sp"}]}`))
		case strings.Contains(r.URL.Path, "/directoryRoles/r2/members"):
			_, _ = w.Write([]byte(`{"value":[{"@odata.type":"#microsoft.graph.user","id":"admin-oid"},{"@odata.type":"#microsoft.graph.user","id":"exch-oid"}]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	roles, err := ListTenantUserRoles(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles["admin-oid"]; len(got) != 2 || got[0] != "Exchange Administrator" || got[1] != "Global Administrator" {
		t.Fatalf("admin roles = %v", got)
	}
	if got := roles["exch-oid"]; len(got) != 1 || len(roles) != 2 {
		t.Fatalf("roles = %v", roles)
	}

	g.serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if _, err := ListTenantUserRoles(context.Background(), "tok"); !errors.Is(err, ErrDirectoryRolesForbidden) {
		t.Fatalf("want ErrDirectoryRolesForbidden, got %v", err)
	}
}

func TestListTenantDirectoryUsers_followsNextLinkAndMaps(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"value":[{"id":"u2","userPrincipalName":"bob@contoso.com","department":"Sales"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[
			{"id":"u1","displayName":"Ann","mail":"ann@contoso.com","accountEnabled":false,"assignedLicenses":[{"skuId":"s"}],"createdDateTime":"2026-01-02T03:04:05Z"},
			{"id":""}
		],"@odata.nextLink":"` + graphBaseURL + `/users?page=2"}`))
	})
	users, err := ListTenantDirectoryUsers(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %+v", users)
	}
	ann, bob := users[0], users[1]
	if ann.Email() != "ann@contoso.com" || ann.AccountEnabled || !ann.HasLicense || ann.CreatedAt == nil || ann.OrgUnitPath() != "/" {
		t.Fatalf("ann = %+v", ann)
	}
	if bob.Email() != "bob@contoso.com" || !bob.AccountEnabled || bob.HasLicense || bob.OrgUnitPath() != "/Sales" {
		t.Fatalf("bob = %+v", bob)
	}
}

func TestListTenantTeams_followsNextLink(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"value":[{"id":"t2","displayName":"Two"}]}`))
			return
		}
		if !strings.Contains(r.URL.RawQuery, "resourceProvisioningOptions") {
			t.Errorf("teams filter missing: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"t1","displayName":"One"}],"@odata.nextLink":"` + graphBaseURL + `/groups?page=2"}`))
	})
	teams, err := ListTenantTeams(context.Background(), "tok", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 2 || teams[1].ID != "t2" {
		t.Fatalf("teams = %+v", teams)
	}
	if g.called("/me/") {
		t.Fatal("tenant listing must not use /me")
	}
}

func TestListTenantSharePointSites_hidesSystemSites(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[
			{"id":"root","displayName":"Communication site","webUrl":"https://contoso.sharepoint.com/"},
			{"id":"sales","displayName":"sales","webUrl":"https://contoso.sharepoint.com/sites/sales"},
			{"id":"searchteam","displayName":"Search Team","webUrl":"https://contoso.sharepoint.com/sites/searchteam"},
			{"id":"unnamed","webUrl":"https://contoso.sharepoint.com/sites/x"},
			{"id":"csp","displayName":"CSP","webUrl":"https://contoso.sharepoint.com/contentstorage/CSP_1"},
			{"id":"search","displayName":"Search","webUrl":"https://contoso.sharepoint.com/search"},
			{"id":"my","displayName":"Ann","webUrl":"https://contoso-my.sharepoint.com/personal/ann"},
			{"id":"apps","displayName":"Apps","webUrl":"https://contoso.sharepoint.com/sites/appcatalog"},
			{"id":"cth","displayName":"Team Site","webUrl":"https://contoso.sharepoint.com/sites/contentTypeHub"}
		]}`))
	})
	sites, err := ListTenantSharePointSites(context.Background(), "tok", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sites {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "root,sales,searchteam" {
		t.Fatalf("sites = %s", got)
	}
}

func TestFetchSiteDefaultDriveID_noDocumentLibrary(t *testing.T) {
	g := &capabilityGraph{}
	g.serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/drives") {
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"itemNotFound"}}`))
	})
	_, err := fetchSiteDefaultDriveID(context.Background(), "tok", "contoso.sharepoint.com,a,b")
	if !errors.Is(err, ErrSharePointSiteNoDocumentLibrary) {
		t.Fatalf("err = %v, want ErrSharePointSiteNoDocumentLibrary", err)
	}
}
