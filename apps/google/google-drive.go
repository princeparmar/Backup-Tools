package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	gs "google.golang.org/api/storage/v1"
)

type FilesJSON struct {
	Name              string    `json:"file_name"`
	ID                string    `json:"file_id"`
	MimeType          string    `json:"mime_type"`
	FileType          string    `json:"file_type"`
	Synced            bool      `json:"synced"`
	Size              int64     `json:"size"`
	FullFileExtension string    `json:"full_file_extension"`
	FileExtension     string    `json:"file_extension"`
	Path              string    `json:"path"`
	CreatedAt         time.Time `json:"created_at"`
}

// PaginatedFilesResponse represents a paginated response for Google Drive files
type PaginatedFilesResponse struct {
	Files         []*FilesJSON `json:"files"`
	NextPageToken string       `json:"next_page_token,omitempty"`
	Limit         int64        `json:"limit"`
	TotalFiles    int64        `json:"total_files"`
}

// createFilesJSON creates a FilesJSON object from a Google Drive file
func createFilesJSON(file *drive.File, synced bool, path string) *FilesJSON {
	// Parse created time
	var createdAt time.Time
	if file.CreatedTime != "" {
		createdAt, _ = time.Parse(time.RFC3339, file.CreatedTime)
	}

	// Handle size for different file types
	size := file.Size

	// Check if it's a Google Apps file (all have no physical size)
	if isGoogleAppsFile(file.MimeType) {
		// For Google Apps files, they don't have a physical size in Drive API
		// Set to -1 to indicate it's a Google Apps file
		size = -1
	}
	// For regular files, if size is 0, it means the file is actually 0 bytes
	// This is different from Google Apps files which have no size concept

	return &FilesJSON{
		Name:              file.Name,
		ID:                file.Id,
		MimeType:          file.MimeType,
		FileType:          getFileType(file.MimeType),
		Path:              path,
		Size:              size,
		FullFileExtension: file.FullFileExtension,
		FileExtension:     file.FileExtension,
		CreatedAt:         createdAt,
		Synced:            synced,
	}
}

// GetFileNames retrieves all file names and their IDs from Google Drive
func GetFileNames(c echo.Context) ([]*FilesJSON, error) {

	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	var fileResp []*FilesJSON

	// Loop to handle pagination
	pageToken := ""
	for {
		r, err := srv.Files.List().Q("trashed=false").Fields("nextPageToken, files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").PageToken(pageToken).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve files: %v", err)
		}

		// Append files to response
		for _, i := range r.Files {
			pathH, _ := GetFolderPathByID(context.Background(), srv, i.Id)
			fileResp = append(fileResp, createFilesJSON(i, false, pathH))
		}

		// Check if there's another page
		pageToken = r.NextPageToken
		if pageToken == "" {
			break // No more pages
		}
	}

	return fileResp, nil
}

// GetFileByID returns file by ID as attachment
func GetFileByID(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	name, data, err := GetFile(c, c.Param("ID"))
	if err != nil {
		return c.String(http.StatusForbidden, "error")
	}

	userCachePath := filepath.Join("./cache", utils.CreateUserTempCacheFolder(), name)
	if err := os.WriteFile(userCachePath, data, 0644); err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	defer os.Remove(userCachePath)

	return c.Attachment(userCachePath, name)
}

// client authenticates the client and returns an HTTP client
func client(c echo.Context) (*http.Client, error) {
	ctx := c.Request().Context()
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %v", err)
	}
	config, err := google.ConfigFromJSON(b, drive.DriveScope, gs.CloudPlatformScope, gs.DevstorageFullControlScope, gs.DevstorageReadWriteScope,
		photoslibrary.PhotoslibraryReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}
	googleToken, err := GetGoogleTokenFromJWT(c)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from JWT: %v", err)
	}

	logger.Info(ctx, "processing google token"+googleToken)

	tok, err := database.AuthRepo.ReadGoogleAuthToken(googleToken)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "user is not authorized")
	}

	logger.Info(ctx, "processing access token"+tok)
	client := config.Client(context.Background(), &oauth2.Token{
		AccessToken: tok,
	})

	return client, nil
}

// Helper function to check if a MIME type is a Google Apps file
func isGoogleAppsFile(mimeType string) bool {
	googleAppsMimeTypes := []string{
		"application/vnd.google-apps.document",     // Google Docs
		"application/vnd.google-apps.spreadsheet",  // Google Sheets
		"application/vnd.google-apps.presentation", // Google Slides
		"application/vnd.google-apps.form",         // Google Forms
		"application/vnd.google-apps.drawing",      // Google Drawings
		"application/vnd.google-apps.map",          // Google My Maps
		"application/vnd.google-apps.site",         // Google Sites
		"application/vnd.google-apps.script",       // Google Apps Script
	}

	for _, googleAppMimeType := range googleAppsMimeTypes {
		if mimeType == googleAppMimeType {
			return true
		}
	}
	return false
}

// Helper function to add file extension for Google Apps files
func addGoogleAppsFileExtension(fileName string, mimeType string) string {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return fileName + ".docx"
	case "application/vnd.google-apps.spreadsheet":
		return fileName + ".xlsx"
	case "application/vnd.google-apps.presentation":
		return fileName + ".pptx"
	case "application/vnd.google-apps.script":
		return fileName + ".json"
	default:
		return fileName
	}
}

// Helper function to get export MIME type and add extension for Google Apps files
func getExportMimeTypeAndExtension(mimeType string) (string, string) {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"
	case "application/vnd.google-apps.spreadsheet":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"
	case "application/vnd.google-apps.presentation":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"
	case "application/vnd.google-apps.site":
		return "text/plain", ""
	case "application/vnd.google-apps.script":
		return "application/vnd.google-apps.script+json", ".json"
	default:
		return mimeType, ""
	}
}

