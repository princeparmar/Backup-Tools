package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/StorX2-0/Backup-Tools/apps/dropbox"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/labstack/echo/v4"
)

// HandleDropboxToSatellite uploads a file from Dropbox to Satellite
func HandleDropboxToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	filePath := c.Param("filePath")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
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
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	err = satellite.UploadObject(context.Background(), accesGrant, "dropbox", file.Name, data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Dropbox to Satellite", file.Name)})
}

// HandleSatelliteToDropbox downloads a file from Satellite and uploads it to Dropbox
func HandleSatelliteToDropbox(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	filePath := c.Param("filePath")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	objData, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Dropbox, filePath)
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

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Satellite to Dropbox", filePath)})
}
