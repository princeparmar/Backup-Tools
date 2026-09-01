package repo

import "testing"

func TestIsRestoreJobTerminal(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		terminal bool
	}{
		{name: "queued", status: RestoreJobStatusQueued, terminal: false},
		{name: "running", status: RestoreJobStatusRunning, terminal: false},
		{name: "completed", status: RestoreJobStatusCompleted, terminal: true},
		{name: "partial_completed", status: RestoreJobStatusPartialCompleted, terminal: true},
		{name: "failed", status: RestoreJobStatusFailed, terminal: true},
		{name: "cancelled", status: RestoreJobStatusCancelled, terminal: true},
		{name: "unknown", status: "unknown", terminal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRestoreJobTerminal(tt.status); got != tt.terminal {
				t.Fatalf("IsRestoreJobTerminal(%q) = %v, want %v", tt.status, got, tt.terminal)
			}
		})
	}
}