// Helper function to sort satellite objects by key
// func sortSatelliteObjects(objects []uplink.Object) {
// 	slices.SortStableFunc(objects, func(a, b uplink.Object) int {
// 		return cmp.Compare(a.Key, b.Key)
// 	})
// }

// // Helper function to check if file is synced using binary search
// func isFileSyncedInObjects(objects []uplink.Object, searchPath string) bool {
// 	_, found := slices.BinarySearchFunc(objects, searchPath, func(a uplink.Object, b string) int {
// 		return cmp.Compare(a.Key, b)
// 	})
// 	return found
// }

// Helper function to check folder sync status
// func checkFolderSyncStatus(c echo.Context, folderID string) (bool, error) {
// 	folderFiles, err := GetFilesInFolderByID(c, folderID)
// 	if err != nil {
// 		return false, err
// 	}

// 	for _, v := range folderFiles.Files {
// 		if !v.Synced {
// 			return false, nil
// 		}
// 	}
// 	return true, nil
// }

// Helper function to get Google Drive service with error handling
func getDriveService(c echo.Context) (*drive.Service, error) {
	client, err := client(c)
	if err != nil {
		return nil, err
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
	}

	return srv, nil
}

func clientUsingToken(token string) (*http.Client, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %v", err)
	}
	config, err := google.ConfigFromJSON(b, drive.DriveScope, gs.CloudPlatformScope, gs.DevstorageFullControlScope, gs.DevstorageReadWriteScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}

	return config.Client(context.Background(), &oauth2.Token{
		AccessToken: token,
	}), nil
}

// GetFile downloads file from Google Drive by ID
func GetFile(c echo.Context, id string) (string, []byte, error) {

	srv, err := getDriveService(c)
	if err != nil {
		return "", nil, err
	}

	file, err := srv.Files.Get(id).Do()
	if err != nil {
		return "", nil, fmt.Errorf("unable to retrieve file metadata: %v", err)
	}

	res, err := srv.Files.Get(id).Download()
	if err != nil {
		if strings.Contains(err.Error(), "Use Export with Docs Editors files., fileNotDownloadable") {
			mt, ext := getExportMimeTypeAndExtension(file.MimeType)
			file.Name += ext
			// handle folders
			if mt != "application/vnd.google-apps.folder" {
				if res, err = srv.Files.Export(id, mt).Download(); err != nil {
					return "", nil, fmt.Errorf("unable to download file: %v", err)
				}
			} else {
				return file.Name, nil, errors.New("folder error")
			}
		} else {
			return "", nil, fmt.Errorf("unable to download file: %v", err)
		}
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", nil, fmt.Errorf("unable to read file content: %v", err)
	}

	return file.Name, data, nil
}

// Uploads file to Google Drive.
func UploadFile(c echo.Context, name string, data []byte) error {

	srv, err := getDriveService(c)
	if err != nil {
		return err
	}
	_, err = srv.Files.Create(&drive.File{Name: name}).Media(bytes.NewReader(data)).Do()
	if err != nil {
		return err
	}

	return nil
}

// // GetFilesInFolder retrieves all files within a specific folder from Google Drive
// func GetFilesInFolder(c echo.Context, folderName string) ([]*FilesJSON, error) {

// 	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accessGrant == "" {
// 		return nil, errors.New("access token is missing")
// 	}
// 	srv, err := getDriveService(c)
// 	if err != nil {
// 		return nil, err
// 	}

// 	// Get user email for sync checking
// 	userDetails, err := GetGoogleAccountDetailsFromContext(c)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to get user email: %w", err)
// 	}
// 	if userDetails.Email == "" {
// 		return nil, errors.New("user email not found, please check access handling")
// 	}

// 	// Get folder ID by name
// 	folderID, err := getFolderIDByName(srv, folderName)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to get folder ID: %v", err)
// 	}
// 	folderName, err = GetFolderPathByID(context.Background(), srv, folderID)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to get folder name: %v", err)
// 	}
// 	// Files are stored with userEmail prefix, so prepend it to folder path
// 	satelliteFolderPath := userDetails.Email + "/" + folderName + "/"
// 	o, err := satellite.GetFilesInFolder(context.Background(), accessGrant, "google-drive", satelliteFolderPath)
// 	if err != nil {
// 		userFriendlyError := satellite.FormatSatelliteError(err)
// 		return nil, fmt.Errorf("%s", userFriendlyError)
// 	}
// 	sortSatelliteObjects(o)
// 	// List all files within the folder
// 	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Fields("files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").Do()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to retrieve files: %v", err)
// 	}

// 	var files []*FilesJSON
// 	for _, i := range r.Files {
// 		if i.MimeType != "application/vnd.google-apps.folder" {
// 			i.Name = addGoogleAppsFileExtension(i.Name, i.MimeType)
// 			// Check sync with userEmail prefix
// 			filePath := userDetails.Email + "/" + path.Join(folderName, i.Name)
// 			synced := isFileSyncedInObjects(o, filePath)
// 			files = append(files, createFilesJSON(i, synced, ""))
// 		} else {
// 			// Check sync with userEmail prefix
// 			folderPath := userDetails.Email + "/" + path.Join(folderName, i.Name) + "/"
// 			synced := isFileSyncedInObjects(o, folderPath)
// 			if synced {
// 				synced, _ = checkFolderSyncStatus(c, i.Id)
// 			}

// 			files = append(files, createFilesJSON(i, synced, ""))
// 		}
// 	}

// 	return files, nil
// }

