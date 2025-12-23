package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// GoogleDriveProcessor handles Google Drive scheduled tasks
type GoogleDriveProcessor struct {
	BaseProcessor
}

func NewScheduledGoogleDriveProcessor(deps *TaskProcessorDeps) *GoogleDriveProcessor {
	return &GoogleDriveProcessor{BaseProcessor{Deps: deps}}
}

func (g *GoogleDriveProcessor) Run(input ScheduledTaskProcessorInput) error {
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

	driveService, err := g.createDriveService(accessToken)
	if err != nil {
		return g.handleError(input.Task, fmt.Sprintf("Failed to create Google Drive service: %s", err), nil)
	}

	// Create placeholder and get existing files
	if err := g.setupStorage(input.Task, satellite.ReserveBucket_Drive); err != nil {
		return err
	}

	fileListFromBucket, err := satellite.ListObjectsWithPrefix(ctx, input.Task.StorxToken, satellite.ReserveBucket_Drive, input.Task.LoginId+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return g.handleError(input.Task, fmt.Sprintf("Failed to list existing files: %s", err), nil)
	}

	return g.processFiles(ctx, input, driveService, fileListFromBucket)
}

func (g *GoogleDriveProcessor) createDriveService(accessToken string) (*drive.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %v", err)
	}

	config, err := oauth2google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %v", err)
	}

	token := &oauth2.Token{AccessToken: accessToken}
	client := config.Client(context.Background(), token)

	service, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create Drive service: %v", err)
	}

	return service, nil
}

func (g *GoogleDriveProcessor) setupStorage(task *repo.ScheduledTasks, bucket string) error {
	return satellite.UploadObject(context.Background(), task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil)
}

func (g *GoogleDriveProcessor) processFiles(ctx context.Context, input ScheduledTaskProcessorInput, service *drive.Service, existingFiles map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedFiles []string

	// Get pending file IDs
	pendingFiles := input.Memory["pending"]
	if pendingFiles == nil {
		pendingFiles = []string{}
	}

	// Deduplicate pending files to prevent processing the same file multiple times
	seen := make(map[string]bool)
	var uniquePendingFiles []string
	for _, fileID := range pendingFiles {
		fileID = strings.TrimSpace(fileID)
		if fileID != "" && !seen[fileID] {
			seen[fileID] = true
			uniquePendingFiles = append(uniquePendingFiles, fileID)
		}
	}
	pendingFiles = uniquePendingFiles
	// Update memory with deduplicated list
	input.Memory["pending"] = uniquePendingFiles

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	for _, fileID := range pendingFiles {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		// Get the full drive.File (same as direct upload) to ensure consistent filename generation
		file, err := service.Files.Get(fileID).Fields("id", "name", "mimeType", "size", "createdTime", "modifiedTime", "fileExtension").Do()
		if err != nil {
			failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
			continue
		}

		// Use collision-safe filename format: fileID_name to avoid duplicates
		filePath := fmt.Sprintf("%s/%s_%s", input.Task.LoginId, file.Id, file.Name)
		if _, exists := existingFiles[filePath]; exists {
			moveEmailToStatus(&input.Memory, fileID, "pending", "skipped: already exists in storage")
			successCount++
			continue
		}

		if err := g.uploadFile(ctx, input, service, file, filePath); err != nil {
			failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
		} else {
			moveEmailToStatus(&input.Memory, fileID, "pending", "synced")
			successCount++
		}
	}

	// Clear pending array after processing
	input.Memory["pending"] = []string{}

	return g.updateTaskStats(&input, successCount, failedCount, failedFiles)
}

func (g *GoogleDriveProcessor) trackFailure(fileID string, err error, failedFiles []string, failedCount int, input ScheduledTaskProcessorInput) ([]string, int) {
	failedFiles = append(failedFiles, fmt.Sprintf("File ID %s: %v", fileID, err))
	failedCount++
	moveEmailToStatus(&input.Memory, fileID, "pending", "error")
	return failedFiles, failedCount
}

func (g *GoogleDriveProcessor) uploadFile(ctx context.Context, input ScheduledTaskProcessorInput, service *drive.Service, file *drive.File, filePath string) error {
	var resp *http.Response
	var err error

	// Handle Google Docs/Sheets/Slides export
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps") {
		exportMimeType := g.getExportMimeType(file.MimeType)
		if exportMimeType == "" {
			return fmt.Errorf("unsupported Google Apps file type: %s", file.MimeType)
		}
		resp, err = service.Files.Export(file.Id, exportMimeType).Download()
		if err != nil {
			return fmt.Errorf("failed to export file: %v", err)
		}
	} else {
		resp, err = service.Files.Get(file.Id).Download()
		if err != nil {
			return fmt.Errorf("failed to download file: %v", err)
		}
	}
	defer resp.Body.Close()

	fileData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read file data: %v", err)
	}

	var createdAt time.Time
	if file.CreatedTime != "" {
		createdAt, _ = time.Parse(time.RFC3339, file.CreatedTime)
	}

	fileType := "file"
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps") {
		fileType = "google_doc"
	} else if strings.HasPrefix(file.MimeType, "image/") {
		fileType = "image"
	} else if strings.HasPrefix(file.MimeType, "video/") {
		fileType = "video"
	} else if strings.HasPrefix(file.MimeType, "audio/") {
		fileType = "audio"
	}

	fileJSON := google.FilesJSON{
		Name:              file.Name,
		ID:                file.Id,
		MimeType:          file.MimeType,
		FileType:          fileType,
		Synced:            true,
		Size:              file.Size,
		FullFileExtension: filepath.Ext(filePath),
		FileExtension:     strings.TrimPrefix(filepath.Ext(filePath), "."),
		Path:              filePath,
		CreatedAt:         createdAt,
	}

	metadataJSON, err := json.Marshal(fileJSON)
	if err != nil {
		return fmt.Errorf("failed to marshal: %v", err)
	}

	// Upload file content to satellite
	if err := satellite.UploadObject(ctx, input.Task.StorxToken, satellite.ReserveBucket_Drive, filePath, fileData); err != nil {
		return fmt.Errorf("failed to upload file: %v", err)
	}

	// Upload metadata as JSON
	metadataPath := filePath + ".metadata.json"
	return satellite.UploadObject(ctx, input.Task.StorxToken, satellite.ReserveBucket_Drive, metadataPath, metadataJSON)
}

func (g *GoogleDriveProcessor) getExportMimeType(mimeType string) string {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "application/vnd.google-apps.spreadsheet":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "application/vnd.google-apps.presentation":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "application/vnd.google-apps.site":
		return "text/plain"
	case "application/vnd.google-apps.script":
		return "application/vnd.google-apps.script+json"
	default:
		return ""
	}
}
