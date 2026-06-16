package google

import (
	"testing"

	"google.golang.org/api/gmail/v1"
)

func TestNormalizeAccountType(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "personal default", in: "", want: AccountTypePersonal},
		{name: "employee", in: "employee_workspace", want: AccountTypeEmployeeWorkspace},
		{name: "admin", in: "ADMIN_WORKSPACE", want: AccountTypeAdminWorkspace},
		{name: "unknown", in: "corp", want: AccountTypePersonal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeAccountType(tt.in); got != tt.want {
				t.Fatalf("NormalizeAccountType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTokenInfoMissingScopes(t *testing.T) {
	tests := []struct {
		name     string
		granted  string
		required []string
		wantLen  int
		wantHas  string
	}{
		{
			name:     "gmail full mail scope present",
			granted:  gmail.MailGoogleComScope + " openid",
			required: RestoreOAuthScopesForService("gmail"),
			wantLen:  0,
		},
		{
			name:     "readonly only missing mail",
			granted:  gmail.GmailReadonlyScope,
			required: RestoreOAuthScopesForService("gmail"),
			wantLen:  1,
			wantHas:  gmail.MailGoogleComScope,
		},
		{
			name:     "empty granted",
			granted:  "",
			required: []string{"https://www.googleapis.com/auth/calendar"},
			wantLen:  1,
			wantHas:  "https://www.googleapis.com/auth/calendar",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing := TokenInfoMissingScopes(tt.granted, tt.required)
			if len(missing) != tt.wantLen {
				t.Fatalf("missing len = %d, want %d (%v)", len(missing), tt.wantLen, missing)
			}
			if tt.wantHas != "" {
				found := false
				for _, m := range missing {
					if m == tt.wantHas {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing %q not in %v", tt.wantHas, missing)
				}
			}
		})
	}
}

func TestRestoreOAuthScopesForService_gmailOnlyMailScope(t *testing.T) {
	scopes := RestoreOAuthScopesForService("gmail")
	if len(scopes) != 1 || scopes[0] != gmail.MailGoogleComScope {
		t.Fatalf("gmail restore scopes = %v, want only %s", scopes, gmail.MailGoogleComScope)
	}
}

func TestRestoreDWDScopesMap(t *testing.T) {
	tests := []struct {
		service      string
		wantNonEmpty bool
	}{
		{"gmail", true}, {"drive", true}, {"calendar", true}, {"contacts", true}, {"photos", true},
		{"unknown", false},
	}
	m := RestoreDWDScopesMap()
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			got := m[tt.service]
			if tt.wantNonEmpty && got == "" {
				t.Fatalf("missing DWD scope for %s", tt.service)
			}
			if !tt.wantNonEmpty && got != "" {
				t.Fatalf("unexpected scope for %s: %s", tt.service, got)
			}
		})
	}
	if len(AllRestoreDWDScopeURLs()) != 5 {
		t.Fatalf("expected 5 DWD scope URLs, got %d", len(AllRestoreDWDScopeURLs()))
	}
}
