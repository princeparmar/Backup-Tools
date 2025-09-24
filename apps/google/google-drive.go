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
	"slices"
	"strings"
	"time"

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

	isGoogleApp := false
	for _, mimeType := range googleAppsMimeTypes {
		if file.MimeType == mimeType {
			isGoogleApp = true
			break
		}
	}

	if isGoogleApp {
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
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
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

// Returns file by ID as attachment.
func GetFileByID(c echo.Context) error {
	id := c.Param("ID")

	name, data, err := GetFile(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, "error")
	}

	userCachePath := "./cache/" + utils.CreateUserTempCacheFolder() + "/" + name

	dbFile, err := os.Create(userCachePath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	_, err = dbFile.Write(data)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete file from cache after user get's it.
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

	fmt.Println("processing google token" + googleToken)

	tok, err := database.ReadGoogleAuthToken(googleToken)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "user is not authorized")
	}

	fmt.Println("processing access token" + tok)
	client := config.Client(context.Background(), &oauth2.Token{
		AccessToken: tok,
	})

	return client, nil
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
	client, err := client(c)
	if err != nil {
		return "", nil, err
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return "", nil, fmt.Errorf("token error")
	}

	file, err := srv.Files.Get(id).Do()
	if err != nil {
		return "", nil, fmt.Errorf("unable to retrieve file metadata: %v", err)
	}

	res, err := srv.Files.Get(id).Download()
	if err != nil {
		if strings.Contains(err.Error(), "Use Export with Docs Editors files., fileNotDownloadable") {
			var mt string
			switch file.MimeType {
			case "application/vnd.google-apps.document":
				mt = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
				file.Name += ".docx"
			case "application/vnd.google-apps.spreadsheet":
				mt = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
				file.Name += ".xlsx"
			case "application/vnd.google-apps.presentation":
				mt = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
				file.Name += ".pptx"
			case "application/vnd.google-apps.site":
				mt = "text/plain"
			case "application/vnd.google-apps.script":
				mt = "application/vnd.google-apps.script+json"
				file.Name += ".json"
			default:
				mt = file.MimeType
			}
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
	client, err := client(c)
	if err != nil {
		return err
	}
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("token error")
	}
	_, err = srv.Files.Create(
		&drive.File{
			Name: name,
		}).Media(
		bytes.NewReader(data),
	).Do()
	if err != nil {
		return err
	}
	return nil
}

// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFilesInFolder(c echo.Context, folderName string) ([]*FilesJSON, error) {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("access token is missing")
	}
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
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
	o, err := satellite.GetFilesInFolder(context.Background(), accesGrant, "google-drive", folderName+"/")
	if err != nil {
		return nil, errors.New("failed to get list from satellite with error:" + err.Error())
	}
	slices.SortStableFunc(o, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})
	// List all files within the folder
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Fields("files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, i := range r.Files {
		if i.MimeType != "application/vnd.google-apps.folder" {
			switch i.MimeType {
			case "application/vnd.google-apps.document":
				i.Name += ".docx"
			case "application/vnd.google-apps.spreadsheet":
				i.Name += ".xlsx"
			case "application/vnd.google-apps.presentation":
				i.Name += ".pptx"
			case "application/vnd.google-apps.site":

			case "application/vnd.google-apps.script":
				i.Name += ".json"
			}
			_, synced := slices.BinarySearchFunc(o, path.Join(folderName, i.Name), func(a uplink.Object, b string) int {
				return cmp.Compare(a.Key, b)
			})
			files = append(files, createFilesJSON(i, synced, ""))
		} else {
			_, synced := slices.BinarySearchFunc(o, path.Join(folderName, i.Name)+"/", func(a uplink.Object, b string) int {
				return cmp.Compare(a.Key, b)
			})
			if synced {
				folderFiles, _ := GetFilesInFolderByID(c, i.Id)
				//var synced bool
				for _, v := range folderFiles {
					synced = v.Synced
				}
			}

			files = append(files, createFilesJSON(i, synced, ""))
		}
	}

	return files, nil
}

