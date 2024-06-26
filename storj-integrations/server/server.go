package server

import (
	"log/slog"
	googlepack "storj-integrations/apps/google"
	"storj-integrations/storage"
	"storj-integrations/storj"

	"github.com/labstack/echo/v4"
)

func StartServer(db *storage.PosgresStore) {
	e := echo.New()
	e.HideBanner = true

	e.Use(DBMiddleware(db))

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Access-Control-Allow-Origin", "*")
			c.Response().Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			c.Response().Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			return next(c)
		}
	})

	e.POST("/storj-auth", storj.HandleStorjAuthentication)

	google := e.Group("/google")

	// See the requests description in README file

	google.GET("/google-auth", googlepack.Autentificate)

	// Google Drive
	google.GET("/drive-to-storj/:ID", handleSendFileFromGoogleDriveToStorj)
	
	google.GET("/storj-to-drive/:name", handleSendFileFromStorjToGoogleDrive)
	// list all files in root and in root folders. 
	google.GET("/drive-root-file-names", handleRootGoogleDriveFileNames)
	// List all files in root and not in root folder. Only files and folders in Root
	google.GET("/drive-get-file-names", handleGetGoogleDriveFileNames)
	google.GET("/drive-get-file/:ID", googlepack.GetFileByID)

	// All files and folders from drive to storj
	google.GET("/all-drive-to-storj", handleSendAllFilesFromGoogleDriveToStorj)
	// List files in a folder by name
	google.GET("/folder/:name/list", handleListAllFolderFiles)
	// sync all files from drive folder to storj using the folder name
	google.POST("/folder/:name/sync", handleSyncAllFolderFiles)
	// list files in folder by folder ID
	google.GET("/folder/:id", handleListAllFolderFilesByID)
	// sync files in folder by folder ID
	google.POST("/folder/:id", handleSyncAllFolderFilesByID)
	// Google Photos
	google.GET("/photos-list-albums", handleListGPhotosAlbums)
	google.GET("/photos-list-photos-in-album/:ID", handleListPhotosInAlbum)
	google.GET("/storj-to-photos/:name", handleSendFileFromStorjToGooglePhotos)
	google.GET("/photos-to-storj/:ID", handleSendFileFromGooglePhotosToStorj)
	google.GET("/all-photos-from-album-to-storj/:ID", handleSendAllFilesFromGooglePhotosToStorj)

	// Gmail
	google.GET("/gmail-list-threads", handleGmailGetThreads)
	google.GET("/gmail-list-messages", handleGmailGetMessages)
	google.GET("/gmail-get-message/:ID", handleGmailGetMessage)
	google.GET("/gmail-message-to-storj/:ID", handleGmailMessageToStorj)
	google.GET("/get-gmail-db-from-storj", handleGetGmailDBFromStorj)

	// Google Cloud Storage
	google.GET("/storage-list-buckets/:projectName", handleStorageListBuckets)
	google.GET("/storage-list-items/:bucketName", handleStorageListObjects)
	google.GET("/storage-item-to-storj/:bucketName/:itemName", handleGoogleCloudItemToStorj)
	google.GET("/storage-item-from-storj-to-google-cloud/:bucketName/:itemName", handleStorjToGoogleCloud)
	google.GET("/storage-all-items-to-storj/:bucketName", handleAllFilesFromGoogleCloudBucketToStorj)

	// Dropbox
	dropbox := e.Group("/dropbox")

	dropbox.GET("/file-to-storj/:filePath", handleDropboxToStorj)
	dropbox.GET("/file-from-storj/:filePath", handleStorjToDropbox)

	// AWS S3
	aws := e.Group("/aws")
	aws.GET("/list-files-in-bucket/:bucketName", handleListAWSs3BucketFiles)
	aws.GET("/file-from-aws-to-storj/:bucketName/:itemName", handleS3toStorj)
	aws.GET("/file-from-storj-to-aws/:bucketName/:itemName", handleStorjToS3)

	// Github
	github := e.Group("/github")
	github.GET("/login", handleGithubLogin)
	github.GET("/callback", handleGithubCallback)
	github.GET("/list-repos", handleListRepos)
	github.GET("/get-repo", handleGetRepository)
	github.GET("/repo-to-storj", handleGithubRepositoryToStorj)
	github.GET("/recover-repo-to-github", handleRepositoryFromStorjToGithub)

	// Shopify
	shopify := e.Group("/shopify")
	shopify.GET("/login", handleShopifyAuth)
	shopify.GET("/callback", handleShopifyAuthRedirect)
	shopify.GET("/products-to-storj/:shopname", handleShopifyProductsToStorj)
	shopify.GET("/customers-to-storj/:shopname", handleShopifyCustomersToStorj)
	shopify.GET("/orders-to-storj/:shopname", handleShopifyOrdersToStorj)

	// Shopify
	quickbooks := e.Group("/quickbooks")
	// shopify.GET("/login", handleShopifyAuth)
	// shopify.GET("/callback", handleShopifyAuthRedirect)
	quickbooks.GET("/customers-to-storj", handleQuickbooksCustomersToStorj)
	quickbooks.GET("/items-to-storj", handleQuickbooksItemsToStorj)
	quickbooks.GET("/invoices-to-storj", handleQuickbooksInvoicesToStorj)

	if err := e.Start(":8005"); err != nil {
		slog.Error("Error starting server", "error", err)
	}
}
