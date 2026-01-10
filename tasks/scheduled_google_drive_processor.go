package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
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
	driveConfig     *oauth2.Config
	driveConfigOnce sync.Once
	driveConfigErr  error
}

func NewScheduledGoogleDriveProcessor(deps *TaskProcessorDeps) *GoogleDriveProcessor {
	return &GoogleDriveProcessor{
		BaseProcessor: BaseProcessor{Deps: deps},
	}
}

func (g *GoogleDriveProcessor) Run(input ScheduledTaskProcessorInput) error {
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
	if !ok || accessToken == "" {
		return g.handleError(input.Task, "Access token not found in task data", nil)
	}

	driveService, err := g.createDriveService(ctx, accessToken)
	if err != nil {
		return g.handleError(input.Task, fmt.Sprintf("Failed to create Google Drive service: %v", err), nil)
	}

	// Create placeholder and get existing files
	if err := g.setupStorage(ctx, input.Task, satellite.ReserveBucket_Drive); err != nil {
		return err
	}

	// Get synced objects from database instead of listing from Satellite
	// Uses common handler.GetSyncedObjectsWithPrefix which ensures bucket exists and queries database
	fileListFromBucket, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Deps.Store, input.Task.StorxToken, satellite.ReserveBucket_Drive, input.Task.LoginId+"/", input.Task.UserID, "google", "drive")
	if err != nil {
		return g.handleError(input.Task, fmt.Sprintf("Failed to list existing files: %v", err), nil)
	}

	return g.processFiles(ctx, input, driveService, fileListFromBucket)
}

// This avoids reading credentials.json on every scheduled run
func (g *GoogleDriveProcessor) loadDriveConfig() (*oauth2.Config, error) {
	g.driveConfigOnce.Do(func() {
		b, err := os.ReadFile("credentials.json")
		if err != nil {
			g.driveConfigErr = fmt.Errorf("unable to read credentials file: %w", err)
			return
		}

		// Use slice for scopes to allow easy future additions
		scopes := []string{drive.DriveReadonlyScope}
		config, err := oauth2google.ConfigFromJSON(b, scopes...)
		if err != nil {
			g.driveConfigErr = fmt.Errorf("unable to parse credentials: %w", err)
			return
		}

		g.driveConfig = config
	})

	if g.driveConfigErr != nil {
		return nil, g.driveConfigErr
	}

	return g.driveConfig, nil
}

func (g *GoogleDriveProcessor) createDriveService(ctx context.Context, accessToken string) (*drive.Service, error) {
	config, err := g.loadDriveConfig()
	if err != nil {
		return nil, err
	}

	token := &oauth2.Token{AccessToken: accessToken}
	client := config.Client(ctx, token)

	service, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create Drive service: %w", err)
	}

	return service, nil
}

func (g *GoogleDriveProcessor) setupStorage(ctx context.Context, task *repo.ScheduledTasks, bucket string) error {
	return handler.UploadObjectAndSync(ctx, g.Deps.Store, task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil, task.UserID)
}

