package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"storj-integrations/apps/aws"
	"storj-integrations/apps/dropbox"
	gthb "storj-integrations/apps/github"
	google "storj-integrations/apps/google"
	"storj-integrations/apps/quickbooks"
	"storj-integrations/apps/shopify"
	"storj-integrations/storage"
	"storj-integrations/storj"
	"storj-integrations/utils"
	"strings"
	"sync"

	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/media_items"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

// <<<<<------------ GOOGLE DRIVE ------------>>>>>

// Get all files names in a google drive even in folder
func handleGetGoogleDriveFileNames(c echo.Context) error {
	fileNames, err := google.GetFileNames(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	return c.JSON(http.StatusOK, fileNames)
}

// Get all files names in a google drive root
func handleRootGoogleDriveFileNames(c echo.Context) error {
	fileNames, err := google.GetFileNamesInRoot(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	return c.JSON(http.StatusOK, fileNames)
}

// Get all files names in a google drive root
func handleSharedGoogleDriveFileNames(c echo.Context) error {
	fileNames, err := google.GetSharedFiles(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	return c.JSON(http.StatusOK, fileNames)
}

// List all files in a folder given the folder name
func handleListAllFolderFiles(c echo.Context) error {
	folderName := c.Param("name")
	fileNames, err := google.GetFilesInFolder(c, folderName)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	return c.JSON(http.StatusOK, fileNames)
}

// List all files in a folder given the folder ID
func handleListAllFolderFilesByID(c echo.Context) error {
	folderName := c.Param("id")
	fileNames, err := google.GetFilesInFolderByID(c, folderName)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	return c.JSON(http.StatusOK, fileNames)
}

func handleFolder(folderName, folderID string, c echo.Context) error {
	fileNames, err := google.GetFilesInFolderByID(c, folderID)
	if err != nil {
		return err
	}
	// If folder is empty, create an empty folder

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return errors.New("error: storj access token is missing")
	}
	err = storj.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
	if err != nil {
		return err
	}

	for _, file := range fileNames {
		name, data, err := google.GetFile(c, file.ID)
		if err != nil {
			if strings.Contains(err.Error(), "folder error") {
				if err = handleFolder(path.Join(folderName, file.Name), file.ID, c); err != nil {
					return err
				}
			} else if strings.Contains(err.Error(), "The requested conversion is not supported") || strings.Contains(err.Error(), "Export only supports Docs Editors files") {
				// No conversion for this type
				continue
			} else {

				return err
			}
		} else {
			accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
			if accesGrant == "" {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "storj access token is missing",
				})
			}
			err = storj.UploadObject(context.Background(), accesGrant, "google-drive", path.Join(folderName, name), data)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func handleSyncAllSharedFolderAndFiles(c echo.Context) error {
	fileNames, err := google.GetSharedFiles(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	// If folder is empty, create an empty folder

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	err = storj.UploadObject(context.Background(), accesGrant, "google-drive", "shared with me/", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
		})
	}

	for _, file := range fileNames {
		name, data, err := google.GetFile(c, file.ID)
		if err != nil {
			if err.Error() == "token error" {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error": "token expired",
				})

			} else if strings.Contains(err.Error(), "folder error") {
				if err = handleFolder(path.Join("shared with me", file.Name), file.ID, c); err != nil {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error": "failed to retrieve file from Google Drive folder:" + err.Error(),
					})
				}
			} else if strings.Contains(err.Error(), "The requested conversion is not supported") || strings.Contains(err.Error(), "Export only supports Docs Editors files") {
				// No conversion for this type
				continue
			} else {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "failed to retrieve file from Google Drive" + err.Error(),
				})
			}
		} else {
			accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
			if accesGrant == "" {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "storj access token is missing",
				})
			}

			err = storj.UploadObject(context.Background(), accesGrant, "google-drive", path.Join("shared with me", name), data)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{
					"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
				})
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "all files were successfully uploaded from Google Drive folder to Storj",
	})

}

