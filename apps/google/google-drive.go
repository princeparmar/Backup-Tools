package google

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	Name              string `json:"file_name"`
	ID                string `json:"file_id"`
	MimeType          string `json:"mime_type"`
	Synced            bool   `json:"synced"`
	Size              int64  `json:"size"`
	FullFileExtension string `json:"full_file_extension"`
	FileExtension     string `json:"file_extension"`
	Path              string `json:"path"`
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
		r, err := srv.Files.List().PageToken(pageToken).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve files: %v", err)
		}

		// Append files to response
		for _, i := range r.Files {
			pathH, _ := GetFolderPathByID(context.Background(), srv, i.Id)
			fileResp = append(fileResp, &FilesJSON{
				Name:              i.Name,
				ID:                i.Id,
				MimeType:          i.MimeType,
				Path:              pathH,
				Size:              i.Size,
				FullFileExtension: i.FullFileExtension,
				FileExtension:     i.FileExtension,
			})
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
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Do()
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
			files = append(files, &FilesJSON{
				Name:     i.Name,
				ID:       i.Id,
				MimeType: i.MimeType,
				Size:     i.Size,
				Synced:   synced,
			})
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

			files = append(files, &FilesJSON{
				Name:     i.Name,
				ID:       i.Id,
				MimeType: i.MimeType,
				Size:     i.Size,
				Synced:   synced,
			})
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
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Do()
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
			files = append(files, &FilesJSON{
				Name:     i.Name,
				ID:       i.Id,
				MimeType: i.MimeType,
				Size:     i.Size,
				Synced:   synced,
			})
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

			files = append(files, &FilesJSON{
				Name:     i.Name,
				ID:       i.Id,
				MimeType: i.MimeType,
				Size:     i.Size,
				Synced:   synced,
			})
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
	r, err := srv.Files.List().Q(fmt.Sprintf("'%s' in parents", folderID)).Do()
	if err != nil {
		return folderName, nil, fmt.Errorf("failed to retrieve files: %v", err)
	}

	var files []*FilesJSON
	for _, f := range r.Files {
		files = append(files, &FilesJSON{
			Name: f.Name,
			ID:   f.Id,
		})
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

// buildFileTypeQuery builds a Google Drive query string based on file type filter
func buildFileTypeQuery(fileType string) string {
	switch strings.ToLower(fileType) {
	case "documents", "docs":
		return " and (mimeType = 'application/vnd.google-apps.document' or mimeType = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' or mimeType = 'application/msword' or mimeType = 'application/vnd.oasis.opendocument.text' or mimeType = 'application/rtf' or mimeType = 'text/plain')"

	case "spreadsheets", "sheets":
		return " and (mimeType = 'application/vnd.google-apps.spreadsheet' or mimeType = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' or mimeType = 'application/vnd.ms-excel' or mimeType = 'application/vnd.oasis.opendocument.spreadsheet' or mimeType = 'text/csv')"

	case "presentations", "slides":
		return " and (mimeType = 'application/vnd.google-apps.presentation' or mimeType = 'application/vnd.openxmlformats-officedocument.presentationml.presentation' or mimeType = 'application/vnd.ms-powerpoint' or mimeType = 'application/vnd.oasis.opendocument.presentation')"

	case "images", "photos":
		return " and (mimeType contains 'image/' or mimeType = 'image/jpeg' or mimeType = 'image/png' or mimeType = 'image/gif' or mimeType = 'image/bmp' or mimeType = 'image/tiff' or mimeType = 'image/svg+xml' or mimeType = 'image/webp')"

	case "videos":
		return " and (mimeType contains 'video/' or mimeType = 'video/webm' or mimeType = 'video/mp4' or mimeType = 'video/3gpp' or mimeType = 'video/quicktime' or mimeType = 'video/x-msvideo' or mimeType = 'video/mpeg' or mimeType = 'video/x-ms-wmv' or mimeType = 'video/x-flv' or mimeType = 'video/ogg')"

	case "audio":
		return " and (mimeType contains 'audio/' or mimeType = 'audio/mpeg' or mimeType = 'audio/mp4' or mimeType = 'audio/wav' or mimeType = 'audio/ogg' or mimeType = 'audio/opus')"

	case "pdfs", "pdf":
		return " and mimeType = 'application/pdf'"

	case "archives", "zip":
		return " and (mimeType = 'application/zip' or mimeType = 'application/x-rar-compressed' or mimeType = 'application/x-tar' or mimeType = 'application/gzip')"

	case "code", "scripts":
		return " and (mimeType = 'text/css' or mimeType = 'text/html' or mimeType = 'text/php' or mimeType = 'text/x-c' or mimeType = 'text/x-c++' or mimeType = 'text/x-h' or mimeType = 'text/javascript' or mimeType = 'text/x-java-source' or mimeType = 'text/x-python' or mimeType = 'text/x-sql' or mimeType = 'text/xml' or mimeType = 'application/json')"

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

	default:
		return ""
	}
}

// buildOwnerQuery builds a Google Drive query string based on owner filter
func buildOwnerQuery(owner string) string {
	switch strings.ToLower(owner) {
	case "me", "myself":
		return " and owners contains 'me'"
	case "others", "not me":
		return " and not owners contains 'me'"
	default:
		// If owner is an email address or specific user identifier
		return " and owners contains '" + owner + "'"
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
func GetFileNamesInRoot(c echo.Context) ([]*FilesJSON, error) {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("access token not found")
	}
	o, err := satellite.ListObjects1(context.Background(), accesGrant, "google-drive")
	if err != nil {
		return nil, errors.New("failed to get list from satellite with error:" + err.Error())
	}

	slices.SortStableFunc(o, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
	}

	var fileResp []*FilesJSON

	folderOnly := c.QueryParam("folder_only")
	filesOnly := c.QueryParam("files_only")
	fileType := c.QueryParam("file_type")
	owner := c.QueryParam("owner")
	dateModified := c.QueryParam("date_modified")

	// Query to list files not in any folders
	query := "'root' in parents"

	if folderOnly == "true" {
		query += " and mimeType = 'application/vnd.google-apps.folder'"
	} else if filesOnly == "true" {
		query += " and mimeType != 'application/vnd.google-apps.folder'"
	} else if fileType != "" {
		// Add file type filtering based on MIME types
		query += buildFileTypeQuery(fileType)
	}

	// Add owner filtering
	if owner != "" {
		query += buildOwnerQuery(owner)
	}

	// Add date modified filtering
	if dateModified != "" {
		query += buildDateModifiedQuery(dateModified)
	}

	// Loop to handle pagination
	pageToken := ""
	for {
		r, err := srv.Files.List().Q(query).PageToken(pageToken).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve files: %v", err)
		}

		// Append files to response
		for _, i := range r.Files {
			//check if file is synced in storx
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
				_, synced := slices.BinarySearchFunc(o, i.Name, func(a uplink.Object, b string) int {
					return cmp.Compare(a.Key, b)
				})
				fileResp = append(fileResp, &FilesJSON{
					Name:              i.Name,
					ID:                i.Id,
					MimeType:          i.MimeType,
					Size:              i.Size,
					Synced:            synced,
					FullFileExtension: i.FullFileExtension,
					FileExtension:     i.FileExtension,
				})
			} else {
				// Checked if the folder exist
				_, synced := slices.BinarySearchFunc(o, i.Name+"/", func(a uplink.Object, b string) int {
					return cmp.Compare(a.Key, b)
				})
				if synced {
					folderFiles, _ := GetFilesInFolderByID(c, i.Id)
					//
					for _, v := range folderFiles {
						synced = v.Synced
					}
				}
				fileResp = append(fileResp, &FilesJSON{
					Name:     i.Name,
					ID:       i.Id,
					MimeType: i.MimeType,
					Synced:   synced,
				})
			}
		}

		// Check if there's another page
		pageToken = r.NextPageToken
		if pageToken == "" {
			break // No more pages
		}
	}

	return fileResp, nil
}

func GetSharedFiles(c echo.Context) ([]*FilesJSON, error) {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("access token not found")
	}
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
	}
	o, err := satellite.GetFilesInFolder(context.Background(), accesGrant, "google-drive", "shared with me/")
	if err != nil {
		return nil, errors.New("failed to get list from satellite with error:" + err.Error())
	}
	slices.SortStableFunc(o, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})

	var files []*FilesJSON

	// Query to list files that have been shared
	query := "sharedWithMe=true"

	// Loop to handle pagination
	pageToken := ""
	for {
		r, err := srv.Files.List().Q(query).PageToken(pageToken).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve shared files: %v", err)
		}

		// Append files to response
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
				_, synced := slices.BinarySearchFunc(o, path.Join("shared with me", i.Name), func(a uplink.Object, b string) int {
					return cmp.Compare(a.Key, b)
				})
				files = append(files, &FilesJSON{
					Name:     i.Name,
					ID:       i.Id,
					MimeType: i.MimeType,
					Size:     i.Size,
					Synced:   synced,
				})
			} else {
				_, synced := slices.BinarySearchFunc(o, path.Join("shared with me", i.Name)+"/", func(a uplink.Object, b string) int {
					return cmp.Compare(a.Key, b)
				})
				if synced {
					folderFiles, _ := GetFilesInFolderByID(c, i.Id)
					//var synced bool
					for _, v := range folderFiles {
						synced = v.Synced
					}
				}

				files = append(files, &FilesJSON{
					Name:     i.Name,
					ID:       i.Id,
					MimeType: i.MimeType,
					Size:     i.Size,
					Synced:   synced,
				})
			}
		}

		// Check if there's another page
		pageToken = r.NextPageToken
		if pageToken == "" {
			break // No more pages
		}
	}

	return files, nil
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