func (g *GoogleDriveProcessor) processFiles(ctx context.Context, input ScheduledTaskProcessorInput, service *drive.Service, existingFiles map[string]bool) error {
	successCount, failedCount := 0, 0
	var failedFiles []string
	folderCount := 0
	fileCount := 0

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

	// Use a dynamic list that can grow as we discover nested files
	processingQueue := uniquePendingFiles
	// Update memory with deduplicated list
	input.Memory["pending"] = uniquePendingFiles

	// Initialize other status arrays if needed
	ensureStatusArray(&input.Memory, "synced")
	ensureStatusArray(&input.Memory, "skipped")
	ensureStatusArray(&input.Memory, "error")

	// CRITICAL: Cache folder paths to avoid repeated API calls (optimization that doesn't change behavior)
	folderPathCache := make(map[string]string)
	// Cache for shared status to avoid repeated API calls when checking parent hierarchy
	sharedStatusCache := make(map[string]bool)

	// Process files - queue grows as nested files are discovered
	for i := 0; i < len(processingQueue); i++ {
		fileID := processingQueue[i]
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		// Get the full drive.File (same as direct upload) to ensure consistent filename generation
		// Include owners, parents, shortcutDetails, and shared fields to check if file is shared, get parent folder info, and resolve shortcuts
		file, err := service.Files.Get(fileID).Fields("id", "name", "mimeType", "size", "createdTime", "modifiedTime", "fileExtension", "owners", "parents", "shortcutDetails", "shared", "driveId", "starred", "permissions").Do()
		if err != nil {
			failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
			continue
		}

		// Check if file is shared - consider Drive's shared flag, file ownership, and parent folder context
		// Edge case: File owned by user but in a shared folder should be treated as shared
		isShared := g.isFileInSharedContext(ctx, service, file, input.Task.LoginId, sharedStatusCache)

		// Build base path - add "shared with me" prefix if file is shared
		basePath := input.Task.LoginId
		if isShared {
			basePath = fmt.Sprintf("%s/shared with me", input.Task.LoginId)
		}

		var parentFolderPath string
		if len(file.Parents) > 0 && file.Parents[0] != "root" {
			if isShared {
				parentFolderPath = g.buildParentFolderPathForShared(ctx, service, file.Parents[0], input.Task.LoginId, folderPathCache)
			} else {
				parentFolderPath = g.buildParentFolderPath(ctx, service, file.Parents[0], folderPathCache)
			}
		}

		// Handle folders - create placeholder and discover nested files
		if file.MimeType == "application/vnd.google-apps.folder" {
			if file.Name == "My Drive" {
				moveEmailToStatus(&input.Memory, fileID, "pending", "skipped: My Drive container")
				successCount++
				continue
			}

			// Build folder path - include parent folder path if not root
			var folderPath string
			if parentFolderPath != "" {
				folderPath = fmt.Sprintf("%s/%s/%s_%s/.file_placeholder", basePath, parentFolderPath, file.Id, file.Name)
			} else {
				folderPath = fmt.Sprintf("%s/%s_%s/.file_placeholder", basePath, file.Id, file.Name)
			}
			if _, exists := existingFiles[folderPath]; exists {
				moveEmailToStatus(&input.Memory, fileID, "pending", "skipped: already exists in storage")
				successCount++
				continue
			}

			// Upload folder placeholder and sync to database
			if err := handler.UploadObjectAndSync(ctx, input.Deps.Store, input.Task.StorxToken, satellite.ReserveBucket_Drive, folderPath, nil, input.Task.UserID); err != nil {
				failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
				continue
			}

			// Mark folder as existing to prevent duplicate uploads in same run
			existingFiles[folderPath] = true

			// Recursively discover and add nested files/folders to pending
			nestedFileIDs, err := g.discoverNestedFiles(ctx, service, file.Id)
			if err == nil && len(nestedFileIDs) > 0 {
				// Deduplicate nested files before adding to queue and memory
				var uniqueNestedIDs []string
				for _, nestedID := range nestedFileIDs {
					nestedID = strings.TrimSpace(nestedID)
					if nestedID != "" && !seen[nestedID] {
						seen[nestedID] = true
						uniqueNestedIDs = append(uniqueNestedIDs, nestedID)
						processingQueue = append(processingQueue, nestedID)
					}
				}
				// Only update memory with deduplicated IDs
				if len(uniqueNestedIDs) > 0 {
					currentPending := input.Memory["pending"]
					if currentPending == nil {
						currentPending = []string{}
					}
					input.Memory["pending"] = append(currentPending, uniqueNestedIDs...)
				}
			}

			moveEmailToStatus(&input.Memory, fileID, "pending", "synced")
			successCount++
			folderCount++
			continue
		}

		// Handle Google Drive shortcuts - resolve to target file
		if file.MimeType == "application/vnd.google-apps.shortcut" {
			if file.ShortcutDetails != nil && file.ShortcutDetails.TargetId != "" {
				targetFile, err := service.Files.Get(file.ShortcutDetails.TargetId).Fields("id", "name", "mimeType", "size", "createdTime", "modifiedTime", "fileExtension", "owners", "parents", "shortcutDetails").Do()
				if err == nil {
					file = targetFile
				}
			}
		}

		// Use collision-safe filename format: fileID_name to avoid duplicates
		var filePath string
		if parentFolderPath != "" {
			filePath = fmt.Sprintf("%s/%s/%s_%s", basePath, parentFolderPath, file.Id, file.Name)
		} else {
			filePath = fmt.Sprintf("%s/%s_%s", basePath, file.Id, file.Name)
		}
		if strings.HasPrefix(file.MimeType, "application/vnd.google-apps") {
			// Add extension for Google Apps files to match sync check logic
			ext := g.getFileExtension(file.MimeType)
			filePath += ext
		}

		if _, exists := existingFiles[filePath]; exists {
			moveEmailToStatus(&input.Memory, fileID, "pending", "skipped: already exists in storage")
			successCount++
			continue
		}

		// Determine LocationType
		locationType := "MY_DRIVE"
		if file.DriveId != "" {
			locationType = "SHARED_DRIVE"
		}

		if err := g.uploadFile(ctx, input, service, file, filePath, locationType); err != nil {
			failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
		} else {
			// Mark file as existing to prevent duplicate uploads in same run
			existingFiles[filePath] = true
			moveEmailToStatus(&input.Memory, fileID, "pending", "synced")
			successCount++
			fileCount++
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

func (g *GoogleDriveProcessor) uploadFile(ctx context.Context, input ScheduledTaskProcessorInput, service *drive.Service, file *drive.File, filePath string, locationType string) error {
	var resp *http.Response
	var err error

	// Handle Google Docs/Sheets/Slides export
	// Note: Shortcuts should already be resolved before reaching this function
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps") {
		// Skip shortcuts - they should have been resolved earlier
		if file.MimeType == "application/vnd.google-apps.shortcut" {
			return fmt.Errorf("shortcut file cannot be downloaded directly, must be resolved to target file first")
		}
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

	// Map permissions
	var permissions []google.DrivePermission
	for _, p := range file.Permissions {
		permissions = append(permissions, google.DrivePermission{
			Type:         p.Type,
			Role:         p.Role,
			EmailAddress: p.EmailAddress,
		})
	}

	// Determine file type
	fileType := "file"
	if file.MimeType == "application/vnd.google-apps.folder" {
		fileType = "folder"
	}

	// Create metadata object
	metadata := google.DriveFileMetadata{
		Key:          filePath,
		Type:         fileType,
		Name:         file.Name,
		MimeType:     file.MimeType,
		Parents:      file.Parents,
		DriveID:      file.DriveId,
		LocationType: locationType,
		Permissions:  permissions,
		ModifiedTime: file.ModifiedTime,
		Starred:      file.Starred,
	}

	// Create backup item
	backupItem := google.DriveBackupItem{
		Metadata: metadata,
		Content:  fileData,
	}

	jsonData, err := json.Marshal(backupItem)
	if err != nil {
		return fmt.Errorf("failed to marshal drive file: %v", err)
	}

	// Upload JSON content to satellite and sync to database
	// Metadata is now included in the JSON object
	return handler.UploadObjectAndSync(ctx, input.Deps.Store, input.Task.StorxToken, satellite.ReserveBucket_Drive, filePath, jsonData, input.Task.UserID)
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

// getFileExtension returns the file extension for Google Apps files to match sync check logic
func (g *GoogleDriveProcessor) getFileExtension(mimeType string) string {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return ".docx"
	case "application/vnd.google-apps.spreadsheet":
		return ".xlsx"
	case "application/vnd.google-apps.presentation":
		return ".pptx"
	case "application/vnd.google-apps.script":
		return ".json"
	default:
		return ""
	}
}

func (g *GoogleDriveProcessor) discoverNestedFiles(ctx context.Context, service *drive.Service, rootFolderID string) ([]string, error) {
	var result []string
	queue := []string{rootFolderID}
	visited := make(map[string]bool)

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		folderID := queue[0]
		queue = queue[1:]

		if visited[folderID] {
			continue
		}
		visited[folderID] = true

		pageToken := ""
		for {
			listCall := service.Files.List().
				Q(fmt.Sprintf("'%s' in parents", folderID)).
				Fields("nextPageToken, files(id, mimeType)")

			if pageToken != "" {
				listCall = listCall.PageToken(pageToken)
			}

			r, err := listCall.Do()
			if err != nil {
				return nil, fmt.Errorf("failed to list files in folder %s: %w", folderID, err)
			}

			for _, f := range r.Files {
				result = append(result, f.Id)
				if f.MimeType == "application/vnd.google-apps.folder" {
					queue = append(queue, f.Id)
				}
			}

			if r.NextPageToken == "" {
				break
			}
			pageToken = r.NextPageToken
		}
	}

	return result, nil
}

// isFileInSharedContext determines if a file should be treated as shared
func (g *GoogleDriveProcessor) isFileInSharedContext(ctx context.Context, service *drive.Service, file *drive.File, currentUserEmail string, cache map[string]bool) bool {
	userOwnsFile := len(file.Owners) > 0 && file.Owners[0].EmailAddress == currentUserEmail

	if userOwnsFile {
		current := file
		for {
			select {
			case <-ctx.Done():
				return false
			default:
			}

			if len(current.Parents) == 0 {
				return false
			}

			foundNext := false
			for _, parentID := range current.Parents {
				if parentID == "root" {
					continue
				}

				cacheKey := parentID + "|" + currentUserEmail

				if cached, ok := cache[cacheKey]; ok {
					if cached {
						return true
					}
					continue
				}

				parent, err := service.Files.Get(parentID).
					Context(ctx).
					Fields("id", "parents", "owners", "shared").
					Do()
				if err != nil {
					continue
				}

				isParentShared := len(parent.Owners) > 0 && parent.Owners[0].EmailAddress != currentUserEmail

				cache[cacheKey] = isParentShared

				if isParentShared {
					return true
				}

				if len(parent.Parents) > 0 {
					current = parent
					foundNext = true
					break
				}
			}

			if !foundNext {
				return false
			}
		}
	}

	// SECOND: If file is NOT owned by current user, it IS "shared with me"
	if len(file.Owners) > 0 && file.Owners[0].EmailAddress != currentUserEmail {
		return true
	}

	return file.Shared
}

// buildParentFolderPath recursively builds the full path from root to the given folder
func (g *GoogleDriveProcessor) buildParentFolderPath(ctx context.Context, service *drive.Service, folderID string, cache map[string]string) string {
	if cachedPath, ok := cache[folderID]; ok {
		return cachedPath
	}

	var pathSegments []string
	currentID := folderID

	for currentID != "" && currentID != "root" {
		select {
		case <-ctx.Done():
			return strings.Join(pathSegments, "/")
		default:
		}

		folder, err := service.Files.Get(currentID).Fields("id", "name", "parents").Do()
		if err != nil {
			break
		}

		if folder.Name != "My Drive" {
			segment := fmt.Sprintf("%s_%s", folder.Id, folder.Name)
			pathSegments = append([]string{segment}, pathSegments...)
		}

		if len(folder.Parents) > 0 {
			currentID = folder.Parents[0]
		} else {
			break
		}
	}

	finalPath := strings.Join(pathSegments, "/")
	cache[folderID] = finalPath
	return finalPath
}

// buildParentFolderPathForShared builds the parent folder path for shared files
func (g *GoogleDriveProcessor) buildParentFolderPathForShared(ctx context.Context, service *drive.Service, folderID string, currentUserEmail string, cache map[string]string) string {
	cacheKey := folderID + "_shared_" + currentUserEmail
	if cachedPath, ok := cache[cacheKey]; ok {
		return cachedPath
	}

	var pathSegments []string
	currentID := folderID

	for currentID != "" && currentID != "root" {
		select {
		case <-ctx.Done():
			return strings.Join(pathSegments, "/")
		default:
		}

		folder, err := service.Files.Get(currentID).
			Context(ctx).
			Fields("id", "name", "parents").
			Do()
		if err != nil {
			break
		}

		if folder.Name != "My Drive" {
			segment := fmt.Sprintf("%s_%s", folder.Id, folder.Name)
			pathSegments = append([]string{segment}, pathSegments...)
		}

		if len(folder.Parents) > 0 {
			currentID = folder.Parents[0]
		} else {
			break
		}
	}

	finalPath := strings.Join(pathSegments, "/")
	cache[cacheKey] = finalPath
	return finalPath
}
