package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func teamsPostServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int32)) (*int32, *string) {
	t.Helper()
	var calls int32
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		lastPath = r.URL.EscapedPath()
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		handle(w, r, n)
	}))
	t.Cleanup(srv.Close)
	prev := graphBaseURL
	graphBaseURL = srv.URL
	t.Cleanup(func() { graphBaseURL = prev })
	return &calls, &lastPath
}

func TestTeamsPostReturnsIDAndBody(t *testing.T) {
	calls, path := teamsPostServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Body struct{ ContentType, Content string }
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body.Body.ContentType != "html" || body.Body.Content != "<p>hi</p>" {
			t.Errorf("payload %s", raw)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1700000000000"}`))
	})
	id, err := PostTeamsChannelMessageID(context.Background(), "tok", "team-1", "19:ch@thread.tacv2", "<p>hi</p>")
	if err != nil || id != "1700000000000" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if *path != "/teams/team-1/channels/19:ch@thread.tacv2/messages" || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("path %q calls %d", *path, atomic.LoadInt32(calls))
	}
	if err := PostTeamsChannelReply(context.Background(), "tok", "team-1", "19:ch@thread.tacv2", "1700000000000", "<p>hi</p>"); err != nil {
		t.Fatal(err)
	}
	if *path != "/teams/team-1/channels/19:ch@thread.tacv2/messages/1700000000000/replies" {
		t.Fatalf("reply path %q", *path)
	}
	if _, err := PostTeamsChatMessage(context.Background(), "tok", "19:chat@thread.v2", "<p>hi</p>"); err != nil {
		t.Fatal(err)
	}
	if *path != "/chats/19:chat@thread.v2/messages" {
		t.Fatalf("chat path %q", *path)
	}
}

func TestTeamsPostServerErrorsAreSentOnceAndUnknown(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"500", http.StatusInternalServerError},
		{"502", http.StatusBadGateway},
		{"504", http.StatusGatewayTimeout},
		{"503 without Retry-After", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, _ := teamsPostServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"code":"InternalServerError","message":"boom"}}`))
			})
			_, err := PostTeamsChannelMessageID(context.Background(), "tok", "team-1", "ch-1", "x")
			if !errors.Is(err, ErrTeamsPostOutcomeUnknown) {
				t.Fatalf("want unknown outcome, got %v", err)
			}
			var ge *TeamsGraphError
			if !errors.As(err, &ge) || ge.Status != tc.status {
				t.Fatalf("graph error kept: %v", err)
			}
			if n := atomic.LoadInt32(calls); n != 1 {
				t.Fatalf("a post with an unknown outcome is not sent again: %d calls", n)
			}
		})
	}
}

func TestTeamsPostRetriesThrottling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
	}{
		{"429", http.StatusTooManyRequests, ""},
		{"503 with Retry-After", http.StatusServiceUnavailable, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, _ := teamsPostServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
				if n == 1 {
					if tc.retryAfter != "" {
						w.Header().Set("Retry-After", tc.retryAfter)
					}
					w.WriteHeader(tc.status)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"m2"}`))
			})
			id, err := PostTeamsChatMessage(context.Background(), "tok", "19:chat@thread.v2", "x")
			if n := atomic.LoadInt32(calls); err != nil || id != "m2" || n != 2 {
				t.Fatalf("id=%q err=%v calls=%d", id, err, n)
			}
		})
	}
}

func TestTeamsPostClientErrorsAreDefinite(t *testing.T) {
	calls, _ := teamsPostServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"Forbidden","message":"not a member"}}`))
	})
	_, err := PostTeamsChatMessage(context.Background(), "tok", "19:chat@thread.v2", "x")
	var ge *TeamsGraphError
	if !errors.As(err, &ge) || ge.Status != http.StatusForbidden || errors.Is(err, ErrTeamsPostOutcomeUnknown) {
		t.Fatalf("want a definite 403, got %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("calls %d", n)
	}
}

func TestTeamsPostDroppedConnectionIsUnknown(t *testing.T) {
	calls, _ := teamsPostServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	})
	_, err := PostTeamsChannelMessageID(context.Background(), "tok", "team-1", "ch-1", "x")
	if !errors.Is(err, ErrTeamsPostOutcomeUnknown) {
		t.Fatalf("want unknown outcome, got %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("a dropped post is not sent again: %d calls", n)
	}
}

func TestIsDialError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, derr := net.Dial("tcp", addr)
	if derr == nil || !isDialError(derr) {
		t.Fatalf("refused connection: %v", derr)
	}
	if isDialError(io.ErrUnexpectedEOF) || isDialError(&net.OpError{Op: "read", Err: io.EOF}) {
		t.Fatal("errors after the connection opened are not dial errors")
	}
}

func TestTeamsRepostBodyIsOriginalMessageOnly(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"createdDateTime":"2026-10-27T08:05:00Z","from":{"user":{"displayName":"Allwyn John"}},"body":{"content":"Hi sales how are you doing"}}`)
	tests := []struct {
		name      string
		objectKey string
		inReplyTo string
	}{
		{name: "plain post", objectKey: "k1"},
		{name: "orphan reply", objectKey: "k2", inReplyTo: "300"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TeamsRepostBody(raw, tt.objectKey, tt.inReplyTo)
			if got != "Hi sales how are you doing" {
				t.Fatalf("body %q", got)
			}
			if strings.Contains(got, "Originally posted") || strings.Contains(got, "In reply to") {
				t.Fatalf("restore must not add extra notes: %q", got)
			}
		})
	}
}
