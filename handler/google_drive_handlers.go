package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/labstack/echo/v4"
)

// Get all files names in a google drive even in folder
func HandleGetGoogleDriveFileNames(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	fileNames, err := google.GetFileNames(c)
	if err != nil {
		return HandleGoogleDriveError(c, err, "retrieve file names from Google Drive")
	}
	return c.JSON(http.StatusOK, fileNames)
}

// Get all files names in a google drive root
func HandleRootGoogleDriveFileNames(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Extract access grant early for webhook processing
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant != "" {
		go func() {
			processCtx := context.Background()
			database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
			if processErr := ProcessWebhookEvents(processCtx, database, accessGrant, 100); processErr != nil {
				logger.Warn(processCtx, "Failed to process webhook events from listing route",
					logger.ErrorField(processErr))
			}
		}()
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return HandleGoogleDriveError(c, err, "authentication failed")
	}

	response, err := google.GetFileNamesInRoot(c, database, userID)
	if err != nil {
		return HandleGoogleDriveError(c, err, "retrieve file names from Google Drive")
	}

	return c.JSON(http.StatusOK, response)
}

// Get all files names in a google drive root
func HandleSharedGoogleDriveFileNames(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return HandleGoogleDriveError(c, err, "authentication failed")
	}

	fileNames, err := google.GetSharedFiles(c, database, userID)
	if err != nil {
		return HandleGoogleDriveError(c, err, "retrieve shared files from Google Drive")
	}
	return c.JSON(http.StatusOK, fileNames)
}

// List all files in a folder given the folder ID
func HandleListAllFolderFilesByID(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Get database and userID for synced_objects query
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return HandleGoogleDriveError(c, err, "authentication failed")
	}

	folderID := c.Param("id")
	fileNames, err := google.GetFilesInFolderByID(c, folderID, database, userID)
	if err != nil {
		return HandleGoogleDriveError(c, err, "retrieve files from Google Drive folder by ID")
	}
	return c.JSON(http.StatusOK, fileNames)
}

// func HandleFolder(folderName, folderID string, c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	fileNames, err := google.GetFilesInFolderByID(c, folderID)
// 	if err != nil {
// 		return HandleGoogleDriveError(c, err, "retrieve files from Google Drive folder")
// 	}
// 	// If folder is empty, create an empty folder

// 	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accessGrant == "" {
// 		return errors.New("error: access token not found")
// 	}
// 	err = satellite.UploadObject(context.Background(), accessGrant, "google-drive", folderName+"/.file_placeholder", nil)
// 	if err != nil {
// 		return HandleGoogleDriveError(c, err, "upload file to Google Drive")
// 	}

// 	for _, file := range fileNames.Files {
// 		name, data, err := google.GetFile(c, file.ID)
// 		if err != nil {
// 			if strings.Contains(err.Error(), "folder error") {
// 				if err = HandleFolder(path.Join(folderName, file.Name), file.ID, c); err != nil {
// 					return HandleGoogleDriveError(c, err, "upload file to Google Drive")
// 				}
// 			} else if strings.Contains(err.Error(), "The requested conversion is not supported") || strings.Contains(err.Error(), "Export only supports Docs Editors files") {
// 				// No conversion for this type
// 				continue
// 			} else {

// 				return HandleGoogleDriveError(c, err, "upload file to Google Drive")
// 			}
// 		} else {
// 			err = satellite.UploadObject(context.Background(), accessGrant, "google-drive", path.Join(folderName, name), data)
// 			if err != nil {
// 				return HandleGoogleDriveError(c, err, "upload file to Google Drive")
// 			}
// 		}
// 	}
// 	return nil
// }

// func HandleSyncAllSharedFolderAndFiles(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	fileNames, err := google.GetSharedFiles(c)
// 	if err != nil {
// 		return HandleGoogleDriveError(c, err, "retrieve shared files from Google Drive")
// 	}
// 	// If folder is empty, create an empty folder