// func embeddedSynced(c echo.Context, folderID, folderName string)
// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFilesInFolderByID(c echo.Context, folderID string) ([]*FilesJSON, error) {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("access token is missing")
	}
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
	}
	//fpath, err :=
	folderName, err := GetFolderPathByID(context.Background(), srv, folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to get folder name: %v", err)
	}
	o, err := satellite.GetFilesInFolder(context.Background(), accesGrant, "google-drive", folderName+"/")
	if err != nil {
		return nil, errors.New("failed to get list from satellite with error:" + err.Error())
	}
	slices.SortStableFunc(o, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})
	// List all files within the folder
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Fields("files(id, name, mimeType, size, createdTime, fullFileExtension, fileExtension)").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, i := range r.Files {
		if i.MimeType != "application/vnd.google-apps.folder" {
			switch i.MimeType {
			case "application/vnd.google-apps.document":
				i.Name += ".docx"
			case "application/vnd.google-apps.spreadsheet":
				i.Name += ".xlsx"
			case "application/vnd.google-apps.presentation":
				i.Name += ".pptx"
			case "application/vnd.google-apps.site":

			case "application/vnd.google-apps.script":
				i.Name += ".json"
			}
			fmt.Println(path.Join(folderName, i.Name))
			_, synced := slices.BinarySearchFunc(o, path.Join(folderName, i.Name), func(a uplink.Object, b string) int {
				return cmp.Compare(a.Key, b)
			})
			files = append(files, createFilesJSON(i, synced, ""))
		} else {
			fmt.Println(path.Join(folderName, i.Name))
			_, synced := slices.BinarySearchFunc(o, path.Join(folderName, i.Name)+"/", func(a uplink.Object, b string) int {
				return cmp.Compare(a.Key, b)
			})
			if synced {
				folderFiles, _ := GetFilesInFolderByID(c, i.Id)
				//var synced bool
				for _, v := range folderFiles {
					synced = v.Synced
				}
			}

			files = append(files, createFilesJSON(i, synced, ""))
		}
	}

	return files, nil
}

