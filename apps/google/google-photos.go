package google

import (
	"context"
	"path"

	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v2"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	"github.com/labstack/echo/v4"
)

type GPotosClient struct {
	*gphotos.Client
}

func NewGPhotosClient(c echo.Context) (*GPotosClient, error) {
	client, err := client(c)
	if err != nil {
		return nil, err
	}

	gpclient, err := gphotos.NewClient(client)
	if err != nil {
		return nil, err
	}
	return &GPotosClient{gpclient}, nil
}

// Function returns all the albums that connected to user google account.
func (gpclient *GPotosClient) ListAlbums(c echo.Context) ([]albums.Album, error) {
	albums, err := gpclient.Albums.List(context.Background())
	if err != nil {
		return nil, err
	}

	return albums, nil
}

// Func takes file and uploads into the given album in Google Photos.
func (gpclient *GPotosClient) UploadFileToGPhotos(c echo.Context, filename, albumName string) error {
	alb, err := gpclient.Albums.GetByTitle(context.Background(), albumName)
	if err != nil {
		alb, err = gpclient.Albums.Create(context.Background(), albumName)
		if err != nil {
			return err
		}
	}

	filepath := path.Join("./cache", filename)
	_, err = gpclient.UploadFileToAlbum(context.Background(), alb.ID, filepath)
	if err != nil {
		return err
	}

	return nil
}

// Func takes Google Photos album ID and returns files data in this album.
func (gpclient *GPotosClient) ListFilesFromAlbum(ctx context.Context, albumID string) ([]media_items.MediaItem, error) {
	files, err := gpclient.MediaItems.ListByAlbum(ctx, albumID)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Func takes Google Photos item ID and returns file data.
func (gpclient *GPotosClient) GetPhoto(c echo.Context, photoID string) (*media_items.MediaItem, error) {
	photo, err := gpclient.MediaItems.Get(context.Background(), photoID)
	if err != nil {
		return nil, err
	}

	return photo, nil
}
