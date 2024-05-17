package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	google "storj-integrations/apps/google"
	"storj-integrations/storj"
	"strings"

	"github.com/labstack/echo/v4"
	"storj.io/uplink"
)

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

// Takes Google Cloud project name as a parameter, returns JSON responce with all the buckets in this project.
func handleStorageListProjects(c echo.Context) error {
	//projectName := c.Param("projectName")

	client, err := google.ListProjects(c)
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
	/*bucketsJSON, err := client.ListBucketsJSON(c, projectName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}*/
	return c.JSON(http.StatusOK, client)
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
	bucket, err := client.GetBucket(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	objects, err := client.ListObjectsInBucketJSON(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	// We use bucket ids since its unique
	o, err := storj.ListObjectsRecurisive(context.Background(), accesGrant, bucket.Id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Storj: %v", err),
		})
	}
	slices.SortStableFunc(o, func(a, b uplink.Object) int {
		return cmp.Compare(a.Key, b.Key)
	})
	var r []any
	for _, item := range objects.Items {
		_, synced := slices.BinarySearchFunc(o, item.Name, func(a uplink.Object, b string) int {
			return cmp.Compare(a.Key, b)
		})
		r = append(r, map[string]any{"item": item, "synced": synced})
	}
	return c.JSON(http.StatusOK, r)

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

	bucket, err := client.GetBucket(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
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

		err = storj.UploadObject(context.Background(), accesGrant, bucket.Id, obj.Name, obj.Data)
		fmt.Println("uploaded : "+obj.Name, "bucketID: ", bucket.Id)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("all objects in bucket '"+bucketName+"' were successfully uploaded from Storj to Google Cloud Storage", bucketName)})

}

func handleBucketMetadata(c echo.Context) error {
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
	bucket, err := client.GetBucket(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, bucket)
}

func syncCloudBucket(c echo.Context, bucketName string) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return errors.New("storj access token is missing")
	}

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		return err
	}

	bucket, err := client.GetBucket(c, bucketName)
	if err != nil {
		return err
	}
	objects, err := client.ListObjectsInBucket(c, bucketName)
	if err != nil {
		return err
	}

	for _, o := range objects.Items {
		obj, err := client.GetObject(c, bucketName, o.Name)
		if err != nil {
			return err
		}

		err = storj.UploadObject(context.Background(), accesGrant, bucket.Id, obj.Name, obj.Data)
		fmt.Println("uploaded : "+obj.Name, "bucketID: ", bucket.Id)
		if err != nil {
			return err
		}
	}
	return nil
}

type ProjectSyncResponse struct {
	ProjectID string
	Err       error
	Buckets   []struct {
		BucketName string
		Success    bool
		Err        error
	}
}

func syncCloudProject(c echo.Context, projectName string) (res *ProjectSyncResponse) {
	res = &ProjectSyncResponse{}
	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		res.Err = err
		return
	}
	bucketsJSON, err := client.ListBucketsJSON(c, projectName)
	if err != nil {
		res.Err = err
		return
	}
	
	for _, bucket := range bucketsJSON.Items {
		err = syncCloudBucket(c, bucket.Name)
		if err != nil {
			res.Buckets = append(res.Buckets, struct {
				BucketName string
				Success    bool
				Err        error
			}{bucket.Name, false, err})
		} else {
			res.Buckets = append(res.Buckets, struct {
				BucketName string
				Success    bool
				Err        error
			}{bucket.Name, true, err})
		}
	}
	return
}

func handleListProjects(c echo.Context) error {
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
	var res []any
	for _, id := range allIDs{
		r :=syncCloudProject(c, id)
		res=append(res, r)
	}
	return c.JSON(200, res)
}


func handleListBuckets(c echo.Context) error {
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
	var res []any
	for _, id := range allIDs{
		err := syncCloudBucket(c, id)
		if err != nil {
			res = append(res, map[string]any{"bukcetID":id, "success":false, "error":err})
		}else{
			res = append(res, map[string]any{"bukcetID":id, "success":false})
		}
	}
	return c.JSON(200, res)
}

func handleSyncCloudItems(c echo.Context) error {
	accesGrant := c.Request().Header.Get("STORJ_ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "storj access token is missing",
		})
	}
	var allIDs []string
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
	/*bucket, err := client.GetBucket(c, bucketName)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}*/
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
	var res []any
	for _, id := range allIDs{
		err := syncItem(c,client,accesGrant,id, bucketName)
		if err != nil {
			res = append(res, map[string]any{"itemID":id, "success":false, "error":err})
		}else{
			res = append(res, map[string]any{"itemID":id, "success":false})
		}
	}
	return c.JSON(200, res)
}

func syncItem(c echo.Context,client *google.StorageClient,accessGrant, itemName, bucketName string) error{		

	obj, err := client.GetObject(c, bucketName, itemName)
	if err != nil {
		return err
	}
	return storj.UploadObject(context.Background(), accessGrant, "google-cloud", obj.Name, obj.Data)
	
}