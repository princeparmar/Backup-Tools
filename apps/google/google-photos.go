package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v2"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	photoslibrary "github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"github.com/labstack/echo/v4"

	"github.com/StorX2-0/Backup-Tools/pkg/utils"
)

type GPotosClient struct {
	*gphotos.Client
	HTTPClient *http.Client
	Service    *photoslibrary.Service
}

type PhotosFilters struct {
	DateRange      string `json:"date_range"`
	MediaType      string `json:"media_type"`
	Limit          int64  `json:"limit,omitempty"`
	PageToken      string `json:"page_token,omitempty"`
	ExcludeAppData bool   `json:"exclude_app_data,omitempty"`
}

type PaginatedAlbumsResponse struct {
	Albums        []albums.Album `json:"albums"`
	NextPageToken string         `json:"next_page_token,omitempty"`
	Limit         int64          `json:"limit"`
	TotalAlbums   int64          `json:"total_albums"`
}

type PaginatedMediaItemsResponse struct {
	MediaItems    []media_items.MediaItem `json:"mediaItems"`
	NextPageToken string                  `json:"next_page_token,omitempty"`
	Limit         int64                   `json:"limit"`
	TotalItems    int64                   `json:"total_items"`
}

func DecodeURLPhotosFilter(urlEncodedFilter string) (*PhotosFilters, error) {
	decodedFilter, err := url.QueryUnescape(urlEncodedFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to URL decode filter: %w", err)
	}

	var filter PhotosFilters
	if err := json.Unmarshal([]byte(decodedFilter), &filter); err != nil {
		return nil, fmt.Errorf("failed to parse filter JSON: %w", err)
	}

	return &filter, nil
}

func (f *PhotosFilters) HasFilters() bool {
	return f != nil && (f.DateRange != "" || f.MediaType != "")
}

func (f *PhotosFilters) IsEmpty() bool {
	return f == nil || (f.DateRange == "" && f.MediaType == "")
}

func NewGPhotosClient(c echo.Context) (*GPotosClient, error) {

	httpClient, err := client(c)
	if err != nil {
		return nil, err
	}

	gpclient, err := gphotos.NewClient(httpClient)
	if err != nil {
		return nil, err
	}

	service, err := photoslibrary.New(httpClient)
	if err != nil {
		return nil, err
	}

	return &GPotosClient{
		Client:     gpclient,
		HTTPClient: httpClient,
		Service:    service,
	}, nil
}

func (gpclient *GPotosClient) ListAlbums(c echo.Context) (*PaginatedAlbumsResponse, error) {

	// Parse filter parameter
	filterParam := c.QueryParam("filter")
	var filters *PhotosFilters
	var err error

	if filterParam != "" {
		filters, err = DecodeURLPhotosFilter(filterParam)
		if err != nil {
			return nil, fmt.Errorf("failed to decode URL photos filter: %w", err)
		}
	}

	// Set defaults if no filters provided
	if filters == nil {
		filters = &PhotosFilters{
			Limit: 25,
		}
	}

	// Ensure page size is within limits
	limit := utils.Min(utils.Max(filters.Limit, 1), 100)

	limit = limit + 1

	// Build the API call
	call := gpclient.Service.Albums.List().PageSize(limit)
	if filters.ExcludeAppData {
		call = call.ExcludeNonAppCreatedData()
	}
	if filters.PageToken != "" {
		call = call.PageToken(filters.PageToken)
	}

	// Execute the API call
	response, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list albums: %w", err)
	}

	return &PaginatedAlbumsResponse{
		Albums:        convertToAlbumType(response.Albums),
		NextPageToken: response.NextPageToken,
		Limit:         limit - 1,
		TotalAlbums:   int64(len(response.Albums)),
	}, nil
}

func parseIntWithLimits(value string, defaultValue, min, max int64) int64 {
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return defaultValue
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}

func convertToAlbumType(apiAlbums []*photoslibrary.Album) []albums.Album {
	result := make([]albums.Album, len(apiAlbums))
	for i, album := range apiAlbums {
		result[i] = albums.Album{
			ID:                album.Id,
			Title:             album.Title,
			ProductURL:        album.ProductUrl,
			IsWriteable:       album.IsWriteable,
			MediaItemsCount:   strconv.FormatInt(album.TotalMediaItems, 10),
			CoverPhotoBaseURL: album.CoverPhotoBaseUrl,
		}
	}
	return result
}

func (gpclient *GPotosClient) UploadFileToGPhotos(c echo.Context, filename, albumName string) error {

	ctx := context.Background()
	alb, err := gpclient.Albums.GetByTitle(ctx, albumName)
	if err != nil {
		alb, err = gpclient.Albums.Create(ctx, albumName)
		if err != nil {
			return fmt.Errorf("failed to create album: %w", err)
		}
	}

	filepath := path.Join("./cache", filename)
	_, err = gpclient.UploadFileToAlbum(ctx, alb.ID, filepath)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}

	return nil
}

