package quota

import (
	"errors"
	"testing"
)

func TestFileFitsStorage(t *testing.T) {
	tests := []struct {
		name      string
		fileBytes int64
		remaining int64
		want      bool
	}{
		{name: "unknown size always fits", fileBytes: 0, remaining: 0, want: true},
		{name: "fits exact", fileBytes: 100, remaining: 100, want: true},
		{name: "too big", fileBytes: 100, remaining: 99, want: false},
		{name: "fits with headroom", fileBytes: 80, remaining: 100, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FileFitsStorage(tt.fileBytes, tt.remaining); got != tt.want {
				t.Fatalf("FileFitsStorage(%d,%d)=%v want %v", tt.fileBytes, tt.remaining, got, tt.want)
			}
		})
	}
}

func TestIsStorageQuota(t *testing.T) {
	err := &ErrStorageQuota{RequiredBytes: 10, RemainingBytes: 1, Method: "gmail"}
	if !IsStorageQuota(err) {
		t.Fatal("expected storage quota")
	}
	if !IsStorageQuota(errors.New("uplink: storage limit exceeded")) {
		t.Fatal("expected uplink match")
	}
	if IsStorageQuota(errors.New("network")) {
		t.Fatal("unexpected")
	}
}

func TestIsBandwidthQuota(t *testing.T) {
	err := &ErrBandwidthQuota{EstimateBytes: 10, RemainingBytes: 1}
	if !IsBandwidthQuota(err) {
		t.Fatal("expected bandwidth quota")
	}
}

func TestUsageRemaining(t *testing.T) {
	u := UsageSnapshot{StorageUsed: 80, StorageLimit: 100, BandwidthUsed: 10, BandwidthLimit: 50}
	if u.RemainingStorage() != 20 {
		t.Fatalf("storage remaining %d", u.RemainingStorage())
	}
	if u.RemainingBandwidth() != 40 {
		t.Fatalf("bw remaining %d", u.RemainingBandwidth())
	}
	unlimited := UsageSnapshot{}
	if unlimited.RemainingStorage() <= 0 {
		t.Fatal("unlimited storage")
	}
}
