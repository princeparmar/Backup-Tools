package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestCredentialHasMicrosoftBackupJobs(t *testing.T) {
	tests := []struct {
		name string
		jobs []repo.CronJobListingDB
		want bool
	}{
		{name: "nil", jobs: nil, want: false},
		{name: "google only", jobs: []repo.CronJobListingDB{{Method: "gmail"}, {Method: "drive"}}, want: false},
		{name: "outlook mail", jobs: []repo.CronJobListingDB{{Method: "outlook"}}, want: true},
		{name: "outlook onedrive", jobs: []repo.CronJobListingDB{{Method: "gmail"}, {Method: "outlook_onedrive"}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credentialHasMicrosoftBackupJobs(tt.jobs); got != tt.want {
				t.Fatalf("credentialHasMicrosoftBackupJobs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestoreCredentialMatchesProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		jobs     []repo.CronJobListingDB
		want     bool
	}{
		{name: "ms with outlook job", provider: "microsoft", jobs: []repo.CronJobListingDB{{Method: "outlook"}}, want: true},
		{name: "ms with google only", provider: "microsoft", jobs: []repo.CronJobListingDB{{Method: "gmail"}}, want: false},
		{name: "ms with no jobs", provider: "microsoft", jobs: nil, want: false},
		{name: "google with gmail", provider: "google", jobs: []repo.CronJobListingDB{{Method: "gmail"}}, want: true},
		{name: "google with no jobs", provider: "google", jobs: nil, want: true},
		{name: "google excludes ms-only", provider: "google", jobs: []repo.CronJobListingDB{{Method: "outlook"}}, want: false},
		{name: "google keeps dual", provider: "google", jobs: []repo.CronJobListingDB{{Method: "gmail"}, {Method: "outlook"}}, want: true},
		{name: "ms keeps dual", provider: "microsoft", jobs: []repo.CronJobListingDB{{Method: "gmail"}, {Method: "outlook"}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restoreCredentialMatchesProvider(tt.provider, tt.jobs); got != tt.want {
				t.Fatalf("restoreCredentialMatchesProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestRestoreAccountKind(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		want        string
	}{
		{name: "personal", accountType: "personal", want: "personal"},
		{name: "empty defaults personal", accountType: "", want: "personal"},
		{name: "admin workspace", accountType: "admin_workspace", want: "workspace"},
		{name: "employee workspace", accountType: "employee_workspace", want: "workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restoreAccountKind(tt.accountType); got != tt.want {
				t.Fatalf("restoreAccountKind(%q) = %q, want %q", tt.accountType, got, tt.want)
			}
		})
	}
}