func handleSyncAllFolderFiles(c echo.Context) error {
	folderName := c.Param("name")
	fileNames, err := google.GetFilesInFolder(c, folderName)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	// If folder is empty, create an empty folder

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	err = storj.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
		})
	}

	for _, file := range fileNames {
		name, data, err := google.GetFile(c, file.ID)
		if err != nil {
			if err.Error() == "token error" {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error": "token expired",
				})

			} else if strings.Contains(err.Error(), "folder error") {
				if err = handleFolder(path.Join(folderName, file.Name), file.ID, c); err != nil {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error": "failed to retrieve file from Google Drive folder:" + err.Error(),
					})
				}
			} else if strings.Contains(err.Error(), "The requested conversion is not supported") || strings.Contains(err.Error(), "Export only supports Docs Editors files") {
				// No conversion for this type
				continue
			} else {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "failed to retrieve file from Google Drive" + err.Error(),
				})
			}
		} else {
			accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
			if accesGrant == "" {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "storj access token is missing",
				})
			}

			err = storj.UploadObject(context.Background(), accesGrant, "google-drive", path.Join(folderName, name), data)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{
					"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
				})
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "all files were successfully uploaded from Google Drive folder to Storj",
	})

}

func handleSTorjDrive(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	o, err := storj.ListObjects1(context.Background(), accesGrant, "google-drive")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Storj: %v", err),
		})
	}
	return c.JSON(http.StatusOK, o)
}

func handleStorjDriveFolder(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	o, err := storj.GetFilesInFolder(context.Background(), accesGrant, "google-drive", c.Param("name")+"/")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Storj: %v", err),
		})
	}
	return c.JSON(http.StatusOK, o)
}
func handleSyncAllFolderFilesByID(c echo.Context) error {
	folderID := c.Param("id")
	folderName, fileNames, err := google.GetFolderNameAndFilesInFolderByID(c, folderID)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving file names from drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	// If folder is empty, create an empty folder

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	err = storj.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
		})
	}

	for _, file := range fileNames {
		name, data, err := google.GetFile(c, file.ID)
		if err != nil {
			if err.Error() == "token error" {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error": "token expired",
				})

			} else if strings.Contains(err.Error(), "folder error") {
				if err = handleFolder(path.Join(folderName, file.Name), file.ID, c); err != nil {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error": "failed to retrieve file from Google Drive folder:" + err.Error(),
					})
				}
			} else if strings.Contains(err.Error(), "The requested conversion is not supported") || strings.Contains(err.Error(), "Export only supports Docs Editors files") {
				// No conversion for this type
				continue
			} else {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "failed to retrieve file from Google Drive" + err.Error(),
				})
			}
		} else {
			accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
			if accesGrant == "" {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "storj access token is missing",
				})
			}

			err = storj.UploadObject(context.Background(), accesGrant, "google-drive", path.Join(folderName, name), data)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{
					"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
				})
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "all files were successfully uploaded from Google Drive folder to Storj",
	})

}

// Sends file from Google Drive to Storj
func handleSendFileFromGoogleDriveToStorj(c echo.Context) error {
	id := c.Param("ID")

	name, data, err := google.GetFile(c, id)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	err = storj.UploadObject(context.Background(), accesGrant, "google-drive", name, data)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Google Drive to Storj", name),
	})
}

func handleSendAllFilesFromGoogleDriveToStorj(c echo.Context) error {
	// Get only file names in root
	resp, err := google.GetFileNamesInRoot(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			slog.Debug("Error retrieving google drive", "error", err)
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}

	for _, f := range resp {
		name, data, err := google.GetFile(c, f.ID)
		if err != nil {
			if err.Error() == "token error" {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error": "token expired",
				})

			} else if strings.Contains(err.Error(), "folder error") {
				if err = handleFolder(f.Name, f.ID, c); err != nil {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error": "failed to retrieve file from Google Drive folder:" + err.Error(),
					})
				}
			} else if strings.Contains(err.Error(), "The requested conversion is not supported") || strings.Contains(err.Error(), "Export only supports Docs Editors files") {
				// No conversion for this type
				continue
			} else {

				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "failed to retrieve file from Google Drive",
				})
			}
		} else {
			accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
			if accesGrant == "" {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": "storj access token is missing",
				})
			}

			err = storj.UploadObject(context.Background(), accesGrant, "google-drive", name, data)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{
					"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
				})
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "all files were successfully uploaded from Google Drive to Storj",
	})
}

