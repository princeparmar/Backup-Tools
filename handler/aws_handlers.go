package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/StorX2-0/Backup-Tools/apps/aws"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/labstack/echo/v4"
)

// HandleListAWSs3BucketFiles lists all files in an AWS S3 bucket
func HandleListAWSs3BucketFiles(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

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

// HandleS3toSatellite downloads a file from AWS S3 and uploads it to Satellite
func HandleS3toSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, itemName)

	file, err := utils.CreateFile(path)
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

	err = satellite.UploadObject(context.Background(), accesGrant, "aws-s3", itemName, data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from AWS S3 bucket to Satellite", itemName)})
}

// HandleSatelliteToS3 downloads a file from Satellite and uploads it to AWS S3
func HandleSatelliteToS3(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	data, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_S3, itemName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"message": "error downloading object from Satellite" + err.Error(), "error": err.Error()})
	}
	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, itemName)

	file, err := utils.CreateFile(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	_, err = file.Write(data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
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
	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Satellite to AWS S3 %s bucket", itemName, bucketName)})

}
