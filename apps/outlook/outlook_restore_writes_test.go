package outlook

import "testing"

func TestParseRestoreCalendarEvent_table(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantSub string
		wantTZ  string
		wantErr bool
	}{
		{
			name:    "flat event with timezone",
			raw:     `{"id":"1","subject":"Standup","start":"2026-01-01T10:00:00","end":"2026-01-01T10:30:00","time_zone":"India Standard Time","body_preview":"daily"}`,
			wantSub: "Standup",
			wantTZ:  "India Standard Time",
		},
		{
			name:    "graph shaped with timezone",
			raw:     `{"subject":"Sync","body":{"content":"notes"},"start":{"dateTime":"2026-01-02T09:00:00","timeZone":"Pacific Standard Time"},"end":{"dateTime":"2026-01-02T10:00:00","timeZone":"Pacific Standard Time"}}`,
			wantSub: "Sync",
			wantTZ:  "Pacific Standard Time",
		},
		{
			name:    "missing subject",
			raw:     `{"id":"x"}`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRestoreCalendarEvent([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Subject != tc.wantSub {
				t.Fatalf("subject=%q want %q", got.Subject, tc.wantSub)
			}
			if got.TimeZone != tc.wantTZ {
				t.Fatalf("timezone=%q want %q", got.TimeZone, tc.wantTZ)
			}
		})
	}
}

func TestParseTeamsIDsFromKey_table(t *testing.T) {
	cases := []struct {
		key         string
		wantTeam    string
		wantChannel string
	}{
		{"team-1/channels/chan-2/meta/2026/01/01/msg.json", "team-1", "chan-2"},
		{"only-team", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			team, ch := ParseTeamsIDsFromKey(tc.key)
			if team != tc.wantTeam || ch != tc.wantChannel {
				t.Fatalf("got (%q,%q) want (%q,%q)", team, ch, tc.wantTeam, tc.wantChannel)
			}
		})
	}
}

func TestResolveTeamsGraphIDs_table(t *testing.T) {
	cases := []struct {
		name        string
		meta        TeamsCronBackupMeta
		snap        *TeamsTeamSnapshot
		key         string
		overrideT   string
		overrideC   string
		wantTeam    string
		wantChannel string
		wantErr     bool
	}{
		{
			name:        "meta has real ids",
			meta:        TeamsCronBackupMeta{TeamID: "real-team", ChannelID: "19:chan@thread.tacv2"},
			key:         "sanitized_team/channels/19%3Achan%40thread.tacv2/data/x.json",
			wantTeam:    "real-team",
			wantChannel: "19:chan@thread.tacv2",
		},
		{
			name:        "team from snapshot when meta missing team",
			meta:        TeamsCronBackupMeta{ChannelID: "chan-1"},
			snap:        &TeamsTeamSnapshot{TeamID: "snap-team"},
			key:         "sanitized/channels/ignored/data/x.json",
			wantTeam:    "snap-team",
			wantChannel: "chan-1",
		},
		{
			name:    "sanitized key alone is not enough for team",
			meta:    TeamsCronBackupMeta{},
			key:     "19_abc_thread_tacv2/channels/chan/data/x.json",
			wantErr: true,
		},
		{
			name:        "override wins",
			meta:        TeamsCronBackupMeta{TeamID: "meta-t", ChannelID: "meta-c"},
			overrideT:   "ovr-t",
			overrideC:   "ovr-c",
			wantTeam:    "ovr-t",
			wantChannel: "ovr-c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotT, gotC, err := ResolveTeamsGraphIDs(tc.meta, tc.snap, tc.key, tc.overrideT, tc.overrideC)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotT != tc.wantTeam || gotC != tc.wantChannel {
				t.Fatalf("got (%q,%q) want (%q,%q)", gotT, gotC, tc.wantTeam, tc.wantChannel)
			}
		})
	}
}

func TestResolveGroupGraphID_table(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		raw      string
		snap     *GroupsGroupSnapshot
		override string
		want     string
		wantErr  bool
	}{
		{
			name: "embedded group_id",
			key:  "sanitized_group/conversations/t/posts/p.json",
			raw:  `{"group_id":"guid-123","topic":"hi"}`,
			want: "guid-123",
		},
		{
			name: "from _group.json snapshot",
			key:  "sanitized_group/calendar/events/e.json",
			snap: &GroupsGroupSnapshot{GroupID: "guid-456"},
			want: "guid-456",
		},
		{
			name: "guid key unchanged by sanitize",
			key:  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/conversations/t/posts/p.json",
			want: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
		{
			name:    "sanitized non-guid key without metadata",
			key:     "weird:group/conversations/t/posts/p.json",
			wantErr: true,
		},
		{
			name:     "override wins",
			key:      "x/conversations/t/posts/p.json",
			override: "ovr-g",
			want:     "ovr-g",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveGroupGraphID(tc.key, []byte(tc.raw), tc.snap, tc.override)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCalendarRestoreTimeZone_table(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "UTC"},
		{"  ", "UTC"},
		{"India Standard Time", "India Standard Time"},
	}
	for _, tc := range cases {
		if got := calendarRestoreTimeZone(tc.in); got != tc.want {
			t.Fatalf("in=%q got %q want %q", tc.in, got, tc.want)
		}
	}
}
