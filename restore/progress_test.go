package restore

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestAdjustedRestoreTotal(t *testing.T) {
	cases := []struct {
		name                              string
		total, skipped, processed, failed uint
		want                              uint
	}{
		{name: "skip placeholders only", total: 30, skipped: 4, processed: 0, failed: 0, want: 26},
		{name: "never below already counted work", total: 2, skipped: 0, processed: 51, failed: 57, want: 108},
		{name: "skip cannot hide finished items", total: 10, skipped: 8, processed: 5, failed: 2, want: 7},
		{name: "no skip", total: 26, skipped: 0, processed: 10, failed: 1, want: 26},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adjustedRestoreTotal(tc.total, tc.skipped, tc.processed, tc.failed)
			if got != tc.want {
				t.Fatalf("adjustedRestoreTotal = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLiftRestoreTotal(t *testing.T) {
	cases := []struct {
		name                     string
		total, processed, failed uint
		want                     uint
	}{
		{name: "contacts counts passed total", total: 2, processed: 51, failed: 57, want: 108},
		{name: "drive counts passed total", total: 26, processed: 56, failed: 2, want: 58},
		{name: "total still ahead", total: 80, processed: 10, failed: 1, want: 80},
		{name: "empty job", total: 0, processed: 0, failed: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := liftRestoreTotal(tc.total, tc.processed, tc.failed)
			if got != tc.want {
				t.Fatalf("liftRestoreTotal = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRecoveredRestoreCursor(t *testing.T) {
	cases := []struct {
		name                  string
		jobCursor, maxTaskEnd uint
		want                  uint
	}{
		{name: "stacked drive cursor still 0 uses task end", jobCursor: 0, maxTaskEnd: 2400, want: 2400},
		{name: "job already ahead of tasks", jobCursor: 24281, maxTaskEnd: 100, want: 24281},
		{name: "no task end keeps job cursor", jobCursor: 0, maxTaskEnd: 0, want: 0},
		{name: "equal cursors unchanged", jobCursor: 50, maxTaskEnd: 50, want: 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recoveredRestoreCursor(tc.jobCursor, tc.maxTaskEnd)
			if got != tc.want {
				t.Fatalf("recoveredRestoreCursor = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRestoreProgressPercentCapsAtCountedTotal(t *testing.T) {
	cases := []struct {
		name                     string
		total, processed, failed uint
		want                     float64
	}{
		{name: "mid job", total: 100, processed: 25, failed: 25, want: 50},
		{name: "done equals total", total: 26, processed: 24, failed: 2, want: 100},
		{name: "done above total still 100", total: 2, processed: 51, failed: 57, want: 100},
		{name: "no total yet", total: 0, processed: 1, failed: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := repo.RestoreProgressPercent(tc.total, tc.processed, tc.failed)
			if got != tc.want {
				t.Fatalf("RestoreProgressPercent = %v, want %v", got, tc.want)
			}
		})
	}
}
