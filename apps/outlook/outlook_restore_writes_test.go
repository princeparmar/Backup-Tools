package outlook

import "testing"

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

func TestParseTeamsReplyAndAttachmentSnapshots(t *testing.T) {
	replyRaw := `{"parent_message_id":"msg-1","replies":[{"id":"reply-1","subject":"Re: hello"}]}`
	replySnap, err := ParseTeamsReplySnapshot([]byte(replyRaw))
	if err != nil {
		t.Fatalf("ParseTeamsReplySnapshot() error = %v", err)
	}
	if replySnap.ParentMessageID != "msg-1" || len(replySnap.Replies) != 1 || replySnap.Replies[0].ID != "reply-1" {
		t.Fatalf("replySnap = %#v", replySnap)
	}

	attachRaw := `{"message_id":"msg-1","attachments":[{"message_id":"msg-1","content_id":"hosted-42","attachment_type":"hosted_content","storage_key":"teamKey/channels/chan-1/hosted/msg-1/hosted-42"}]}`
	attachSnap, err := ParseTeamsAttachmentSnapshot([]byte(attachRaw))
	if err != nil {
		t.Fatalf("ParseTeamsAttachmentSnapshot() error = %v", err)
	}
	if attachSnap.MessageID != "msg-1" || len(attachSnap.Attachments) != 1 || attachSnap.Attachments[0].ContentID != "hosted-42" {
		t.Fatalf("attachSnap = %#v", attachSnap)
	}
}
