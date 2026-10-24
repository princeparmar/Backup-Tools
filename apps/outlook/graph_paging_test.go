package outlook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// listPagesServer serves a two-page Graph listing under /v1.0: the first request gets one row and
// next(base) as nextLink, a request carrying $skip or $skiptoken gets the last row.
func listPagesServer(t *testing.T, next func(base string) string) (base string, hits *[]string) {
	t.Helper()
	var requests []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		q := r.URL.Query()
		if q.Has("$skip") || q.Has("$skiptoken") {
			_, _ = w.Write([]byte(`{"value":[{"id":"2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"1"}],"@odata.nextLink":"` + next(srv.URL+"/v1.0") + `"}`))
	}))
	t.Cleanup(srv.Close)
	prev := graphBaseURL
	graphBaseURL = srv.URL + "/v1.0"
	t.Cleanup(func() { graphBaseURL = prev })
	return graphBaseURL, &requests
}

func foreignServer(t *testing.T) (string, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"value":[{"id":"stolen"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1.0", &hits
}

type idRow struct {
	ID string `json:"id"`
}

func TestGraphListAllAcceptsEquivalentNextLinks(t *testing.T) {
	cases := []struct{ name, first, next string }{
		{"me written as the user id",
			"/me/memberOf/microsoft.graph.directoryRole?$select=roleTemplateId",
			"/users/0f0f0f0f-0000-0000-0000-000000000001/memberOf/microsoft.graph.directoryRole?$select=roleTemplateId&$skiptoken=2"},
		{"function with parentheses",
			"/sites/getAllSites?$select=id",
			"/sites/microsoft.graph.getAllSites()?$select=id&$skiptoken=2"},
		{"odata key form",
			"/users/a@contoso.com/mailFolders?$select=totalItemCount&$top=250",
			"/users('a%40contoso.com')/mailFolders?$select=totalItemCount&$top=250&$skip=250"},
		{"same path", "/users?$top=999", "/users?$top=999&$skiptoken=2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, hits := listPagesServer(t, func(base string) string { return base + c.next })
			rows, err := graphListAll[idRow](context.Background(), "tok", base+c.first, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[1].ID != "2" || len(*hits) != 2 {
				t.Fatalf("rows %+v hits %v", rows, *hits)
			}
		})
	}
}

func TestGraphListAllRejectsBadNextLinks(t *testing.T) {
	evil, evilHits := foreignServer(t)
	const first = "/users/u1/mailFolders?$top=250"
	cases := map[string]func(base string) string{
		"foreign host":   func(string) string { return evil + "/users/u1/mailFolders?$skip=250" },
		"other resource": func(base string) string { return base + "/groups?$skiptoken=2" },
		"other user":     func(base string) string { return base + "/users/u2/mailFolders?$skip=250" },
		"repeated":       func(base string) string { return base + first },
		"malformed":      func(string) string { return "::not a url" },
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			base, hits := listPagesServer(t, next)
			rows, err := graphListAll[idRow](context.Background(), "tok", base+first, 0)
			if !errors.Is(err, ErrTeamsPaging) {
				t.Fatalf("err %v", err)
			}
			if len(rows) != 1 || len(*hits) != 1 {
				t.Fatalf("rows %+v hits %v", rows, *hits)
			}
		})
	}
	if atomic.LoadInt32(evilHits) != 0 {
		t.Fatal("token sent to a foreign host")
	}
}

func TestListTenantSharePointSitesNeverFollowsForeignLinks(t *testing.T) {
	evil, evilHits := foreignServer(t)
	site := func(id string) string {
		return `{"id":"` + id + `","displayName":"Site ` + id + `","webUrl":"https://contoso.sharepoint.com/sites/` + id + `"}`
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getAllSites"):
			_, _ = w.Write([]byte(`{"value":[` + site("a") + `],"@odata.nextLink":"` + evil + `/sites/getAllSites?$skiptoken=2"}`))
		case r.URL.Query().Has("$skiptoken"):
			_, _ = w.Write([]byte(`{"value":[` + site("c") + `]}`))
		default:
			_, _ = w.Write([]byte(`{"value":[` + site("b") + `],"@odata.nextLink":"` + srv.URL + `/v1.0/sites?search=*&$skiptoken=2"}`))
		}
	}))
	t.Cleanup(srv.Close)
	prev := graphBaseURL
	graphBaseURL = srv.URL + "/v1.0"
	t.Cleanup(func() { graphBaseURL = prev })

	sites, err := ListTenantSharePointSites(context.Background(), "tok", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 || sites[0].ID != "b" || sites[1].ID != "c" {
		t.Fatalf("sites %+v", sites)
	}
	if atomic.LoadInt32(evilHits) != 0 {
		t.Fatal("token sent to a foreign host")
	}
}

