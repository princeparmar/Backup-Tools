package restore

import (
	"testing"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestResolveRestoreAuthMode(t *testing.T) {
	cred := &repo.GoogleBackupCredentialDB{Email: "admin@corp.com", AccountType: google.AccountTypeAdminWorkspace}

	tests := []struct {
		name        string
		service     string
		accountType string
		loginID     string
		holder      string
		want        string
	}{
		{
			name: "personal always oauth",
			service: "gmail", accountType: google.AccountTypePersonal,
			loginID: "user@gmail.com", holder: "user@gmail.com",
			want: repo.RestoreAuthModeOAuth,
		},
		{
			name: "employee oauth",
			service: "gmail", accountType: google.AccountTypeEmployeeWorkspace,
			loginID: "emp@corp.com", holder: "emp@corp.com",
			want: repo.RestoreAuthModeOAuth,
		},
		{
			name: "admin workspace child gmail dwd",
			service: "gmail", accountType: google.AccountTypeAdminWorkspace,
			loginID: "child@corp.com", holder: "admin@corp.com",
			want: repo.RestoreAuthModeDWD,
		},
		{
			name: "admin workspace holder gmail oauth",
			service: "gmail", accountType: google.AccountTypeAdminWorkspace,
			loginID: "admin@corp.com", holder: "admin@corp.com",
			want: repo.RestoreAuthModeOAuth,
		},
		{
			name: "admin workspace child drive dwd",
			service: "drive", accountType: google.AccountTypeAdminWorkspace,
			loginID: "child@corp.com", holder: "admin@corp.com",
			want: repo.RestoreAuthModeDWD,
		},
		{
			name: "admin workspace child photos dwd",
			service: "photos", accountType: google.AccountTypeAdminWorkspace,
			loginID: "child@corp.com", holder: "admin@corp.com",
			want: repo.RestoreAuthModeDWD,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := *cred
			c.AccountType = tt.accountType
			got := ResolveRestoreAuthMode(tt.service, tt.accountType, tt.loginID, tt.holder, &c, nil)
			if got != tt.want {
				t.Fatalf("ResolveRestoreAuthMode() = %q, want %q", got, tt.want)
			}
		})
	}
}