// func embeddedSynced(c echo.Context, folderID, folderName string)
// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFilesInFolderByID(c echo.Context, folderID string, database *db.PostgresDb, userID string) (*PaginatedFilesResponse, error) {

	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token is missing")
	}
	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	// Get user email for sync checking
	userDetails, err := GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get user email: %w", err)
	}
	if userDetails.Email == "" {
		return nil, errors.New("user email not found, please check access handling")
	}

	folderName, err := GetFolderPathByID(context.Background(), srv, folderID)
	if err != nil {
		logger.Warn(c.Request().Context(), "Failed to get folder path, using empty path",
			logger.String("folder_id", folderID),
			logger.ErrorField(err),
		)
		folderName = ""
	}

	// Get synced objects from database and create map for fast lookup
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Drive, "google", "drive")
	if err != nil {
		logger.Warn(c.Request().Context(), "Failed to get synced objects from database, continuing with empty map",
			logger.String("user_id", userID),
			logger.String("bucket", satellite.ReserveBucket_Drive),
			logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}

	syncedMap := make(map[string]bool)
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	// Parse filter
	filter, err := ParseFilter(c.QueryParam("filter"))
	if err != nil {
		return nil, err
	}

	// Build query
	query := fmt.Sprintf("'%s' in parents and trashed=false", folderID)
	if filter != nil && filter.Query != "" {
		query = filter.Query
	} else if filter != nil {
		query = applyFiltersToQuery(query, filter)
	}

	// Set up pagination
	pageSize, pageToken := GetPaginationParams(filter)

	// List all files within the folder (include shortcutDetails to resolve shortcuts, owners to detect shared files)
	r, err := srv.Files.List().
		Q(query).
		Fields("nextPageToken, files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension, shortcutDetails, owners)").
		PageToken(pageToken).
		PageSize(pageSize).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, i := range r.Files {
		// Resolve shortcut to target file for sync check
		fileIDForSync, fileNameForSync, mimeTypeForSync := resolveShortcutForSync(srv, i, c.Request().Context())

		// Determine if file is shared by checking owner (O(1), no extra API/DB calls)
		isShared := len(i.Owners) > 0 && i.Owners[0].EmailAddress != userDetails.Email

		if i.MimeType != "application/vnd.google-apps.folder" {
			i.Name = addGoogleAppsFileExtension(i.Name, i.MimeType)
			// Don't add extension to fileNameForSync - isFileSyncedWithMap will add it internally based on mimeType
			synced := isFileSyncedWithMap(syncedMap, fileIDForSync, fileNameForSync, mimeTypeForSync, userDetails.Email, folderName, isShared)
			files = append(files, createFilesJSON(i, synced, ""))
		} else {
			// Check sync with folder path prefix for nested folders
			synced := isFileSyncedWithMap(syncedMap, fileIDForSync, fileNameForSync, mimeTypeForSync, userDetails.Email, folderName, isShared)
			if synced {
				// Recursively check if all files in this nested folder are synced
				// Use helper function that checks ALL pages, not just the first page
				nestedFolderPath := folderName
				if nestedFolderPath != "" {
					nestedFolderPath = fmt.Sprintf("%s/%s_%s", nestedFolderPath, fileIDForSync, fileNameForSync)
				} else {
					nestedFolderPath = fmt.Sprintf("%s_%s", fileIDForSync, fileNameForSync)
				}
				if allNestedSynced := checkAllFilesInFolderSyncedRecursive(c, fileIDForSync, database, userID, syncedMap, userDetails.Email, nestedFolderPath); !allNestedSynced {
					synced = false
				}
			}

			files = append(files, createFilesJSON(i, synced, ""))
		}
	}

	return &PaginatedFilesResponse{
		Files:         files,
		NextPageToken: r.NextPageToken,
		Limit:         pageSize,
		TotalFiles:    int64(len(files)),
	}, nil
}

// GetFilesInFolder retrieves all files within a specific folder from Google Drive(need to comment out this function)
// func GetFolderNameAndFilesInFolderByID(c echo.Context, folderID string) (string, []*FilesJSON, error) {

// 	srv, err := getDriveService(c)
// 	if err != nil {
// 		return "", nil, err
// 	}
// 	folderName, err := getFolderNameByID(srv, folderID)
// 	if err != nil {
// 		return "", nil, fmt.Errorf("failed to get folder name: %v", err)
// 	}
// 	// List all files within the folder
// 	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents and trashed=false", folderID)).Fields("files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").Do()
// 	if err != nil {
// 		return folderName, nil, fmt.Errorf("failed to retrieve files: %v", err)
// 	}

// 	var files []*FilesJSON
// 	for _, f := range r.Files {
// 		files = append(files, createFilesJSON(f, false, ""))
// 	}

// 	return folderName, files, nil
// }

// Helper function to get folder ID by name
// func getFolderIDByName(srv *drive.Service, folderName string) (string, error) {

// 	r, err := srv.Files.List().Q(fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder'", folderName)).Fields("files(id)").Do()
// 	if err != nil {
// 		return "", fmt.Errorf("failed to retrieve folder ID: %v", err)
// 	}
// 	if len(r.Files) == 0 {
// 		return "", fmt.Errorf("folder '%s' not found", folderName)
// 	}
// 	return r.Files[0].Id, nil
// }

// Helper function to get folder ID by name
// func getFolderNameByID(srv *drive.Service, folderID string) (string, error) {

// 	r, err := srv.Files.List().Q(fmt.Sprintf("id='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false", folderID)).Fields("files(name)").Do()
// 	if err != nil {
// 		return "", fmt.Errorf("failed to retrieve folder ID: %v", err)
// 	}
// 	if len(r.Files) == 0 {
// 		return "", fmt.Errorf("folder '%s' not found", folderID)
// 	}
// 	return r.Files[0].Name, nil
// }

