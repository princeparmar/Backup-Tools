package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/utils"

	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

// <<<<<------------ GOOGLE PHOTOS ------------>>>>>

type AlbumsJSON struct {
	Title string `json:"album_title"`
	ID    string `json:"album_id"`
	Items string `json:"items_count"`
}

// Shows list of user's Google Photos albums.
// Supports date range filtering via 'date_range' query parameter:
// - today, yesterday, last_7_days, last_30_days, this_year, last_year
// - Custom dates: "2024-01-01" or "2024-01-01,2024-12-31"
func handleListGPhotosAlbums(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// Use date filtering if date_range parameter is provided
	filterParam := c.QueryParam("filter")
	var filter *google.PhotosFilters
	if filterParam != "" {
		filter, err = google.DecodeURLPhotosFilter(filterParam)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	var albs []albums.Album

	albs, err = client.ListAlbums(c, filter)

	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	var photosListJSON []*AlbumsJSON
	for _, v := range albs {
		photosListJSON = append(photosListJSON, &AlbumsJSON{
			Title: v.Title,
			ID:    v.ID,
			Items: v.MediaItemsCount,
		})
	}

	return c.JSON(http.StatusOK, photosListJSON)

}

type PhotosJSON struct {
	Name string `json:"file_name"`
	ID   string `json:"file_id"`
}

// Shows list of user's Google Photos items in given album.
// Supports filtering via query parameters:
// - date_range: Filter by creation date (today, last_7_days, last_30_days, this_year, last_year, custom_date_range)
// - media_type: Filter by media type (all, photos, videos)
func handleListPhotosInAlbum(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	id := c.Param("ID")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	albm, err := client.Albums.GetById(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Check if any filters are provided
	filterParam := c.QueryParam("filter")

	var filters *google.PhotosFilters
	if filterParam != "" {
		filters, err = google.DecodeURLPhotosFilter(filterParam)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	// Filters are already properly parsed by DecodeURLPhotosFilter
	// No need to overwrite them with the raw filterParam

	files, err := client.ListFilesFromAlbum(c.Request().Context(), id, filters)

	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	listFromSatellite, err := satellite.ListObjects(c.Request().Context(), accesGrant, "google-photos")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to list objects from Satellite: %v", err),
		})
	}

	var photosRespJSON []*AllPhotosJSON
	for _, v := range files {
		photosRespJSON = append(photosRespJSON, &AllPhotosJSON{
			Name:         v.Filename,
			ID:           v.ID,
			Description:  v.Description,
			BaseURL:      v.BaseURL,
			ProductURL:   v.ProductURL,
			MimeType:     v.MimeType,
			AlbumName:    albm.Title,
			CreationTime: v.MediaMetadata.CreationTime,
			Width:        v.MediaMetadata.Width,
			Height:       v.MediaMetadata.Height,
			Synced:       listFromSatellite[v.Filename],
		})
	}

	return c.JSON(http.StatusOK, photosRespJSON)
}

type AllPhotosJSON struct {
	Name         string `json:"file_name"`
	ID           string `json:"file_id"`
	Description  string `json:"description"`
	BaseURL      string `json:"base_url"`
	ProductURL   string `json:"product_url"`
	MimeType     string `json:"mime_type"`
	AlbumName    string `json:"album_name"`
	CreationTime string `json:"creation_time"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	Synced       bool   `json:"synced"`
}

func handleListAllPhotos(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	albs, err := client.ListAlbums(c, nil)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	type albumData struct {
		albumTitle string
		files      []media_items.MediaItem
	}

	var finalData []albumData

	var mt sync.Mutex
	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	for _, alb := range albs {
		func(alb albums.Album) { // added this function to avoid closure issue https://stackoverflow.com/questions/26692844/captured-closure-for-loop-variable-in-go
			g.Go(func() error {
				files, err := client.ListFilesFromAlbum(ctx, alb.ID, nil)
				if err != nil {
					return err
				}

				mt.Lock()
				defer mt.Unlock()
				finalData = append(finalData, albumData{
					albumTitle: alb.Title,
					files:      files,
				})

				return nil
			})
		}(alb)
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	var photosRespJSON []*AllPhotosJSON
	for _, data := range finalData {
		for _, v := range data.files {
			photosRespJSON = append(photosRespJSON, &AllPhotosJSON{
				Name:         v.Filename,
				ID:           v.ID,
				Description:  v.Description,
				BaseURL:      v.BaseURL,
				ProductURL:   v.ProductURL,
				MimeType:     v.MimeType,
				AlbumName:    data.albumTitle,
				CreationTime: v.MediaMetadata.CreationTime,
				Width:        v.MediaMetadata.Width,
				Height:       v.MediaMetadata.Height,
			})
		}
	}

	return c.JSON(http.StatusOK, photosRespJSON)

}

// Sends photo item from Satellite to Google Photos.
func handleSendFileFromSatelliteToGooglePhotos(c echo.Context) error {
	name := c.Param("name")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	data, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Photos, name)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	path := filepath.Join("./cache", utils.CreateUserTempCacheFolder(), name)
	file, err := os.Create(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Write(data)
	file.Close()
	defer os.Remove(path)

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	err = client.UploadFileToGPhotos(c, name, "Satellite Album")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "file " + name + " was successfully uploaded from Satellite to Google Photos",
	})
}

// Sends photo item from Google Photos to Satellite.
func handleSendFileFromGooglePhotosToSatellite(c echo.Context) error {

	ids := c.FormValue("ids")
	if ids == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "ids are missing",
		})
	}
	allIDs := strings.Split(ids, ",")

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				err := uploadSingleFileFromPhotosToSatellite(ctx, client, id, accesGrant)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				processedIDs.Add(id)
				return nil
			})
		}(id)
	}

	// as we are not returning any error this should not happend in any case
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all files were successfully uploaded from Google Photos to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

func uploadSingleFileFromPhotosToSatellite(ctx context.Context, client *google.GPotosClient, id, accesGrant string) error {
	item, err := client.GetPhoto(ctx, id)
	if err != nil {
		return err
	}

	resp, err := http.Get(item.BaseURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return satellite.UploadObject(context.Background(), accesGrant, "google-photos", item.Filename, body)

}

func handleSendAllFilesFromGooglePhotosToSatellite(c echo.Context) error {
	id := c.FormValue("album_id")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	files, err := client.ListFilesFromAlbum(c.Request().Context(), id, nil)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	var photosRespJSON []*PhotosJSON
	for _, v := range files {
		photosRespJSON = append(photosRespJSON, &PhotosJSON{
			Name: v.Filename,
			ID:   v.ID,
		})
	}
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	for _, p := range photosRespJSON {
		err := uploadSingleFileFromPhotosToSatellite(c.Request().Context(), client, p.ID, accesGrant)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "all photos from album were successfully uploaded from Google Photos to Satellite"})
}

func handleSendListFilesFromGooglePhotosToSatellite(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	var allIDs []string
	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		// Decode JSON array from request body
		if err := json.NewDecoder(c.Request().Body).Decode(&allIDs); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid JSON format",
			})
		}
	} else {
		// Handle form data
		formIDs := c.FormValue("ids")
		allIDs = strings.Split(formIDs, ",")
	}
	for _, p := range allIDs {
		err := uploadSingleFileFromPhotosToSatellite(c.Request().Context(), client, p, accesGrant)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "all photos from album were successfully uploaded from Google Photos to Satellite"})
}
