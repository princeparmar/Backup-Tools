package restore

import (
	"context"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestInviteOAuthReadinessDoesNotAskForDelegation(t *testing.T) {
	out := &ReadinessResult{Service: "gmail", LoginID: "support@salestalker.com"}
	got, err := evaluateInviteOAuthReadiness(context.Background(), out, "gmail", out.LoginID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready {
		t.Fatal("empty Google sign-in should not be ready")
	}
	if got.Reason != ReadinessReasonInviteOAuthRequired {
		t.Fatalf("reason = %q", got.Reason)
	}
	if strings.Contains(strings.ToLower(got.Message), "authorize the gmail restore scope") {
		t.Fatalf("message = %q", got.Message)
	}
	if strings.Contains(got.Message, "Admin Console") {
		t.Fatalf("message = %q", got.Message)
	}
}

func TestApplyInviteRestoreAuth(t *testing.T) {
	cases := []struct {
		name         string
		tag          string
		jobID        uint
		storeToken   string
		startRefresh string
		wantToken    string
		wantRefresh  string
		wantMode     string
	}{
		{
			name:         "invite tag plus member token uses that Google sign-in",
			tag:          "invite_restored",
			jobID:        11,
			storeToken:   "member-access",
			startRefresh: "owner-refresh",
			wantToken:    "member-access",
			wantRefresh:  "",
			wantMode:     RestoreAuthModeOAuth,
		},
		{
			name:         "invite tag without token keeps owner oauth for history tag",
			tag:          "invite_restored",
			jobID:        12,
			startRefresh: "owner-refresh",
			wantToken:    "",
			wantRefresh:  "owner-refresh",
			wantMode:     "",
		},
		{
			name:         "owner restore is unchanged",
			tag:          "",
			jobID:        13,
			startRefresh: "owner-refresh",
			wantToken:    "",
			wantRefresh:  "owner-refresh",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ForgetInviteAccessToken(tc.jobID)
			if tc.storeToken != "" {
				RememberInviteAccessToken(tc.jobID, tc.storeToken)
			}
			deps := &RestoreDeps{RefreshToken: tc.startRefresh}
			job := &repo.RestoreJobListingDB{RestoreTag: tc.tag}
			job.ID = tc.jobID
			applyInviteRestoreAuth(deps, job)
			if deps.GoogleToken != tc.wantToken {
				t.Fatalf("GoogleToken = %q, want %q", deps.GoogleToken, tc.wantToken)
			}
			if deps.RefreshToken != tc.wantRefresh {
				t.Fatalf("RefreshToken = %q, want %q", deps.RefreshToken, tc.wantRefresh)
			}
			if tc.wantMode != "" && deps.AuthMode != tc.wantMode {
				t.Fatalf("AuthMode = %q, want %q", deps.AuthMode, tc.wantMode)
			}
			ForgetInviteAccessToken(tc.jobID)
		})
	}
}

func TestInviteRestoreNeedsMailboxSignIn(t *testing.T) {
	cases := []struct {
		name     string
		authMode string
		want     bool
	}{
		{name: "personal oauth uses owner token", authMode: RestoreAuthModeOAuth, want: false},
		{name: "empty mode uses owner token", authMode: "", want: false},
		{name: "workspace dwd still needs mailbox sign-in", authMode: RestoreAuthModeDWD, want: true},
		{name: "dwd is case-insensitive", authMode: "DWD", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InviteRestoreNeedsMailboxSignIn(tc.authMode); got != tc.want {
				t.Fatalf("InviteRestoreNeedsMailboxSignIn(%q) = %v, want %v", tc.authMode, got, tc.want)
			}
		})
	}
}
