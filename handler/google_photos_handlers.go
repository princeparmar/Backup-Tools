package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/google/uuid"
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
// AlbumWithSyncStatus wraps the original Album struct with a Synced boolean
type AlbumWithSyncStatus struct {
	albums.Album
	Synced bool `json:"synced"`
}

// PaginatedAlbumsResponseWithSync wraps the paginated response with the new AlbumWithSyncStatus struct
type PaginatedAlbumsResponseWithSync struct {
	Albums        []AlbumWithSyncStatus `json:"albums"`
	NextPageToken string                `json:"next_page_token,omitempty"`
	Limit         int64                 `json:"limit"`
	TotalAlbums   int64                 `json:"total_albums"`
}

// PhotosService provides consolidated Google Photos operations
type PhotosService struct {
	client      *google.GPotosClient
	accessGrant string
	userEmail   string
}

// NewPhotosService creates a new PhotosService instance
func NewPhotosService(client *google.GPotosClient, accessGrant, userEmail string) *PhotosService {
	return &PhotosService{
		client:      client,
		accessGrant: accessGrant,
		userEmail:   userEmail,
	}
}

// Helper to build consistent album folder name
func buildAlbumFolderName(albumID, title string) string {
	safe := strings.ReplaceAll(title, "/", "_")
	safe = strings.ReplaceAll(safe, "|", "-")
	return fmt.Sprintf("%s_%s", albumID, safe)
}

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
			// Fix 4: Safe goroutine context with timeout
			processCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

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

	// Get user email and userID for sync checking
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

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "authentication failed",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	// Get synced objects from database
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Photos, "google", "photos")
	if err != nil {
		logger.Warn(ctx, "Failed to get synced objects from database, continuing with empty map",
			logger.String("user_id", userID),
			logger.String("bucket", satellite.ReserveBucket_Photos),
			logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}

	albumFileCounts := make(map[string]int)
	// Map: "AlbumID_SafeTitle" -> true if placeholder exists
	albumPlaceholderExists := make(map[string]bool)

	// Fix 1: Use LoginId consistently (assuming Email is used as LoginId in this context based on userDetails)
	// If LoginId is different, it should be fetched from userDetails or another source.
	// In this codebase, userDetails.Email seems to be the primary identifier used for paths.
	prefix := userDetails.Email + "/"

	for _, obj := range syncedObjects {
		// obj.ObjectKey format: "email/AlbumID_Title/PhotoID_Name"
		if !strings.HasPrefix(obj.ObjectKey, prefix) {
			continue
		}

		relPath := strings.TrimPrefix(obj.ObjectKey, prefix)
		// relPath format: "AlbumID_Title/PhotoID_Name" or just "filename" (direct upload)

		parts := strings.SplitN(relPath, "/", 2)
		if len(parts) != 2 {
			continue // Not in an album folder (e.g. direct sync)
		}

		albumFolder := parts[0]
		filename := parts[1]

		if filename == ".file_placeholder" {
			albumPlaceholderExists[albumFolder] = true
		} else {
			albumFileCounts[albumFolder]++
		}
	}

	var responseAlbums []AlbumWithSyncStatus
	for _, alb := range response.Albums {
		// Fix 3: Use centralized album folder builder
		folderName := buildAlbumFolderName(alb.ID, alb.Title)

		// Logic:
		// 1. Check if album placeholder exists in DB. If NOT -> Not Synced.
		// 2. If placeholder exists:
		//    a. If Google Album is empty (0 items) -> Synced.
		//    b. If Google Album has items -> Check if DB count >= Google count.

		isSynced := false
		if albumPlaceholderExists[folderName] {
			// Fix 2: Safe MediaItemsCount parsing
			var googleCount int
			if alb.MediaItemsCount != "" {
				if cnt, err := strconv.Atoi(alb.MediaItemsCount); err == nil {
					googleCount = cnt
				}
			}

			if googleCount == 0 {
				// Empty album, but placeholder exists -> Synced
				isSynced = true
			} else {
				// Non-empty album, check file counts
				syncedCount := albumFileCounts[folderName]
				isSynced = syncedCount >= googleCount
			}
		}

		responseAlbums = append(responseAlbums, AlbumWithSyncStatus{
			Album:  alb,
			Synced: isSynced,
		})
	}

	return c.JSON(http.StatusOK, PaginatedAlbumsResponseWithSync{
		Albums:        responseAlbums,
		NextPageToken: response.NextPageToken,
		Limit:         response.Limit,
		TotalAlbums:   response.TotalAlbums,
	})
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
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Extract access grant early for webhook processing
	if accesGrant != "" {
		go func() {
			processCtx := context.Background()
			if processErr := ProcessWebhookEvents(processCtx, database, accesGrant, 100); processErr != nil {
				logger.Warn(processCtx, "Failed to process webhook events from listing route",
					logger.ErrorField(processErr))
			}
		}()
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

	// Get user email and userID for sync checking
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

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "authentication failed",
		})
	}

	// Get synced objects from database and create map for fast lookup
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Photos, "google", "photos")
	if err != nil {
		logger.Warn(ctx, "Failed to get synced objects from database, continuing with empty map",
			logger.String("user_id", userID),
			logger.String("bucket", satellite.ReserveBucket_Photos),
			logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}

	syncedMap := make(map[string]bool)
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	var photosRespJSON []*AllPhotosJSON

	// Pre-calculate sanitized album title for sync path checking
	safeAlbumTitle := strings.ReplaceAll(albm.Title, "/", "_")
	safeAlbumTitle = strings.ReplaceAll(safeAlbumTitle, "|", "-")

	for _, v := range paginatedResponse.MediaItems {
		// Check sync status - scheduled processor uses photoID_filename format, direct upload uses filename
		// Format 1: Direct upload - email/filename
		syncPath1 := userDetails.Email + "/" + v.Filename
		// Format 2: Scheduled processor (Standalone) - email/photoID_filename
		syncPath2 := fmt.Sprintf("%s/%s_%s", userDetails.Email, v.ID, v.Filename)
		// Format 3: Scheduled processor (Album) - email/AlbumID_AlbumTitle/PhotoID_Filename
		syncPath3 := fmt.Sprintf("%s/%s_%s/%s_%s", userDetails.Email, albm.ID, safeAlbumTitle, v.ID, v.Filename)

		synced := syncedMap[syncPath1] || syncedMap[syncPath2] || syncedMap[syncPath3]

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
			Synced:       synced,
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

// parseGooglePhotosKey extracts albumID, albumTitle and filename from a Satellite key
func parseGooglePhotosKey(key string) (albumID, albumTitle, filename string) {
	if key == "" {
		return
	}

	// Split into at most 3 parts: email / albumFolder / filename
	parts := strings.SplitN(key, "/", 3)

	switch len(parts) {
	case 3:
		// email/AlbumID_AlbumTitle/PhotoID_Filename
		albumFolder := parts[1]
		filename = parts[2]

		// Google Photos Album IDs are typically 76 characters.
		// Check if we have an underscore at index 76 (the separator we added).
		if len(albumFolder) > 76 && albumFolder[76] == '_' {
			albumID = albumFolder[:76]
			albumTitle = albumFolder[77:]
		} else if idx := strings.Index(albumFolder, "_"); idx > 0 {
			// Fallback to first underscore if length doesn't match
			albumID = albumFolder[:idx]
			albumTitle = albumFolder[idx+1:]
		}
		fmt.Println("Album ID: ", albumID)
		fmt.Println("Album Title: ", albumTitle)
		fmt.Println("Filename: ", filename)

	case 2:
		// email/PhotoID_Filename (standalone)
		filename = parts[1]

	default:
		// Fallback (unexpected format)
		filename = key
	}

	// Normalize filename: strip PhotoID_
	// Google Photos Media Item IDs are typically 98 characters.
	if len(filename) > 98 && filename[98] == '_' {
		filename = filename[99:]
	} else if idx := strings.Index(filename, "_"); idx > 0 {
		filename = filename[idx+1:]
	}

	return
}

// RestorePhotosFromSatellite restores photos from Satellite to Google Photos
func (s *PhotosService) RestorePhotosFromSatellite(ctx context.Context, keys []string) (*DownloadResult, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(5)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	albumCache := make(map[string]*albums.Album)
	var cacheMu sync.Mutex

	for _, key := range keys {
		if key == "" {
			continue
		}
		key := key
		g.Go(func() error {
			// 0. Check for context cancellation
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// a. Download from Satellite
			data, err := satellite.DownloadObject(ctx, s.accessGrant, satellite.ReserveBucket_Photos, key)
			if err != nil {
				logger.Error(ctx, "failed to download photo from satellite", logger.ErrorField(err), logger.String("key", key))
				failedIDs.Add(key)
				return nil
			}

			// b. Parse key
			albumID, albumTitle, filename := parseGooglePhotosKey(key)
			fmt.Println("Album ID: ", albumID)
			fmt.Println("Album Title: ", albumTitle)
			fmt.Println("Filename: ", filename)

			// c. Save to unique temp file to avoid concurrent collision
			tempDir := filepath.Join("./cache", utils.CreateUserTempCacheFolder(), uuid.NewString())
			if err := os.MkdirAll(tempDir, 0755); err != nil {
				failedIDs.Add(key)
				return nil
			}
			defer os.RemoveAll(tempDir)

			tempPath := filepath.Join(tempDir, filename)
			if err := os.WriteFile(tempPath, data, 0644); err != nil {
				failedIDs.Add(key)
				return nil
			}

			// d. Determine target album
			targetAlbumID := ""
			if albumTitle != "" {
				cacheKey := albumID
				if cacheKey == "" {
					cacheKey = "title:" + albumTitle
				}

				// Double-check pattern for thread-safe caching
				cacheMu.Lock()
				alb, ok := albumCache[cacheKey]
				cacheMu.Unlock()

				if !ok {
					// Fetch or create album outside the lock to avoid blocking other workers
					var newAlb *albums.Album
					if albumID != "" {
						newAlb, _ = s.client.Albums.GetById(ctx, albumID)
					}

					if newAlb == nil {
						var createErr error
						newAlb, createErr = s.client.Albums.Create(ctx, albumTitle)
						if createErr != nil {
							logger.Error(ctx, "failed to create album", logger.ErrorField(createErr), logger.String("title", albumTitle))
							failedIDs.Add(key)
							return nil
						}
					}

					// Update cache with double-check
					cacheMu.Lock()
					if existing, exists := albumCache[cacheKey]; exists {
						alb = existing
					} else {
						alb = newAlb
						albumCache[cacheKey] = alb
					}
					cacheMu.Unlock()
				}
				targetAlbumID = alb.ID
				fmt.Println("Album ID22: ", targetAlbumID)
				fmt.Println("temp path: ", tempPath)
			}

			// e. Upload to Google Photos (empty targetAlbumID = Library upload)
			_, err = s.client.UploadFileToAlbum(ctx, targetAlbumID, tempPath)
			if err != nil {
				logger.Error(ctx, "failed to upload photo to Google Photos",
					logger.ErrorField(err),
					logger.String("album", albumTitle),
					logger.String("key", key))
				failedIDs.Add(key)
				return nil
			}

			processedIDs.Add(key)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &DownloadResult{
		ProcessedIDs: processedIDs.Get(),
		FailedIDs:    failedIDs.Get(),
		Message:      "restore process completed",
	}, nil
}

// HandleGooglePhotosRestore - restores photos from Satellite to Google Photos.
// It recreates albums if they don't exist based on the path structure.
func HandleGooglePhotosRestore(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// 1. Get Access Grant
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// 2. Parse and Decode Keys (Paths)
	allKeys, err := validateAndProcessRequestIDs(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// 3. Get user details for email
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		logger.Warn(ctx, "failed to get user email for restore", logger.ErrorField(err))
		userDetails = &google.GoogleAuthResponse{} // Continue with empty email if needed
	}

	// 4. Create GPhotos Client
	client, err := google.NewGPhotosClient(c)
	fmt.Println("client", client)
	if err != nil {
		if err.Error() == "token error" {
			fmt.Println("token expired", err)
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
	}

	// Send start notification
	priority := "normal"
	startData := map[string]interface{}{
		"event":      "google_photos_restore_started",
		"level":      2,
		"login_id":   userDetails.Email,
		"method":     "google_photos",
		"type":       "restore",
		"timestamp":  "now",
		"item_count": len(allKeys),
	}
	satellite.SendNotificationAsync(ctx, userID, "Google Photos Restore Started", fmt.Sprintf("Restore of %d photos for %s has started", len(allKeys), userDetails.Email), &priority, startData, nil)

	// 5. Create Photos Service and Restore
	photosService := NewPhotosService(client, accessGrant, userDetails.Email)
	result, err := photosService.RestorePhotosFromSatellite(ctx, allKeys)
	if err != nil {
		// Send failure notification
		failPriority := "high"
		failData := map[string]interface{}{
			"event":     "google_photos_restore_failed",
			"level":     4,
			"login_id":  userDetails.Email,
			"method":    "google_photos",
			"type":      "restore",
			"timestamp": "now",
			"error":     err.Error(),
		}
		satellite.SendNotificationAsync(context.Background(), userID, "Google Photos Restore Failed", fmt.Sprintf("Restore for %s failed: %v", userDetails.Email, err), &failPriority, failData, nil)

		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Send completion notification
	compPriority := "normal"
	compData := map[string]interface{}{
		"event":           "google_photos_restore_completed",
		"level":           2,
		"login_id":        userDetails.Email,
		"method":          "google_photos",
		"type":            "restore",
		"timestamp":       "now",
		"processed_count": len(result.ProcessedIDs),
		"failed_count":    len(result.FailedIDs),
	}
	satellite.SendNotificationAsync(ctx, userID, "Google Photos Restore Completed", fmt.Sprintf("Restore for %s completed. %d succeeded, %d failed", userDetails.Email, len(result.ProcessedIDs), len(result.FailedIDs)), &compPriority, compData, nil)

	return c.JSON(http.StatusOK, result)
}