// Helper function to determine file type based on MIME type
func getFileType(mimeType string) string {
	switch {
	case utils.Contains([]string{
		"application/vnd.google-apps.document",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/msword", "application/vnd.oasis.opendocument.text",
		"application/rtf", "text/plain",
	}, mimeType):
		return "docs"

	case utils.Contains([]string{
		"application/vnd.google-apps.spreadsheet",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel", "application/vnd.oasis.opendocument.spreadsheet",
		"text/csv",
	}, mimeType):
		return "sheets"

	case utils.Contains([]string{
		"application/vnd.google-apps.presentation",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.ms-powerpoint", "application/vnd.oasis.opendocument.presentation",
	}, mimeType):
		return "slides"

	case utils.Contains([]string{
		"image/jpeg", "image/png", "image/gif", "image/bmp", "image/tiff",
		"image/svg+xml", "image/webp",
	}, mimeType) || strings.HasPrefix(mimeType, "image/"):
		return "images"

	case utils.Contains([]string{
		"video/webm", "video/mp4", "video/3gpp", "video/quicktime",
		"video/x-msvideo", "video/mpeg", "video/x-ms-wmv", "video/x-flv",
		"video/ogg", "video/mov", "video/avi", "video/mpegps",
	}, mimeType) || strings.HasPrefix(mimeType, "video/"):
		return "videos"

	case utils.Contains([]string{
		"audio/mpeg", "audio/mp4", "audio/wav", "audio/ogg", "audio/opus",
	}, mimeType) || strings.HasPrefix(mimeType, "audio/"):
		return "audio"

	case mimeType == "application/pdf":
		return "pdf"

	case utils.Contains([]string{
		"application/zip", "application/x-rar-compressed", "application/x-rar", "application/x-tar",
		"application/gzip", "application/x-gzip", "application/x-7z-compressed", "application/epub+zip",
		"application/x-bzip2", "application/x-bzip", "application/java-archive", "application/vnd.android.package-archive",
		"application/x-deb", "application/x-rpm", "application/x-apple-diskimage", "application/vnd.ms-cab-compressed",
		"application/x-lzh-compressed", "application/x-compress", "application/x-ace-compressed", "application/x-arj",
		"application/x-cpio",
	}, mimeType):
		return "zip"

	case utils.Contains([]string{
		"text/css", "text/html", "text/php", "text/x-c", "text/x-c++",
		"text/x-h", "text/javascript", "text/x-java-source", "text/x-python",
		"text/x-sql", "text/xml", "application/json", "text/markdown",
		"text/tab-separated-values",
	}, mimeType):
		return "code"

	case mimeType == "application/vnd.google-apps.drawing":
		return "drawings"
	case mimeType == "application/vnd.google-apps.form":
		return "forms"
	case mimeType == "application/vnd.google-apps.site":
		return "sites"
	case mimeType == "application/vnd.google-apps.script":
		return "scripts_apps"
	case mimeType == "application/vnd.google-apps.jam":
		return "jams"
	case mimeType == "application/vnd.google-apps.folder":
		return "folders"

	default:
		return "other"
	}
}

// GoogleDriveFilter represents filter parameters for Google Drive file queries
type GoogleDriveFilter struct {
	FolderOnly   bool   `json:"folder_only,omitempty"`   // Filter only folders
	FilesOnly    bool   `json:"files_only,omitempty"`    // Filter only files (not folders)
	FileType     string `json:"file_type,omitempty"`     // Filter by file type (documents, images, etc.)
	Owner        string `json:"owner,omitempty"`         // Filter by owner
	DateModified string `json:"date_modified,omitempty"` // Filter by date modified
	Query        string `json:"query,omitempty"`         // Raw Google Drive search query
	Limit        int64  `json:"limit,omitempty"`         // Number of files per page (max 1000, default 100)
	PageToken    string `json:"page_token,omitempty"`    // Token for pagination (next page)
}