func (gpclient *GPotosClient) ListFilesFromAlbum(ctx context.Context, albumID string, filters *PhotosFilters) (*PaginatedMediaItemsResponse, error) {

	limit := parseIntWithLimits("", 25, 1, 100)
	if filters != nil && filters.Limit > 0 {
		if filters.Limit > 100 {
			limit = 100
		} else {
			limit = filters.Limit
		}
	}

	searchReq := &photoslibrary.SearchMediaItemsRequest{
		AlbumId:   albumID,
		PageSize:  limit,
		PageToken: getPageToken(filters),
	}

	response, err := gpclient.Service.MediaItems.Search(searchReq).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to search media items: %w", err)
	}

	filteredItems := filterMediaItems(response.MediaItems, filters)

	return &PaginatedMediaItemsResponse{
		MediaItems:    filteredItems,
		NextPageToken: response.NextPageToken,
		Limit:         limit,
		TotalItems:    int64(len(filteredItems)),
	}, nil
}

func getPageToken(filters *PhotosFilters) string {
	if filters != nil {
		return filters.PageToken
	}
	return ""
}

func filterMediaItems(apiItems []*photoslibrary.MediaItem, filters *PhotosFilters) []media_items.MediaItem {
	var result []media_items.MediaItem
	for _, item := range apiItems {
		mediaItem := convertToMediaItem(item)
		if filters == nil || filters.IsEmpty() || passesFilters(mediaItem, filters) {
			result = append(result, mediaItem)
		}
	}
	return result
}

func convertToMediaItem(item *photoslibrary.MediaItem) media_items.MediaItem {
	return media_items.MediaItem{
		ID:         item.Id,
		ProductURL: item.ProductUrl,
		BaseURL:    item.BaseUrl,
		MimeType:   item.MimeType,
		MediaMetadata: media_items.MediaMetadata{
			CreationTime: item.MediaMetadata.CreationTime,
			Width:        strconv.FormatInt(item.MediaMetadata.Width, 10),
			Height:       strconv.FormatInt(item.MediaMetadata.Height, 10),
		},
		Filename: item.Filename,
	}
}

func passesFilters(file media_items.MediaItem, filters *PhotosFilters) bool {
	if filters.DateRange != "" && !passesDateFilter(file, filters.DateRange) {
		return false
	}
	if filters.MediaType != "" && !passesMediaTypeFilter(file, filters.MediaType) {
		return false
	}
	return true
}

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
	return (startDate.IsZero() || creationTime.After(startDate)) &&
		(endDate.IsZero() || creationTime.Before(endDate))
}

func passesMediaTypeFilter(file media_items.MediaItem, mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "photos":
		return strings.HasPrefix(file.MimeType, "image/")
	case "videos":
		return strings.HasPrefix(file.MimeType, "video/")
	default:
		return true
	}
}

func (gpclient *GPotosClient) GetPhoto(ctx context.Context, photoID string) (*media_items.MediaItem, error) {
	photo, err := gpclient.MediaItems.Get(ctx, photoID)
	if err != nil {
		return nil, fmt.Errorf("failed to get photo: %w", err)
	}
	return photo, nil
}

func parseDateRange(dateRange string) (time.Time, time.Time, error) {
	switch strings.ToLower(dateRange) {
	case "today":
		return getDayRange(0)
	case "yesterday":
		return getDayRange(-1)
	case "last_7_days", "last7days", "7days":
		return getDaysRange(7)
	case "last_30_days", "last30days", "30days":
		return getDaysRange(30)
	case "this_year", "thisyear":
		return getYearRange(time.Now().Year())
	case "last_year", "lastyear":
		return getYearRange(time.Now().Year() - 1)
	default:
		return parseCustomDateRange(dateRange)
	}
}

func getDayRange(daysOffset int) (time.Time, time.Time, error) {
	date := time.Now().AddDate(0, 0, daysOffset)
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	end := start.Add(24*time.Hour - time.Nanosecond)
	return start, end, nil
}

func getDaysRange(days int) (time.Time, time.Time, error) {
	end := time.Now()
	start := end.AddDate(0, 0, -days)
	return start, end, nil
}

func getYearRange(year int) (time.Time, time.Time, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, 12, 31, 23, 59, 59, 999999999, time.UTC)
	return start, end, nil
}

func parseCustomDateRange(dateRange string) (time.Time, time.Time, error) {
	dates := strings.Split(dateRange, ",")
	if len(dates) == 1 {
		startDate, err := time.Parse("2006-01-02", strings.TrimSpace(dates[0]))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		return startDate, time.Now(), nil
	}
	if len(dates) == 2 {
		startDate, err := time.Parse("2006-01-02", strings.TrimSpace(dates[0]))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		endDate, err := time.Parse("2006-01-02", strings.TrimSpace(dates[1]))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		return startDate, endDate.Add(23*time.Hour + 59*time.Minute + 59*time.Second), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("invalid date range format")
}
