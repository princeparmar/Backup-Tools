package outlook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const globalAdminTemplateID = "62e90394-69f5-4237-9190-012177145e10"

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
