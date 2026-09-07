package google

import "testing"

func TestBuildPhotosSyncedIDSet(t *testing.T) {
	tests := []struct {
		name       string
		objectKeys map[string]bool
		email      string
		wantIDs    []string
	}{
		{
			name: "dated meta and data keys",
			objectKeys: map[string]bool{
				"user@gmail.com/meta/2026/07/21/abc123.json":        true,
				"user@gmail.com/data/2026/07/21/xyz789_holiday.jpg": true,
			},
			email:   "user@gmail.com",
			wantIDs: []string{"abc123", "xyz789"},
		},
		{
			name: "ignores undated keys",
			objectKeys: map[string]bool{
				"user@gmail.com/meta/abc123.json":        true,
				"user@gmail.com/data/xyz789_holiday.jpg": true,
			},
			email:   "user@gmail.com",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildPhotosSyncedIDSet(tt.objectKeys, tt.email)
			if len(tt.wantIDs) == 0 {
				if len(got) != 0 {
					t.Fatalf("expected empty set, got %v", got)
				}
				return
			}
			for _, id := range tt.wantIDs {
				if _, ok := got[id]; !ok {
					t.Fatalf("expected id %q in set, got %v", id, got)
				}
			}
		})
	}
}

func TestPhotosIDBasedDataKey(t *testing.T) {
	got := PhotosIDBasedDataKey("user@gmail.com", "ABC", "photo.jpg", "2026-07-21T10:00:00Z")
	want := "user@gmail.com/data/2026/07/21/ABC_photo.jpg"
	if got != want {
		t.Fatalf("PhotosIDBasedDataKey() = %q, want %q", got, want)
	}
}

func TestPageHasAnyNewPhotosItems(t *testing.T) {
	synced := map[string]struct{}{"known": {}}
	tests := []struct {
		name  string
		items []FlatPhotosMediaItem
		want  bool
	}{
		{name: "all known", items: []FlatPhotosMediaItem{{ID: "known"}}, want: false},
		{name: "has new", items: []FlatPhotosMediaItem{{ID: "known"}, {ID: "new"}}, want: true},
		{name: "empty page", items: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PageHasAnyNewPhotosItems(tt.items, synced); got != tt.want {
				t.Fatalf("PageHasAnyNewPhotosItems() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsPhotosMediaItemSynced(t *testing.T) {
	syncedMap := map[string]bool{
		"user@gmail.com/meta/2026/07/21/id4.json":    true,
		"user@gmail.com/data/2026/07/21/id5_pic.jpg": true,
		"user@gmail.com/meta/id1.json":               true,
	}
	tests := []struct {
		name     string
		id       string
		filename string
		want     bool
	}{
		{name: "dated meta synced", id: "id4", want: true},
		{name: "dated data synced", id: "id5", filename: "pic.jpg", want: true},
		{name: "undated ignored", id: "id1", want: false},
		{name: "not synced", id: "id3", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPhotosMediaItemSynced(syncedMap, "user@gmail.com", tt.id, tt.filename, "", ""); got != tt.want {
				t.Fatalf("IsPhotosMediaItemSynced() = %v, want %v", got, tt.want)
			}
		})
	}
}