// 	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accesGrant == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "access token not found",
// 		})
// 	}
// 	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", "shared with me/", nil)
// 	if err != nil {
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
// 		})
// 	}

// 	g, ctx := errgroup.WithContext(c.Request().Context())
// 	g.SetLimit(10)

// 	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
// 	for _, file := range fileNames.Files {
// 		func(file *google.FilesJSON) {
// 			g.Go(func() error {
// 				name, data, err := google.GetFile(c, file.ID)
// 				if err != nil {

// 					failedIDs.Add(file.ID)
// 					return nil

// 				}

// 				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
// 				if err != nil {
// 					failedIDs.Add(file.ID)
// 					return nil
// 				}
// 				processedIDs.Add(file.ID)
// 				return nil

// 			})
// 		}(file)
// 	}
// 	if err := g.Wait(); err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error":         err.Error(),
// 			"failed_ids":    failedIDs.Get(),
// 			"processed_ids": processedIDs.Get(),
// 		})
// 	}

// 	return c.JSON(http.StatusOK, map[string]interface{}{
// 		"message":       "all files were successfully uploaded from Google drive to Satellite",
// 		"failed_ids":    failedIDs.Get(),
// 		"processed_ids": processedIDs.Get(),
// 	})
// }

// func HandleSyncAllFolderFiles(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	folderName := c.Param("name")
// 	fileNames, err := google.GetFilesInFolder(c, folderName)
// 	if err != nil {
// 		return HandleGoogleDriveError(c, err, "retrieve files from Google Drive folder")
// 	}
// 	// If folder is empty, create an empty folder

// 	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accesGrant == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "access token not found",
// 		})
// 	}
// 	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
// 	if err != nil {
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
// 		})
// 	}

// 	g, ctx := errgroup.WithContext(c.Request().Context())
// 	g.SetLimit(10)

// 	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
// 	for _, file := range fileNames {
// 		func(file *google.FilesJSON) {
// 			g.Go(func() error {
// 				name, data, err := google.GetFile(c, file.ID)
// 				if err != nil {

// 					failedIDs.Add(file.ID)
// 					return nil

// 				}

// 				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
// 				if err != nil {
// 					failedIDs.Add(file.ID)
// 					return nil
// 				}
// 				processedIDs.Add(file.ID)
// 				return nil

// 			})
// 		}(file)
// 	}
// 	if err := g.Wait(); err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error":         err.Error(),
// 			"failed_ids":    failedIDs.Get(),
// 			"processed_ids": processedIDs.Get(),
// 		})
// 	}

// 	return c.JSON(http.StatusOK, map[string]interface{}{
// 		"message":       "all files were successfully uploaded from Google drive to Satellite",
// 		"failed_ids":    failedIDs.Get(),
// 		"processed_ids": processedIDs.Get(),
// 	})

// }

func HandleSatelliteDrive(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	o, err := satellite.ListObjects(context.Background(), accesGrant, "google-drive")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Satellite: %v", err),
		})
	}
	return c.JSON(http.StatusOK, o)
}