func TestDriveDeltaGuardLinks(t *testing.T) {
	const g = "https://graph.microsoft.com/v1.0"
	cases := []struct {
		name, root, link string
		ok               bool
	}{
		{"user drive, same form", g + "/users/u1/drive", g + "/users/u1/drive/root/delta?token=a", true},
		{"user drive answered as drives", g + "/users/u1/drive", g + "/drives/b!x/root/delta?token=a", true},
		{"token in the path", g + "/users/u1/drive", g + "/drives('b!x')/root/delta(token='a,b=')", true},
		{"cast form", g + "/drives/b!x", g + "/drives/b!x/root/microsoft.graph.delta(token='a')", true},
		{"upn answered as id", g + "/users/a@contoso.com/drive", g + "/users/0f0f/drive/root/delta?token=a", true},
		{"me answered as user", g + "/me/drive", g + "/users/0f0f/drive/root/delta?token=a", true},
		{"group drive", g + "/groups/g1/drive", g + "/groups/g1/drive/root/delta?token=a", true},
		{"library", g + "/drives/b!lib", g + "/drives/b!lib/root/delta?token=a", true},

		{"foreign host", g + "/drives/b!lib", "https://evil.example.com/v1.0/drives/b!lib/root/delta?token=a", false},
		{"plain http", g + "/drives/b!lib", "http://graph.microsoft.com/v1.0/drives/b!lib/root/delta?token=a", false},
		{"other version", g + "/drives/b!lib", "https://graph.microsoft.com/beta/drives/b!lib/root/delta?token=a", false},
		{"other library", g + "/drives/b!lib", g + "/drives/b!other/root/delta?token=a", false},
		{"library answered as user", g + "/drives/b!lib", g + "/users/u1/drive/root/delta?token=a", false},
		{"other user", g + "/users/u1/drive", g + "/users/u2/drive/root/delta?token=a", false},
		{"group for user", g + "/users/u1/drive", g + "/groups/g1/drive/root/delta?token=a", false},
		{"folder delta", g + "/drives/b!lib", g + "/drives/b!lib/items/F1/delta?token=a", false},
		{"not a delta", g + "/drives/b!lib", g + "/drives/b!lib/root/children?$skiptoken=a", false},
		{"mail delta", g + "/users/u1/drive", g + "/users/u1/messages/delta?token=a", false},
		{"userinfo", g + "/drives/b!lib", "https://x@graph.microsoft.com/v1.0/drives/b!lib/root/delta", false},
		{"relative", g + "/drives/b!lib", "/drives/b!lib/root/delta?token=a", false},
		{"dot segment", g + "/drives/b!lib", g + "/drives/b!lib/../b!other/root/delta", false},
		{"root not on graph", "https://evil.example.com/v1.0/drives/b!lib", "https://evil.example.com/v1.0/drives/b!lib/root/delta", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := NewDriveDeltaGuard(c.root).Next(c.link)
			if c.ok != (err == nil) {
				t.Fatalf("Next(%s) = %v", c.link, err)
			}
			if err != nil && !errors.Is(err, ErrOneDriveDeltaPaging) {
				t.Fatalf("error kind %v", err)
			}
		})
	}
}

func TestDriveDeltaGuardRound(t *testing.T) {
	const root = "https://graph.microsoft.com/v1.0/users/u1/drive"
	g := NewDriveDeltaGuard(root)
	if err := g.Start(OneDriveInitialDeltaURL(root)); err != nil {
		t.Fatal(err)
	}
	if err := g.Next("https://graph.microsoft.com/v1.0/drives/b!x/root/delta?token=1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Next("https://graph.microsoft.com/v1.0/drives/b!y/root/delta?token=2"); !errors.Is(err, ErrOneDriveDeltaPaging) {
		t.Fatalf("switch to another drive mid-round: %v", err)
	}
	if err := g.Next("https://graph.microsoft.com/v1.0/drives/b!x/root/delta?token=1"); !errors.Is(err, ErrOneDriveDeltaPaging) {
		t.Fatalf("repeated nextLink: %v", err)
	}
	if err := g.Next(OneDriveInitialDeltaURL(root)); !errors.Is(err, ErrOneDriveDeltaPaging) {
		t.Fatalf("loop back to the first URL: %v", err)
	}
	if err := g.Final("https://graph.microsoft.com/v1.0/drives/b!x/root/delta?token=end"); err != nil {
		t.Fatal(err)
	}
	if err := g.Final("https://graph.microsoft.com/v1.0/drives/b!y/root/delta?token=end"); !errors.Is(err, ErrOneDriveDeltaPaging) {
		t.Fatalf("deltaLink to another drive: %v", err)
	}

	capped := NewDriveDeltaGuard(root)
	capped.Max = 1
	if err := capped.Next(root + "/root/delta?token=1"); err != nil {
		t.Fatal(err)
	}
	if err := capped.Next(root + "/root/delta?token=2"); !errors.Is(err, ErrOneDriveDeltaPaging) {
		t.Fatalf("page cap: %v", err)
	}
}
