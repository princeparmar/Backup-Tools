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
	"storj-integrations/storage"
	"storj-integrations/storj"
	"storj-integrations/utils"
	"strings"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
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
				Name:     i.Name,
				ID:       i.Id,
				MimeType: i.MimeType,
				Path: pathH,
				Size: i.Size,
				FullFileExtension: i.FullFileExtension,
				FileExtension: i.FileExtension,
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
	config, err := google.ConfigFromJSON(b, drive.DriveScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}
	googleToken, err := GetGoogleTokenFromJWT(c)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from JWT: %v", err)
	}
	tok, err := database.ReadGoogleAuthToken(googleToken)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "user is not authorized")
	}
	client := config.Client(context.Background(), &tok)

	return client, nil
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
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("storj access missing")
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
	o, err := storj.GetFilesInFolder(context.Background(), accesGrant, "google-drive", folderName+"/")
	if err != nil {
		return nil, errors.New("failed to get list from storj with error:" + err.Error())
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
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("storj access missing")
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
	o, err := storj.GetFilesInFolder(context.Background(), accesGrant, "google-drive", folderName+"/")
	if err != nil {
		return nil, errors.New("failed to get list from storj with error:" + err.Error())
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

// This function gets files only in root. It does not list files in folders
func GetFileNamesInRoot(c echo.Context) ([]*FilesJSON, error) {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("storj access missing")
	}
	o, err := storj.ListObjects1(context.Background(), accesGrant, "google-drive")
	if err != nil {
		return nil, errors.New("failed to get list from storj with error:" + err.Error())
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
	// Query to list files not in any folders
	query := "'root' in parents"

	if folderOnly == "true" {
		query += " and mimeType = 'application/vnd.google-apps.folder'"
	} else if filesOnly == "true" {
		query += " and mimeType != 'application/vnd.google-apps.folder'"
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
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return nil, errors.New("storj access missing")
	}
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to get Google Drive client: %v", err)
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %v", err)
	}
	o, err := storj.GetFilesInFolder(context.Background(), accesGrant, "google-drive", "shared with me/")
	if err != nil {
		return nil, errors.New("failed to get list from storj with error:" + err.Error())
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
	p, err := GetFolderPathByID(context.Background(), srv,file.Id)
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