func HandleSatelliteDriveFolder(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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

// func HandleSyncAllFolderFilesByID(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	folderID := c.Param("id")
// 	folderName, fileNames, err := google.GetFolderNameAndFilesInFolderByID(c, folderID)
// 	if err != nil {
// 		return HandleGoogleDriveError(c, err, "retrieve folder and files from Google Drive")
// 	}
// 	// If folder is empty, create an empty folder

// 	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accesGrant == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "access token not found",
// 		})
// 	}
// 	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", folderName+"/", nil)
// 	if err != nil {
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
// 		})
// 	}

// 	g, ctx := errgroup.WithContext(c.Request().Context())
// 	g.SetLimit(10)

// 	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
// 	for _, file := range fileNames {
// 		func(file *google.FilesJSON) {
// 			g.Go(func() error {
// 				name, data, err := google.GetFile(c, file.ID)
// 				if err != nil {

// 					failedIDs.Add(file.ID)
// 					return nil

// 				}

// 				err = satellite.UploadObject(ctx, accesGrant, "google-drive", path.Join("shared with me", name), data)
// 				if err != nil {
// 					failedIDs.Add(file.ID)
// 					return nil
// 				}
// 				processedIDs.Add(file.ID)
// 				return nil

// 			})
// 		}(file)
// 	}
// 	if err := g.Wait(); err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error":         err.Error(),
// 			"failed_ids":    failedIDs.Get(),
// 			"processed_ids": processedIDs.Get(),
// 		})
// 	}

// 	return c.JSON(http.StatusOK, map[string]interface{}{
// 		"message":       "all files were successfully uploaded from Google drive to Satellite",
// 		"failed_ids":    failedIDs.Get(),
// 		"processed_ids": processedIDs.Get(),
// 	})
// }

// Sends file from Google Drive to Satellite
func HandleSendFileFromGoogleDriveToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	id := c.Param("ID")

	name, data, err := google.GetFileAndPath(c, id)
	if err != nil {
		return HandleGoogleDriveError(c, err, "retrieve file from Google Drive")
	}
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// Get user email to create user-specific directory
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to get user email: " + err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "user email not found, please check access handling",
		})
	}

	// Create path with user email directory: userEmail/filename
	drivePath := userDetails.Email + "/" + name

	err = satellite.UploadObject(context.Background(), accesGrant, "google-drive", drivePath, data)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Google Drive to Satellite", name),
	})
}

// func HandleSendAllFilesFromGoogleDriveToSatellite(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accesGrant == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "access token not found",
// 		})
// 	}

// 	// Get user email to create user-specific directory
// 	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
// 	if err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "failed to get user email: " + err.Error(),
// 		})
// 	}

// 	if userDetails.Email == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "user email not found, please check access handling",
// 		})
// 	}

// 	// Get only file names in root
// 	shared := c.QueryParam("include_shared")
// 	g, ctx := errgroup.WithContext(c.Request().Context())
// 	g.SetLimit(10)

// 	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
// 	if shared == "true" {
// 		fileNames, err := google.GetSharedFiles(c)
// 		if err != nil {
// 			return HandleGoogleDriveError(c, err, "retrieve shared files from Google Drive")
// 		}
// 		// If folder is empty, create an empty folder
// 		sharedFolderPath := userDetails.Email + "/shared with me/"

// 		err = satellite.UploadObject(ctx, accesGrant, "google-drive", sharedFolderPath, nil)
// 		if err != nil {
// 			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 				"error": fmt.Sprintf("failed to upload file to Satellite: %v", err),
// 			})
// 		}

// 		for _, file := range fileNames.Files {
// 			func(file *google.FilesJSON) {
// 				g.Go(func() error {
// 					name, data, err := google.GetFile(c, file.ID)
// 					if err != nil {

// 						failedIDs.Add(file.ID)
// 						return nil

// 					}

// 					// Create path with user email directory: userEmail/shared with me/filename
// 					drivePath := userDetails.Email + "/" + path.Join("shared with me", name)

// 					err = satellite.UploadObject(ctx, accesGrant, "google-drive", drivePath, data)
// 					if err != nil {
// 						failedIDs.Add(file.ID)
// 						return nil
// 					}
// 					processedIDs.Add(file.ID)
// 					return nil

// 				})
// 			}(file)
// 		}
// 	}
// 	response, err := google.GetFileNamesInRoot(c)
// 	if err != nil {
// 		return HandleGoogleDriveError(c, err, "retrieve files from Google Drive root")
// 	}

// 	for _, file := range response.Files {
// 		func(file *google.FilesJSON) {
// 			g.Go(func() error {
// 				name, data, err := google.GetFile(c, file.ID)
// 				if err != nil {

// 					failedIDs.Add(file.ID)
// 					return nil

