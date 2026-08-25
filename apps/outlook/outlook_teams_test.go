package outlook

import "testing"

func TestSanitizeTeamsTeamKey(t *testing.T) {
	got := SanitizeTeamsTeamKey("00000000-0000-0000-0000-000000000001")
	if got == "" {
		t.Fatal("expected non-empty team key")
	}
}

func TestTeamsChannelMessagesInitialURL(t *testing.T) {
	url := TeamsChannelMessagesInitialURL("team-1", "channel-1")
	if url == "" || !containsAll(url, "team-1", "channel-1", "messages") {
		t.Fatalf("unexpected url: %s", url)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if p == "" {
			continue
		}
		if !containsSubstring(s, p) {
			return false
		}
	}
	return true
}

func containsSubstring(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
