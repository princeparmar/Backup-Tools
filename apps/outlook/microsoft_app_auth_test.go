package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func stubTokenEndpoint(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	prev := microsoftLoginBaseURL
	microsoftLoginBaseURL = srv.URL
	t.Setenv("OUTLOOK_CLIENT_ID", "platform-client")
	t.Setenv("OUTLOOK_CLIENT_SECRET", "platform-secret")
	t.Cleanup(func() {
		microsoftLoginBaseURL = prev
		srv.Close()
	})
}

func TestAppOnlyToken_cachesPerTenantAndParsesRoles(t *testing.T) {
	var calls int32
	tok := fakeJWT(t, map[string]interface{}{"roles": []string{"User.Read.All", "Mail.Read"}})
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "platform-client" {
			t.Errorf("unexpected form: %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": tok, "expires_in": 3600})
	})
	tenant := "cache-tenant-1"
	InvalidateAppOnlyToken(tenant)

	for i := 0; i < 3; i++ {
		got, roles, err := AppOnlyToken(context.Background(), tenant)
		if err != nil || got != tok {
			t.Fatalf("AppOnlyToken: tok=%q err=%v", got, err)
		}
		if !HasAllRoles(roles, "mail.read", "User.Read.All") {
			t.Fatalf("roles = %v", roles)
		}
	}
	if calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (cached)", calls)
	}

	InvalidateAppOnlyToken(tenant)
	if _, _, err := AppOnlyToken(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("after invalidate calls = %d, want 2", calls)
	}
}

func TestAppOnlyToken_classifiesErrors(t *testing.T) {
	status := http.StatusUnauthorized
	stubTokenEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": "bad secret"})
	})
	tenant := "err-tenant"
	InvalidateAppOnlyToken(tenant)

	_, _, err := AppOnlyToken(context.Background(), tenant)
	var tokErr *AppOnlyTokenError
	if !errors.As(err, &tokErr) || tokErr.Code != "invalid_client" || tokErr.Temporary() {
		t.Fatalf("err = %#v", err)
	}

	status = http.StatusServiceUnavailable
	_, _, err = AppOnlyToken(context.Background(), tenant)
	if !errors.As(err, &tokErr) || !tokErr.Temporary() {
		t.Fatalf("5xx must be temporary: %#v", err)
	}
}

func TestMissingRoles(t *testing.T) {
	missing := MissingRoles([]string{"Mail.Read"}, "Mail.Read", "User.Read.All")
	if len(missing) != 1 || missing[0] != "User.Read.All" {
		t.Fatalf("missing = %v", missing)
	}
}
