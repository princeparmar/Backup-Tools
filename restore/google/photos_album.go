package googlestore

import (
	"context"

	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/StorX2-0/Backup-Tools/restore"
)

// GetOrCreateAlbum caches Photos albums on the shared restore deps.
func GetOrCreateAlbum(ctx context.Context, d *restore.RestoreDeps, albumID, albumTitle string) (*albums.Album, error) {
	cacheKey := albumID
	if cacheKey == "" {
		cacheKey = "title:" + albumTitle
	}
	d.PhotosAlbumMu.Lock()
	if alb, ok := d.PhotosAlbumCache[cacheKey]; ok {
		d.PhotosAlbumMu.Unlock()
		return alb, nil
	}
	d.PhotosAlbumMu.Unlock()

	var alb *albums.Album
	var err error
	if albumID != "" {
		alb, _ = d.PhotosClient.Albums.GetById(ctx, albumID)
	}
	if alb == nil {
		alb, err = d.PhotosClient.Albums.Create(ctx, albumTitle)
		if err != nil {
			return nil, err
		}
	}

	d.PhotosAlbumMu.Lock()
	d.PhotosAlbumCache[cacheKey] = alb
	d.PhotosAlbumMu.Unlock()
	return alb, nil
}
