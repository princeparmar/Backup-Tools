package google

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v2"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	"github.com/labstack/echo/v4"
)

type GPotosClient struct {
	*gphotos.Client
}

// PhotosFilters represents filtering options for Google Photos media items
type PhotosFilters struct {
	DateRange string `json:"date_range"` // Date range filter (today, last_7_days, etc.)
	MediaType string `json:"media_type"` // Media type filter (photos, videos, all)
}

// HasFilters returns true if any filters are set
func (f *PhotosFilters) HasFilters() bool {
	return f != nil && (f.DateRange != "" || f.MediaType != "")
}

// IsEmpty returns true if no filters are set
func (f *PhotosFilters) IsEmpty() bool {
	return f == nil || (f.DateRange == "" && f.MediaType == "")
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
// Optional dateRange parameter can be provided to filter albums by their media items' creation dates.
func (gpclient *GPotosClient) ListAlbums(c echo.Context, dateRange *string) ([]albums.Album, error) {
	allAlbums, err := gpclient.Albums.List(context.Background())
	if err != nil {
		return nil, err
	}

	// If no date filter is provided, return all albums
	if dateRange == nil || *dateRange == "" {
		return allAlbums, nil
	}

	// Parse date range
	startDate, endDate, err := parseDateRange(*dateRange)
	if err != nil {
		return nil, err
	}

	// Filter albums based on their media items' creation dates
	var filteredAlbums []albums.Album
	for _, album := range allAlbums {
		// Get media items from this album
		mediaItems, err := gpclient.MediaItems.ListByAlbum(context.Background(), album.ID)
		if err != nil {
			continue // Skip albums we can't access
		}

		// Check if any media item in the album falls within the date range
		hasMatchingItems := false
		for _, item := range mediaItems {
			if item.MediaMetadata.CreationTime != "" {
				creationTime, err := time.Parse(time.RFC3339, item.MediaMetadata.CreationTime)
				if err != nil {
					continue
				}

				if (startDate.IsZero() || !creationTime.Before(startDate)) &&
					(endDate.IsZero() || !creationTime.After(endDate)) {
					hasMatchingItems = true
					break
				}
			}
		}

		if hasMatchingItems {
			filteredAlbums = append(filteredAlbums, album)
		}
	}

	return filteredAlbums, nil
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
// Optional filters parameter can be provided to filter the results.
func (gpclient *GPotosClient) ListFilesFromAlbum(ctx context.Context, albumID string, filters *PhotosFilters) ([]media_items.MediaItem, error) {
	files, err := gpclient.MediaItems.ListByAlbum(ctx, albumID)
	if err != nil {
		return nil, err
	}

	if filters.IsEmpty() {
		return files, nil
	}

	var filtered []media_items.MediaItem
	for _, file := range files {
		if passesFilters(file, filters) {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}

// passesFilters checks if a media item passes all the specified filters
func passesFilters(file media_items.MediaItem, filters *PhotosFilters) bool {
	if filters == nil {
		return true
	}

	if filters.DateRange != "" && !passesDateFilter(file, filters.DateRange) {
		return false
	}
	if filters.MediaType != "" && !passesMediaTypeFilter(file, filters.MediaType) {
		return false
	}
	return true
}

// passesDateFilter checks if media item creation date falls within the specified range
func passesDateFilter(file media_items.MediaItem, dateRange string) bool {
	if file.MediaMetadata.CreationTime == "" {
		return false
	}
	creationTime, err := time.Parse(time.RFC3339, file.MediaMetadata.CreationTime)
	if err != nil {
		return false
	}
	startDate, endDate, err := parseDateRange(dateRange)
	if err != nil {
		return false
	}
	return (startDate.IsZero() || !creationTime.Before(startDate)) &&
		(endDate.IsZero() || !creationTime.After(endDate))
}

// passesMediaTypeFilter checks if media item matches the specified media type
func passesMediaTypeFilter(file media_items.MediaItem, mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "photos":
		return strings.HasPrefix(file.MimeType, "image/")
	case "videos":
		return strings.HasPrefix(file.MimeType, "video/")
	default:
		return true // "all" or unknown types
	}
}

// Func takes Google Photos item ID and returns file data.
func (gpclient *GPotosClient) GetPhoto(ctx context.Context, photoID string) (*media_items.MediaItem, error) {
	photo, err := gpclient.MediaItems.Get(ctx, photoID)
	if err != nil {
		return nil, err
	}

	return photo, nil
}

// parseDateRange parses date range string and returns start and end dates
func parseDateRange(dateRange string) (time.Time, time.Time, error) {
	var startDate, endDate time.Time

	switch strings.ToLower(dateRange) {
	case "today":
		startDate = getPhotosTodayStart()
		endDate = getPhotosTodayEnd()
	case "yesterday":
		startDate = getPhotosYesterdayStart()
		endDate = getPhotosYesterdayEnd()
	case "last_7_days", "last7days", "7days":
		startDate = getPhotosDaysAgoStart(7)
		endDate = time.Now()
	case "last_30_days", "last30days", "30days":
		startDate = getPhotosDaysAgoStart(30)
		endDate = time.Now()
	case "this_year", "thisyear":
		startDate = getPhotosYearStart()
		endDate = time.Now()
	case "last_year", "lastyear":
		startDate = getPhotosLastYearStart()
		endDate = getPhotosYearStart()
	case "custom_date_range":
		// For custom date range, we'll need additional parameters
		// This will be handled by the custom date range parsing
		return time.Time{}, time.Time{}, fmt.Errorf("custom date range requires additional parameters")
	default:
		// Handle custom date ranges like "2024-01-01" or "2024-01-01,2024-12-31"
		return parseCustomDateRange(dateRange)
	}

	return startDate, endDate, nil
}

// parseCustomDateRange handles custom date range formats
func parseCustomDateRange(dateRange string) (time.Time, time.Time, error) {
	dates := strings.Split(dateRange, ",")
	if len(dates) == 1 {
		// Single date - from this date to now
		startDate, err := time.Parse("2006-01-02", strings.TrimSpace(dates[0]))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		return startDate, time.Now(), nil
	} else if len(dates) == 2 {
		// Date range
		startDate, err := time.Parse("2006-01-02", strings.TrimSpace(dates[0]))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		endDate, err := time.Parse("2006-01-02", strings.TrimSpace(dates[1]))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		// Set end date to end of day
		endDate = endDate.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		return startDate, endDate, nil
	}

	return time.Time{}, time.Time{}, fmt.Errorf("invalid date range format")
}

// Helper functions for date calculations (Photos specific)
func getPhotosTodayStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

func getPhotosTodayEnd() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 999999999, now.Location())
}

func getPhotosYesterdayStart() time.Time {
	yesterday := time.Now().AddDate(0, 0, -1)
	return time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, yesterday.Location())
}

func getPhotosYesterdayEnd() time.Time {
	yesterday := time.Now().AddDate(0, 0, -1)
	return time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 23, 59, 59, 999999999, yesterday.Location())
}

func getPhotosDaysAgoStart(days int) time.Time {
	date := time.Now().AddDate(0, 0, -days)
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
}

func getPhotosYearStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
}

func getPhotosLastYearStart() time.Time {
	now := time.Now()
	return time.Date(now.Year()-1, 1, 1, 0, 0, 0, 0, now.Location())
}
