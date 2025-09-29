package google

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/StorX2-0/Backup-Tools/utils"
	"github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	gs "google.golang.org/api/storage/v1"
	"storj.io/uplink"
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
		r, err := srv.Files.List().Fields("nextPageToken, files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").PageToken(pageToken).Do()
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
	database := c.Get(dbContextKey).(*storage.PosgresStore)

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

	logger.Info("processing google token" + googleToken)

	tok, err := database.ReadGoogleAuthToken(googleToken)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "user is not authorized")
	}

	logger.Info("processing access token" + tok)
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
func sortSatelliteObjects(objects []uplink.Object) {
	slices.SortStableFunc(objects, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})
}

// Helper function to check if file is synced using binary search
func isFileSyncedInObjects(objects []uplink.Object, searchPath string) bool {
	_, found := slices.BinarySearchFunc(objects, searchPath, func(a uplink.Object, b string) int {
		return cmp.Compare(a.Key, b)
	})
	return found
}

// Helper function to check folder sync status
func checkFolderSyncStatus(c echo.Context, folderID string) (bool, error) {
	folderFiles, err := GetFilesInFolderByID(c, folderID)
	if err != nil {
		return false, err
	}

	for _, v := range folderFiles.Files {
		if !v.Synced {
			return false, nil
		}
	}
	return true, nil
}

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

// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFilesInFolder(c echo.Context, folderName string) ([]*FilesJSON, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token is missing")
	}
	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	// Get folder ID by name
	folderID, err := getFolderIDByName(srv, folderName)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder ID: %v", err)
	}
	folderName, err = GetFolderPathByID(context.Background(), srv, folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder name: %v", err)
	}
	o, err := satellite.GetFilesInFolder(context.Background(), accessGrant, "google-drive", folderName+"/")
	if err != nil {
		return nil, errors.New("failed to get list from satellite with error:" + err.Error())
	}
	sortSatelliteObjects(o)
	// List all files within the folder
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Fields("files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, i := range r.Files {
		if i.MimeType != "application/vnd.google-apps.folder" {
			i.Name = addGoogleAppsFileExtension(i.Name, i.MimeType)
			synced := isFileSyncedInObjects(o, path.Join(folderName, i.Name))
			files = append(files, createFilesJSON(i, synced, ""))
		} else {
			synced := isFileSyncedInObjects(o, path.Join(folderName, i.Name)+"/")
			if synced {
				synced, _ = checkFolderSyncStatus(c, i.Id)
			}

			files = append(files, createFilesJSON(i, synced, ""))
		}
	}

	return files, nil
}

// func embeddedSynced(c echo.Context, folderID, folderName string)
// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFilesInFolderByID(c echo.Context, folderID string) (*PaginatedFilesResponse, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token is missing")
	}
	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}
	//fpath, err :=
	folderName, err := GetFolderPathByID(context.Background(), srv, folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder name: %v", err)
	}
	o, err := satellite.GetFilesInFolder(context.Background(), accessGrant, "google-drive", folderName+"/")
	if err != nil {
		return nil, errors.New("failed to get list from satellite with error:" + err.Error())
	}
	sortSatelliteObjects(o)

	// Parse filter
	filter, err := ParseFilter(c.QueryParam("filter"))
	if err != nil {
		return nil, err
	}

	// Build query
	query := fmt.Sprintf("'%s' in parents", folderID)
	if filter != nil && filter.Query != "" {
		query = filter.Query
	} else if filter != nil {
		query = applyFiltersToQuery(query, filter)
	}

	// Set up pagination
	pageSize, pageToken := GetPaginationParams(filter)

	// List all files within the folder
	r, err := srv.Files.List().
		Q(query).
		Fields("nextPageToken, files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").
		PageToken(pageToken).
		PageSize(pageSize).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, i := range r.Files {
		if i.MimeType != "application/vnd.google-apps.folder" {
			i.Name = addGoogleAppsFileExtension(i.Name, i.MimeType)
			synced := isFileSyncedInObjects(o, path.Join(folderName, i.Name))
			files = append(files, createFilesJSON(i, synced, ""))
		} else {
			synced := isFileSyncedInObjects(o, path.Join(folderName, i.Name)+"/")
			if synced {
				synced, _ = checkFolderSyncStatus(c, i.Id)
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

// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFolderNameAndFilesInFolderByID(c echo.Context, folderID string) (string, []*FilesJSON, error) {

	srv, err := getDriveService(c)
	if err != nil {
		return "", nil, err
	}
	folderName, err := getFolderNameByID(srv, folderID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get folder name: %v", err)
	}
	// List all files within the folder
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Fields("files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").Do()
	if err != nil {
		return folderName, nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, f := range r.Files {
		files = append(files, createFilesJSON(f, false, ""))
	}

	return folderName, files, nil
}

// Helper function to get folder ID by name
func getFolderIDByName(srv *drive.Service, folderName string) (string, error) {
	r, err := srv.Files.List().Q(fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder'", folderName)).Fields("files(id)").Do()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve folder ID: %v", err)
	}
	if len(r.Files) == 0 {
		return "", fmt.Errorf("folder '%s' not found", folderName)
	}
	return r.Files[0].Id, nil
}

// Helper function to get folder ID by name
func getFolderNameByID(srv *drive.Service, folderID string) (string, error) {
	r, err := srv.Files.List().Q(fmt.Sprintf("id='%s' and mimeType='application/vnd.google-apps.folder'", folderID)).Fields("files(name)").Do()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve folder ID: %v", err)
	}
	if len(r.Files) == 0 {
		return "", fmt.Errorf("folder '%s' not found", folderID)
	}
	return r.Files[0].Name, nil
}

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
		"application/zip", "application/x-rar-compressed", "application/x-tar",
		"application/gzip", "application/x-7z-compressed", "application/epub+zip",
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
		return " and (mimeType = 'application/zip' or mimeType = 'application/x-rar-compressed' or mimeType = 'application/x-tar' or mimeType = 'application/gzip' or mimeType = 'application/x-7z-compressed' or mimeType = 'application/epub+zip' or mimeType = 'application/x-bzip2' or mimeType = 'application/x-bzip' or mimeType = 'application/java-archive' or mimeType = 'application/vnd.android.package-archive' or mimeType = 'application/x-deb' or mimeType = 'application/x-rpm' or mimeType = 'application/x-apple-diskimage' or mimeType = 'application/vnd.ms-cab-compressed' or mimeType = 'application/x-lzh-compressed' or mimeType = 'application/x-compress' or mimeType = 'application/x-ace-compressed' or mimeType = 'application/x-arj' or mimeType = 'application/x-cpio')"
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
func GetFileNamesInRoot(c echo.Context) (*PaginatedFilesResponse, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token is missing")
	}

	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	// Get satellite objects for sync checking
	satelliteObjects, err := satellite.ListObjects1(context.Background(), accessGrant, "google-drive")
	if err != nil {
		return nil, fmt.Errorf("failed to get satellite list: %w", err)
	}
	sortSatelliteObjects(satelliteObjects)

	// Parse filter
	filter, err := ParseFilter(c.QueryParam("filter"))
	if err != nil {
		return nil, err
	}

	// Build query
	query := "'root' in parents"
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

	// Process files
	files := processRootFiles(response.Files, satelliteObjects, c)

	return &PaginatedFilesResponse{
		Files:         files,
		NextPageToken: response.NextPageToken,
		Limit:         pageSize,
		TotalFiles:    int64(len(files)),
	}, nil
}

func GetSharedFiles(c echo.Context) (*PaginatedFilesResponse, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token not found")
	}
	srv, err := getDriveService(c)
	if err != nil {
		return nil, err
	}

	// Get satellite objects for sync checking
	satelliteObjects, err := satellite.GetFilesInFolder(context.Background(), accessGrant, "google-drive", "shared with me/")
	if err != nil {
		return nil, fmt.Errorf("failed to get satellite list: %w", err)
	}
	sortSatelliteObjects(satelliteObjects)

	// Parse filter
	filter, err := ParseFilter(c.QueryParam("filter"))
	if err != nil {
		return nil, err
	}

	// Build query
	query := "sharedWithMe=true"
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
		return nil, fmt.Errorf("failed to retrieve shared files: %w", err)
	}

	// Process files
	files := processSharedFiles(response.Files, satelliteObjects, c)

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
			pageSize = min(filter.Limit, 1000)
		}
		pageToken = filter.PageToken
	}
	return pageSize, pageToken
}