// Sends file from Storj to Google Drive
func handleSendFileFromStorjToGoogleDrive(c echo.Context) error {
	name := c.Param("name")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant, "google-drive", name)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to download object from Storj: %v", err),
		})
	}

	err = google.UploadFile(c, name, data)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Storj to Google Drive", name),
	})
}

// <<<<<------------ GOOGLE PHOTOS ------------>>>>>

type AlbumsJSON struct {
	Title string `json:"album_title"`
	ID    string `json:"album_id"`
	Items string `json:"items_count"`
}

// Shows list of user's Google Photos albums.
func handleListGPhotosAlbums(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	albs, err := client.ListAlbums(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	var photosListJSON []*AlbumsJSON
	for _, v := range albs {
		photosListJSON = append(photosListJSON, &AlbumsJSON{
			Title: v.Title,
			ID:    v.ID,
			Items: v.MediaItemsCount,
		})
	}

	return c.JSON(http.StatusOK, photosListJSON)

}

type PhotosJSON struct {
	Name string `json:"file_name"`
	ID   string `json:"file_id"`
}

// Shows list of user's Google Photos items in given album.
func handleListPhotosInAlbum(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	id := c.Param("ID")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	albm, err := client.Albums.GetById(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	files, err := client.ListFilesFromAlbum(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	listFromStorj, err := storj.ListObjects(c.Request().Context(), accesGrant, "google-photos")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to list objects from Storj: %v", err),
		})
	}

	var photosRespJSON []*AllPhotosJSON
	for _, v := range files {
		photosRespJSON = append(photosRespJSON, &AllPhotosJSON{
			Name:         v.Filename,
			ID:           v.ID,
			Description:  v.Description,
			BaseURL:      v.BaseURL,
			ProductURL:   v.ProductURL,
			MimeType:     v.MimeType,
			AlbumName:    albm.Title,
			CreationTime: v.MediaMetadata.CreationTime,
			Width:        v.MediaMetadata.Width,
			Height:       v.MediaMetadata.Height,
			Synced:       listFromStorj[v.Filename],
		})
	}

	return c.JSON(http.StatusOK, photosRespJSON)
}

