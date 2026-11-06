package outlook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeMicrosoftUserServiceAccess(t *testing.T) {
	prev := graphBaseURL
	t.Cleanup(func() { graphBaseURL = prev })

	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus string
		wantReason string
	}{
		{
			name:       "success",
			status:     200,
			body:       `{"value":[{"id":"1"}]}`,
			wantStatus: AccessProbeAccessible,
		},
		{
			name:       "mailbox missing",
			status:     404,
			body:       `{"error":{"code":"MailboxNotEnabledForRESTAPI","message":"The mailbox is either inactive, soft-deleted, or is hosted on-premises."}}`,
			wantStatus: AccessProbeNoAccess,
			wantReason: AccessReasonMissingResource,
		},
		{
			name:       "access denied",
			status:     403,
			body:       `{"error":{"code":"ErrorAccessDenied","message":"Access is denied."}}`,
			wantStatus: AccessProbeNoAccess,
			wantReason: AccessReasonDenied,
		},
		{
			name:       "throttled",
			status:     429,
			body:       `{"error":{"code":"TooManyRequests","message":"slow down"}}`,
			wantStatus: AccessProbeError,
			wantReason: AccessReasonProbeFailed,
		},
		{
			name:       "server error",
			status:     503,
			body:       `{"error":{"code":"ServiceUnavailable","message":"busy"}}`,
			wantStatus: AccessProbeError,
			wantReason: AccessReasonProbeFailed,
		},
		{
			name:       "auth error inconclusive",
			status:     401,
			body:       `{"error":{"code":"InvalidAuthenticationToken","message":"token expired"}}`,
			wantStatus: AccessProbeError,
			wantReason: AccessReasonProbeFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/users/") {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			graphBaseURL = srv.URL

			got := ProbeMicrosoftUserServiceAccess(context.Background(), "tok", "user-1", "outlook")
			if got.Status != tc.wantStatus {
				t.Fatalf("status=%s want %s (msg=%q code=%q)", got.Status, tc.wantStatus, got.Message, got.GraphCode)
			}
			if tc.wantReason != "" && got.ReasonCode != tc.wantReason {
				t.Fatalf("reason=%s want %s", got.ReasonCode, tc.wantReason)
			}
			if got.CheckedAt.IsZero() {
				t.Fatal("checked_at required")
			}
			if tc.wantStatus == AccessProbeNoAccess && strings.TrimSpace(got.Message) == "" {
				t.Fatal("no_access requires customer message")
			}
		})
	}
}

func TestProbeMicrosoftUserServiceAccessUnsupportedMethod(t *testing.T) {
	got := ProbeMicrosoftUserServiceAccess(context.Background(), "tok", "user-1", "outlook_teams")
	if got.Status != AccessProbeError {
		t.Fatalf("status=%s want probe_error", got.Status)
	}
}

func TestParseGraphError(t *testing.T) {
	code, msg := parseGraphError([]byte(`{"error":{"code":"MailboxNotEnabledForRESTAPI","message":"no mailbox"}}`))
	if code != "MailboxNotEnabledForRESTAPI" || msg != "no mailbox" {
		t.Fatalf("code=%q msg=%q", code, msg)
	}
	raw, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "X"}})
	code, msg = parseGraphError(raw)
	if code != "X" || msg != "" {
		t.Fatalf("code=%q msg=%q", code, msg)
	}
}
