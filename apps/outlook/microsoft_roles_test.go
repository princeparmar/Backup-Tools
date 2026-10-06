package outlook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	globalAdminTemplateID   = "62e90394-69f5-4237-9190-012177145e10"
	exchangeAdminTemplateID = "29232cdf-9323-42fd-ade2-1d097af3e4de"
	defaultUserTemplateID   = "b79fbf4d-3ef9-4689-8143-76b194e85509"
)

type fakeGraph struct {
	memberOf       []directoryRoleRow
	memberOfStatus int
}

func (f *fakeGraph) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/me/memberOf/"):
			if f.memberOfStatus != 0 {
				w.WriteHeader(f.memberOfStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"value": f.memberOf})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	prev := graphBaseURL
	graphBaseURL = srv.URL
	t.Cleanup(func() {
		graphBaseURL = prev
		srv.Close()
	})
	return srv
}

func TestDetectEntraAdminRoles_fromWIDs(t *testing.T) {
	(&fakeGraph{memberOfStatus: http.StatusForbidden}).serve(t)
	token := fakeJWT(t, map[string]interface{}{"wids": []string{defaultUserTemplateID, exchangeAdminTemplateID}})

	roles := DetectEntraAdminRoles(context.Background(), token, "")
	if !roles.IsAdmin() {
		t.Fatal("Exchange Administrator in wids must set is_admin")
	}
	if len(roles.Names) != 1 || roles.Names[0] != "Exchange Administrator" {
		t.Fatalf("admin_roles = %v", roles.Names)
	}
}

// Global Admin signed in with only User.Read: the access token has no wids and memberOf is
// forbidden, so the id_token's wids claim is the only role source.
func TestDetectEntraAdminRoles_fromIDTokenWIDs(t *testing.T) {
	(&fakeGraph{memberOfStatus: http.StatusForbidden}).serve(t)
	access := fakeJWT(t, map[string]interface{}{"tid": "7820c47f-0b9d-41f9-8977-95630a2cda00"})
	idToken := fakeJWT(t, map[string]interface{}{"wids": []string{globalAdminTemplateID, defaultUserTemplateID}})

	roles := DetectEntraAdminRoles(context.Background(), access, idToken)
	if !roles.IsAdmin() || len(roles.Names) != 1 || roles.Names[0] != "Global Administrator" {
		t.Fatalf("roles = %+v, want Global Administrator", roles)
	}
}

func TestDetectEntraAdminRoles_memberOfFallbackAndUnion(t *testing.T) {
	(&fakeGraph{
		memberOf: []directoryRoleRow{
			{RoleTemplateID: globalAdminTemplateID, DisplayName: "Global Administrator"},
			{RoleTemplateID: "00000000-0000-0000-0000-000000000001", DisplayName: "Some Future Administrator"},
			{RoleTemplateID: "00000000-0000-0000-0000-000000000002", DisplayName: "Directory Readers"},
		},
	}).serve(t)
	token := fakeJWT(t, map[string]interface{}{"wids": []string{exchangeAdminTemplateID}})

	roles := DetectEntraAdminRoles(context.Background(), token, "")
	want := []string{"Exchange Administrator", "Global Administrator", "Some Future Administrator"}
	if strings.Join(roles.Names, ",") != strings.Join(want, ",") {
		t.Fatalf("admin_roles = %v, want %v", roles.Names, want)
	}
}

func TestDetectEntraAdminRoles_noAdminRole(t *testing.T) {
	(&fakeGraph{memberOf: []directoryRoleRow{{RoleTemplateID: "x", DisplayName: "Directory Readers"}}}).serve(t)
	token := fakeJWT(t, map[string]interface{}{"wids": []string{defaultUserTemplateID}})

	roles := DetectEntraAdminRoles(context.Background(), token, "")
	if roles.IsAdmin() || len(roles.Names) != 0 {
		t.Fatalf("expected no admin roles, got %+v", roles)
	}
}

func TestAuthTokenResponseForAccountDetection_requestsOpenIDScope(t *testing.T) {
	var gotScope string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotScope = r.PostForm.Get("scope")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at", "id_token": "it"})
	}))
	defer srv.Close()
	t.Setenv("OUTLOOK_CLIENT_ID", "client")
	t.Setenv("OUTLOOK_CLIENT_SECRET", "secret")
	prev := tokenEndpointURL
	tokenEndpointURL = srv.URL
	t.Cleanup(func() { tokenEndpointURL = prev })

	tok, err := AuthTokenResponseForAccountDetection("rt")
	if err != nil {
		t.Fatal(err)
	}
	if tok.IDToken != "it" {
		t.Fatalf("id_token = %q", tok.IDToken)
	}
	scopes, _ := url.QueryUnescape(gotScope)
	if !strings.Contains(" "+scopes+" ", " openid ") {
		t.Fatalf("scope %q must include openid", scopes)
	}
}
