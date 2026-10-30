package outlook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSanitizeTeamsTeamKey(t *testing.T) {
	got := SanitizeTeamsTeamKey("00000000-0000-0000-0000-000000000001")
	if got == "" {
		t.Fatal("expected non-empty team key")
	}
}

func TestListTeamChannelMembersPreservesGraphIdentities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/teams/team-1/channels/channel-1/members" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("$select"); got != "id,displayName" {
			t.Fatalf("unexpected $select: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"id":"user-1","displayName":"Ada Lovelace"},{"id":"user-2","displayName":"Grace Hopper"}]}`))
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	members, err := ListTeamChannelMembers(context.Background(), "access-token", "team-1", "channel-1")
	if err != nil {
		t.Fatalf("ListTeamChannelMembers() error = %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members length = %d, want 2", len(members))
	}
	if members[0].ID != "user-1" || members[0].DisplayName != "Ada Lovelace" {
		t.Fatalf("members[0] = %#v", members[0])
	}
	if members[1].ID != "user-2" || members[1].DisplayName != "Grace Hopper" {
		t.Fatalf("members[1] = %#v", members[1])
	}
}

func TestListTeamChannelsIncludesMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/teams/team-1/channels":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"id":"channel-1","displayName":"General","description":"General channel"}]}`))
		case "/teams/team-1/channels/channel-1/members":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[{"id":"user-1","displayName":"Ada Lovelace"},{"id":"user-2","displayName":"Grace Hopper"}]}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	channels, err := ListTeamChannels(context.Background(), "access-token", "team-1")
	if err != nil {
		t.Fatalf("ListTeamChannels() error = %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("channels length = %d, want 1", len(channels))
	}
	if len(channels[0].Members) != 2 {
		t.Fatalf("channel members length = %d, want 2", len(channels[0].Members))
	}
	if channels[0].Members[0].ID != "user-1" || channels[0].Members[0].DisplayName != "Ada Lovelace" {
		t.Fatalf("channel members[0] = %#v", channels[0].Members[0])
	}
}

func TestTeamsChannelMessagesInitialURL(t *testing.T) {
	url := TeamsChannelMessagesInitialURL("team-1", "channel-1")
	if url == "" || !containsAll(url, "team-1", "channel-1", "messages") {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestFetchTeamsBytesSendsConsistencyLevelOnlyWhenAsked(t *testing.T) {
	var levels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chats/chat-1/messages/m1/hostedContents/c1/$value" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		levels = append(levels, r.Header.Get("ConsistencyLevel"))
		_, _ = w.Write([]byte("PNG"))
	}))
	defer server.Close()
	previousBaseURL := graphBaseURL
	graphBaseURL = server.URL
	defer func() { graphBaseURL = previousBaseURL }()

	u := TeamsHostedValueURL(TeamsChatHostedListURL("chat-1", "m1"), "c1")
	for _, eventual := range []bool{true, false} {
		b, err := FetchTeamsBytes(context.Background(), "tok", u, eventual)
		if err != nil || string(b) != "PNG" {
			t.Fatalf("eventual=%v: %q %v", eventual, b, err)
		}
	}
	if len(levels) != 2 || levels[0] != "eventual" || levels[1] != "" {
		t.Fatalf("ConsistencyLevel headers = %q", levels)
	}
}

func TestParseTeamsHostedRef(t *testing.T) {
	const g = "https://graph.microsoft.com/v1.0"
	chat := "19:bcf84b15c2994a909770f7d05bc4fe16@thread.v2"
	ok := map[string]TeamsHostedRef{
		g + "/chats/" + chat + "/messages/1706763669648/hostedContents/aWQ9eA==/$value":    {ChatID: chat, MessageID: "1706763669648", ContentID: "aWQ9eA=="},
		g + "/users/u-1/chats/Chat-1/messages/M1/hostedContents/C1/$value":                 {ChatID: "Chat-1", MessageID: "M1", ContentID: "C1"},
		g + "/teams/team-1/channels/ch-a/messages/100/hostedContents/img/$value":           {TeamID: "team-1", ChannelID: "ch-a", RootID: "100", MessageID: "100", ContentID: "img"},
		g + "/teams/team-1/channels/ch-a/messages/100/replies/101/hostedContents/r/$value": {TeamID: "team-1", ChannelID: "ch-a", RootID: "100", MessageID: "101", ContentID: "r"},
		g + "/chats('chat-1')/messages('owner')/hostedContents('emo')/$value":              {ChatID: "chat-1", MessageID: "owner", ContentID: "emo"},
		"HTTPS://Graph.Microsoft.com/V1.0/Chats/chat-1/Messages/m/HostedContents/c/$value": {ChatID: "chat-1", MessageID: "m", ContentID: "c"},
	}
	for link, want := range ok {
		got, parsed := ParseTeamsHostedRef(link)
		if !parsed || got != want {
			t.Errorf("%s: got %+v %v, want %+v", link, got, parsed, want)
		}
	}
	for _, link := range []string{
		"",
		"hostedContents/c/$value",
		"../hostedContents/c/$value",
		"/chats/chat-1/messages/m/hostedContents/c/$value",
		"https://evil.example/v1.0/chats/chat-1/messages/m/hostedContents/c/$value",
		"http://graph.microsoft.com/v1.0/chats/chat-1/messages/m/hostedContents/c/$value",
		"https://user@graph.microsoft.com/v1.0/chats/chat-1/messages/m/hostedContents/c/$value",
		g[:len(g)-4] + "beta/chats/chat-1/messages/m/hostedContents/c/$value",
		g + "/chats/chat-1/messages/m/hostedContents/c/$value?x=1",
		g + "/chats/chat-1/messages/m/hostedContents/c/$value#f",
		g + "/chats/chat-1/messages/m/hostedContents/c",
		g + "/chats/chat-1/messages/m/../../chat-2/messages/x/hostedContents/c/$value",
		g + "/chats/chat-1/messages/a%2Fb/hostedContents/c/$value",
		g + "/chats/chat-1/hostedContents/c/$value",
		g + "/me/chats/chat-1/messages/m/hostedContents/c/$value",
		g + "/drives/d/items/i/hostedContents/c/$value",
		g + "/teams/t/channels/c/messages/m/replies/r/extra/hostedContents/c/$value",
	} {
		if ref, parsed := ParseTeamsHostedRef(link); parsed {
			t.Errorf("%q must be rejected, got %+v", link, ref)
		}
	}
	ref := TeamsHostedRef{TeamID: "team-1", ChannelID: "ch-a", RootID: "100", MessageID: "101", ContentID: "r"}
	if got := ref.ValueURL(); got != g+"/teams/team-1/channels/ch-a/messages/100/replies/101/hostedContents/r/$value" {
		t.Fatalf("reply ValueURL = %s", got)
	}
	ref = TeamsHostedRef{ChatID: "chat-1", MessageID: "owner", ContentID: "emo"}
	if got := ref.ValueURL(); got != g+"/chats/chat-1/messages/owner/hostedContents/emo/$value" {
		t.Fatalf("chat ValueURL = %s", got)
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

// ConsistencyLevel: eventual is sent only when the caller asks for it (hosted content of an
// edited or deleted message), never on other hosted fetches.