func processSharedFiles(driveFiles []*drive.File, satelliteObjects []uplink.Object, c echo.Context) []*FilesJSON {
	var files []*FilesJSON

	for _, file := range driveFiles {
		fullPath := path.Join("shared with me", file.Name)
		synced := isFileSynced(satelliteObjects, fullPath, file.MimeType)

		if file.MimeType == "application/vnd.google-apps.folder" && synced {
			// Check if all files in folder are synced
			synced, _ = checkFolderSyncStatus(c, file.Id)
		}

		files = append(files, createFilesJSON(file, synced, ""))
	}

	return files
}

func processRootFiles(driveFiles []*drive.File, satelliteObjects []uplink.Object, c echo.Context) []*FilesJSON {
	var files []*FilesJSON

	for _, file := range driveFiles {
		synced := isFileSynced(satelliteObjects, file.Name, file.MimeType)

		if file.MimeType == "application/vnd.google-apps.folder" && synced {
			// Check if all files in folder are synced
			synced, _ = checkFolderSyncStatus(c, file.Id)
		}

		files = append(files, createFilesJSON(file, synced, ""))
	}

	return files
}

// Helper function to check if a file is synced with proper path handling
func isFileSynced(satelliteObjects []uplink.Object, filePath, mimeType string) bool {
	searchPath := filePath
	if mimeType == "application/vnd.google-apps.folder" {
		searchPath += "/"
	} else {
		// Add appropriate extensions for Google Apps files
		searchPath += addGoogleAppsFileExtension("", mimeType)
	}

	return isFileSyncedInObjects(satelliteObjects, searchPath)
}

func GetFolderPathByID(ctx context.Context, srv *drive.Service, folderID string) (string, error) {
	var segments []string
	for id := folderID; id != "root"; {
		folder, err := srv.Files.Get(id).Fields("name,parents").Do()
		if err != nil {
			return "", fmt.Errorf("unable to retrieve folder metadata: %v", err)
		}
		if folder.Name != "My Drive" {
			segments = append([]string{folder.Name}, segments...)
		}
		if len(folder.Parents) == 0 {
			break
		}
		id = folder.Parents[0]
	}
	return path.Join(segments...), nil
}

// GetFileAndPath downloads file from Google Drive by ID and returns path with content
func GetFileAndPath(c echo.Context, id string) (string, []byte, error) {
	srv, err := getDriveService(c)
	if err != nil {
		return "", nil, err
	}

	file, err := srv.Files.Get(id).Do()
	if err != nil {
		return "", nil, fmt.Errorf("unable to retrieve file metadata: %w", err)
	}

	p, err := GetFolderPathByID(context.Background(), srv, file.Id)
	if err != nil {
		return "", nil, fmt.Errorf("unable to get file path: %w", err)
	}

	res, err := downloadFile(srv, id, file.MimeType, &p)
	if err != nil {
		return "", nil, err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", nil, fmt.Errorf("unable to read file content: %w", err)
	}

	return p, data, nil
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
