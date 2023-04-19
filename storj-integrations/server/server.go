package server

import (
	googlepack "storj-integrations/apps/google"
	"storj-integrations/storage"
	"storj-integrations/storj"

	"github.com/labstack/echo/v4"
)

func StartServer(db *storage.PosgresStore) {
	e := echo.New()
	e.HideBanner = true

	e.Use(DBMiddleware(db))

	e.POST("/storj-auth", storj.HandleStorjAuthentication)

	google := e.Group("/google")

	// See the requests description in README file

	google.GET("/google-auth", googlepack.Autentificate)

	// Google Drive
	google.GET("/drive-to-storj/:ID", handleSendFileFromGoogleDriveToStorj)
	google.GET("/storj-to-drive/:name", handleSendFileFromStorjToGoogleDrive)
	google.GET("/drive-get-file-names", googlepack.GetFileNames)
	google.GET("/drive-get-file/:ID", googlepack.GetFileByID)

	// Google Photos
	google.GET("/photos-list-albums", handleListGPhotosAlbums)
	google.GET("/photos-list-photos-in-album/:ID", handleListPhotosInAlbum)
	google.GET("/storj-to-photos/:name", handleSendFileFromStorjToGooglePhotos)
	google.GET("/photos-to-storj/:ID", handleSendFileFromGooglePhotosToStorj)

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

	e.Start(":8000")
}
