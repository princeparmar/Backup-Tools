package crons

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v2"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	photoslibrary "github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
)

// GooglePhotosProcessor handles Google Photos scheduled tasks
type GooglePhotosProcessor struct {
	BaseProcessor
}

func NewScheduledGooglePhotosProcessor(deps *TaskProcessorDeps) *GooglePhotosProcessor {
	return &GooglePhotosProcessor{BaseProcessor{Deps: deps}}
}

func (g *GooglePhotosProcessor) Run(input ScheduledTaskProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Process webhook events using access grant from database (auto-sync)
	// Run in background, non-blocking - process at beginning so webhooks are handled even if sync fails
	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Deps.Store, input.Task.StorxToken, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync",
				logger.ErrorField(processErr))
		}
	}()

	if err = input.HeartBeatFunc(); err != nil {
		return err
	}

	accessToken, ok := input.InputData["access_token"].(string)
	if !ok {
		return g.handleError(input.Task, "Access token not found in task data", nil)
	}

	photosClient, err := g.createPhotosClient(accessToken)
	if err != nil {
		return g.handleError(input.Task, fmt.Sprintf("Failed to create Google Photos client: %s", err), nil)
	}

	// Create placeholder and get existing photos
	if err := g.setupStorage(input.Task, satellite.ReserveBucket_Photos); err != nil {
		return err
	}

	// Get synced objects from database instead of listing from Satellite
	// Uses common handler.GetSyncedObjectsWithPrefix which ensures bucket exists and queries database
	photoListFromBucket, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Deps.Store, input.Task.StorxToken, satellite.ReserveBucket_Photos, input.Task.LoginId+"/", input.Task.UserID, "google", "photos")
	if err != nil {
		return g.handleError(input.Task, fmt.Sprintf("Failed to list existing photos: %s", err), nil)
	}

	return g.processPhotos(ctx, input, photosClient, photoListFromBucket)
}

func (g *GooglePhotosProcessor) createPhotosClient(accessToken string) (*google.GPotosClient, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %v", err)
	}

	config, err := oauth2google.ConfigFromJSON(b, photoslibrary.PhotoslibraryReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %v", err)
	}

	token := &oauth2.Token{AccessToken: accessToken}
	httpClient := config.Client(context.Background(), token)

	// Create gphotos client
	gphotosClient, err := gphotos.NewClient(httpClient)
	if err != nil {
		return nil, fmt.Errorf("unable to create GPhotos client: %v", err)
	}

	// Create photoslibrary service
	service, err := photoslibrary.New(httpClient)
	if err != nil {
		return nil, fmt.Errorf("unable to create Photos service: %v", err)
	}

	return &google.GPotosClient{
		Client:     gphotosClient,
		HTTPClient: httpClient,
		Service:    service,
	}, nil
}

func (g *GooglePhotosProcessor) setupStorage(task *repo.ScheduledTasks, bucket string) error {
	return handler.UploadObjectAndSync(context.Background(), g.Deps.Store, task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil, task.UserID)
}

