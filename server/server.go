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

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Access-Control-Allow-Origin", "*")
			c.Response().Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			c.Response().Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			return next(c)
		}
	})

	e.POST("/storj-auth", storj.HandleStorjAuthentication)
	e.GET("/google-auth", googlepack.Autentificate)

	google := e.Group("/google")

	google.Use(JWTMiddleware)

	// See the requests description in README file

	google.GET("/google-auth", googlepack.Autentificate)

	// Google Drive
	google.GET("/drive-to-storj/:ID", handleSendFileFromGoogleDriveToStorj)
	google.GET("/storj-to-drive/:name", handleSendFileFromStorjToGoogleDrive)
	google.GET("/drive-get-file-names", googlepack.GetFileNames)
	google.GET("/google-auth", googlepack.Autentificate)
	google.GET("/drive-get-file/:ID", googlepack.GetFileByID)
	google.GET("/all-drive-to-storj", handleSendAllFilesFromGoogleDriveToStorj)

	// Google Photos
	google.GET("/photos-list-albums", handleListGPhotosAlbums)
	google.GET("/photos-list-photos-in-album/:ID", handleListPhotosInAlbum)
	google.GET("/storj-to-photos/:name", handleSendFileFromStorjToGooglePhotos)
	google.GET("/photos-to-storj/:ID", handleSendFileFromGooglePhotosToStorj)

	e.Start(":8000")
}