// 				}

// 				// Create path with user email directory: userEmail/filename
// 				drivePath := userDetails.Email + "/" + name

// 				err = satellite.UploadObject(ctx, accesGrant, "google-drive", drivePath, data)
// 				if err != nil {
// 					failedIDs.Add(file.ID)
// 					return nil
// 				}
// 				processedIDs.Add(file.ID)
// 				return nil

// 			})
// 		}(file)
// 	}
// 	if err := g.Wait(); err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error":         err.Error(),
// 			"failed_ids":    failedIDs.Get(),
// 			"processed_ids": processedIDs.Get(),
// 		})
// 	}

// 	return c.JSON(http.StatusOK, map[string]interface{}{
// 		"message":       "all files were successfully uploaded from Google drive to Satellite",
// 		"failed_ids":    failedIDs.Get(),
// 		"processed_ids": processedIDs.Get(),
// 	})
// }

// Sends file from Satellite to Google Drive
func HandleSendFileFromSatelliteToGoogleDrive(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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
		return HandleGoogleDriveError(c, err, "upload file to Google Drive")
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Satellite to Google Drive", name),
	})
}

// func HandleSendListFromGoogleDriveToSatellite(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	// Parse request IDs
// 	allIDs, err := parseRequestIDs(c)
// 	if err != nil {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"error": err.Error(),
// 		})
// 	}

// 	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accessGrant == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "access token not found",
// 		})
// 	}

// 	// Get user email to create user-specific directory
// 	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
// 	if err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "failed to get user email: " + err.Error(),
// 		})
// 	}

// 	if userDetails.Email == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "user email not found, please check access handling",
// 		})
// 	}

// 	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

// 	g, ctx := errgroup.WithContext(c.Request().Context())
// 	g.SetLimit(10)

// 	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()
// 	for _, id := range allIDs {
// 		func(id string) {
// 			g.Go(func() error {
// 				name, data, err := google.GetFileAndPath(c, id)
// 				if err != nil {
// 					if strings.Contains(err.Error(), "folder error") {
// 						// Create path with user email directory: userEmail/foldername
// 						folderPath := userDetails.Email + "/" + name
// 						if err = HandleFolder(folderPath, id, c); err != nil {
// 							failedIDs.Add(id)
// 							return nil
// 						} else {
// 							processedIDs.Add(id)
// 							return nil
// 						}
// 					} else {

// 						failedIDs.Add(id)
// 						return nil
// 					}
// 				} else {
// 					// Create path with user email directory: userEmail/filename
// 					drivePath := userDetails.Email + "/" + name

// 					// Use helper function to upload and sync to database
// 					// Source and Type are automatically derived from bucket name (hardcoded)
// 					// Source: "google", Type: "drive" (from bucket name "google-drive")
// 					if err = UploadObjectAndSync(ctx, database, accessGrant, "google-drive", drivePath, data, userDetails.Email); err != nil {
// 						failedIDs.Add(id)
// 						return nil
// 					}
// 					processedIDs.Add(id)
// 					return nil
// 				}
// 			})
// 		}(id)
// 	}
// 	if err := g.Wait(); err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error":         err.Error(),
// 			"failed_ids":    failedIDs.Get(),
// 			"processed_ids": processedIDs.Get(),
// 		})
// 	}

// 	return c.JSON(http.StatusOK, map[string]interface{}{
// 		"message":       "all files were successfully uploaded from Google Drive to Satellite",
// 		"failed_ids":    failedIDs.Get(),
// 		"processed_ids": processedIDs.Get(),
// 	})
// }

// Helper function to handle Google Drive errors with consistent response format
func HandleGoogleDriveError(c echo.Context, err error, operation string) error {
	if err.Error() == "token error" {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "token expired",
		})
	} else {
		slog.Debug("Error in Google Drive operation", "operation", operation, "error", err)
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to " + operation,
		})
	}
}