// buildFileTypeQuery builds a Google Drive query string based on file type filter
func buildFileTypeQuery(fileType string) string {
	switch strings.ToLower(fileType) {
	case "docs":
		return " and (mimeType = 'application/vnd.google-apps.document' or mimeType = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' or mimeType = 'application/msword' or mimeType = 'application/vnd.oasis.opendocument.text' or mimeType = 'application/rtf' or mimeType = 'text/plain')"

	case "sheets":
		return " and (mimeType = 'application/vnd.google-apps.spreadsheet' or mimeType = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' or mimeType = 'application/vnd.ms-excel' or mimeType = 'application/vnd.oasis.opendocument.spreadsheet' or mimeType = 'text/csv')"

	case "slides":
		return " and (mimeType = 'application/vnd.google-apps.presentation' or mimeType = 'application/vnd.openxmlformats-officedocument.presentationml.presentation' or mimeType = 'application/vnd.ms-powerpoint' or mimeType = 'application/vnd.oasis.opendocument.presentation')"

	case "images":
		return " and (mimeType contains 'image/' or mimeType = 'image/jpeg' or mimeType = 'image/png' or mimeType = 'image/gif' or mimeType = 'image/bmp' or mimeType = 'image/tiff' or mimeType = 'image/svg+xml' or mimeType = 'image/webp')"

	case "videos":
		return " and (mimeType contains 'video/' or mimeType = 'video/webm' or mimeType = 'video/mp4' or mimeType = 'video/3gpp' or mimeType = 'video/quicktime' or mimeType = 'video/x-msvideo' or mimeType = 'video/mpeg' or mimeType = 'video/x-ms-wmv' or mimeType = 'video/x-flv' or mimeType = 'video/ogg' or mimeType = 'video/mov' or mimeType = 'video/avi' or mimeType = 'video/mpegps')"

	case "audio":
		return " and (mimeType contains 'audio/' or mimeType = 'audio/mpeg' or mimeType = 'audio/mp4' or mimeType = 'audio/wav' or mimeType = 'audio/ogg' or mimeType = 'audio/opus')"

	case "pdf":
		return " and mimeType = 'application/pdf'"

	case "zip":
		return " and (mimeType = 'application/zip' or mimeType = 'application/x-rar-compressed' or mimeType = 'application/x-rar' or mimeType = 'application/x-tar' or mimeType = 'application/gzip' or mimeType = 'application/x-gzip' or mimeType = 'application/x-7z-compressed' or mimeType = 'application/epub+zip' or mimeType = 'application/x-bzip2' or mimeType = 'application/x-bzip' or mimeType = 'application/java-archive' or mimeType = 'application/vnd.android.package-archive' or mimeType = 'application/x-deb' or mimeType = 'application/x-rpm' or mimeType = 'application/x-apple-diskimage' or mimeType = 'application/vnd.ms-cab-compressed' or mimeType = 'application/x-lzh-compressed' or mimeType = 'application/x-compress' or mimeType = 'application/x-ace-compressed' or mimeType = 'application/x-arj' or mimeType = 'application/x-cpio')"
	case "code":
		return " and (mimeType = 'text/css' or mimeType = 'text/html' or mimeType = 'text/php' or mimeType = 'text/x-c' or mimeType = 'text/x-c++' or mimeType = 'text/x-h' or mimeType = 'text/javascript' or mimeType = 'text/x-java-source' or mimeType = 'text/x-python' or mimeType = 'text/x-sql' or mimeType = 'text/xml' or mimeType = 'application/json' or mimeType = 'text/markdown' or mimeType = 'text/csv' or mimeType = 'text/tab-separated-values')"

	case "drawings":
		return " and mimeType = 'application/vnd.google-apps.drawing'"

	case "forms":
		return " and mimeType = 'application/vnd.google-apps.form'"

	case "sites":
		return " and mimeType = 'application/vnd.google-apps.site'"

	case "scripts_apps":
		return " and mimeType = 'application/vnd.google-apps.script'"

	case "jams":
		return " and mimeType = 'application/vnd.google-apps.jam'"

	case "folders":
		return " and mimeType = 'application/vnd.google-apps.folder'"

	default:
		return ""
	}
}
func buildCustomDateQuery(dateRange string) string {
	dates := strings.Split(dateRange, ",")
	if len(dates) == 1 {
		return " and modifiedTime > '" + strings.TrimSpace(dates[0]) + "T00:00:00.000Z'"
	}
	if len(dates) == 2 {
		start := strings.TrimSpace(dates[0]) + "T00:00:00.000Z"
		end := strings.TrimSpace(dates[1]) + "T23:59:59.999Z"
		return " and modifiedTime > '" + start + "' and modifiedTime < '" + end + "'"
	}
	return ""
}

func buildDateModifiedQuery(dateModified string) string {
	now := time.Now()
	lower := strings.ToLower(dateModified)

	type dateFn func() (start, end string)
	ranges := map[string]dateFn{
		"today":        func() (string, string) { return formatTime(now), "" },
		"yesterday":    func() (string, string) { return formatTime(now.AddDate(0, 0, -1)), formatTime(now) },
		"last_7_days":  func() (string, string) { return formatTime(now.AddDate(0, 0, -7)), "" },
		"last_30_days": func() (string, string) { return formatTime(now.AddDate(0, 0, -30)), "" },
		"this_year": func() (string, string) {
			return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02T00:00:00.000Z"), ""
		},
		"last_year": func() (string, string) {
			lastYear := now.Year() - 1
			return time.Date(lastYear, 1, 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02T00:00:00.000Z"),
				time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02T00:00:00.000Z")
		},
	}

	if fn, exists := ranges[lower]; exists {
		start, end := fn()
		if end == "" {
			return " and modifiedTime > '" + start + "'"
		}
		return " and modifiedTime > '" + start + "' and modifiedTime < '" + end + "'"
	}
	return buildCustomDateQuery(dateModified)
}

func formatTime(t time.Time) string { return t.Format("2006-01-02T00:00:00.000Z") }

// This function gets files only in root. It does not list files in folders
func GetFileNamesInRoot(c echo.Context, database *db.PostgresDb, userID string) (*PaginatedFilesResponse, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token is missing")
	}

	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	// Get user email for sync checking
	userDetails, err := GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get user email: %w", err)
	}
	if userDetails.Email == "" {
		return nil, errors.New("user email not found, please check access handling")
	}

	// Get synced objects from database and create map for fast lookup
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Drive, "google", "drive")
	if err != nil {
		logger.Warn(c.Request().Context(), "Failed to get synced objects from database, continuing with empty map",
			logger.String("user_id", userID),
			logger.String("bucket", satellite.ReserveBucket_Drive),
			logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}

	syncedMap := make(map[string]bool)
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	// Parse filter
	filter, err := ParseFilter(c.QueryParam("filter"))
	if err != nil {
		return nil, err
	}

	// Build query
	query := "'root' in parents and trashed=false"
	if filter != nil && filter.Query != "" {
		query = filter.Query
	} else if filter != nil {
		query = applyFiltersToQuery(query, filter)
	}

	// Set up pagination
	pageSize, pageToken := GetPaginationParams(filter)

	// Fetch files from Google Drive
	response, err := srv.Files.List().
		Q(query).
		Fields("nextPageToken, files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").
		PageToken(pageToken).
		PageSize(pageSize).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %w", err)
	}

	// Process files using syncedMap instead of satelliteObjects
	files := processRootFilesWithMap(response.Files, syncedMap, c, userDetails.Email, database, userID)

	return &PaginatedFilesResponse{
		Files:         files,
		NextPageToken: response.NextPageToken,
		Limit:         pageSize,
		TotalFiles:    int64(len(files)),
	}, nil
}

