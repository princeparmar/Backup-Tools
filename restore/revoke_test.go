package restore

import (
	"errors"
	"testing"
)

func TestIsGoogleRefreshRevoked(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "invalid_grant", err: errors.New("token refresh failed: invalid_grant: Token has been expired or revoked."), want: true},
		{name: "expired or revoked", err: errors.New("token refresh failed: invalid_grant: Token has been expired or revoked."), want: true},
		{name: "missing token is not revoke", err: errors.New("Google refresh token is missing"), want: false},
		{name: "network timeout is not revoke", err: errors.New("error making HTTP request: context deadline exceeded (timeout)"), want: false},
		{name: "connection refused is not revoke", err: errors.New("error making HTTP request: connection refused"), want: false},
		{name: "google http 500 is not revoke", err: errors.New("token refresh failed: HTTP 500: internal error"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGoogleRefreshRevoked(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestShouldRecordGoogleRestoreRevoke(t *testing.T) {
	cases := []struct {
		name string
		out  *ReadinessResult
		want bool
	}{
		{name: "nil", out: nil, want: false},
		{name: "ready", out: &ReadinessResult{Ready: true, Reason: ReadinessReasonTokenRefreshFailed, Service: "contacts"}, want: false},
		{
			name: "contacts token refresh failed",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonTokenRefreshFailed, Service: "contacts", AuthMode: RestoreAuthModeOAuth, Message: restoreReadinessRefreshInvalid},
			want: true,
		},
		{
			name: "missing token is not revoke",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonTokenRefreshFailed, Service: "contacts", AuthMode: RestoreAuthModeOAuth, Message: restoreReadinessRefreshMissing},
			want: false,
		},
		{
			name: "missing scopes is not revoke",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonMissingPermissions, Service: "contacts"},
			want: false,
		},
		{
			name: "dwd is not stored-token revoke",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonTokenRefreshFailed, Service: "contacts", AuthMode: RestoreAuthModeDWD},
			want: false,
		},
		{
			name: "outlook is not google revoke",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonTokenRefreshFailed, Service: "outlook", AuthMode: RestoreAuthModeOAuth, Message: restoreReadinessMicrosoftRefreshInvalid},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRecordGoogleRestoreRevoke(tc.out); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestIsMicrosoftRefreshRevoked(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "invalid_grant", err: errors.New("microsoft token: invalid_grant"), want: true},
		{name: "aadsts50173", err: errors.New("AADSTS50173: The provided grant has expired due to it being revoked"), want: true},
		{name: "network timeout is not revoke", err: errors.New("error making HTTP request: context deadline exceeded (timeout)"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMicrosoftRefreshRevoked(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestShouldRecordMicrosoftRestoreRevoke(t *testing.T) {
	cases := []struct {
		name string
		out  *ReadinessResult
		want bool
	}{
		{name: "nil", out: nil, want: false},
		{name: "ready", out: &ReadinessResult{Ready: true, Reason: ReadinessReasonTokenRefreshFailed, Service: "outlook"}, want: false},
		{
			name: "outlook token refresh failed",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonTokenRefreshFailed, Service: "outlook", Message: restoreReadinessMicrosoftRefreshInvalid},
			want: true,
		},
		{
			name: "gmail is not microsoft revoke",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonTokenRefreshFailed, Service: "gmail", Message: restoreReadinessRefreshInvalid},
			want: false,
		},
		{
			name: "missing scopes is not revoke",
			out:  &ReadinessResult{Ready: false, Reason: ReadinessReasonMissingPermissions, Service: "outlook"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRecordMicrosoftRestoreRevoke(tc.out); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
