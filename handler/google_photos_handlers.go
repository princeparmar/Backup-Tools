package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

// <<<<<------------ GOOGLE PHOTOS ------------>>>>>

// Shows list of user's Google Photos albums.
// Supports date range filtering via 'date_range' query parameter:
// - today, yesterday, last_7_days, last_30_days, this_year, last_year
// - Custom dates: "2024-01-01" or "2024-01-01,2024-12-31"
func HandleListGPhotosAlbums(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Extract access grant early for webhook processing
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant != "" {
		go func() {
			processCtx := context.Background()
			database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
			if processErr := ProcessWebhookEvents(processCtx, database, accessGrant, 100); processErr != nil {
				logger.Warn(processCtx, "Failed to process webhook events from listing route",
					logger.ErrorField(processErr))
			}
		}()
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

	// Get paginated albums response
	response, err := client.ListAlbums(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, response)

}

type PhotosJSON struct {
	Name string `json:"file_name"`
	ID   string `json:"file_id"`
}

// Shows list of user's Google Photos items in given album.
// Supports filtering via query parameters:
// - date_range: Filter by creation date (today, last_7_days, last_30_days, this_year, last_year, custom_date_range)
// - media_type: Filter by media type (all, photos, videos)
func HandleListPhotosInAlbum(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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

	paginatedResponse, err := client.ListFilesFromAlbum(c.Request().Context(), id, filters)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Get user email for sync checking
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to get user email: " + err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	listFromSatellite, listErr := satellite.ListObjectsWithPrefix(c.Request().Context(), accesGrant, "google-photos", userDetails.Email+"/")
	if listErr != nil {
		logger.Error(ctx, "Failed to list objects from satellite", logger.ErrorField(listErr))
		userFriendlyError := satellite.FormatSatelliteError(listErr)
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": userFriendlyError,
		})
	}

	var photosRespJSON []*AllPhotosJSON
	for _, v := range paginatedResponse.MediaItems {
		// Check sync status using userEmail + "/" + filename to match upload path format
		syncPath := userDetails.Email + "/" + v.Filename
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
			Synced:       listFromSatellite[syncPath],
		})
	}

	// Return paginated response
	return c.JSON(http.StatusOK, map[string]interface{}{
		"mediaItems":    photosRespJSON,
		"nextPageToken": paginatedResponse.NextPageToken,
		"limit":         paginatedResponse.Limit,
		"totalItems":    paginatedResponse.TotalItems,
	})
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

func HandleListAllPhotos(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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
	response, err := client.ListAlbums(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	albs := response.Albums

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
				paginatedResponse, err := client.ListFilesFromAlbum(ctx, alb.ID, nil)
				if err != nil {
					return err
				}
				files := paginatedResponse.MediaItems

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
func HandleSendFileFromSatelliteToGooglePhotos(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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
	file, err := utils.CreateFile(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	_, err = file.Write(data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
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
func HandleSendFileFromGooglePhotosToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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

	// Get user email to create user-specific directory
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to get user email: " + err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				err := uploadSingleFileFromPhotosToSatellite(ctx, client, id, accesGrant, userDetails.Email, database)
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

func uploadSingleFileFromPhotosToSatellite(ctx context.Context, client *google.GPotosClient, id, accesGrant, userEmail string, database *db.PostgresDb) error {
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

	// Create path with user email directory: userEmail/filename
	photoPath := userEmail + "/" + item.Filename

	// Use helper function to upload and sync to database
	// Source and Type are automatically derived from bucket name (hardcoded)
	// Source: "google", Type: "photos" (from bucket name "google-photos")
	return UploadObjectAndSync(ctx, database, accesGrant, "google-photos", photoPath, body, userEmail)
}

func HandleSendAllFilesFromGooglePhotosToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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
	paginatedResponse, err := client.ListFilesFromAlbum(c.Request().Context(), id, nil)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	files := paginatedResponse.MediaItems

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

	// Get user email to create user-specific directory
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to get user email: " + err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	for _, p := range photosRespJSON {
		err := uploadSingleFileFromPhotosToSatellite(c.Request().Context(), client, p.ID, accesGrant, userDetails.Email, database)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "all photos from album were successfully uploaded from Google Photos to Satellite"})
}

func HandleSendListFilesFromGooglePhotosToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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

	// Get user email to create user-specific directory
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to get user email: " + err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

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
		err := uploadSingleFileFromPhotosToSatellite(c.Request().Context(), client, p, accesGrant, userDetails.Email, database)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "all photos from album were successfully uploaded from Google Photos to Satellite"})
}
