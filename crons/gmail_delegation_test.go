package crons

import "testing"

func TestGmailUseDelegationOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		refreshToken    string
		needsDelegation bool
		canDelegate     bool
		want            bool
	}{
		{name: "employee with revoked admin token still uses delegation", refreshToken: "revoked-token", needsDelegation: true, canDelegate: true, want: true},
		{name: "admin own mailbox keeps the refresh token", refreshToken: "admin-token", needsDelegation: false, canDelegate: true, want: false},
		{name: "employee without delegation still needs the token", refreshToken: "admin-token", needsDelegation: true, canDelegate: false, want: false},
		{name: "no token and delegation available", refreshToken: "", needsDelegation: false, canDelegate: true, want: true},
		{name: "no token and delegation unavailable", refreshToken: "  ", needsDelegation: true, canDelegate: false, want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := gmailUseDelegationOnly(tt.refreshToken, tt.needsDelegation, tt.canDelegate)
			if got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
