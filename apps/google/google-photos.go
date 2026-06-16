package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
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

// FlatPhotosMediaResponse is the flat library listing page (cron + GET /google/photos-flat-media).
type FlatPhotosMediaResponse struct {
	MediaItems    []FlatPhotosMediaItem `json:"mediaItems"`
	NextPageToken string                `json:"nextPageToken"`
	NextPageTokenLegacy string          `json:"next_page_token,omitempty"`
}

// FlatPhotosMediaItem is a slim media item for flat library listing.
type FlatPhotosMediaItem struct {
	ID           string `json:"id"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	CreationTime string `json:"creation_time"`
	BaseURL      string `json:"base_url"`
	ProductURL   string `json:"product_url"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	Synced       bool   `json:"synced,omitempty"`
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

	return newGPotosClientFromHTTPClient(httpClient)
}

// NewGPhotosClientUsingToken builds a Photos client using the access token as-is.
func NewGPhotosClientUsingToken(accessToken string) (*GPotosClient, error) {
	httpClient, err := clientUsingToken(accessToken)
	if err != nil {
		return nil, err
	}
	return newGPotosClientFromHTTPClient(httpClient)
}

// NewGPhotosClientForRestore builds a Photos client for restore (requires photoslibrary.appendonly on token).
func NewGPhotosClientForRestore(accessToken string) (*GPotosClient, error) {
	httpClient, err := clientUsingTokenScopes(accessToken, restorePhotosScope)
	if err != nil {
		return nil, err
	}
	return newGPotosClientFromHTTPClient(httpClient)
}

func newGPotosClientFromHTTPClient(httpClient *http.Client) (*GPotosClient, error) {
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

const flatPhotosPageSize = 100

// ListAllMediaItemsFlat returns a paginated flat library listing (no album/filters).
func ListAllMediaItemsFlat(c echo.Context, pageToken string) (*FlatPhotosMediaResponse, error) {
	client, err := NewGPhotosClient(c)
	if err != nil {
		return nil, err
	}
	return ListAllMediaItemsFlatWithService(client.Service, pageToken)
}

// ListAllMediaItemsFlatWithService is shared by HTTP route and cron flow.
func ListAllMediaItemsFlatWithService(service *photoslibrary.Service, pageToken string) (*FlatPhotosMediaResponse, error) {
	if service == nil {
		return nil, fmt.Errorf("photos service is nil")
	}
	searchReq := &photoslibrary.SearchMediaItemsRequest{
		PageSize:  flatPhotosPageSize,
		PageToken: strings.TrimSpace(pageToken),
	}
	response, err := service.MediaItems.Search(searchReq).Do()
	if err != nil {
		return nil, fmt.Errorf("search media items: %w", err)
	}
	items := make([]FlatPhotosMediaItem, 0, len(response.MediaItems))
	for _, item := range response.MediaItems {
		if item == nil || strings.TrimSpace(item.Id) == "" {
			continue
		}
		items = append(items, flatPhotosMediaItemFromAPI(item))
	}
	// Sorting only for deterministic processing order, NOT for correctness or stop logic.
	SortFlatPhotosMediaItemsDesc(items)
	next := strings.TrimSpace(response.NextPageToken)
	return &FlatPhotosMediaResponse{
		MediaItems:          items,
		NextPageToken:       next,
		NextPageTokenLegacy: next,
	}, nil
}

func flatPhotosMediaItemFromAPI(item *photoslibrary.MediaItem) FlatPhotosMediaItem {
	out := FlatPhotosMediaItem{
		ID:         item.Id,
		Filename:   item.Filename,
		MimeType:   item.MimeType,
		BaseURL:    item.BaseUrl,
		ProductURL: item.ProductUrl,
	}
	if item.MediaMetadata != nil {
		out.CreationTime = item.MediaMetadata.CreationTime
		out.Width = strconv.FormatInt(item.MediaMetadata.Width, 10)
		out.Height = strconv.FormatInt(item.MediaMetadata.Height, 10)
	}
	return out
}

// SortFlatPhotosMediaItemsDesc sorts by creationTime DESC, then id DESC (processing order only).
func SortFlatPhotosMediaItemsDesc(items []FlatPhotosMediaItem) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.CreationTime != b.CreationTime {
			return a.CreationTime > b.CreationTime
		}
		return a.ID > b.ID
	})
}

// PageHasAnyNewPhotosItems returns true if any item ID is not in syncedSet.
func PageHasAnyNewPhotosItems(items []FlatPhotosMediaItem, syncedSet map[string]struct{}) bool {
	for i := range items {
		if _, ok := syncedSet[items[i].ID]; !ok {
			return true
		}
	}
	return false
}

