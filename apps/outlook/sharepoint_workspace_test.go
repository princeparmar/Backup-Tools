package outlook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapSharePointPermissionPrincipalFailClosed(t *testing.T) {
	dir := map[string]string{
		"old@x.com": "new-id",
		"deleted":   "", // blocked
	}
	if id, ok := MapSharePointPermissionPrincipal(map[string]any{"userPrincipalName": "old@x.com"}, dir); !ok || id != "new-id" {
		t.Fatalf("mapped got %q %v", id, ok)
	}
	if _, ok := MapSharePointPermissionPrincipal(map[string]any{"id": "deleted"}, dir); ok {
		t.Fatal("deleted must fail closed")
	}
	if _, ok := MapSharePointPermissionPrincipal(map[string]any{"id": "unknown-guest"}, dir); ok {
		t.Fatal("unmapped guest must fail closed when directory provided")
	}
	if _, ok := MapSharePointPermissionPrincipal(map[string]any{"displayName": "Only Name"}, nil); ok {
		t.Fatal("displayName alone never enough")
	}
	if id, ok := MapSharePointPermissionPrincipal(map[string]any{"id": "same-tenant"}, nil); !ok || id != "same-tenant" {
		t.Fatalf("nil directory passthrough %q %v", id, ok)
	}
}

func TestSharePointListWriteSite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		destination string
		meta        string
		want        string
	}{
		{name: "in place uses backup site", meta: "site-src", want: "site-src"},
		{name: "migration uses destination site", destination: "site-dest", meta: "site-src", want: "site-dest"},
		{name: "blank destination falls back", destination: "  ", meta: " site-src ", want: "site-src"},
		{
			name:        "sanitized dest becomes graph id",
			destination: "space2.sharepoint.com_edf2e428-d991-4aa0-9c11-aaaaaaaaaaaa_bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			meta:        "site-src",
			want:        "space2.sharepoint.com,edf2e428-d991-4aa0-9c11-aaaaaaaaaaaa,bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sharePointListWriteSite(tt.destination, tt.meta); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestValidateSharePointListLookups(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"fields": map[string]any{"Title": "x", "RelatedLookupId": "99"},
	})
	issues := ValidateSharePointListLookups(raw, map[string]bool{"1": true})
	if len(issues) != 1 || issues[0].Target != "99" {
		t.Fatalf("issues %#v", issues)
	}
	if n := len(ValidateSharePointListLookups(raw, map[string]bool{"99": true})); n != 0 {
		t.Fatalf("expected none, got %d", n)
	}
}

func TestRestoreSharePointListItemStripsSystemFields(t *testing.T) {
	// Unit-level: ensure IsSharePointListItemKey and item key helpers stay consistent.
	dir := SharePointListDir("t/site/s", "list1")
	key := SharePointListItemKey(dir, "42")
	if !IsSharePointListItemKey(key) {
		t.Fatalf("expected list item key %s", key)
	}
	if IsSharePointListItemKey(dir + SharePointListMetaName) {
		t.Fatal("meta must not look like item")
	}
	if IsSharePointListItemKey(dir + "attachments/42/a_file.txt") {
		t.Fatal("attachment must not look like item")
	}
}

func TestUniqueSharePointPageName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{in: "Home.aspx", want: "Home-restored.aspx"},
		{in: "Home.ASPX", want: "Home-restored.aspx"},
		{in: "Notes", want: "Notes-restored"},
		{in: "", want: "restored.aspx"},
	}
	for _, tt := range tests {
		if got := uniqueSharePointPageName(tt.in); got != tt.want {
			t.Fatalf("%q: got %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestSharePointPageDir(t *testing.T) {
	if d := SharePointPageDir("t/site/s"); !strings.HasSuffix(d, "/pages/") {
		t.Fatalf("page dir %s", d)
	}
}

func TestSharePointListDirUsesHumanName(t *testing.T) {
	dir := SharePointListDir("t/site/s", "42", "Tasks")
	if !strings.Contains(dir, "/lists/Tasks_42/") {
		t.Fatalf("dir %s", dir)
	}
	if got := SharePointListDisplayName(dir + "1.json"); got != "Tasks" {
		t.Fatalf("display %q", got)
	}
}

func TestSharePointPageObjectKeyUsesHumanName(t *testing.T) {
	key := SharePointPageObjectKey("t/site/s", "4313cc6c-031c-4afc-96ee-25090c2d7546", "Home.aspx", "Home")
	if !strings.Contains(key, "/pages/Home.aspx_") || !strings.HasSuffix(key, ".json") {
		t.Fatalf("key %s", key)
	}
	if got := SharePointPageDisplayName(key); got != "Home.aspx" {
		t.Fatalf("display %q", got)
	}
	// Legacy GUID-only key still displays something usable.
	if got := SharePointPageDisplayName("t/site/s/pages/4313cc6c-031c-4afc-96ee-25090c2d7546.json"); got != "4313cc6c-031c-4afc-96ee-25090c2d7546" {
		t.Fatalf("legacy display %q", got)
	}
}