type AllPhotosJSON struct {
	Name         string `json:"file_name"`
	ID           string `json:"file_id"`
	Description  string `json:"description"`
	BaseURL      string `json:"base_url"`
	ProductURL   string `json:"product_url"`
	MimeType     string `json:"mime_type"`
	AlbumName    string `json:"album_name"`
	CreationTime string `json:"creation_time"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	Synced       bool   `json:"synced"`
}

func handleListAllPhotos(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	albs, err := client.ListAlbums(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	type albumData struct {
		albumTitle string
		files      []media_items.MediaItem
	}

	var finalData []albumData

	var mt sync.Mutex
	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	for _, alb := range albs {
		func(alb albums.Album) { // added this function to avoid closure issue https://stackoverflow.com/questions/26692844/captured-closure-for-loop-variable-in-go
			g.Go(func() error {
				files, err := client.ListFilesFromAlbum(ctx, alb.ID)
				if err != nil {
					return err
				}

				mt.Lock()
				defer mt.Unlock()
				finalData = append(finalData, albumData{
					albumTitle: alb.Title,
					files:      files,
				})

				return nil
			})
		}(alb)
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	var photosRespJSON []*AllPhotosJSON
	for _, data := range finalData {
		for _, v := range data.files {
			photosRespJSON = append(photosRespJSON, &AllPhotosJSON{
				Name:         v.Filename,
				ID:           v.ID,
				Description:  v.Description,
				BaseURL:      v.BaseURL,
				ProductURL:   v.ProductURL,
				MimeType:     v.MimeType,
				AlbumName:    data.albumTitle,
				CreationTime: v.MediaMetadata.CreationTime,
				Width:        v.MediaMetadata.Width,
				Height:       v.MediaMetadata.Height,
			})
		}
	}

	return c.JSON(http.StatusOK, photosRespJSON)

}

// Sends photo item from Storj to Google Photos.
func handleSendFileFromStorjToGooglePhotos(c echo.Context) error {
	name := c.Param("name")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant, "google-photos", name)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	path := filepath.Join("./cache", utils.CreateUserTempCacheFolder(), name)
	file, err := os.Create(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Write(data)
	file.Close()
	defer os.Remove(path)

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	err = client.UploadFileToGPhotos(c, name, "Storj Album")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "file " + name + " was successfully uploaded from Storj to Google Photos",
	})
}

// Sends photo item from Google Photos to Storj.
func handleSendFileFromGooglePhotosToStorj(c echo.Context) error {

	ids := c.FormValue("ids")
	if ids == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "ids are missing",
		})
	}
	allIDs := strings.Split(ids, ",")

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				return uploadSingleFileFromPhotosToStorj(ctx, client, id, accesGrant)
			})
		}(id)
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "all files were successfully uploaded from Google Photos to Storj",
	})
}

func uploadSingleFileFromPhotosToStorj(ctx context.Context, client *google.GPotosClient, id, accesGrant string) error {
	item, err := client.GetPhoto(ctx, id)
	if err != nil {
		return err
	}

	resp, err := http.Get(item.BaseURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return storj.UploadObject(context.Background(), accesGrant, "google-photos", item.Filename, body)

}

func handleSendAllFilesFromGooglePhotosToStorj(c echo.Context) error {
	id := c.FormValue("album_id")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	files, err := client.ListFilesFromAlbum(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	var photosRespJSON []*PhotosJSON
	for _, v := range files {
		photosRespJSON = append(photosRespJSON, &PhotosJSON{
			Name: v.Filename,
			ID:   v.ID,
		})
	}
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	for _, p := range photosRespJSON {
		err := uploadSingleFileFromPhotosToStorj(c.Request().Context(), client, p.ID, accesGrant)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "all photos from album were successfully uploaded from Google Photos to Storj"})
}

// <<<<<------------ GMAIL ------------>>>>>

type ThreadJSON struct {
	ID      string `json:"thread_id"`
	Snippet string `json:"snippet"`
}

// Fetches user threads, returns their IDs and snippets.
func handleGmailGetThreads(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	threads, err := GmailClient.GetUserThreads("")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// TODO: implement next page token (now only first page is avialable)

	var jsonResp []*ThreadJSON
	for _, v := range threads.Threads {
		jsonResp = append(jsonResp, &ThreadJSON{
			ID:      v.ID,
			Snippet: v.Snippet,
		})
	}
	return c.JSON(http.StatusOK, jsonResp)
}

type MessageListJSON struct {
	ID       string `json:"message_id"`
	ThreadID string `json:"thread_id"`
}

// Fetches user messages, returns their ID's and threat's IDs.
func handleGmailGetMessages(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	msgs, err := GmailClient.GetUserMessages("")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// TODO: implement next page token (now only first page is avialable)

	var jsonMessages []*MessageListJSON
	for _, v := range msgs.Messages {
		jsonMessages = append(jsonMessages, &MessageListJSON{
			ID:       v.ID,
			ThreadID: v.ThreadID,
		})
	}
	return c.JSON(http.StatusOK, jsonMessages)
}

// Returns Gmail message in JSON format.
func handleGmailGetMessage(c echo.Context) error {
	id := c.Param("ID")

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, msg)
}

// Fetches message from Gmail by given ID as a parameter and writes it into SQLite Database in Storj.
// If there's no database yet - creates one.
func handleGmailMessageToStorj(c echo.Context) error {
	id := c.Param("ID")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// FETCH THE EMAIL TO GOLANG STRUCT

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	msgToSave := storage.GmailMessageSQL{
		ID:      msg.ID,
		Date:    msg.Date,
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Body:    msg.Body,
	}

	// SAVE ATTACHMENTS TO THE STORJ BUCKET AND WRITE THEIR NAMES TO STRUCT

	if len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			err = storj.UploadObject(context.Background(), accesGrant, "gmail", att.FileName, att.Data)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
		}
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToEmailDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// WRITE ALL EMAILS TO THE DATABASE LOCALLY

	err = db.WriteEmailToDB(&msgToSave)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Email was successfully uploaded"})
}

func handleGetGmailDBFromStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	// Copy file from storj to local cache if everything's fine.
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "gmail", "gmails.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"message": "no emails saved in Storj database", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"

	dbFile, err := os.Create(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	_, err = dbFile.Write(byteDB)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete db from cache after user get's it.
	defer os.Remove(userCacheDBPath)

	return c.Attachment(userCacheDBPath, "gmails.db")
}

// <<<<<------------ GOOGLE CLOUD STORAGE ------------>>>>>

// Takes Google Cloud project name as a parameter, returns JSON responce with all the buckets in this project.
func handleStorageListBuckets(c echo.Context) error {
	projectName := c.Param("projectName")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}
	bucketsJSON, err := client.ListBucketsJSON(c, projectName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, bucketsJSON)
}

// Takes Google Cloud bucket name as a parameter, returns JSON responce with all the items in this bucket.
func handleStorageListObjects(c echo.Context) error {
	bucketName := c.Param("bucketName")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	objects, err := client.ListObjectsInBucketJSON(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, objects)

}

// Takes bucket name and item name as a parameters, downloads the object from Google Cloud Storage and uploads it into Storj "google-cloud" bucket.
func handleGoogleCloudItemToStorj(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	obj, err := client.GetObject(c, bucketName, itemName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	err = storj.UploadObject(context.Background(), accesGrant, "google-cloud", obj.Name, obj.Data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Google Cloud Storage to Storj", obj.Name)})

}

// Takes bucket name and item name as a parameters, downloads the object from Storj bucket and uploads it into Google Cloud Storage bucket.
func handleStorjToGoogleCloud(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant, "google-cloud", itemName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	err = client.UploadObject(c, bucketName, &google.StorageObject{
		Name: itemName,
		Data: data,
	})
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Storj to Google Cloud Storage", itemName)})

}

func handleAllFilesFromGoogleCloudBucketToStorj(c echo.Context) error {
	bucketName := c.Param("bucketName")

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	objects, err := client.ListObjectsInBucket(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	for _, o := range objects.Items {
		obj, err := client.GetObject(c, bucketName, o.Name)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}

		err = storj.UploadObject(context.Background(), accesGrant, "google-cloud", obj.Name, obj.Data)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("all objects in bucket '"+bucketName+"' were successfully uploaded from Storj to Google Cloud Storage", bucketName)})

}

// <<<<<------------ DROPBOX ------------>>>>>

func handleDropboxToStorj(c echo.Context) error {
	filePath := c.Param("filePath")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, err := dropbox.NewDropboxClient()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	file, err := client.DownloadFile("/" + filePath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	data, err := io.ReadAll(file.Data)

	err = storj.UploadObject(context.Background(), accesGrant, "dropbox", file.Name, data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Dropbox to Storj", file.Name)})
}

func handleStorjToDropbox(c echo.Context) error {
	filePath := c.Param("filePath")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	objData, err := storj.DownloadObject(context.Background(), accesGrant, "dropbox", filePath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	client, err := dropbox.NewDropboxClient()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	data := bytes.NewReader(objData)
	err = client.UploadFile(data, "/"+filePath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Storj to Dropbox", filePath)})
}

// <<<<<------------ AWS S3 ------------>>>>>

func handleListAWSs3BucketFiles(c echo.Context) error {
	bucketName := c.Param("bucketName")

	s3sess := aws.ConnectAws()
	data, err := s3sess.ListFiles(bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("%+v", data)})
}

func handleS3toStorj(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, itemName)
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	defer os.Remove(path)

	s3sess := aws.ConnectAws()
	err = s3sess.DownloadFile(bucketName, itemName, file)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	err = storj.UploadObject(context.Background(), accesGrant, "aws-s3", itemName, data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from AWS S3 bucket to Storj", itemName)})
}

func handleStorjToS3(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant, "aws-s3", itemName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"message": "error downloading object from Storj" + err.Error(), "error": err.Error()})
	}
	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, itemName)
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Write(data)
	file.Close()
	defer os.Remove(path)

	cachedFile, err := os.Open(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	s3sess := aws.ConnectAws()
	err = s3sess.UploadFile(bucketName, itemName, cachedFile)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Storj to AWS S3 %s bucket", itemName, bucketName)})

}

// <<<<<------------ GITHUB ------------>>>>>

func handleGithubLogin(c echo.Context) error {
	return gthb.AuthenticateGithub(c)
}

func handleGithubCallback(c echo.Context) error {
	code := c.QueryParam("code")

	githubAccessToken := gthb.GetGithubAccessToken(code)
	cookie := new(http.Cookie)
	cookie.Name = "github-auth"
	cookie.Value = githubAccessToken
	c.SetCookie(cookie)

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "you have been successfuly authenticated to github"})
}

func handleListRepos(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}
	reps, err := gh.ListReps(accessToken.Value)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}
	var repositories []string
	for _, r := range reps {
		repositories = append(repositories, *r.FullName)
	}
	return c.JSON(http.StatusOK, repositories)
}

func handleGetRepository(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}
	owner := c.QueryParam("owner")
	if owner == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "owner is now specified"})
	}
	repo := c.QueryParam("repo")
	if repo == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "repo name is now specified"})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	repoPath, err := gh.DownloadRepositoryToCache(owner, repo, accessToken.Value)
	dir, _ := filepath.Split(repoPath)
	defer os.RemoveAll(dir)

	return c.File(repoPath)
}

func handleGithubRepositoryToStorj(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	owner := c.QueryParam("owner")
	if owner == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "owner is now specified"})
	}
	repo := c.QueryParam("repo")
	if repo == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "repo name is now specified"})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	repoPath, err := gh.DownloadRepositoryToCache(owner, repo, accessToken.Value)
	dir, repoName := filepath.Split(repoPath)
	defer os.RemoveAll(dir)
	file, err := os.Open(repoPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	err = storj.UploadObject(context.Background(), accesGrant, "github", repoName, data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Close()

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("repo %s was successfully uploaded from Github to Storj", repoName)})
}

func handleRepositoryFromStorjToGithub(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	repo := c.QueryParam("repo")
	if repo == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "repo name is now specified"})
	}

	repoData, err := storj.DownloadObject(context.Background(), accesGrant, "github", repo)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"message": "error downloading object from Storj" + err.Error(), "error": err.Error()})
	}
	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	basePath := filepath.Join(dirPath, repo+".zip")
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(basePath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Write(repoData)
	file.Close()

	defer os.RemoveAll(dirPath)

	unzipPath := filepath.Join(dirPath, "unarchived")
	os.Mkdir(unzipPath, 0777)

	err = utils.Unzip(basePath, unzipPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}
	username, err := gh.GetAuthenticatedUserName()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	url := "https://api.github.com/user/repos"

	jsonBody := []byte(`{"name": "` + repo + `","private": true,}`)
	bodyReader := bytes.NewReader(jsonBody)

	req, _ := http.NewRequest(http.MethodPost, url, bodyReader)
	req.Header.Add("Authorization", "bearer "+accessToken.Value)

	err = filepath.WalkDir(unzipPath, func(path string, di fs.DirEntry, err error) error {
		if !di.IsDir() {
			gitFile, err := os.Open(path)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			gitFileData, err := io.ReadAll(gitFile)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			gh.UploadFileToGithub(username, repo, path, gitFileData)
			gitFile.Close()
		}
		return nil
	})

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "repository " + repo + " restored to Github from Storj"})
}

// <<<<<<<--------- SHOPIFY --------->>>>>>>

func createShopifyCleint(c echo.Context, shopname string) *shopify.ShopifyClient {
	cookieToken, err := c.Cookie("shopify-auth")
	if err != nil {
		c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
		return nil
	}
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	token, err := database.ReadShopifyAuthToken(cookieToken.Value)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Error reading token from database",
			"error":   err.Error(),
		})
		return nil
	}
	cleint := shopify.CreateClient(token, shopname)
	return cleint
}

func handleShopifyProductsToStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	products, err := client.GetProducts()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting products", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "shopify", "shopify.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, product := range products {
		err = db.WriteProductsToDB(&product)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with products data was successfully uploaded"})
}

func handleShopifyCustomersToStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	customers, err := client.GetCustomers()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting customers", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "shopify", "shopify.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, customer := range customers {
		err = db.WriteCustomersToDB(&customer)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with customers data was successfully uploaded"})

}

func handleShopifyOrdersToStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	orders, err := client.GetOrders()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting orders", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "shopify", "shopify.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, order := range orders {
		err = db.WriteOrdersToDB(&order)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with orders data was successfully uploaded"})
}

// Create an oauth-authorize url for the app and redirect to it.
func handleShopifyAuth(c echo.Context) error {
	shopName := c.QueryParam("shop")
	state := c.QueryParam("state")

	authUrl := shopify.ShopifyInitApp.App.AuthorizeUrl(shopName, state)

	return c.Redirect(http.StatusFound, authUrl)
}

func handleShopifyAuthRedirect(c echo.Context) error {
	// Check that the callback signature is valid
	if ok, err := shopify.ShopifyInitApp.App.VerifyAuthorizationURL(c.Request().URL); !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Signature",
			"error":   err.Error(),
		})
	}
	query := c.Request().URL.Query()
	shopName := query.Get("shop")
	code := query.Get("code")
	token, err := shopify.ShopifyInitApp.App.GetAccessToken(shopName, code)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Signature",
			"error":   err.Error(),
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

	cookieNew := new(http.Cookie)
	cookieNew.Name = "shopify-auth"
	cookieNew.Value = utils.RandStringRunes(50)
	database.WriteShopifyAuthToken(cookieNew.Value, token)

	c.SetCookie(cookieNew)

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Authorized!"})
}

// <<<<<<<--------- QUICKBOOKS --------->>>>>>>

// func loginQuickbooksClient(c echo.Context) *quickbooks.QBClient {
// 	cookieToken, err := c.Cookie("quickbooks-auth")
// 	if err != nil {
// 		c.String(http.StatusUnauthorized, "Unauthorized")
// 		return nil
// 	}
// 	database := c.Get(dbContextKey).(*storage.PosgresStore)
// 	token, err := database.ReadQuickbooksAuthToken(cookieToken.Value)
// 	if err != nil {
// 		c.String(http.StatusBadRequest, err.Error())
// 		return nil
// 	}
// 	client, _ := quickbooks.CreateClient()

// 	return client
// }

// func AuthenticateQuickbooks(c echo.Context) error {
// 	// Get the environment variable
// 	client, _ := quickbooks.CreateClient()

// 	// Create the dynamic redirect URL for login
// 	redirectURL := "https://developer.intuit.com/v2/OAuth2Playground/RedirectUrl"

// 	return c.Redirect(http.StatusMovedPermanently, redirectURL)
// }

// func GetCompanyInfo(c echo.Context) error {
// 	client, _ := quickbooks.CreateClient()
// 	companyInfo, err := client.Client.FetchCompanyInfo()
// 	if err != nil {
// 		c.JSON(http.StatusForbidden, map[string]interface{}{ "message":  err.Error(), "error": err.Error()})
// 	}
// }

func handleQuickbooksCustomersToStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, _ := quickbooks.CreateClient()
	customers, err := client.Client.FetchCustomers()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range customers {
		err = db.WriteCustomersToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "customers are successfully uploaded from quickbooks to storj"})
}

func handleQuickbooksItemsToStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, _ := quickbooks.CreateClient()
	items, err := client.Client.FetchItems()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range items {
		err = db.WriteItemsToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "items are successfully uploaded from quickbooks to storj"})
}

func handleQuickbooksInvoicesToStorj(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}

	client, _ := quickbooks.CreateClient()
	invoices, err := client.Client.FetchInvoices()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range invoices {
		err = db.WriteInvoicesToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "invoices are successfully uploaded from quickbooks to storj"})
}
