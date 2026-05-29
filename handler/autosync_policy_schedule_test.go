package handler

import "testing"

func TestValidateScheduleIntervalOn(t *testing.T) {
	tests := []struct {
		name      string
		interval  string
		on        string
		wantError bool
	}{
		{name: "daily lowercase", interval: "daily", on: "12am"},
		{name: "daily uppercase on", interval: "daily", on: "12AM"},
		{name: "daily title interval", interval: "Daily", on: "12am"},
		{name: "nightly alias", interval: "nightly", on: ""},
		{name: "12h empty on", interval: "12h", on: ""},
		{name: "12h with time confuses users", interval: "12h", on: "12am", wantError: true},
		{name: "weekly monday", interval: "weekly", on: "Monday"},
		{name: "unknown interval", interval: "hourly", on: "", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateScheduleIntervalOn(tt.interval, tt.on)
			if tt.wantError && err == nil {
				t.Fatalf("expected error for interval=%q on=%q", tt.interval, tt.on)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
