package google

import "testing"

func TestContactsIDFromResourceName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "people resource", input: "people/c123456789", want: "c123456789"},
		{name: "bare id", input: "c123456789", want: "c123456789"},
		{name: "empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContactsIDFromResourceName(tt.input); got != tt.want {
				t.Fatalf("ContactsIDFromResourceName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContactsObjectKey(t *testing.T) {
	got := ContactsObjectKey("user@gmail.com", "people/c123", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/contacts/2026/07/21/c123.json"
	if got != want {
		t.Fatalf("ContactsObjectKey() = %q, want %q", got, want)
	}
}

func TestBuildContactsSyncedIDSet(t *testing.T) {
	tests := []struct {
		name       string
		objectKeys map[string]bool
		email      string
		wantIDs    []string
	}{
		{
			name: "dated contacts json keys",
			objectKeys: map[string]bool{
				"user@gmail.com/contacts/2026/07/21/c123.json": true,
				"user@gmail.com/contacts/2026/07/22/c456.json": true,
			},
			email:   "user@gmail.com",
			wantIDs: []string{"c123", "c456"},
		},
		{
			name: "ignores undated and placeholders",
			objectKeys: map[string]bool{
				"user@gmail.com/contacts/c123.json": true,
				"user@gmail.com/.file_placeholder":  true,
			},
			email:   "user@gmail.com",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildContactsSyncedIDSet(tt.objectKeys, tt.email)
			for _, id := range tt.wantIDs {
				if _, ok := got[id]; !ok {
					t.Fatalf("expected id %q in set, got %v", id, got)
				}
			}
			if len(tt.wantIDs) == 0 && len(got) != 0 {
				t.Fatalf("expected empty set, got %v", got)
			}
		})
	}
}

func TestPageHasAnyNewContactsItems(t *testing.T) {
	synced := map[string]struct{}{"known": {}}
	tests := []struct {
		name  string
		items []FlatContact
		want  bool
	}{
		{name: "all known", items: []FlatContact{{ID: "people/known"}}, want: false},
		{name: "has new", items: []FlatContact{{ID: "people/known"}, {ID: "people/new"}}, want: true},
		{name: "empty page", items: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PageHasAnyNewContactsItems(tt.items, synced); got != tt.want {
				t.Fatalf("PageHasAnyNewContactsItems() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsContactSynced(t *testing.T) {
	syncedMap := map[string]bool{
		"user@gmail.com/contacts/c123.json":            true,
		"user@gmail.com/contacts/2026/07/21/c999.json": true,
	}
	tests := []struct {
		name         string
		resourceName string
		want         bool
	}{
		{name: "undated ignored", resourceName: "people/c123", want: false},
		{name: "dated synced", resourceName: "people/c999", want: true},
		{name: "not synced", resourceName: "people/c000", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContactSynced(syncedMap, "user@gmail.com", tt.resourceName); got != tt.want {
				t.Fatalf("IsContactSynced() = %v, want %v", got, tt.want)
			}
		})
	}
}
