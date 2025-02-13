package server

import (
	"context"
	"fmt"
	"net/http"

	googlepack "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func StartServer(db *storage.PosgresStore, address string) {
	e := echo.New()
	e.HideBanner = true

	// Enable logging middleware
	e.Use(middleware.Logger())

	// Enable recovery middleware to handle panics and return a 500
	e.Use(middleware.Recover())

	e.Use(DBMiddleware(db))

	e.Use(middleware.CORS())

	e.POST("/satellite-auth", satellite.HandleSatelliteAuthentication)
	e.POST("/google-auth", googlepack.Autentificate)
	e.GET("/google-auth", googlepack.Autentificateg)

	autoSync := e.Group("/auto-sync")
	job := autoSync.Group("/job")
	job.GET("/", handleAutomaticSyncListForUser)
	job.GET("/:job_id", handleAutomaticSyncDetails)
	job.POST("/gmail", handleAutomaticSyncCreateGmail)
	job.POST("/database/:method", handleAutomaticSyncCreateDatabase)
	job.PUT("/:job_id", handleAutomaticBackupUpdate)
	job.DELETE("/:job_id", handleAutomaticSyncDelete)

	job.GET("/interval", handleIntervalOnConfig)

	task := autoSync.Group("/task")
	task.GET("/:job_id", handleAutomaticSyncTaskList)

	google := e.Group("/google")

	google.Use(JWTMiddleware)

	// See the requests description in README file

	// Google Drive
	google.GET("/drive-to-satellite/:ID", handleSendFileFromGoogleDriveToSatellite)

	google.GET("/satellite-to-drive/:name", handleSendFileFromSatelliteToGoogleDrive)
	// list all files in root and in root folders.
	google.GET("/drive-root-file-names", handleRootGoogleDriveFileNames)
	// List all files in root and not in root folder. Only files and folders in Root
	google.GET("/drive-get-file-names", handleGetGoogleDriveFileNames)
	google.GET("/drive-get-file/:ID", googlepack.GetFileByID)

	// list drive files in satellite
	google.GET("/satellite-drive", handleSatelliteDrive)

	//get list of files in satellite folder from drive
	google.GET("/satellite-drive-folder/:name", handleSatelliteDriveFolder)
	// All files and folders from drive to satellite
	google.GET("/all-drive-to-satellite", handleSendAllFilesFromGoogleDriveToSatellite)
	// List files in a folder by name
	google.GET("/folder/:name/list", handleListAllFolderFiles)
	// sync all files from drive folder to satellite using the folder name
	google.POST("/folder/:name/sync", handleSyncAllFolderFiles)
	// list files in folder by folder ID
	google.GET("/folder/:id", handleListAllFolderFilesByID)
	// sync files in folder by folder ID
	google.POST("/folder/:id", handleSyncAllFolderFilesByID)
	// Get all shared files
	google.GET("/get-shared-filenames", handleSharedGoogleDriveFileNames)
	// Sync all shared files
	google.POST("/sync-shared", handleSyncAllSharedFolderAndFiles)
	// Send a list of items from google drive to satellite
	google.POST("/sync-list-from-drive", handleSendListFromGoogleDriveToSatellite)

	// Google Photos
	google.GET("/photos-list-albums", handleListGPhotosAlbums)
	google.GET("/photos-list-photos-in-album/:ID", handleListPhotosInAlbum)
	google.GET("/photos-list-all", handleListAllPhotos)
	google.GET("/satellite-to-photos/:name", handleSendFileFromSatelliteToGooglePhotos)
	google.POST("/photos-to-satellite", handleSendFileFromGooglePhotosToSatellite)
	google.POST("/all-photos-from-album-to-satellite", handleSendAllFilesFromGooglePhotosToSatellite)
	google.POST("/list-photos-to-satellite", handleSendListFilesFromGooglePhotosToSatellite)

	// Gmail
	google.GET("/gmail-list-threads", handleGmailGetThreads)
	google.GET("/gmail-list-messages", handleGmailGetMessages)
	// google.GET("/gmail-list-messages-using-workers", handleGmailGetMessagesUsingWorkers)
	google.GET("/gmail-list-messages-ids", handleGmailGetMessagesIDs)
	google.GET("/gmail-list-threads-ids", handleGmailGetThreadsIDs)
	google.GET("/gmail-get-message/:ID", handleGmailGetMessage)
	google.GET("/gmail-get-thread/:ID", handleGmailGetThread)
	// In the existing google group routes section
	google.POST("/gmail/insert-mail", handleGmailDownloadAndInsert)

	// google.POST("/all-gmail-to-satellite", handleAllGmailMessagesToSatellite)
	google.POST("/gmail-list-to-satellite", handleListGmailMessagesToSatellite)
	google.POST("/gmail-message-to-satellite/:ID", handleGmailMessageToSatellite)
	google.GET("/get-gmail-db-from-satellite", handleGetGmailDBFromSatellite)
	google.GET("/query-messages", handleGmailGetThreadsIDsControlled)

	// Google Cloud Storage
	google.GET("/storage-list-buckets/:projectName", handleStorageListBuckets)
	google.GET("/storage-list-items/:bucketName", handleStorageListObjects)
	google.GET("/bucket-metadata/:bucketName", handleBucketMetadata)
	google.GET("/storage-item-to-satellite/:bucketName/:itemName", handleGoogleCloudItemToSatellite)
	google.GET("/storage-item-from-satellite-to-google-cloud/:bucketName/:itemName", handleSatelliteToGoogleCloud)
	google.POST("/storage-all-items-to-satellite/:bucketName", handleAllFilesFromGoogleCloudBucketToSatellite)
	google.POST("/list-projects-to-satellite", handleListProjects)
	google.POST("/list-buckets-to-satellite", handleListBuckets)
	google.POST("/list-items-to-satellite", handleSyncCloudItems)
	e.GET("/satellite/:bucketName", func(c echo.Context) error {
		bucketName := c.Param("bucketName")

		accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
		if accesGrant == "" {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "access token not found",
			})
		}
		list, err := satellite.ListObjectsRecurisive(context.Background(), accesGrant, bucketName)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		return c.JSON(http.StatusOK, list)
	})

	google.GET("/cloud/list-projects", handleStorageListProjects)
	google.GET("/cloud/list-organizations", handleStorageListOrganizations)

	// Dropbox
	dropbox := e.Group("/dropbox")

	dropbox.GET("/file-to-satellite/:filePath", handleDropboxToSatellite)
	dropbox.GET("/file-from-satellite/:filePath", handleSatelliteToDropbox)

	// AWS S3
	aws := e.Group("/aws")
	aws.GET("/list-files-in-bucket/:bucketName", handleListAWSs3BucketFiles)
	aws.GET("/file-from-aws-to-satellite/:bucketName/:itemName", handleS3toSatellite)
	aws.GET("/file-from-satellite-to-aws/:bucketName/:itemName", handleSatelliteToS3)

	// Github
	github := e.Group("/github")
	github.GET("/login", handleGithubLogin)
	github.GET("/callback", handleGithubCallback)
	github.GET("/list-repos", handleListRepos)
	github.GET("/get-repo", handleGetRepository)
	github.GET("/repo-to-satellite", handleGithubRepositoryToSatellite)
	github.GET("/recover-repo-to-github", handleRepositoryFromSatelliteToGithub)

	// Shopify
	shopify := e.Group("/shopify")
	shopify.GET("/login", handleShopifyAuth)
	shopify.GET("/callback", handleShopifyAuthRedirect)
	shopify.GET("/products-to-satellite/:shopname", handleShopifyProductsToSatellite)
	shopify.GET("/customers-to-satellite/:shopname", handleShopifyCustomersToSatellite)
	shopify.GET("/orders-to-satellite/:shopname", handleShopifyOrdersToSatellite)

	// Shopify
	quickbooks := e.Group("/quickbooks")
	// shopify.GET("/login", handleShopifyAuth)
	// shopify.GET("/callback", handleShopifyAuthRedirect)
	quickbooks.GET("/customers-to-satellite", handleQuickbooksCustomersToSatellite)
	quickbooks.GET("/items-to-satellite", handleQuickbooksItemsToSatellite)
	quickbooks.GET("/invoices-to-satellite", handleQuickbooksInvoicesToSatellite)

	err := e.Start(address)
	if err != nil {
		fmt.Println("Error starting server", err)
	}
}