func (g *GooglePhotosProcessor) processPhotos(ctx context.Context, input ScheduledTaskProcessorInput, client *google.GPotosClient, existingPhotos map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedPhotos []string

	// Get pending photo/album IDs
	pendingItems := input.Memory["pending"]
	if pendingItems == nil {
		pendingItems = []string{}
	}

	// Deduplicate pending items to prevent processing the same item multiple times
	seen := make(map[string]bool)
	var uniquePendingItems []string
	for _, itemID := range pendingItems {
		itemID = strings.TrimSpace(itemID)
		if itemID != "" && !seen[itemID] {
			seen[itemID] = true
			uniquePendingItems = append(uniquePendingItems, itemID)
		}
	}

	// Use a dynamic list that can grow as we discover photos in albums
	processingQueue := uniquePendingItems
	// Update memory with deduplicated list
	input.Memory["pending"] = uniquePendingItems

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	// Process items - queue grows as photos are discovered in albums
	for i := 0; i < len(processingQueue); i++ {
		itemID := processingQueue[i]
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		// Check if this is an album ID (try to get album info first)
		album, err := client.Albums.GetById(ctx, itemID)
		if err == nil && album != nil {
			// This is an album - discover all photos in it
			nestedPhotoIDs, err := g.discoverPhotosInAlbum(ctx, client, album.ID)
			if err == nil && len(nestedPhotoIDs) > 0 {
				// Add nested photos to processing queue for immediate processing in this run
				for _, nestedID := range nestedPhotoIDs {
					nestedID = strings.TrimSpace(nestedID)
					if nestedID != "" && !seen[nestedID] {
						seen[nestedID] = true
						processingQueue = append(processingQueue, nestedID)
					}
				}
				// Also update memory for persistence
				currentPending := input.Memory["pending"]
				if currentPending == nil {
					currentPending = []string{}
				}
				input.Memory["pending"] = append(currentPending, nestedPhotoIDs...)
			}

			moveEmailToStatus(&input.Memory, itemID, "pending", "synced")
			successCount++
			continue
		}

		// Not an album - treat as photo ID
		mediaItem, err := client.GetPhoto(ctx, itemID)
		if err != nil {
			failedPhotos, failedCount = g.trackFailure(itemID, err, failedPhotos, failedCount, input)
			continue
		}

		// Use collision-safe filename format: photoID_filename to avoid duplicates
		photoPath := fmt.Sprintf("%s/%s_%s", input.Task.LoginId, mediaItem.ID, mediaItem.Filename)
		if _, exists := existingPhotos[photoPath]; exists {
			moveEmailToStatus(&input.Memory, itemID, "pending", "skipped: already exists in storage")
			successCount++
			continue
		}

		if err := g.uploadPhoto(ctx, input, mediaItem, photoPath); err != nil {
			failedPhotos, failedCount = g.trackFailure(itemID, err, failedPhotos, failedCount, input)
		} else {
			moveEmailToStatus(&input.Memory, itemID, "pending", "synced")
			successCount++
		}
	}

	// Clear pending array after processing
	input.Memory["pending"] = []string{}

	return g.updateTaskStats(&input, successCount, failedCount, failedPhotos)
}

func (g *GooglePhotosProcessor) trackFailure(photoID string, err error, failedPhotos []string, failedCount int, input ScheduledTaskProcessorInput) ([]string, int) {
	failedPhotos = append(failedPhotos, fmt.Sprintf("Photo ID %s: %v", photoID, err))
	failedCount++
	moveEmailToStatus(&input.Memory, photoID, "pending", "error")
	return failedPhotos, failedCount
}

func (g *GooglePhotosProcessor) uploadPhoto(ctx context.Context, input ScheduledTaskProcessorInput, mediaItem *media_items.MediaItem, photoPath string) error {
	// Google Photos requires =d query param to force binary download
	downloadURL := mediaItem.BaseURL + "=d"

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download photo: %v", err)
	}
	defer resp.Body.Close()

	// Validate HTTP status code
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download photo, status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read photo data: %v", err)
	}

	// Upload to satellite and sync to database
	return handler.UploadObjectAndSync(ctx, input.Deps.Store, input.Task.StorxToken, satellite.ReserveBucket_Photos, photoPath, body, input.Task.UserID)
}

// discoverPhotosInAlbum recursively discovers all photos inside an album
func (g *GooglePhotosProcessor) discoverPhotosInAlbum(ctx context.Context, client *google.GPotosClient, albumID string) ([]string, error) {
	var allPhotoIDs []string
	pageToken := ""

	for {
		searchReq := &photoslibrary.SearchMediaItemsRequest{
			AlbumId:   albumID,
			PageSize:  100,
			PageToken: pageToken,
		}

		response, err := client.Service.MediaItems.Search(searchReq).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list photos in album: %w", err)
		}

		for _, item := range response.MediaItems {
			allPhotoIDs = append(allPhotoIDs, item.Id)
		}

		if response.NextPageToken == "" {
			break
		}
		pageToken = response.NextPageToken
	}

	return allPhotoIDs, nil
}