// BuildPhotosSyncedIDSet builds a mediaItemId set from synced object keys under email prefix.
func BuildPhotosSyncedIDSet(objectKeys map[string]bool, emailPrefix string) map[string]struct{} {
	set := make(map[string]struct{})
	prefix := strings.TrimSpace(emailPrefix)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	metaPrefix := prefix + "meta/"
	dataPrefix := prefix + "data/"
	for key := range objectKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if id := extractPhotosIDFromMetaKey(key, metaPrefix); id != "" {
			set[id] = struct{}{}
			continue
		}
		if id := extractPhotosIDFromDataKey(key, dataPrefix); id != "" {
			set[id] = struct{}{}
			continue
		}
		if id := extractPhotosIDFromLegacyObjectKey(key, prefix); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func extractPhotosIDFromMetaKey(key, metaPrefix string) string {
	if !strings.HasPrefix(key, metaPrefix) || !strings.HasSuffix(key, ".json") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, metaPrefix), ".json")
	return strings.TrimSpace(id)
}

func extractPhotosIDFromDataKey(key, dataPrefix string) string {
	if !strings.HasPrefix(key, dataPrefix) {
		return ""
	}
	segment := strings.TrimSpace(strings.TrimPrefix(key, dataPrefix))
	if segment == "" || strings.Contains(segment, "/") {
		return ""
	}
	if id := extractMediaItemIDFromFilenameSegment(segment); id != "" {
		return id
	}
	return segment
}

// MediaItemIDFromPhotosObjectSegment extracts Google media item id from PhotoID_filename segment.
func MediaItemIDFromPhotosObjectSegment(segment string) string {
	return extractMediaItemIDFromFilenameSegment(segment)
}

func extractPhotosIDFromLegacyObjectKey(key, emailPrefix string) string {
	if emailPrefix != "" && !strings.HasPrefix(key, emailPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(key, emailPrefix)
	if rest == "" || strings.Contains(rest, "/meta/") || strings.Contains(rest, "/data/") {
		return ""
	}
	// Standalone: PhotoID_Filename or album/PhotoID_Filename — take last segment.
	if idx := strings.LastIndex(rest, "/"); idx >= 0 {
		rest = rest[idx+1:]
	}
	return extractMediaItemIDFromFilenameSegment(rest)
}

func extractMediaItemIDFromFilenameSegment(segment string) string {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return ""
	}
	// Google Photos media item IDs are typically 98 chars before underscore separator.
	if len(segment) > 98 && segment[98] == '_' {
		return segment[:98]
	}
	if idx := strings.Index(segment, "_"); idx > 0 {
		return segment[:idx]
	}
	return ""
}

// PhotosStandaloneObjectKey is legacy manual standalone storage (email root, no meta/data folders).
func PhotosStandaloneObjectKey(email, mediaItemID, filename string) string {
	return fmt.Sprintf("%s/%s_%s", strings.TrimSpace(email), strings.TrimSpace(mediaItemID), strings.TrimSpace(filename))
}

// PhotosIDBasedMetaKey returns cron metadata object key: {email}/meta/{mediaItemId}.json
func PhotosIDBasedMetaKey(email, mediaItemID string) string {
	return fmt.Sprintf("%s/meta/%s.json", strings.TrimSpace(email), strings.TrimSpace(mediaItemID))
}

// PhotosIDBasedDataKey returns cron photo bytes key: {email}/data/{mediaItemId}_{filename}
func PhotosIDBasedDataKey(email, mediaItemID, filename string) string {
	return fmt.Sprintf("%s/data/%s_%s", strings.TrimSpace(email), strings.TrimSpace(mediaItemID), strings.TrimSpace(filename))
}

// PhotosLegacyBareDataKey is older cron layout {email}/data/{id} (still recognized for dedupe/restore).
func PhotosLegacyBareDataKey(email, mediaItemID string) string {
	return fmt.Sprintf("%s/data/%s", strings.TrimSpace(email), strings.TrimSpace(mediaItemID))
}

// IsPhotosMediaItemSynced checks ID-based and legacy synced object paths.
func IsPhotosMediaItemSynced(syncedMap map[string]bool, email, mediaItemID, filename, albumID, safeAlbumTitle string) bool {
	if syncedMap == nil {
		return false
	}
	id := strings.TrimSpace(mediaItemID)
	em := strings.TrimSpace(email)
	fn := strings.TrimSpace(filename)
	if id != "" {
		if syncedMap[PhotosIDBasedMetaKey(em, id)] {
			return true
		}
		if fn != "" && syncedMap[PhotosIDBasedDataKey(em, id, fn)] {
			return true
		}
		if syncedMap[PhotosLegacyBareDataKey(em, id)] {
			return true
		}
	}
	if fn != "" {
		if syncedMap[em+"/"+fn] {
			return true
		}
		if id != "" && syncedMap[fmt.Sprintf("%s/%s_%s", em, id, fn)] {
			return true
		}
		if albumID != "" && safeAlbumTitle != "" && id != "" && fn != "" {
			if syncedMap[fmt.Sprintf("%s/%s_%s/%s_%s", em, albumID, safeAlbumTitle, id, fn)] {
				return true
			}
		}
	}
	return false
}