func GetSharedFiles(c echo.Context, database *db.PostgresDb, userID string) (*PaginatedFilesResponse, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token not found")
	}
	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	// Get user email for sync checking
	userDetails, err := GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get user email: %w", err)
	}
	if userDetails.Email == "" {
		return nil, errors.New("user email not found, please check access handling")
	}

	// Get synced objects from database and create map for fast lookup
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Drive, "google", "drive")
	if err != nil {
		logger.Warn(c.Request().Context(), "Failed to get synced objects from database, continuing with empty map",
			logger.String("user_id", userID),
			logger.String("bucket", satellite.ReserveBucket_Drive),
			logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}

	syncedMap := make(map[string]bool)
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	// Parse filter
	filter, err := ParseFilter(c.QueryParam("filter"))
	if err != nil {
		return nil, err
	}

	// Build query
	query := "sharedWithMe=true and trashed=false"
	if filter != nil && filter.Query != "" {
		query = filter.Query
	} else if filter != nil {
		query = applyFiltersToQuery(query, filter)
	}

	// Set up pagination
	pageSize, pageToken := GetPaginationParams(filter)

	// Fetch files from Google Drive (include owners to detect shared files)
	response, err := srv.Files.List().
		Q(query).
		Fields("nextPageToken, files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension, owners)").
		PageToken(pageToken).
		PageSize(pageSize).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve shared files: %w", err)
	}

	// Process files using syncedMap instead of satelliteObjects
	files := processSharedFilesWithMap(response.Files, syncedMap, c, userDetails.Email, database, userID)

	return &PaginatedFilesResponse{
		Files:         files,
		NextPageToken: response.NextPageToken,
		Limit:         pageSize,
		TotalFiles:    int64(len(files)),
	}, nil
}

func ParseFilter(filterParam string) (*GoogleDriveFilter, error) {
	if filterParam == "" {
		return nil, nil
	}

	// URL decode the filter string
	decodedFilter, err := url.QueryUnescape(filterParam)
	if err != nil {
		return nil, fmt.Errorf("failed to URL decode filter: %w", err)
	}

	// Parse the JSON string into GoogleDriveFilter struct
	var filter GoogleDriveFilter
	if err := json.Unmarshal([]byte(decodedFilter), &filter); err != nil {
		return nil, fmt.Errorf("failed to parse filter JSON: %w", err)
	}

	return &filter, nil
}

func applyFiltersToQuery(baseQuery string, filter *GoogleDriveFilter) string {
	query := baseQuery

	// Always exclude trashed files unless already in query
	if !strings.Contains(query, "trashed") {
		query += " and trashed=false"
	}

	// Apply folder/file filters
	if filter.FolderOnly {
		query += " and mimeType = 'application/vnd.google-apps.folder'"
	} else if filter.FilesOnly {
		query += " and mimeType != 'application/vnd.google-apps.folder'"
	}

	// Add file type filtering
	if filter.FileType != "" {
		query += buildFileTypeQuery(filter.FileType)
	}

	// Add date modified filtering
	if filter.DateModified != "" {
		query += buildDateModifiedQuery(filter.DateModified)
	}

	return query
}

func GetPaginationParams(filter *GoogleDriveFilter) (int64, string) {
	pageSize, pageToken := int64(25), ""
	if filter != nil {
		if filter.Limit > 0 {
			pageSize = utils.Min(filter.Limit, 1000)
		}
		pageToken = filter.PageToken
	}
	return pageSize, pageToken
}

// processSharedFilesWithMap processes shared files using synced_objects map instead of Satellite API
func processSharedFilesWithMap(driveFiles []*drive.File, syncedMap map[string]bool, c echo.Context, userEmail string, database *db.PostgresDb, userID string) []*FilesJSON {
	var files []*FilesJSON

	for _, file := range driveFiles {
		// Resolve shortcut to target file for sync check
		srv, err := getDriveService(c)
		if err == nil {
			fileIDForSync, fileNameForSync, mimeTypeForSync := resolveShortcutForSync(srv, file, c.Request().Context())

			// Determine if file is shared by checking owner (O(1), no extra API/DB calls)
			// Note: Files from GetSharedFiles query are already shared, but we check owner for consistency
			isShared := len(file.Owners) > 0 && file.Owners[0].EmailAddress != userEmail

			// Check sync status for shared files - paths are: email/shared with me/filename
			synced := isFileSyncedWithMap(syncedMap, fileIDForSync, fileNameForSync, mimeTypeForSync, userEmail, "", isShared)

			// For folders, check if all nested files are synced
			// If placeholder exists OR all nested files are synced, consider folder as synced
			// This handles cases where placeholder wasn't created but all files were uploaded
			if mimeTypeForSync == "application/vnd.google-apps.folder" {
				folderPath := fmt.Sprintf("%s_%s", fileIDForSync, fileNameForSync)
				allNestedSynced := checkAllFilesInFolderSyncedRecursive(c, fileIDForSync, database, userID, syncedMap, userEmail, folderPath)
				// Folder is synced if placeholder exists OR all nested files are synced
				synced = synced || allNestedSynced
			}

			files = append(files, createFilesJSON(file, synced, ""))
		} else {
			// Fallback if service can't be created
			synced := isFileSyncedWithMap(syncedMap, file.Id, file.Name, file.MimeType, userEmail, "", true)
			files = append(files, createFilesJSON(file, synced, ""))
		}
	}

	return files
}

