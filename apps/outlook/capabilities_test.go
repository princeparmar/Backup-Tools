package outlook

import (
	"context"
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
