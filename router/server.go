package router

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	googlepack "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/crons"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"

	middleware "github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/labstack/echo/v4"
)

func StartServer(db *db.PostgresDb, address string) *echo.Echo {
	e := echo.New()
	e.HideBanner = true

	// Initialize all middleware (includes compression if enabled)
	middleware.InitializeAllMiddleware(e, db)

	// Enable HTTP compression for better performance (after other middleware)
	e.Use(middleware.GzipMiddleware())

	// Prometheus metrics endpoints
	e.GET("/metrics", echo.WrapHandler(monitor.CreateMetricsHandler()))

	// Swagger documentation endpoints
	e.GET("/swagger", handler.SwaggerUIHandler)

	e.POST("/satellite-auth", satellite.HandleSatelliteAuthentication)
	e.POST("/google-auth", googlepack.Autentificate)
	e.GET("/google-auth", googlepack.Autentificateg)
	e.GET("/autobackup/summary", handler.HandleAutomaticBackupSummary)

	autoSync := e.Group("/auto-sync")
	autoSync.GET("/live", handler.HandleAutomaticSyncActiveJobsForUser)

	job := autoSync.Group("/job")
	job.GET("/", handler.HandleAutomaticSyncListForUser)
	job.GET("/:job_id", handler.HandleAutomaticSyncDetails)
	job.POST("/:method", handler.HandleAutomaticSyncCreate)
	job.PUT("/:job_id", handler.HandleAutomaticBackupUpdate)
	job.DELETE("/:job_id", handler.HandleAutomaticSyncDelete)

	job.GET("/interval", handler.HandleIntervalOnConfig)

	task := autoSync.Group("/task")
	task.POST("/:job_id", handler.HandleAutomaticSyncCreateTask)
	task.GET("/:job_id", handler.HandleAutomaticSyncTaskList)

	// Admin endpoint for deleting jobs by email
	autoSync.DELETE("/delete-jobs-by-email", handler.HandleDeleteJobsByEmail)

	google := e.Group("/google")

	google.Use(middleware.JWTMiddleware)

	// See the requests description in README file

	// Google Drive
	google.GET("/drive-to-satellite/:ID", handler.HandleSendFileFromGoogleDriveToSatellite)

	google.GET("/satellite-to-drive/:name", handler.HandleSendFileFromSatelliteToGoogleDrive)
	// list all files in root and in root folders.
	google.GET("/drive-root-file-names", handler.HandleRootGoogleDriveFileNames)
	// List all files in root and not in root folder. Only files and folders in Root
	google.GET("/drive-get-file-names", handler.HandleGetGoogleDriveFileNames)
	google.GET("/drive-get-file/:ID", googlepack.GetFileByID)

	// list drive files in satellite
	google.GET("/satellite-drive", handler.HandleSatelliteDrive)

	//get list of files in satellite folder from drive
	google.GET("/satellite-drive-folder/:name", handler.HandleSatelliteDriveFolder)
	// All files and folders from drive to satellite
	google.GET("/all-drive-to-satellite", handler.HandleSendAllFilesFromGoogleDriveToSatellite)
	// List files in a folder by name
	google.GET("/folder/:name/list", handler.HandleListAllFolderFiles)
	// sync all files from drive folder to satellite using the folder name
	google.POST("/folder/:name/sync", handler.HandleSyncAllFolderFiles)
	// list files in folder by folder ID
	google.GET("/folder/:id", handler.HandleListAllFolderFilesByID)
	// sync files in folder by folder ID
	google.POST("/folder/:id", handler.HandleSyncAllFolderFilesByID)
	// Get all shared files
	google.GET("/get-shared-filenames", handler.HandleSharedGoogleDriveFileNames)
	// Sync all shared files
	google.POST("/sync-shared", handler.HandleSyncAllSharedFolderAndFiles)
	// Send a list of items from google drive to satellite
	google.POST("/sync-list-from-drive", handler.HandleSendListFromGoogleDriveToSatellite)

	// Google Photos
	google.GET("/photos-list-albums", handler.HandleListGPhotosAlbums)
	google.GET("/photos-list-photos-in-album/:ID", handler.HandleListPhotosInAlbum)
	google.GET("/photos-list-all", handler.HandleListAllPhotos)
	google.GET("/satellite-to-photos/:name", handler.HandleSendFileFromSatelliteToGooglePhotos)
	google.POST("/photos-to-satellite", handler.HandleSendFileFromGooglePhotosToSatellite)
	google.POST("/all-photos-from-album-to-satellite", handler.HandleSendAllFilesFromGooglePhotosToSatellite)
	google.POST("/list-photos-to-satellite", handler.HandleSendListFilesFromGooglePhotosToSatellite)

	// In the existing google group routes section
	google.POST("/gmail/insert-mail", handler.HandleGmailDownloadAndInsert)             // used by desktop app to sync emails to satellite.
	google.POST("/gmail-list-to-satellite", handler.HandleListGmailMessagesToSatellite) // used by desktop app to sync emails to satellite.
	google.GET("/query-messages", handler.HandleGmailGetThreadsIDsControlled)           // used by desktop app to show email list on backup tools UI.

	// Google Cloud Storage
	google.GET("/storage-list-buckets/:projectName", handler.HandleStorageListBuckets)
	google.GET("/storage-list-items/:bucketName", handler.HandleStorageListObjects)
	google.GET("/bucket-metadata/:bucketName", handler.HandleBucketMetadata)
	google.GET("/storage-item-to-satellite/:bucketName/:itemName", handler.HandleGoogleCloudItemToSatellite)
	google.GET("/storage-item-from-satellite-to-google-cloud/:bucketName/:itemName", handler.HandleSatelliteToGoogleCloud)
	google.POST("/storage-all-items-to-satellite/:bucketName", handler.HandleAllFilesFromGoogleCloudBucketToSatellite)
	google.POST("/list-projects-to-satellite", handler.HandleListProjects)
	google.POST("/list-buckets-to-satellite", handler.HandleListBuckets)
	google.POST("/list-items-to-satellite", handler.HandleSyncCloudItems)
	e.GET("/satellite/:bucketName", func(c echo.Context) error {
		bucketName := c.Param("bucketName")

		accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
		if accesGrant == "" {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "access token not found",
			})
		}
		list, err := satellite.ListObjectsRecursive(c.Request().Context(), accesGrant, bucketName)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		return c.JSON(http.StatusOK, list)
	})

	google.GET("/cloud/list-projects", handler.HandleStorageListProjects)
	google.GET("/cloud/list-organizations", handler.HandleStorageListOrganizations)

	// Dropbox
	dropbox := e.Group("/dropbox")

	dropbox.GET("/file-to-satellite/:filePath", handler.HandleDropboxToSatellite)
	dropbox.GET("/file-from-satellite/:filePath", handler.HandleSatelliteToDropbox)

	// office 365
	office365 := e.Group("/office365")
	office365.GET("/get-outlook-messages", handler.HandleOutlookGetMessages)
	office365.GET("/get-outlook-messages/:id", handler.HandleOutlookGetMessageById)
	office365.POST("/outlook-messages-to-satellite", handler.HandleListOutlookMessagesToSatellite)
	office365.POST("/satellite-to-outlook", handler.HandleOutlookDownloadAndInsert)
	// AWS S3
	aws := e.Group("/aws")
	aws.GET("/list-files-in-bucket/:bucketName", handler.HandleListAWSs3BucketFiles)
	aws.GET("/file-from-aws-to-satellite/:bucketName/:itemName", handler.HandleS3toSatellite)
	aws.GET("/file-from-satellite-to-aws/:bucketName/:itemName", handler.HandleSatelliteToS3)

	// Github
	github := e.Group("/github")
	github.GET("/login", handler.HandleGithubLogin)
	github.GET("/callback", handler.HandleGithubCallback)
	github.GET("/list-repos", handler.HandleListRepos)
	github.GET("/get-repo", handler.HandleGetRepository)
	github.GET("/repo-to-satellite", handler.HandleGithubRepositoryToSatellite)
	github.GET("/recover-repo-to-github", handler.HandleRepositoryFromSatelliteToGithub)

	// Shopify
	shopify := e.Group("/shopify")
	shopify.GET("/login", handler.HandleShopifyAuth)
	shopify.GET("/callback", handler.HandleShopifyAuthRedirect)
	shopify.GET("/products-to-satellite/:shopname", handler.HandleShopifyProductsToSatellite)
	shopify.GET("/customers-to-satellite/:shopname", handler.HandleShopifyCustomersToSatellite)
	shopify.GET("/orders-to-satellite/:shopname", handler.HandleShopifyOrdersToSatellite)

	// Shopify
	quickbooks := e.Group("/quickbooks")
	// shopify.GET("/login", handleShopifyAuth)
	// shopify.GET("/callback", handleShopifyAuthRedirect)
	quickbooks.GET("/customers-to-satellite", handler.HandleQuickbooksCustomersToSatellite)
	quickbooks.GET("/items-to-satellite", handler.HandleQuickbooksItemsToSatellite)
	quickbooks.GET("/invoices-to-satellite", handler.HandleQuickbooksInvoicesToSatellite)

	// Scheduled tasks
	scheduledTasks := e.Group("/tasks")
	scheduledTasks.POST("/:method", handler.HandleCreateScheduledTask)
	scheduledTasks.GET("", handler.HandleGetScheduledTasksByUserID)
	scheduledTasks.GET("/live", handler.HandleGetRunningScheduledTasks)

	// Return the echo instance for graceful shutdown handling
	return e
}

// StartServerWithGracefulShutdown starts the server with graceful shutdown support
func StartServerWithGracefulShutdown(db *db.PostgresDb, address string, autosyncManager *crons.AutosyncManager) {
	e := StartServer(db, address)

	// Start server in a goroutine
	go func() {
		if err := e.Start(address); err != nil && err != http.ErrServerClosed {
			logger.Error(context.Background(), "Error starting server", logger.ErrorField(err))
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	logger.Info(context.Background(), "Shutting down server...")

	// Create a context with timeout for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stop background jobs first
	if autosyncManager != nil {
		autosyncManager.Stop()
	}

	// Shutdown the server
	if err := e.Shutdown(ctx); err != nil {
		logger.Error(context.Background(), "Server forced to shutdown", logger.ErrorField(err))
	} else {
		logger.Info(context.Background(), "Server exited gracefully")
	}
}
