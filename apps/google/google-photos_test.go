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
			name: "meta and data keys with filename in data path",
			objectKeys: map[string]bool{
				"user@gmail.com/meta/abc123.json":           true,
				"user@gmail.com/data/xyz789_holiday.jpg":    true,
			},
			email:   "user@gmail.com",
			wantIDs: []string{"abc123", "xyz789"},
		},
		{
			name: "legacy bare data id",
			objectKeys: map[string]bool{
				"user@gmail.com/data/xyz789": true,
			},
			email:   "user@gmail.com",
			wantIDs: []string{"xyz789"},
		},
		{
			name: "legacy standalone path",
			objectKeys: map[string]bool{
				"user@gmail.com/PhotoID123_filename.jpg": true,
			},
			email:   "user@gmail.com",
			wantIDs: []string{"PhotoID123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildPhotosSyncedIDSet(tt.objectKeys, tt.email)
			for _, id := range tt.wantIDs {
				if _, ok := got[id]; !ok {
					t.Fatalf("expected id %q in set, got %v", id, got)
				}
			}
		})
	}
}

func TestPhotosIDBasedDataKey(t *testing.T) {
	got := PhotosIDBasedDataKey("user@gmail.com", "ABC", "photo.jpg")
	want := "user@gmail.com/data/ABC_photo.jpg"
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
		"user@gmail.com/meta/id1.json":      true,
		"user@gmail.com/data/id2_photo.jpg": true,
	}
	tests := []struct {
		name     string
		id       string
		filename string
		want     bool
	}{
		{name: "id meta synced", id: "id1", want: true},
		{name: "id data synced", id: "id2", filename: "photo.jpg", want: true},
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
