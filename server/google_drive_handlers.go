package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/utils"

	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

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

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return errors.New("error: access token not found")
	}
	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/.file_placeholder", nil)
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
			err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", path.Join(folderName, name), data)
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

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", "shared with me/", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
		})
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, file := range fileNames {
		func(file *google.FilesJSON) {
			g.Go(func() error {
				name, data, err := google.GetFile(c, file.ID)
				if err != nil {

					failedIDs.Add(file.ID)
					return nil

				}

				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
				if err != nil {
					failedIDs.Add(file.ID)
					return nil
				}
				processedIDs.Add(file.ID)
				return nil

			})
		}(file)
	}
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all files were successfully uploaded from Google drive to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
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

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
		})
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, file := range fileNames {
		func(file *google.FilesJSON) {
			g.Go(func() error {
				name, data, err := google.GetFile(c, file.ID)
				if err != nil {

					failedIDs.Add(file.ID)
					return nil

				}

				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
				if err != nil {
					failedIDs.Add(file.ID)
					return nil
				}
				processedIDs.Add(file.ID)
				return nil

			})
		}(file)
	}
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all files were successfully uploaded from Google drive to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})

}

func handleSatelliteDrive(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	o, err := satellite.ListObjects1(context.Background(), accesGrant, "google-drive")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Satellite: %v", err),
		})
	}
	return c.JSON(http.StatusOK, o)
}

func handleSatelliteDriveFolder(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	o, err := satellite.GetFilesInFolder(context.Background(), accesGrant, "google-drive", c.Param("name")+"/")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Satellite: %v", err),
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

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
		})
	}

	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, file := range fileNames {
		func(file *google.FilesJSON) {
			g.Go(func() error {
				name, data, err := google.GetFile(c, file.ID)
				if err != nil {

					failedIDs.Add(file.ID)
					return nil

				}

				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
				if err != nil {
					failedIDs.Add(file.ID)
					return nil
				}
				processedIDs.Add(file.ID)
				return nil

			})
		}(file)
	}
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all files were successfully uploaded from Google drive to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

// Sends file from Google Drive to Satellite
func handleSendFileFromGoogleDriveToSatellite(c echo.Context) error {
	id := c.Param("ID")

	name, data, err := google.GetFileAndPath(c, id)
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
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", name, data)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Google Drive to Satellite", name),
	})
}

func handleSendAllFilesFromGoogleDriveToSatellite(c echo.Context) error {

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	// Get only file names in root
	shared := c.QueryParam("include_shared")
	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	if shared == "true" {
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

		err = satellite.UploadObject(ctx, accesGrant, "google-drive", "shared with me/", nil)
		/*if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
			})
		}*/

		for _, file := range fileNames {
			func(file *google.FilesJSON) {
				g.Go(func() error {
					name, data, err := google.GetFile(c, file.ID)
					if err != nil {

						failedIDs.Add(file.ID)
						return nil

					}

					err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
					if err != nil {
						failedIDs.Add(file.ID)
						return nil
					}
					processedIDs.Add(file.ID)
					return nil

				})
			}(file)
		}
	}
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

	for _, file := range resp {
		func(file *google.FilesJSON) {
			g.Go(func() error {
				name, data, err := google.GetFile(c, file.ID)
				if err != nil {

					failedIDs.Add(file.ID)
					return nil

				}

				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
				if err != nil {
					failedIDs.Add(file.ID)
					return nil
				}
				processedIDs.Add(file.ID)
				return nil

			})
		}(file)
	}
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all files were successfully uploaded from Google drive to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

// Sends file from Satellite to Google Drive
func handleSendFileFromSatelliteToGoogleDrive(c echo.Context) error {
	name := c.Param("name")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	data, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Drive, name)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to download object from Satellite: %v", err),
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
		"message": fmt.Sprintf("file %s was successfully uploaded from Satellite to Google Drive", name),
	})
}

func handleSendListFromGoogleDriveToSatellite(c echo.Context) error {
	// Get only file names in root
	var allIDs []string
	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		// Decode JSON array from request body
		if err := json.NewDecoder(c.Request().Body).Decode(&allIDs); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid JSON format",
			})
		}
	} else {
		// Handle form data
		formIDs := c.FormValue("ids")
		allIDs = strings.Split(formIDs, ",")
	}
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				name, data, err := google.GetFileAndPath(c, id)
				if err != nil {
					if strings.Contains(err.Error(), "folder error") {
						if err = handleFolder(name, id, c); err != nil {
							failedIDs.Add(id)
							return nil
						} else {
							processedIDs.Add(id)
							return nil
						}
					} else {

						failedIDs.Add(id)
						return nil
					}
				} else {

					if err = satellite.UploadObject(ctx, accesGrant, "google-drive", name, data); err != nil {
						failedIDs.Add(id)
						return nil
					}
					processedIDs.Add(id)
					return nil
				}
			})
		}(id)
	}
	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "all files were successfully uploaded from Google Drive to Satellite",
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}
