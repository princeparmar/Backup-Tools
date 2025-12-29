package crons

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/StorX2-0/Backup-Tools/handler"
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
	return handler.UploadObjectAndSync(context.Background(), g.Deps.Store, task.StorxToken, bucket, task.LoginId+"/.file_placeholder", nil, task.UserID)
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

	// Process files - queue grows as nested files are discovered
	for i := 0; i < len(processingQueue); i++ {
		fileID := processingQueue[i]
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}

		// Get the full drive.File (same as direct upload) to ensure consistent filename generation
		// Include owners, parents, and shortcutDetails fields to check if file is shared, get parent folder info, and resolve shortcuts
		file, err := service.Files.Get(fileID).Fields("id", "name", "mimeType", "size", "createdTime", "modifiedTime", "fileExtension", "owners", "parents", "shortcutDetails").Do()
		if err != nil {
			failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
			continue
		}

		// Check if file is shared (owner is not the current user)
		isShared := false
		if len(file.Owners) > 0 {
			ownerEmail := file.Owners[0].EmailAddress
			// File is shared if owner email doesn't match user's email
			isShared = ownerEmail != input.Task.LoginId
		}

		// Build base path - add "shared with me" prefix if file is shared
		basePath := input.Task.LoginId
		if isShared {
			basePath = fmt.Sprintf("%s/shared with me", input.Task.LoginId)
		}

		// Get full parent folder path if file is not in root
		// Build path from root to immediate parent: folder1ID_folder1/folder2ID_folder2/...
		// For shared files: Only use parent path if parent folder is also shared with us
		var parentFolderPath string
		if len(file.Parents) > 0 && file.Parents[0] != "root" {
			if isShared {
				// For shared files, check if parent folder is part of the shared structure
				// If parent folder is NOT owned by current user, it's part of shared structure → use it
				// If parent folder IS owned by current user, it's in our drive → don't use path
				parentFolder, err := service.Files.Get(file.Parents[0]).Fields("id", "name", "owners").Do()
				if err == nil && len(parentFolder.Owners) > 0 {
					parentOwnerEmail := parentFolder.Owners[0].EmailAddress

					// If parent is NOT owned by current user, it's part of shared structure
					if parentOwnerEmail != input.Task.LoginId {
						// Parent is a shared folder - build path but only include shared folders
						parentFolderPath = g.buildParentFolderPathForShared(ctx, service, file.Parents[0], input.Task.LoginId, folderPathCache)
					}
					// If parent IS owned by current user, parentFolderPath stays empty (file stored at root)
				}
			} else {
				// Regular file (not shared) - use parent path normally
				parentFolderPath = g.buildParentFolderPath(ctx, service, file.Parents[0], folderPathCache)
			}
		}

		// Handle folders - create placeholder and discover nested files
		if file.MimeType == "application/vnd.google-apps.folder" {
			// Skip "My Drive" folder - it's just the root container, not a real folder
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

			// Recursively discover and add nested files/folders to pending
			nestedFileIDs, err := g.discoverNestedFiles(ctx, service, file.Id)
			if err == nil && len(nestedFileIDs) > 0 {
				// Add nested files to processing queue for immediate processing in this run
				for _, nestedID := range nestedFileIDs {
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
				input.Memory["pending"] = append(currentPending, nestedFileIDs...)
			}

			moveEmailToStatus(&input.Memory, fileID, "pending", "synced")
			successCount++
			folderCount++
			continue
		}

		// Handle Google Drive shortcuts - resolve to target file
		if file.MimeType == "application/vnd.google-apps.shortcut" {
			if file.ShortcutDetails != nil && file.ShortcutDetails.TargetId != "" {
				// Get the target file and process it instead
				targetFile, err := service.Files.Get(file.ShortcutDetails.TargetId).Fields("id", "name", "mimeType", "size", "createdTime", "modifiedTime", "fileExtension", "owners", "parents", "shortcutDetails").Do()
				if err == nil {
					// Use target file's name but keep shortcut's parent path
					file = targetFile
				}
			}
		}

		// Use collision-safe filename format: fileID_name to avoid duplicates
		// For Google Apps files, add the appropriate extension
		// Include parent folder path if file is not in root
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

		if err := g.uploadFile(ctx, input, service, file, filePath); err != nil {
			failedFiles, failedCount = g.trackFailure(fileID, err, failedFiles, failedCount, input)
		} else {
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

func (g *GoogleDriveProcessor) uploadFile(ctx context.Context, input ScheduledTaskProcessorInput, service *drive.Service, file *drive.File, filePath string) error {
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

	// Upload file content to satellite and sync to database
	// Metadata is handled by the backend via Google Drive API and database tracking
	// No need to upload separate metadata.json file
	return handler.UploadObjectAndSync(ctx, input.Deps.Store, input.Task.StorxToken, satellite.ReserveBucket_Drive, filePath, fileData, input.Task.UserID)
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

// discoverNestedFiles recursively discovers all files and folders inside a folder
func (g *GoogleDriveProcessor) discoverNestedFiles(ctx context.Context, service *drive.Service, folderID string) ([]string, error) {
	var allFileIDs []string
	pageToken := ""

	for {
		query := fmt.Sprintf("'%s' in parents", folderID)
		listCall := service.Files.List().Q(query).Fields("nextPageToken, files(id, name, mimeType)")

		if pageToken != "" {
			listCall = listCall.PageToken(pageToken)
		}

		r, err := listCall.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list files in folder: %w", err)
		}

		for _, file := range r.Files {
			allFileIDs = append(allFileIDs, file.Id)
			if file.MimeType == "application/vnd.google-apps.folder" {
				if nestedIDs, err := g.discoverNestedFiles(ctx, service, file.Id); err == nil {
					allFileIDs = append(allFileIDs, nestedIDs...)
				}
			}
		}

		if r.NextPageToken == "" {
			break
		}
		pageToken = r.NextPageToken
	}

	return allFileIDs, nil
}

// buildParentFolderPath recursively builds the full path from root to the given folder
// Returns path like: "folder1ID_folder1/folder2ID_folder2" for nested folders
// Skips "My Drive" folder as it's the root container
// Uses cache to avoid repeated API calls (optimization that doesn't change behavior)
func (g *GoogleDriveProcessor) buildParentFolderPath(ctx context.Context, service *drive.Service, folderID string, cache map[string]string) string {
	// Check cache first - avoids expensive API calls (optimization)
	if cachedPath, ok := cache[folderID]; ok {
		return cachedPath
	}

	var pathSegments []string
	currentID := folderID

	for currentID != "" && currentID != "root" {
		folder, err := service.Files.Get(currentID).Fields("id", "name", "parents").Do()
		if err != nil {
			break
		}

		// Skip "My Drive" folder - it's just the root container
		if folder.Name != "My Drive" {
			// Build segment: folderID_folderName
			segment := fmt.Sprintf("%s_%s", folder.Id, folder.Name)
			pathSegments = append([]string{segment}, pathSegments...)
		}

		// Move to parent
		if len(folder.Parents) > 0 {
			currentID = folder.Parents[0]
		} else {
			break
		}
	}

	finalPath := strings.Join(pathSegments, "/")
	// Cache the result for future use (optimization)
	cache[folderID] = finalPath
	return finalPath
}

// buildParentFolderPathForShared builds the parent folder path for shared files
// It stops building when it encounters a folder owned by the current user (shared folder boundary)
// This ensures we only include folders that are actually shared with us
func (g *GoogleDriveProcessor) buildParentFolderPathForShared(ctx context.Context, service *drive.Service, folderID string, currentUserEmail string, cache map[string]string) string {
	// Check cache first - use a different cache key for shared paths
	cacheKey := folderID + "_shared_" + currentUserEmail
	if cachedPath, ok := cache[cacheKey]; ok {
		return cachedPath
	}

	var pathSegments []string
	currentID := folderID

	for currentID != "" && currentID != "root" {
		folder, err := service.Files.Get(currentID).Fields("id", "name", "parents", "owners").Do()
		if err != nil {
			break
		}

		// Stop if folder is owned by current user - we've reached the shared folder boundary
		if len(folder.Owners) > 0 && folder.Owners[0].EmailAddress == currentUserEmail {
			break
		}

		// Skip "My Drive" folder - it's just the root container
		if folder.Name != "My Drive" {
			// Build segment: folderID_folderName
			segment := fmt.Sprintf("%s_%s", folder.Id, folder.Name)
			pathSegments = append([]string{segment}, pathSegments...)
		}

		// Move to parent
		if len(folder.Parents) > 0 {
			currentID = folder.Parents[0]
		} else {
			break
		}
	}

	finalPath := strings.Join(pathSegments, "/")
	// Cache the result for future use
	cache[cacheKey] = finalPath
	return finalPath
}