// GetFilesInFolder retrieves all files within a specific folder from Google Drive
func GetFolderNameAndFilesInFolderByID(c echo.Context, folderID string) (string, []*FilesJSON, error) {

	client, err := client(c)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return "", nil, fmt.Errorf("failed to create Drive service: %v", err)
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
	return r.Files[0].Id, nil
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
		return " and (mimeType = 'application/zip' or mimeType = 'application/x-rar-compressed' or mimeType = 'application/x-tar' or mimeType = 'application/gzip' or mimeType = 'application/x-7z-compressed' or mimeType = 'application/epub+zip')"

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

// buildDateModifiedQuery builds a Google Drive query string based on date modified filter
func buildDateModifiedQuery(dateModified string) string {
	switch strings.ToLower(dateModified) {
	case "today":
		return " and modifiedTime > '" + getTodayStart() + "'"
	case "yesterday":
		return " and modifiedTime > '" + getYesterdayStart() + "' and modifiedTime < '" + getTodayStart() + "'"
	case "last_7_days", "last7days", "7days":
		return " and modifiedTime > '" + getDaysAgoStart(7) + "'"
	case "last_30_days", "last30days", "30days":
		return " and modifiedTime > '" + getDaysAgoStart(30) + "'"
	case "this_year", "thisyear":
		return " and modifiedTime > '" + getYearStart() + "'"
	case "last_year", "lastyear":
		return " and modifiedTime > '" + getLastYearStart() + "' and modifiedTime < '" + getYearStart() + "'"
	default:
		// For custom date ranges, expect format like "2024-01-01" or "2024-01-01,2024-12-31"
		return buildCustomDateQuery(dateModified)
	}
}

// Helper functions for date calculations
func getTodayStart() string {
	now := time.Now()
	return now.Format("2006-01-02T00:00:00.000Z")
}

func getYesterdayStart() string {
	yesterday := time.Now().AddDate(0, 0, -1)
	return yesterday.Format("2006-01-02T00:00:00.000Z")
}

func getDaysAgoStart(days int) string {
	date := time.Now().AddDate(0, 0, -days)
	return date.Format("2006-01-02T00:00:00.000Z")
}

func getYearStart() string {
	now := time.Now()
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
	return yearStart.Format("2006-01-02T00:00:00.000Z")
}

func getLastYearStart() string {
	now := time.Now()
	lastYearStart := time.Date(now.Year()-1, 1, 1, 0, 0, 0, 0, now.Location())
	return lastYearStart.Format("2006-01-02T00:00:00.000Z")
}

func buildCustomDateQuery(dateRange string) string {
	// Handle custom date ranges like "2024-01-01" or "2024-01-01,2024-12-31"
	dates := strings.Split(dateRange, ",")
	if len(dates) == 1 {
		// Single date - files modified on or after this date
		return " and modifiedTime > '" + dates[0] + "T00:00:00.000Z'"
	} else if len(dates) == 2 {
		// Date range - files modified between these dates
		startDate := strings.TrimSpace(dates[0]) + "T00:00:00.000Z"
		endDate := strings.TrimSpace(dates[1]) + "T23:59:59.999Z"
		return " and modifiedTime > '" + startDate + "' and modifiedTime < '" + endDate + "'"
	}
	return ""
}

// This function gets files only in root. It does not list files in folders
func GetFileNamesInRoot(c echo.Context) (*PaginatedFilesResponse, error) {
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return nil, errors.New("access token not found")
	}

	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %w", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %w", err)
	}

	// Get satellite objects for sync checking
	satelliteObjects, err := satellite.ListObjects1(context.Background(), accessGrant, "google-drive")
	if err != nil {
		return nil, fmt.Errorf("failed to get satellite list: %w", err)
	}
	slices.SortStableFunc(satelliteObjects, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})

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
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %w", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %w", err)
	}

	// Get satellite objects for sync checking
	satelliteObjects, err := satellite.GetFilesInFolder(context.Background(), accessGrant, "google-drive", "shared with me/")
	if err != nil {
		return nil, fmt.Errorf("failed to get satellite list: %w", err)
	}
	slices.SortStableFunc(satelliteObjects, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})

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
	pageSize := int64(25)
	pageToken := ""

	if filter != nil {
		if filter.Limit > 0 {
			pageSize = min(filter.Limit, 1000)
		}
		if filter.PageToken != "" {
			pageToken = filter.PageToken
		}
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
			folderFiles, err := GetFilesInFolderByID(c, file.Id)
			if err == nil {
				allSynced := true
				for _, v := range folderFiles {
					if !v.Synced {
						allSynced = false
						break
					}
				}
				synced = allSynced
			} else {
				synced = false // If we can't check folder contents, assume not synced
			}
		}

		files = append(files, createFilesJSON(file, synced, ""))
	}

	return files
}

func processRootFiles(driveFiles []*drive.File, satelliteObjects []uplink.Object, c echo.Context) []*FilesJSON {
	var files []*FilesJSON

	for _, file := range driveFiles {
		synced := isRootFileSynced(satelliteObjects, file.Name, file.MimeType)

		if file.MimeType == "application/vnd.google-apps.folder" && synced {
			// Check if all files in folder are synced
			folderFiles, err := GetFilesInFolderByID(c, file.Id)
			if err == nil {
				allSynced := true
				for _, v := range folderFiles {
					if !v.Synced {
						allSynced = false
						break
					}
				}
				synced = allSynced
			} else {
				synced = false // If we can't check folder contents, assume not synced
			}
		}

		files = append(files, createFilesJSON(file, synced, ""))
	}

	return files
}