// processRootFilesWithMap processes root files using synced_objects map instead of Satellite API
func processRootFilesWithMap(driveFiles []*drive.File, syncedMap map[string]bool, c echo.Context, userEmail string, database *db.PostgresDb, userID string) []*FilesJSON {
	var files []*FilesJSON

	for _, file := range driveFiles {
		// Resolve shortcut to target file for sync check
		srv, err := getDriveService(c)
		if err == nil {
			fileIDForSync, fileNameForSync, mimeTypeForSync := resolveShortcutForSync(srv, file, c.Request().Context())

			// Check sync status - scheduled processor uses fileID_filename format
			synced := isFileSyncedWithMap(syncedMap, fileIDForSync, fileNameForSync, mimeTypeForSync, userEmail, "", false)
			if mimeTypeForSync == "application/vnd.google-apps.folder" && synced {
				folderPath := fmt.Sprintf("%s_%s", fileIDForSync, fileNameForSync)
				if allNestedSynced := checkAllFilesInFolderSyncedRecursive(c, fileIDForSync, database, userID, syncedMap, userEmail, folderPath); !allNestedSynced {
					synced = false
				}
			}

			files = append(files, createFilesJSON(file, synced, ""))
		} else {
			// Fallback if service can't be created
			synced := isFileSyncedWithMap(syncedMap, file.Id, file.Name, file.MimeType, userEmail, "", false)
			files = append(files, createFilesJSON(file, synced, ""))
		}
	}

	return files
}

// checkAllFilesInFolderSyncedRecursive recursively checks if all files in a folder are synced
// This function handles pagination to check ALL files, not just the first page
func checkAllFilesInFolderSyncedRecursive(c echo.Context, folderID string, database *db.PostgresDb, userID string, syncedMap map[string]bool, userEmail string, folderPath string) bool {
	srv, err := getDriveService(c)
	if err != nil {
		return false
	}

	pageToken := ""
	for {
		query := fmt.Sprintf("'%s' in parents and trashed=false", folderID)
		listCall := srv.Files.List().Q(query).Fields("nextPageToken, files(id, name, mimeType, shortcutDetails, owners)")

		if pageToken != "" {
			listCall = listCall.PageToken(pageToken)
		}

		r, err := listCall.Do()
		if err != nil {
			logger.Warn(c.Request().Context(), "Failed to list files in folder for sync check",
				logger.String("folder_id", folderID),
				logger.ErrorField(err))
			return false
		}

		// Process files in this page
		hasFiles := false
		for _, file := range r.Files {
			hasFiles = true
			// Resolve shortcut to target file for sync check
			fileIDForSync, fileNameForSync, mimeTypeForSync := resolveShortcutForSync(srv, file, c.Request().Context())

			// Determine if file is shared by checking owner (O(1), no extra API/DB calls)
			isShared := len(file.Owners) > 0 && file.Owners[0].EmailAddress != userEmail

			// Use mimeTypeForSync (resolved) to check if it's a folder, not file.MimeType
			if mimeTypeForSync != "application/vnd.google-apps.folder" {
				// For files, isFileSyncedWithMap will add the extension
				if !isFileSyncedWithMap(syncedMap, fileIDForSync, fileNameForSync, mimeTypeForSync, userEmail, folderPath, isShared) {
					return false
				}
			} else {
				// For folders, check if folder placeholder exists
				if !isFileSyncedWithMap(syncedMap, fileIDForSync, fileNameForSync, mimeTypeForSync, userEmail, folderPath, isShared) {
					return false
				}
				// Recursively check nested folder
				nestedFolderPath := folderPath
				if nestedFolderPath != "" {
					nestedFolderPath = fmt.Sprintf("%s/%s_%s", nestedFolderPath, fileIDForSync, fileNameForSync)
				} else {
					nestedFolderPath = fmt.Sprintf("%s_%s", fileIDForSync, fileNameForSync)
				}
				if !checkAllFilesInFolderSyncedRecursive(c, fileIDForSync, database, userID, syncedMap, userEmail, nestedFolderPath) {
					return false
				}
			}
		}

		// If no more pages and no files found, folder is empty and considered synced
		if !hasFiles && r.NextPageToken == "" {
			return true
		}

		if r.NextPageToken == "" {
			break
		}
		pageToken = r.NextPageToken
	}

	return true
}

// getAlternateBasePath returns the opposite base path (shared vs regular) for checking alternate locations
func getAlternateBasePath(userEmail string, isShared bool) string {
	if isShared {
		return userEmail
	}
	return fmt.Sprintf("%s/shared with me", userEmail)
}

// resolveShortcutForSync resolves a shortcut to its target file for sync checking
// Returns target file's ID, name, and MIME type (or shortcut's own if resolution fails)
func resolveShortcutForSync(srv *drive.Service, file *drive.File, ctx context.Context) (fileID, fileName, mimeType string) {
	// Regular file - use its own ID
	if file.MimeType != "application/vnd.google-apps.shortcut" {
		return file.Id, file.Name, file.MimeType
	}

	// Shortcut without target - use shortcut ID
	if file.ShortcutDetails == nil || file.ShortcutDetails.TargetId == "" {
		return file.Id, file.Name, file.MimeType
	}

	// Get target file to use its ID for sync check
	targetFile, err := srv.Files.Get(file.ShortcutDetails.TargetId).
		Fields("id", "name", "mimeType").
		Do()
	if err != nil {
		logger.Warn(ctx, "Failed to get target file for shortcut, using shortcut ID",
			logger.String("shortcut_id", file.Id),
			logger.String("shortcut_name", file.Name),
			logger.String("target_id", file.ShortcutDetails.TargetId),
			logger.ErrorField(err),
		)
		return file.Id, file.Name, file.MimeType
	}

	// Update file for display
	file.Name = targetFile.Name
	file.MimeType = targetFile.MimeType

	// Use target file's ID and name for sync check (matches upload format)
	return targetFile.Id, targetFile.Name, targetFile.MimeType
}

