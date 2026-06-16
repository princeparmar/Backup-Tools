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
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type googleDriveProcessor struct{}

func NewGoogleDriveProcessor() *googleDriveProcessor {
	return &googleDriveProcessor{}
}

func (p *googleDriveProcessor) Run(input ProcessorInput) error {
	return runGoogleDriveAutosync(input)
}

func refreshTokenFromCronJob(job *repo.CronJobListingDB) string {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return ""
	}
	if rt, ok := (*job.InputData.Json())["refresh_token"].(string); ok {
		return strings.TrimSpace(rt)
	}
	return ""
}

func scheduledTaskShellFromCronJob(job *repo.CronJobListingDB, accessToken, storx string) *repo.ScheduledTasks {
	return &repo.ScheduledTasks{
		UserID:     job.UserID,
		LoginId:    job.Name,
		Method:     job.Method,
		StorxToken: strings.TrimSpace(storx),
		Status:     "running",
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"access_token": accessToken,
		}),
		Errors: *database.NewDbJsonFromValue([]string{}),
	}
}

func googleMediaAutosyncPreflight(input ProcessorInput) (accessToken, storx string, err error) {
	storx = strings.TrimSpace(input.Database.CronJobRepo.ResolvedStorxToken(input.Job))
	if storx == "" {
		return "", "", fmt.Errorf("storx_token is required on job (set via PUT /auto-sync/job/:id)")
	}
	rt := strings.TrimSpace(input.Database.CronJobRepo.ResolvedRefreshToken(input.Job))
	if rt == "" {
		return "", "", fmt.Errorf("refresh token not found in job input_data")
	}
	accessToken, err = google.AuthTokenUsingRefreshToken(rt)
	if err != nil {
		return "", "", fmt.Errorf("error while generating auth token: %w", err)
	}
	if strings.TrimSpace(accessToken) == "" {
		return "", "", fmt.Errorf("error while generating auth token: empty access token")
	}
	if err := input.HeartBeatFunc(); err != nil {
		return "", "", err
	}
	return accessToken, storx, nil
}

func runGoogleDriveAutosync(input ProcessorInput) error {
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

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Drive, task.LoginId+"/.file_placeholder", nil, task.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	service, err := createDriveServiceWithAccessToken(ctx, accessToken)
	if err != nil {
		return err
	}
	// First run = baseline from flat listing, later runs = changes API.
	if input.Job.TaskMemory.DrivePageToken == nil || strings.TrimSpace(*input.Job.TaskMemory.DrivePageToken) == "" {
		newToken, syncErr := runDriveBaselineSync(ctx, input, task, service)
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.DrivePageToken = &newToken
		input.Job.TaskMemory.DriveBaselineDone = true
	} else {
		newToken, syncErr := runDriveIncrementalSync(ctx, input, task, service, strings.TrimSpace(*input.Job.TaskMemory.DrivePageToken))
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.DrivePageToken = &newToken
		input.Job.TaskMemory.DriveBaselineDone = true
	}
	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

type driveMetaObject struct {
	FileID           string                   `json:"file_id"`
	Name             string                   `json:"name"`
	MimeType         string                   `json:"mime_type"`
	Parents          []string                 `json:"parents,omitempty"`
	ModifiedTime     string                   `json:"modified_time,omitempty"`
	Version          int64                    `json:"version,omitempty"`
	Md5Checksum      string                   `json:"md5_checksum,omitempty"`
	DriveID          string                   `json:"drive_id,omitempty"`
	LocationType     string                   `json:"location_type,omitempty"`
	Permissions      []google.DrivePermission `json:"permissions,omitempty"`
	Starred          bool                     `json:"starred"`
	Trashed          bool                     `json:"trashed"`
	RemovedFromDrive bool                     `json:"removed_from_drive,omitempty"`
	DeletedAt        string                   `json:"deleted_at,omitempty"`
	LastKnownParents []string                 `json:"last_known_parents,omitempty"`
	IsFolder         bool                     `json:"is_folder"`
	DataObjectKey    string                   `json:"data_object_key,omitempty"`
	ExportMimeType   string                   `json:"export_mime_type,omitempty"`
	UpdatedAt        string                   `json:"updated_at,omitempty"`
}

func createDriveServiceWithAccessToken(ctx context.Context, accessToken string) (*drive.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	token := &oauth2.Token{AccessToken: accessToken}
	client := config.Client(ctx, token)
	svc, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create drive service: %w", err)
	}
	return svc, nil
}

func runDriveBaselineSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service) (string, error) {
	startTok, err := service.Changes.GetStartPageToken().SupportsAllDrives(true).Do()
	if err != nil {
		return "", fmt.Errorf("drive start page token: %w", err)
	}
	shortcutTargetCache := make(map[string]*drive.File)
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		page, err := google.ListNonFolderFilesFlatWithService(service, pageToken)
		if err != nil {
			return "", err
		}
		for i := range page.Files {
			if err := retrySyncDriveFileByID(ctx, input, task, service, page.Files[i].ID, nil, shortcutTargetCache); err != nil {
				logger.Warn(ctx, "Drive baseline file sync failed", logger.String("file_id", page.Files[i].ID), logger.ErrorField(err))
			}
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return strings.TrimSpace(startTok.StartPageToken), nil
}

func runDriveIncrementalSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, pageToken string) (string, error) {
	currentPageToken := pageToken
	newStartToken := pageToken
	shortcutTargetCache := make(map[string]*drive.File)
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		ch, err := service.Changes.List(currentPageToken).
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true).
			IncludeRemoved(true).
			PageSize(1000).
			Fields("nextPageToken,newStartPageToken,changes(fileId,removed,file(id,name,mimeType,parents,modifiedTime,version,md5Checksum,permissions,driveId,starred,trashed,shortcutDetails(targetId))))").
			Do()
		if err != nil {
			return "", fmt.Errorf("drive changes list: %w", err)
		}
		for _, change := range ch.Changes {
			if change == nil || strings.TrimSpace(change.FileId) == "" {
				continue
			}
			if change.Removed {
				if err := writeDriveRemovedMetadata(ctx, input, task, change.FileId); err != nil {
					logger.Warn(ctx, "drive removed metadata update failed", logger.String("file_id", change.FileId), logger.ErrorField(err))
				}
				continue
			}
			if err := retrySyncDriveFileByID(ctx, input, task, service, change.FileId, change.File, shortcutTargetCache); err != nil {
				logger.Warn(ctx, "drive incremental file sync failed", logger.String("file_id", change.FileId), logger.ErrorField(err))
			}
		}
		if strings.TrimSpace(ch.NextPageToken) == "" {
			if strings.TrimSpace(ch.NewStartPageToken) != "" {
				newStartToken = strings.TrimSpace(ch.NewStartPageToken)
			}
			break
		}
		currentPageToken = strings.TrimSpace(ch.NextPageToken)
	}
	return newStartToken, nil
}

func syncDriveFileByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, fileID string, preloaded *drive.File, shortcutTargetCache map[string]*drive.File) error {
	file := preloaded
	var err error
	if file == nil || strings.TrimSpace(file.Id) == "" || strings.TrimSpace(file.MimeType) == "" || strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.ModifiedTime) == "" {
		file, err = service.Files.Get(fileID).Fields("id,name,mimeType,parents,modifiedTime,version,md5Checksum,permissions,driveId,starred,trashed,shortcutDetails(targetId)").SupportsAllDrives(true).Do()
		if err != nil {
			return fmt.Errorf("get file metadata: %w", err)
		}
	}
	// Resolve shortcut target to keep stable keying by target file ID.
	if file.MimeType == "application/vnd.google-apps.shortcut" && file.ShortcutDetails != nil && strings.TrimSpace(file.ShortcutDetails.TargetId) != "" {
		targetID := strings.TrimSpace(file.ShortcutDetails.TargetId)
		if cached, ok := shortcutTargetCache[targetID]; ok {
			file = cached
		} else {
			file, err = service.Files.Get(targetID).Fields("id,name,mimeType,parents,modifiedTime,version,md5Checksum,permissions,driveId,starred,trashed").SupportsAllDrives(true).Do()
			if err != nil {
				return fmt.Errorf("resolve shortcut target: %w", err)
			}
			shortcutTargetCache[targetID] = file
		}
	}
	metaKey := fmt.Sprintf("%s/meta/%s.json", task.LoginId, file.Id)
	dataKey := fmt.Sprintf("%s/data/%s", task.LoginId, file.Id)
	meta := driveMetaObject{
		FileID:        file.Id,
		Name:          file.Name,
		MimeType:      file.MimeType,
		Parents:       file.Parents,
		ModifiedTime:  file.ModifiedTime,
		Version:       file.Version,
		Md5Checksum:   file.Md5Checksum,
		DriveID:       file.DriveId,
		LocationType:  map[bool]string{true: "SHARED_DRIVE", false: "MY_DRIVE"}[file.DriveId != ""],
		Starred:       file.Starred,
		Trashed:       file.Trashed,
		IsFolder:      file.MimeType == "application/vnd.google-apps.folder",
		DataObjectKey: dataKey,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	for _, p := range file.Permissions {
		meta.Permissions = append(meta.Permissions, google.DrivePermission{Type: p.Type, Role: p.Role, EmailAddress: p.EmailAddress})
	}
	// File-only architecture: skip parent-folder traversal/metadata writes to avoid extra API calls.
	if meta.IsFolder {
		return nil
	}

	metaChangedOnly, err := shouldSkipDriveContentUpload(ctx, task, metaKey, meta)
	if err != nil {
		logger.Warn(ctx, "drive metadata compare failed; continuing with full upload", logger.String("file_id", file.Id), logger.ErrorField(err))
	}
	if metaChangedOnly {
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, metaKey, b, task.UserID, input.StorxRecovery)
	}

	content, exportMime, err := downloadDriveFileContent(service, file)
	if err != nil {
		return err
	}
	if exportMime != "" {
		meta.ExportMimeType = exportMime
	}
	if err := handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, dataKey, content, task.UserID, input.StorxRecovery); err != nil {
		return err
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, metaKey, b, task.UserID, input.StorxRecovery)
}

func retrySyncDriveFileByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, fileID string, preloaded *drive.File, shortcutTargetCache map[string]*drive.File) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := syncDriveFileByID(ctx, input, task, service, fileID, preloaded, shortcutTargetCache); err != nil {
			lastErr = err
			logger.Warn(ctx, "drive sync attempt failed", logger.String("file_id", fileID), logger.Int("attempt", attempt), logger.ErrorField(err))
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}

func shouldSkipDriveContentUpload(ctx context.Context, task *repo.ScheduledTasks, metaKey string, next driveMetaObject) (bool, error) {
	oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_Drive, metaKey)
	if err != nil {
		// Missing previous metadata/object is expected on first sync.
		return false, nil
	}
	var prev driveMetaObject
	if err := json.Unmarshal(oldBytes, &prev); err != nil {
		return false, err
	}
	if prev.RemovedFromDrive || next.RemovedFromDrive {
		return false, nil
	}
	if strings.TrimSpace(prev.DataObjectKey) == "" {
		return false, nil
	}
	// Content-change heuristic: version/mtime/checksum differences require re-download.
	contentSame := prev.Version == next.Version &&
		strings.TrimSpace(prev.ModifiedTime) == strings.TrimSpace(next.ModifiedTime) &&
		strings.TrimSpace(prev.Md5Checksum) == strings.TrimSpace(next.Md5Checksum)
	return contentSame, nil
}

func downloadDriveFileContent(service *drive.Service, file *drive.File) ([]byte, string, error) {
	var resp *http.Response
	var err error
	exportMime := ""
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps") {
		switch file.MimeType {
		case "application/vnd.google-apps.document":
			exportMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case "application/vnd.google-apps.spreadsheet":
			exportMime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case "application/vnd.google-apps.presentation":
			exportMime = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		default:
			exportMime = ""
		}
		if exportMime == "" {
			return nil, "", fmt.Errorf("unsupported export mime for %s", file.MimeType)
		}
		resp, err = service.Files.Export(file.Id, exportMime).Download()
	} else {
		resp, err = service.Files.Get(file.Id).Download()
	}
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return body, exportMime, nil
}

func writeDriveRemovedMetadata(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, fileID string) error {
	metaKey := fmt.Sprintf("%s/meta/%s.json", task.LoginId, fileID)
	var lastKnownParents []string
	if oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_Drive, metaKey); err == nil {
		var prev driveMetaObject
		if err := json.Unmarshal(oldBytes, &prev); err == nil && len(prev.Parents) > 0 {
			lastKnownParents = prev.Parents
		}
	}
	meta := driveMetaObject{
		FileID:           fileID,
		RemovedFromDrive: true,
		Trashed:          true,
		DeletedAt:        time.Now().UTC().Format(time.RFC3339),
		LastKnownParents: lastKnownParents,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, metaKey, b, task.UserID, input.StorxRecovery)
}