func isRootFileSynced(satelliteObjects []uplink.Object, fileName, mimeType string) bool {
	searchPath := fileName
	if mimeType == "application/vnd.google-apps.folder" {
		searchPath += "/"
	} else {
		// Add appropriate extensions for Google Apps files
		switch mimeType {
		case "application/vnd.google-apps.document":
			searchPath += ".docx"
		case "application/vnd.google-apps.spreadsheet":
			searchPath += ".xlsx"
		case "application/vnd.google-apps.presentation":
			searchPath += ".pptx"
		case "application/vnd.google-apps.script":
			searchPath += ".json"
		}
	}

	_, found := slices.BinarySearchFunc(satelliteObjects, searchPath, func(a uplink.Object, b string) int {
		return cmp.Compare(a.Key, b)
	})
	return found
}

func isFileSynced(satelliteObjects []uplink.Object, fullPath, mimeType string) bool {
	searchPath := fullPath
	if mimeType == "application/vnd.google-apps.folder" {
		searchPath += "/"
	} else {
		// Add appropriate extensions for Google Apps files
		switch mimeType {
		case "application/vnd.google-apps.document":
			searchPath += ".docx"
		case "application/vnd.google-apps.spreadsheet":
			searchPath += ".xlsx"
		case "application/vnd.google-apps.presentation":
			searchPath += ".pptx"
		case "application/vnd.google-apps.script":
			searchPath += ".json"
		}
	}

	_, found := slices.BinarySearchFunc(satelliteObjects, searchPath, func(a uplink.Object, b string) int {
		return cmp.Compare(a.Key, b)
	})
	return found
}

func GetFolderPathByID(ctx context.Context, srv *drive.Service, folderID string) (string, error) {
	// Get folder metadata
	folder, err := srv.Files.Get(folderID).Fields("name,parents").Do()
	if err != nil {
		return "", fmt.Errorf("unable to retrieve folder metadata: %v", err)
	}
	// Check if the folder is in the root
	if len(folder.Parents) == 0 {
		return folder.Name, nil
	}

	// Initialize the path with the folder name
	p := folder.Name

	// Recursively traverse parent folders to build the full path
	for {
		if len(folder.Parents) == 0 {
			break
		}
		parentID := folder.Parents[0]
		if parentID == "root" {
			break // Reached the root folder
		}

		// Get the parent folder metadata
		parent, err := srv.Files.Get(parentID).Fields("name,parents").Do()
		if err != nil {
			return "", fmt.Errorf("unable to retrieve parent folder metadata: %v", err)
		}

		// Prepend the parent folder name to the path
		if parent.Name != "My Drive" {
			p = path.Join(parent.Name, p)
		}

		// Update folder to the parent folder for the next iteration
		folder = parent
	}

	return p, nil
}

// GetFile downloads file from Google Drive by ID
func GetFileAndPath(c echo.Context, id string) (string, []byte, error) {
	client, err := client(c)
	if err != nil {
		return "", nil, err
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return "", nil, fmt.Errorf("token error")
	}

	file, err := srv.Files.Get(id).Do()
	if err != nil {
		return "", nil, fmt.Errorf("unable to retrieve file metadata: %v", err)
	}
	p, err := GetFolderPathByID(context.Background(), srv, file.Id)
	if err != nil {
		return "", nil, fmt.Errorf("unable to read file content: %v", err)
	}
	res, err := srv.Files.Get(id).Download()
	if err != nil {
		if strings.Contains(err.Error(), "Use Export with Docs Editors files., fileNotDownloadable") {
			var mt string
			switch file.MimeType {
			case "application/vnd.google-apps.document":
				mt = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
				p += ".docx"
			case "application/vnd.google-apps.spreadsheet":
				mt = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
				p += ".xlsx"
			case "application/vnd.google-apps.presentation":
				mt = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
				p += ".pptx"
			case "application/vnd.google-apps.site":
				mt = "text/plain"
			case "application/vnd.google-apps.script":
				mt = "application/vnd.google-apps.script+json"
				p += ".json"
			default:
				mt = file.MimeType
			}
			// handle folders
			if mt != "application/vnd.google-apps.folder" {
				if res, err = srv.Files.Export(id, mt).Download(); err != nil {
					return "", nil, fmt.Errorf("unable to download file: %v", err)
				}
			} else {
				return p, nil, errors.New("folder error")
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

	return p, data, nil
}
