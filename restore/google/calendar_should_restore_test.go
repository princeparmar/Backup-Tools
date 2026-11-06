package googlestore

import "testing"

func TestCalendarProcessor_ShouldRestoreKey(t *testing.T) {
	p := &calendarProcessor{}
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{
			name: "classroom event",
			key:  "dhavalder.office93@gmail.com/calendar/events/c_classroom32c90b75@group.calendar.google.com_7 IT A & B/2026/07/21/ev1_Standup.json",
			want: true,
		},
		{
			name: "primary event",
			key:  "dhavalder.office93@gmail.com/calendar/events/dhavalder.office93@gmail.com_untitled/2026/07/21/ev1_Standup.json",
			want: true,
		},
		{
			name: "holiday skipped",
			key:  "dhavalder.office93@gmail.com/calendar/events/en.indian#holiday@group.v.calendar.google.com_Holidays in India/2026/07/21/ev1_Diwali.json",
			want: false,
		},
		{
			name: "calendar meta skipped",
			key:  "dhavalder.office93@gmail.com/calendar/calendars/c_classroom32c90b75@group.calendar.google.com_7 IT A & B.json",
			want: false,
		},
		{
			name: "index skipped",
			key:  "dhavalder.office93@gmail.com/calendar/_index.json",
			want: false,
		},
		{
			name: "placeholder skipped",
			key:  "dhavalder.office93@gmail.com/.file_placeholder",
			want: false,
		},
		{
			name: "undated event skipped",
			key:  "dhavalder.office93@gmail.com/calendar/events/cal1_Work/ev1_Standup.json",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.ShouldRestoreKey(tt.key); got != tt.want {
				t.Fatalf("ShouldRestoreKey(%q)=%v want %v", tt.key, got, tt.want)
			}
		})
	}
}
