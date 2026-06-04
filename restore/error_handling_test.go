package restore

import (
	"strings"
	"testing"
)

func TestClassifyRestoreError(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		errMsg       string
		wantKind     restoreErrorKind
		wantClearStx bool
		wantClearGo  bool
		wantJobMsg   string
	}{
		{
			name:       "storx missing",
			method:     "google_drive",
			errMsg:     "storx access grant not found for this job",
			wantKind:   restoreKindStorxMissing,
			wantClearStx: true,
			wantJobMsg: restoreJobStorxMissing,
		},
		{
			name:       "refresh token missing",
			method:     "gmail",
			errMsg:     "google refresh token missing",
			wantKind:   restoreKindGoogleAuth,
			wantClearGo: true,
			wantJobMsg: restoreJobGoogleAuth,
		},
		{
			name:       "invalid grant",
			method:     "google_photos",
			errMsg:     "google token refresh: oauth2: invalid_grant",
			wantKind:   restoreKindGoogleAuth,
			wantClearGo: true,
			wantJobMsg: restoreJobGoogleAuth,
		},
		{
			name:       "insufficient scope",
			method:     "google_calendar",
			errMsg:     "googleapi: insufficient authentication scopes",
			wantKind:   restoreKindGoogleInsufficient,
			wantClearGo: true,
			wantJobMsg: restoreJobGoogleInsufficientScope,
		},
		{
			name:       "storx uplink",
			method:     "gmail",
			errMsg:     "uplink: permission denied",
			wantKind:   restoreKindStorxUplink,
			wantClearStx: true,
			wantJobMsg: restoreJobStorxUplink,
		},
		{
			name:       "delegation",
			method:     "gmail",
			errMsg:     "oauth2: cannot fetch token: unauthorized_client",
			wantKind:   restoreKindGoogleDelegation,
			wantJobMsg: restoreJobDelegation,
		},
		{
			name:       "network",
			method:     "gmail",
			errMsg:     "tcp connector failed",
			wantKind:   restoreKindNetwork,
			wantJobMsg: restoreJobNetwork,
		},
		{
			name:       "generic",
			method:     "gmail",
			errMsg:     "something unexpected",
			wantKind:   restoreKindGeneric,
			wantJobMsg: restoreJobGeneric,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyRestoreError(tt.method, tt.errMsg)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.ClearStorx != tt.wantClearStx {
				t.Fatalf("ClearStorx = %v, want %v", got.ClearStorx, tt.wantClearStx)
			}
			if got.ClearGoogle != tt.wantClearGo {
				t.Fatalf("ClearGoogle = %v, want %v", got.ClearGoogle, tt.wantClearGo)
			}
			if got.JobMessage != tt.wantJobMsg {
				t.Fatalf("JobMessage = %q, want %q", got.JobMessage, tt.wantJobMsg)
			}
		})
	}
}

func TestGoogleAuthImmediateFailure(t *testing.T) {
	tests := []struct {
		name   string
		method string
		errMsg string
		want   bool
	}{
		{name: "gmail invalid grant", method: "gmail", errMsg: "invalid_grant", want: true},
		{name: "drive token exchange", method: "google_drive", errMsg: "error while generating auth token: revoked", want: true},
		{name: "outlook ignored", method: "outlook", errMsg: "invalid_grant", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := googleAuthImmediateFailure(tt.method, tt.errMsg); got != tt.want {
				t.Fatalf("googleAuthImmediateFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestoreBatchRetryMessage(t *testing.T) {
	tests := []struct {
		name    string
		attempt uint
		wantSub string
	}{
		{name: "attempt 1", attempt: 1, wantSub: "attempt 1 of 3"},
		{name: "attempt 2", attempt: 2, wantSub: "attempt 2 of 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restoreBatchRetryMessage(tt.attempt)
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("restoreBatchRetryMessage() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}
