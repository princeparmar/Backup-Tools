package outlook

import (
	"strings"
	"testing"
)

const keyTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestResourceKeyPrefix(t *testing.T) {
	cases := []struct {
		tenant, typ, id, want string
	}{
		{keyTenant, "user", "oid-1", keyTenant + "/user/oid-1"},
		{" AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA ", "USER", "oid-1", keyTenant + "/user/oid-1"},
		{keyTenant, "site", "contoso.sharepoint.com,abc,def", keyTenant + "/site/contoso.sharepoint.com_abc_def"},
		{keyTenant, "team", "a/b:c?d*e#f\\g", keyTenant + "/team/a_b_c_d_e_f_g"},
		{"", "user", "oid-1", ""},
		{keyTenant, "", "oid-1", ""},
		{keyTenant, "user", " ", ""},
	}
	for _, c := range cases {
		if got := ResourceKeyPrefix(c.tenant, c.typ, c.id); got != c.want {
			t.Errorf("ResourceKeyPrefix(%q,%q,%q) = %q, want %q", c.tenant, c.typ, c.id, got, c.want)
		}
	}
}

func TestResourceKeyPrefix_sameMailboxDifferentTenants(t *testing.T) {
	a := ResourceKeyPrefix(keyTenant, "user", "oid-1")
	b := ResourceKeyPrefix("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "user", "oid-1")
	if a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		t.Fatalf("prefixes overlap: %q %q", a, b)
	}
}

func TestSplitResourceKey(t *testing.T) {
	prefix := ResourceKeyPrefix(keyTenant, "user", "oid-1")
	key := OutlookMailObjectKey(prefix, "Inbox", "2026-01-02T03:04:05Z", "a@b.c", "s", "c", "msg-1")
	gotPrefix, tid, typ, id, rest, ok := SplitResourceKey(key)
	if !ok || gotPrefix != prefix || tid != keyTenant || typ != "user" || id != "oid-1" || !strings.HasPrefix(rest, "Inbox/") {
		t.Fatalf("SplitResourceKey(%q) = %q %q %q %q %q %v", key, gotPrefix, tid, typ, id, rest, ok)
	}

	for _, legacy := range []string{
		"ann@contoso.com/data/2026/msg.json",
		"not-a-guid/user/oid-1/data/x.json",
		keyTenant + "/mailbox/oid-1/data/x.json",
		keyTenant + "/user",
		"",
	} {
		if _, _, _, _, _, ok := SplitResourceKey(legacy); ok {
			t.Errorf("SplitResourceKey(%q) accepted a non-tenant key", legacy)
		}
	}
}

func TestKeyBuildersStartWithResourcePrefix(t *testing.T) {
	user := ResourceKeyPrefix(keyTenant, "user", "oid-1")
	site := ResourceKeyPrefix(keyTenant, "site", "site-1")
	team := ResourceKeyPrefix(keyTenant, "team", "team-1")
	group := ResourceKeyPrefix(keyTenant, "group", "group-1")
	keys := map[string]string{
		OutlookMailObjectKey(user, "Inbox", "2026-01-02T03:04:05Z", "a", "s", "c", "m"): user,
		OneDriveFileKey(user, OneDriveSectionMyDrive, []string{"f"}, "i", "file.txt"):    user,
		SharePointIDBasedDataKey(site, "i", "file.txt", "2026-01-02T03:04:05Z"):         site,
		TeamsIDBasedDataKey(team, "chan", "m", "2026-01-02T03:04:05Z"):                  team,
		GroupCalendarEventKey(group, "e"):                                               group,
	}
	for key, prefix := range keys {
		got, _, _, _, _, ok := SplitResourceKey(key)
		if !ok || got != prefix {
			t.Errorf("key %q: prefix %q ok=%v, want %q", key, got, ok, prefix)
		}
	}
}

func TestParseKeysFromTenantLayout(t *testing.T) {
	team := ResourceKeyPrefix(keyTenant, "team", "team-1")
	teamKey, channel := ParseTeamsIDsFromKey(TeamsIDBasedDataKey(team, "chan-1", "m", "2026-01-02T03:04:05Z"))
	if teamKey != team || channel != "chan-1" {
		t.Fatalf("ParseTeamsIDsFromKey = %q %q", teamKey, channel)
	}

	group := ResourceKeyPrefix(keyTenant, "group", "group-1")
	if got := GroupKeyFromObjectKey(GroupCalendarEventKey(group, "e")); got != group {
		t.Fatalf("GroupKeyFromObjectKey = %q, want %q", got, group)
	}
}
