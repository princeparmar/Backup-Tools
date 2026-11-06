package crons

import "testing"

func TestGoogleOAuthTokenExchangeFailure(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want bool
	}{
		{name: "invalid_grant", err: "token refresh failed: invalid_grant", want: true},
		{name: "contacts preflight", err: "error while generating auth token: invalid_grant", want: true},
		{name: "expired or revoked", err: "token refresh failed: Token has been expired or revoked.", want: true},
		{name: "storage quota is not oauth", err: "not enough CyberLS storage", want: false},
		{name: "network timeout is not oauth", err: "error making HTTP request: timeout", want: false},
		{name: "google http 500 is not oauth", err: "token refresh failed: HTTP 500: internal error", want: false},
		{name: "generic auth wrap without grant is not oauth", err: "error while generating auth token: empty access token", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := googleOAuthTokenExchangeFailure(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