func isFileSyncedWithMap(syncedMap map[string]bool, fileID, fileName, mimeType, userEmail string, folderPath string, isShared bool) bool {
	// For Google Apps files, add extension (for regular files like PDFs, fileName already includes extension)
	_, ext := getExportMimeTypeAndExtension(mimeType)

	// Build base paths once
	basePath := userEmail
	if isShared {
		basePath = fmt.Sprintf("%s/shared with me", userEmail)
	}
	altBasePath := getAlternateBasePath(userEmail, isShared)

	// Build full path
	var fullPath string
	if folderPath != "" {
		fullPath = fmt.Sprintf("%s/%s/%s_%s%s", basePath, folderPath, fileID, fileName, ext)
	} else {
		fullPath = fmt.Sprintf("%s/%s_%s%s", basePath, fileID, fileName, ext)
	}

	if mimeType == "application/vnd.google-apps.folder" {
		// For folders, check both with and without .file_placeholder
		baseFolderPath := strings.TrimSuffix(fullPath, ext)
		path1 := baseFolderPath + "/"
		path2 := baseFolderPath + "/.file_placeholder"

		// Also check root paths in case folder was uploaded as root folder
		rootBasePath := fmt.Sprintf("%s/%s_%s", userEmail, fileID, fileName)
		sharedRootBasePath := fmt.Sprintf("%s/shared with me/%s_%s", userEmail, fileID, fileName)

		// For nested folders, also check the opposite path (regular vs shared)
		if folderPath != "" {
			altFullPath := fmt.Sprintf("%s/%s/%s_%s%s", altBasePath, folderPath, fileID, fileName, ext)
			altBaseFolderPath := strings.TrimSuffix(altFullPath, ext)
			path3 := altBaseFolderPath + "/"
			path4 := altBaseFolderPath + "/.file_placeholder"

			return syncedMap[path1] || syncedMap[path2] || syncedMap[path3] || syncedMap[path4] ||
				syncedMap[rootBasePath+"/"] || syncedMap[rootBasePath+"/.file_placeholder"] ||
				syncedMap[sharedRootBasePath+"/"] || syncedMap[sharedRootBasePath+"/.file_placeholder"]
		}

		// For root folders, also check alternate path (regular vs shared) and root paths
		altRootBasePath := fmt.Sprintf("%s/%s_%s", altBasePath, fileID, fileName)
		path3 := altRootBasePath + "/"
		path4 := altRootBasePath + "/.file_placeholder"

		return syncedMap[path1] || syncedMap[path2] || syncedMap[path3] || syncedMap[path4] ||
			syncedMap[rootBasePath+"/"] || syncedMap[rootBasePath+"/.file_placeholder"] ||
			syncedMap[sharedRootBasePath+"/"] || syncedMap[sharedRootBasePath+"/.file_placeholder"]
	}

	// For files in nested folders, also check the opposite path (regular vs shared)
	if folderPath != "" {
		altFullPath := fmt.Sprintf("%s/%s/%s_%s%s", altBasePath, folderPath, fileID, fileName, ext)
		return syncedMap[fullPath] || syncedMap[altFullPath]
	}

	return syncedMap[fullPath]
}

func GetFolderPathByID(ctx context.Context, srv *drive.Service, folderID string) (string, error) {
	var segments []string
	for id := folderID; id != "root"; {
		folder, err := srv.Files.Get(id).Fields("id,name,parents").Do()
		if err != nil {
			return "", fmt.Errorf("unable to retrieve folder metadata: %v", err)
		}
		if folder.Name != "My Drive" {
			// Build path segment in format: folderID_folderName (matches upload path format)
			segment := fmt.Sprintf("%s_%s", folder.Id, folder.Name)
			segments = append([]string{segment}, segments...)
		}
		if len(folder.Parents) == 0 {
			break
		}
		id = folder.Parents[0]
	}
	return strings.Join(segments, "/"), nil
}

// GetFileAndPath downloads file from Google Drive by ID and returns path with content
func GetFileAndPath(c echo.Context, id string) (string, []byte, error) {
	path, _, data, err := GetFileAndPathWithMimeType(c, id)
	return path, data, err
}

// GetFileAndPathWithMimeType downloads file from Google Drive by ID and returns path, MimeType, and content
func GetFileAndPathWithMimeType(c echo.Context, id string) (string, string, []byte, error) {
	srv, err := getDriveService(c)
	if err != nil {
		return "", "", nil, err
	}

	file, err := srv.Files.Get(id).Do()
	if err != nil {
		return "", "", nil, fmt.Errorf("unable to retrieve file metadata: %w", err)
	}

	p, err := GetFolderPathByID(context.Background(), srv, file.Id)
	if err != nil {
		return "", "", nil, fmt.Errorf("unable to get file path: %w", err)
	}

	res, err := downloadFile(srv, id, file.MimeType, &p)
	if err != nil {
		return "", "", nil, err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", "", nil, fmt.Errorf("unable to read file content: %w", err)
	}

	return p, file.MimeType, data, nil
}

// downloadFile handles both regular files and Google Docs export
func downloadFile(srv *drive.Service, id, mimeType string, path *string) (*http.Response, error) {
	res, err := srv.Files.Get(id).Download()
	if err == nil {
		return res, nil
	}

	if !strings.Contains(err.Error(), "Use Export with Docs Editors files., fileNotDownloadable") {
		return nil, fmt.Errorf("unable to download file: %w", err)
	}

	mt, ext := getExportMimeTypeAndExtension(mimeType)
	*path += ext

	if mt == "application/vnd.google-apps.folder" {
		return nil, errors.New("folder error")
	}

	res, err = srv.Files.Export(id, mt).Download()
	if err != nil {
		return nil, fmt.Errorf("unable to export file: %w", err)
	}

	return res, nil
}
