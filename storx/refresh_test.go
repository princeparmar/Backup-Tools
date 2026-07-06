package storx

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsStorageLimitError(t *testing.T) {
	base := errors.New("setup storage placeholder: failed to upload object to Satellite: commit object: uplink: storage limit exceeded")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "wrapped storage limit", err: fmt.Errorf("upload failed: %w", base), want: true},
		{name: "permission error", err: errors.New("uplink: permission denied"), want: false},
		{name: "direct match", err: errors.New("uplink: storage limit exceeded"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStorageLimitError(tt.err); got != tt.want {
				t.Fatalf("IsStorageLimitError() = %v, want %v", got, tt.want)
			}
		})
	}
}
