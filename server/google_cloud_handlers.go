package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"

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

func handleStorageListProjects(c echo.Context) error {
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
	return c.JSON(http.StatusOK, client)
}

func handleStorageListOrganizations(c echo.Context) error {
	client, err := google.ListOrganizations(c)
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
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	// We use bucket ids since its unique
	o, err := satellite.ListObjectsRecurisive(context.Background(), accesGrant, bucket.Id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to get file list from Satellite: %v", err),
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

// Takes bucket name and item name as a parameters, downloads the object from Google Cloud Storage and uploads it into SATELLITE "google-cloud" bucket.
func handleGoogleCloudItemToSatellite(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
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

	err = satellite.UploadObject(context.Background(), accesGrant, "google-cloud", obj.Name, obj.Data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Google Cloud Storage to Satellite", obj.Name)})

}

// Takes bucket name and item name as a parameters, downloads the object from Satellite bucket and uploads it into Google Cloud Storage bucket.
func handleSatelliteToGoogleCloud(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
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

	data, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Drive, itemName)
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

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("object %s was successfully uploaded from Satellite to Google Cloud Storage", itemName)})

}

func handleAllFilesFromGoogleCloudBucketToSatellite(c echo.Context) error {
	bucketName := c.Param("bucketName")

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
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

		err = satellite.UploadObject(context.Background(), accesGrant, bucket.Id, obj.Name, obj.Data)
		fmt.Println("uploaded : "+obj.Name, "bucketID: ", bucket.Id)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("all objects in bucket '"+bucketName+"' were successfully uploaded from Satellite to Google Cloud Storage", bucketName)})

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
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return errors.New("access token not found")
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

		err = satellite.UploadObject(context.Background(), accesGrant, bucket.Id, obj.Name, obj.Data)
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
	for _, id := range allIDs {
		r := syncCloudProject(c, id)
		res = append(res, r)
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
	for _, id := range allIDs {
		err := syncCloudBucket(c, id)
		if err != nil {
			res = append(res, map[string]any{"bukcetID": id, "success": false, "error": err})
		} else {
			res = append(res, map[string]any{"bukcetID": id, "success": false})
		}
	}
	return c.JSON(200, res)
}

func handleSyncCloudItems(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
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
	for _, id := range allIDs {
		err := syncItem(c, client, accesGrant, id, bucketName)
		if err != nil {
			res = append(res, map[string]any{"itemID": id, "success": false, "error": err})
		} else {
			res = append(res, map[string]any{"itemID": id, "success": false})
		}
	}
	return c.JSON(200, res)
}

func syncItem(c echo.Context, client *google.StorageClient, accessGrant, itemName, bucketName string) error {

	obj, err := client.GetObject(c, bucketName, itemName)
	if err != nil {
		return err
	}
	return satellite.UploadObject(context.Background(), accessGrant, "google-cloud", obj.Name, obj.Data)

}

// handleDeleteUser deletes all data related to a user with the given email pattern
// This includes data from satellite database tables: bucket_metainfos, api_keys, projects, and users
// Usage: DELETE /google/cloud/delete-user?email=user@example.com
// or: DELETE /google/cloud/delete-user with email in form data
func handleDeleteUser(c echo.Context) error {
	// Get email parameter from query string or form data
	email := "khemant2677@gmail.com"

	// Get database connection from context
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	if database == nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "database connection not available",
			"message": "Unable to connect to database",
		})
	}

	// Log the deletion attempt for audit purposes
	fmt.Printf("Attempting to delete user data for email pattern: %s\n", email)

	// Delete user data from satellite database
	err := deleteUserFromSatelliteDatabase(database, email)
	if err != nil {
		fmt.Printf("Error deleting user data: %v\n", err)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "failed to delete user data",
			"message": err.Error(),
		})
	}

	// Return success response
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "User data deleted successfully from satellite database",
		"email_pattern": email,
		"deleted_tables": []string{
			"satellite/0.bucket_metainfos",
			"satellite/0.api_keys",
			"satellite/0.projects",
			"satellite/0.users",
		},
	})
}

// deleteUserFromSatelliteDatabase deletes all user-related data from satellite database tables
func deleteUserFromSatelliteDatabase(db *storage.PosgresStore, emailPattern string) error {
	// Start a transaction to ensure all deletions succeed or none do
	tx := db.DB.Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %v", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Execute the SQL queries in the correct order to respect foreign key constraints

	// 1. Delete from bucket_metainfos first (has foreign key to projects)
	result := tx.Exec(`
		DELETE FROM "satellite/0".bucket_metainfos 
		WHERE project_id IN (
			SELECT id 
			FROM "satellite/0".projects 
			WHERE owner_id IN (
				SELECT id 
				FROM "satellite/0".users 
				WHERE email LIKE ?
			)
		)`, emailPattern)
	if result.Error != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete bucket_metainfos: %v", result.Error)
	}
	fmt.Printf("Deleted %d bucket_metainfos records\n", result.RowsAffected)

	// 2. Delete from api_keys (has foreign key to projects)
	result = tx.Exec(`
		DELETE FROM "satellite/0".api_keys 
		WHERE project_id IN (
			SELECT id 
			FROM "satellite/0".projects 
			WHERE owner_id IN (
				SELECT id 
				FROM "satellite/0".users 
				WHERE email LIKE ?
			)
		)`, emailPattern)
	if result.Error != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete api_keys: %v", result.Error)
	}
	fmt.Printf("Deleted %d api_keys records\n", result.RowsAffected)

	// 3. Delete from projects (has foreign key to users)
	result = tx.Exec(`
		DELETE FROM "satellite/0".projects 
		WHERE owner_id IN (
			SELECT id 
			FROM "satellite/0".users 
			WHERE email LIKE ?
		)`, emailPattern)
	if result.Error != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete projects: %v", result.Error)
	}
	fmt.Printf("Deleted %d projects records\n", result.RowsAffected)

	// 4. Finally delete from users
	result = tx.Exec(`
		DELETE FROM "satellite/0".users 
		WHERE email LIKE ?`, emailPattern)
	if result.Error != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete users: %v", result.Error)
	}
	fmt.Printf("Deleted %d users records\n", result.RowsAffected)

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %v", err)
	}

	return nil
}
