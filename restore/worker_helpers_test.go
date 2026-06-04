package restore

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestShouldFetchAnotherBatch(t *testing.T) {
	tests := []struct {
		name      string
		rowCount  int
		batchSize int
		want      bool
	}{
		{name: "full page continues", rowCount: 25, batchSize: 25, want: true},
		{name: "partial page stops", rowCount: 10, batchSize: 25, want: false},
		{name: "empty stops", rowCount: 0, batchSize: 25, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFetchAnotherBatch(tt.rowCount, tt.batchSize); got != tt.want {
				t.Fatalf("shouldFetchAnotherBatch(%d,%d) = %v, want %v", tt.rowCount, tt.batchSize, got, tt.want)
			}
		})
	}
}

func TestResolveJobTerminalStatus(t *testing.T) {
	tests := []struct {
		name       string
		failed     uint
		wantStatus string
	}{
		{name: "no failures completed", failed: 0, wantStatus: repo.RestoreJobStatusCompleted},
		{name: "some failures partial", failed: 3, wantStatus: repo.RestoreJobStatusPartialCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := resolveJobTerminalStatus(tt.failed)
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", status, tt.wantStatus)
			}
		})
	}
}

func TestShouldOAuth401Retry(t *testing.T) {
	tests := []struct {
		name   string
		deps   *RestoreDeps
		result BatchResult
		want   bool
	}{
		{
			name: "dwd never retries",
			deps: &RestoreDeps{AuthMode: repo.RestoreAuthModeDWD, RefreshToken: "rt"},
			result: BatchResult{Failed: 1, FailedKeys: []FailedKey{{ErrorCode: "401"}}},
			want: false,
		},
		{
			name: "oauth all 401 retries",
			deps: &RestoreDeps{AuthMode: repo.RestoreAuthModeOAuth, RefreshToken: "rt"},
			result: BatchResult{Failed: 2, FailedKeys: []FailedKey{{ErrorCode: "401"}, {ErrorCode: "401"}}},
			want: true,
		},
		{
			name: "mixed success no retry",
			deps: &RestoreDeps{AuthMode: repo.RestoreAuthModeOAuth, RefreshToken: "rt"},
			result: BatchResult{Processed: 1, Failed: 1, FailedKeys: []FailedKey{{ErrorCode: "401"}}},
			want: false,
		},
		{
			name: "no refresh token",
			deps: &RestoreDeps{AuthMode: repo.RestoreAuthModeOAuth},
			result: BatchResult{Failed: 1, FailedKeys: []FailedKey{{ErrorCode: "401"}}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldOAuth401Retry(tt.deps, tt.result); got != tt.want {
				t.Fatalf("shouldOAuth401Retry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsActiveRestoreConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "conflict", err: errActiveConflict(), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsActiveRestoreConflict(tt.err); got != tt.want {
				t.Fatalf("IsActiveRestoreConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}

func errActiveConflict() error {
	return errTest("a restore is already in progress for this account and service")
}

type errTest string

func (e errTest) Error() string { return string(e) }
