package crons

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
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

	photoListFromBucket, err := satellite.ListObjectsWithPrefix(ctx, input.Task.StorxToken, satellite.ReserveBucket_Photos, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
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
	return satellite.UploadObject(context.Background(), task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil)
}

func (g *GooglePhotosProcessor) processPhotos(ctx context.Context, input ScheduledTaskProcessorInput, client *google.GPotosClient, existingPhotos map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedPhotos []string

	// Get pending photo IDs
	pendingPhotos := input.Memory["pending"]
	if pendingPhotos == nil {
		pendingPhotos = []string{}
	}

	// Deduplicate pending photos to prevent processing the same photo multiple times
	seen := make(map[string]bool)
	var uniquePendingPhotos []string
	for _, photoID := range pendingPhotos {
		photoID = strings.TrimSpace(photoID)
		if photoID != "" && !seen[photoID] {
			seen[photoID] = true
			uniquePendingPhotos = append(uniquePendingPhotos, photoID)
		}
	}
	pendingPhotos = uniquePendingPhotos
	// Update memory with deduplicated list
	input.Memory["pending"] = uniquePendingPhotos

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	for _, photoID := range pendingPhotos {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		mediaItem, err := client.GetPhoto(ctx, photoID)
		if err != nil {
			failedPhotos, failedCount = g.trackFailure(photoID, err, failedPhotos, failedCount, input)
			continue
		}

		// Use collision-safe filename format: photoID_filename to avoid duplicates
		photoPath := fmt.Sprintf("%s/%s_%s", input.Task.LoginId, mediaItem.ID, mediaItem.Filename)
		if _, exists := existingPhotos[photoPath]; exists {
			moveEmailToStatus(&input.Memory, photoID, "pending", "skipped")
			successCount++
			continue
		}

		if err := g.uploadPhoto(ctx, input, mediaItem, photoPath); err != nil {
			failedPhotos, failedCount = g.trackFailure(photoID, err, failedPhotos, failedCount, input)
		} else {
			moveEmailToStatus(&input.Memory, photoID, "pending", "synced")
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

	// Upload to satellite
	return satellite.UploadObject(ctx, input.Task.StorxToken, satellite.ReserveBucket_Photos, photoPath, body)
}
