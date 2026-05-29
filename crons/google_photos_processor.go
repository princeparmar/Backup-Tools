package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	photoslibrary "github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
)

type googlePhotosProcessor struct{}

func NewGooglePhotosProcessor() *googlePhotosProcessor {
	return &googlePhotosProcessor{}
}

func (p *googlePhotosProcessor) Run(input ProcessorInput) error {
	return runGooglePhotosAutosync(input)
}

type photosMetaObject struct {
	MediaItemID   string `json:"media_item_id"`
	Filename      string `json:"filename"`
	MimeType      string `json:"mime_type"`
	CreationTime  string `json:"creation_time"`
	BaseURL       string `json:"base_url"`
	ProductURL    string `json:"product_url"`
	Width         string `json:"width"`
	Height        string `json:"height"`
	DataObjectKey string `json:"data_object_key"`
	UpdatedAt     string `json:"updated_at"`
}

func runGooglePhotosAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := googleMediaAutosyncPreflight(input)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync", logger.ErrorField(processErr))
		}
	}()

	task := scheduledTaskShellFromCronJob(input.Job, accessToken)
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Photos, task.LoginId+"/.file_placeholder", nil, task.UserID); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	service, err := createPhotosServiceWithAccessToken(ctx, accessToken)
	if err != nil {
		return err
	}

	syncedSet, err := loadPhotosSyncedIDSet(ctx, input, task, storx)
	if err != nil {
		return err
	}

	if !input.Job.TaskMemory.PhotosBaselineDone {
		if err := runPhotosBaselineSync(ctx, input, task, service, syncedSet); err != nil {
			return err
		}
		input.Job.TaskMemory.PhotosBaselineDone = true
	} else if err := runPhotosIncrementalSync(ctx, input, task, service, syncedSet); err != nil {
		return err
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func createPhotosServiceWithAccessToken(ctx context.Context, accessToken string) (*photoslibrary.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, photoslibrary.PhotoslibraryReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	token := &oauth2.Token{AccessToken: accessToken}
	httpClient := config.Client(ctx, token)
	svc, err := photoslibrary.New(httpClient)
	if err != nil {
		return nil, fmt.Errorf("unable to create photos service: %w", err)
	}
	return svc, nil
}

func loadPhotosSyncedIDSet(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, storx string) (map[string]struct{}, error) {
	objectKeys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_Photos, task.LoginId+"/", task.UserID, "google", "photos")
	if err != nil {
		return nil, fmt.Errorf("load synced objects: %w", err)
	}
	return google.BuildPhotosSyncedIDSet(objectKeys, task.LoginId), nil
}

func runPhotosBaselineSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *photoslibrary.Service, syncedSet map[string]struct{}) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListAllMediaItemsFlatWithService(service, pageToken)
		if err != nil {
			return err
		}
		if _, err := processPhotosPage(ctx, input, task, page.MediaItems, syncedSet); err != nil {
			return err
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return nil
}

func runPhotosIncrementalSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *photoslibrary.Service, syncedSet map[string]struct{}) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListAllMediaItemsFlatWithService(service, pageToken)
		if err != nil {
			return err
		}
		newFoundInPage, err := processPhotosPage(ctx, input, task, page.MediaItems, syncedSet)
		if err != nil {
			return err
		}
		nextToken := strings.TrimSpace(page.NextPageToken)
		if !newFoundInPage {
			if nextToken == "" {
				break
			}
			lookahead, err := google.ListAllMediaItemsFlatWithService(service, nextToken)
			if err != nil {
				return err
			}
			if !google.PageHasAnyNewPhotosItems(lookahead.MediaItems, syncedSet) {
				break
			}
		}
		if nextToken == "" {
			break
		}
		pageToken = nextToken
	}
	return nil
}

func processPhotosPage(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, items []google.FlatPhotosMediaItem, syncedSet map[string]struct{}) (bool, error) {
	newFound := false
	for i := range items {
		id := strings.TrimSpace(items[i].ID)
		if id == "" {
			continue
		}
		if _, synced := syncedSet[id]; synced {
			continue
		}
		newFound = true
		if err := retrySyncPhotosMediaByID(ctx, input, task, items[i]); err != nil {
			logger.Warn(ctx, "Photos media sync failed", logger.String("media_item_id", id), logger.ErrorField(err))
			continue
		}
		syncedSet[id] = struct{}{}
	}
	return newFound, nil
}

func syncPhotosMediaByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item google.FlatPhotosMediaItem) error {
	metaKey := google.PhotosIDBasedMetaKey(task.LoginId, item.ID)
	dataKey := google.PhotosIDBasedDataKey(task.LoginId, item.ID, item.Filename)
	meta := photosMetaObject{
		MediaItemID:   item.ID,
		Filename:      item.Filename,
		MimeType:      item.MimeType,
		CreationTime:  item.CreationTime,
		BaseURL:       item.BaseURL,
		ProductURL:    item.ProductURL,
		Width:         item.Width,
		Height:        item.Height,
		DataObjectKey: dataKey,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	skipContent, err := shouldSkipPhotosContentUpload(ctx, task, metaKey, meta)
	if err != nil {
		logger.Warn(ctx, "photos metadata compare failed; continuing with full upload", logger.String("media_item_id", item.ID), logger.ErrorField(err))
	}
	if skipContent {
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Photos, metaKey, b, task.UserID)
	}
	body, err := downloadPhotosMediaContent(ctx, item.BaseURL)
	if err != nil {
		return err
	}
	if err := handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Photos, dataKey, body, task.UserID); err != nil {
		return err
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Photos, metaKey, b, task.UserID)
}

func retrySyncPhotosMediaByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item google.FlatPhotosMediaItem) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := syncPhotosMediaByID(ctx, input, task, item); err != nil {
			lastErr = err
			logger.Warn(ctx, "photos sync attempt failed", logger.String("media_item_id", item.ID), logger.Int("attempt", attempt), logger.ErrorField(err))
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}

func shouldSkipPhotosContentUpload(ctx context.Context, task *repo.ScheduledTasks, metaKey string, next photosMetaObject) (bool, error) {
	oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_Photos, metaKey)
	if err != nil {
		return false, nil
	}
	var prev photosMetaObject
	if err := json.Unmarshal(oldBytes, &prev); err != nil {
		return false, err
	}
	if strings.TrimSpace(prev.DataObjectKey) == "" {
		return false, nil
	}
	return strings.TrimSpace(prev.CreationTime) == strings.TrimSpace(next.CreationTime) &&
		strings.TrimSpace(prev.MimeType) == strings.TrimSpace(next.MimeType) &&
		strings.TrimSpace(prev.Filename) == strings.TrimSpace(next.Filename), nil
}

func downloadPhotosMediaContent(ctx context.Context, baseURL string) ([]byte, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("empty base URL")
	}
	downloadURL := baseURL + "=d"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download photo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download photo status: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
