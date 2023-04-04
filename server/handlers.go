package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"storj-integrations/apps/aws"
	"storj-integrations/apps/dropbox"
	gthb "storj-integrations/apps/github"
	google "storj-integrations/apps/google"
	"storj-integrations/apps/quickbooks"
	"storj-integrations/apps/shopify"
	"storj-integrations/storage"
	"storj-integrations/storj"
	"storj-integrations/utils"

	"github.com/labstack/echo/v4"
)

// <<<<<------------ GOOGLE DRIVE ------------>>>>>

func handleGetGoogleDriveFileNames(c echo.Context) error {
	fileNames, err := google.GetFileNames(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	return c.JSON(http.StatusOK, fileNames)
}

// Sends file from Google Drive to Storj
func handleSendFileFromGoogleDriveToStorj(c echo.Context) error {
	id := c.Param("ID")

	name, data, err := google.GetFile(c, id)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "Storj is unauthenticated",
		})
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "google-drive", name, data)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Google Drive to Storj", name),
	})
}

func handleSendAllFilesFromGoogleDriveToStorj(c echo.Context) error {
	resp, err := google.GetFileNames(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}

	for _, f := range resp {
		name, data, err := google.GetFile(c, f.ID)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
		accesGrant, err := c.Cookie("storj_access_token")
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "Storj is unauthenticated",
			})
		}

		err = storj.UploadObject(context.Background(), accesGrant.Value, "google-drive", name, data)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error": fmt.Sprintf("failed to upload file to Storj: %v", err),
			})
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "all files were successfully uploaded from Google Drive to Storj",
	})
}

// Sends file from Storj to Google Drive
func handleSendFileFromStorjToGoogleDrive(c echo.Context) error {
	name := c.Param("name")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "Storj is unauthenticated",
		})
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant.Value, "google-drive", name)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": fmt.Sprintf("failed to download object from Storj: %v", err),
		})
	}

	err = google.UploadFile(c, name, data)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error": "token expired",
			})
		} else {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "failed to retrieve file from Google Drive",
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("file %s was successfully uploaded from Storj to Google Drive", name),
	})
}

// <<<<<------------ GOOGLE PHOTOS ------------>>>>>

type AlbumsJSON struct {
	Title string `json:"album_title"`
	ID    string `json:"album_id"`
	Items string `json:"items_count"`
}

// Shows list of user's Google Photos albums.
func handleListGPhotosAlbums(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	albs, err := client.ListAlbums(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	var photosListJSON []*AlbumsJSON
	for _, v := range albs {
		photosListJSON = append(photosListJSON, &AlbumsJSON{
			Title: v.Title,
			ID:    v.ID,
			Items: v.MediaItemsCount,
		})
	}

	return c.JSON(http.StatusOK, photosListJSON)

}

type PhotosJSON struct {
	Name string `json:"file_name"`
	ID   string `json:"file_id"`
}

// Shows list of user's Google Photos items in given album.
func handleListPhotosInAlbum(c echo.Context) error {
	id := c.Param("ID")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	files, err := client.ListFilesFromAlbum(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	var photosRespJSON []*PhotosJSON
	for _, v := range files {
		photosRespJSON = append(photosRespJSON, &PhotosJSON{
			Name: v.Filename,
			ID:   v.ID,
		})
	}

	return c.JSON(http.StatusOK, photosRespJSON)
}

// Sends photo item from Storj to Google Photos.
func handleSendFileFromStorjToGooglePhotos(c echo.Context) error {
	name := c.Param("name")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant.Value, "google-photos", name)
	if err != nil {
		return c.String(http.StatusForbidden, "error downloading object from Storj"+err.Error())
	}

	path := filepath.Join("./cache", utils.CreateUserTempCacheFolder(), name)
	file, err := os.Create(path)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	file.Write(data)
	file.Close()
	defer os.Remove(path)

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	err = client.UploadFileToGPhotos(c, name, "Storj Album")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "file "+name+" was successfully uploaded from Storj to Google Photos")
}

// Sends photo item from Google Photos to Storj.
func handleSendFileFromGooglePhotosToStorj(c echo.Context) error {
	id := c.Param("ID")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	item, err := client.GetPhoto(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	resp, err := http.Get(item.BaseURL)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "google-photos", item.Filename, body)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "file "+item.Filename+" was successfully uploaded from Google Photos to Storj")
}

func handleSendAllFilesFromGooglePhotosToStorj(c echo.Context) error {
	id := c.Param("ID")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	files, err := client.ListFilesFromAlbum(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	var photosRespJSON []*PhotosJSON
	for _, v := range files {
		photosRespJSON = append(photosRespJSON, &PhotosJSON{
			Name: v.Filename,
			ID:   v.ID,
		})
	}
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	for _, p := range photosRespJSON {

		item, err := client.GetPhoto(c, p.ID)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		resp, err := http.Get(item.BaseURL)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}

		err = storj.UploadObject(context.Background(), accesGrant.Value, "google-photos", item.Filename, body)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	return c.String(http.StatusOK, "all photos from album were successfully uploaded from Google Photos to Storj")
}

// <<<<<------------ GMAIL ------------>>>>>

type ThreadJSON struct {
	ID      string `json:"thread_id"`
	Snippet string `json:"snippet"`
}

// Fetches user threads, returns their IDs and snippets.
func handleGmailGetThreads(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	threads, err := GmailClient.GetUserThreads("")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// TODO: implement next page token (now only first page is avialable)

	var jsonResp []*ThreadJSON
	for _, v := range threads.Threads {
		jsonResp = append(jsonResp, &ThreadJSON{
			ID:      v.ID,
			Snippet: v.Snippet,
		})
	}
	return c.JSON(http.StatusOK, jsonResp)
}

type MessageListJSON struct {
	ID       string `json:"message_id"`
	ThreadID string `json:"thread_id"`
}

// Fetches user messages, returns their ID's and threat's IDs.
func handleGmailGetMessages(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	msgs, err := GmailClient.GetUserMessages("")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// TODO: implement next page token (now only first page is avialable)

	var jsonMessages []*MessageListJSON
	for _, v := range msgs.Messages {
		jsonMessages = append(jsonMessages, &MessageListJSON{
			ID:       v.ID,
			ThreadID: v.ThreadID,
		})
	}
	return c.JSON(http.StatusOK, jsonMessages)
}

// Returns Gmail message in JSON format.
func handleGmailGetMessage(c echo.Context) error {
	id := c.Param("ID")

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.JSON(http.StatusOK, msg)
}

// Fetches message from Gmail by given ID as a parameter and writes it into SQLite Database in Storj.
// If there's no database yet - creates one.
func handleGmailMessageToStorj(c echo.Context) error {
	id := c.Param("ID")
	accesGrant, err := c.Cookie("storj_access_token")

	// FETCH THE EMAIL TO GOLANG STRUCT

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	msgToSave := storage.GmailMessageSQL{
		ID:      msg.ID,
		Date:    msg.Date,
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Body:    msg.Body,
	}

	// SAVE ATTACHMENTS TO THE STORJ BUCKET AND WRITE THEIR NAMES TO STRUCT

	if len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			err = storj.UploadObject(context.Background(), accesGrant.Value, "gmail", att.FileName, att.Data)
			if err != nil {
				return c.String(http.StatusForbidden, err.Error())
			}
			msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
		}
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToEmailDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// WRITE ALL EMAILS TO THE DATABASE LOCALLY

	err = db.WriteEmailToDB(&msgToSave)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "gmail", "gmails.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "Email was successfully uploaded")
}

func handleGetGmailDBFromStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")

	// Copy file from storj to local cache if everything's fine.
	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "gmail", "gmails.db")
	if err != nil {
		return c.String(http.StatusForbidden, "no emails saved in Storj database")
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/gmails.db"

	dbFile, err := os.Create(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	_, err = dbFile.Write(byteDB)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete db from cache after user get's it.
	defer os.Remove(userCacheDBPath)

	return c.Attachment(userCacheDBPath, "gmails.db")
}

// <<<<<------------ GOOGLE CLOUD STORAGE ------------>>>>>

// Takes Google Cloud project name as a parameter, returns JSON responce with all the buckets in this project.
func handleStorageListBuckets(c echo.Context) error {
	projectName := c.Param("projectName")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	bucketsJSON, err := client.ListBucketsJSON(c, projectName)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	return c.JSON(http.StatusOK, bucketsJSON)
}

// Takes Google Cloud bucket name as a parameter, returns JSON responce with all the items in this bucket.
func handleStorageListObjects(c echo.Context) error {
	bucketName := c.Param("bucketName")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	objects, err := client.ListObjectsInBucketJSON(c, bucketName)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.JSON(http.StatusOK, objects)

}

// Takes bucket name and item name as a parameters, downloads the object from Google Cloud Storage and uploads it into Storj "google-cloud" bucket.
func handleGoogleCloudItemToStorj(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant, err := c.Cookie("storj_access_token")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	obj, err := client.GetObject(c, bucketName, itemName)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "google-cloud", obj.Name, obj.Data)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	return c.String(http.StatusOK, fmt.Sprintf("object %s was successfully uploaded from Google Cloud Storage to Storj", obj.Name))

}

// Takes bucket name and item name as a parameters, downloads the object from Storj bucket and uploads it into Google Cloud Storage bucket.
func handleStorjToGoogleCloud(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant, err := c.Cookie("storj_access_token")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant.Value, "google-cloud", itemName)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	err = client.UploadObject(c, bucketName, &google.StorageObject{
		Name: itemName,
		Data: data,
	})
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, fmt.Sprintf("object %s was successfully uploaded from Storj to Google Cloud Storage", itemName))

}

func handleAllFilesFromGoogleCloudBucketToStorj(c echo.Context) error {
	bucketName := c.Param("bucketName")

	accesGrant, err := c.Cookie("storj_access_token")

	client, err := google.NewGoogleStorageClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	objects, err := client.ListObjectsInBucket(c, bucketName)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	for _, o := range objects.Items {
		obj, err := client.GetObject(c, bucketName, o.Name)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}

		err = storj.UploadObject(context.Background(), accesGrant.Value, "google-cloud", obj.Name, obj.Data)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	return c.String(http.StatusOK, fmt.Sprintf("all objects in bucket '"+bucketName+"' were successfully uploaded from Storj to Google Cloud Storage", bucketName))

}

// <<<<<------------ DROPBOX ------------>>>>>

func handleDropboxToStorj(c echo.Context) error {
	filePath := c.Param("filePath")
	accesGrant, err := c.Cookie("storj_access_token")

	client, err := dropbox.NewDropboxClient()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	file, err := client.DownloadFile("/" + filePath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	data, err := io.ReadAll(file.Data)

	err = storj.UploadObject(context.Background(), accesGrant.Value, "dropbox", file.Name, data)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, fmt.Sprintf("object %s was successfully uploaded from Dropbox to Storj", file.Name))
}

func handleStorjToDropbox(c echo.Context) error {
	filePath := c.Param("filePath")
	accesGrant, err := c.Cookie("storj_access_token")

	objData, err := storj.DownloadObject(context.Background(), accesGrant.Value, "dropbox", filePath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	client, err := dropbox.NewDropboxClient()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	data := bytes.NewReader(objData)
	err = client.UploadFile(data, "/"+filePath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, fmt.Sprintf("object %s was successfully uploaded from Storj to Dropbox", filePath))
}

// <<<<<------------ AWS S3 ------------>>>>>

func handleListAWSs3BucketFiles(c echo.Context) error {
	bucketName := c.Param("bucketName")

	s3sess := aws.ConnectAws()
	data, err := s3sess.ListFiles(bucketName)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, fmt.Sprintf("%+v", data))
}

func handleS3toStorj(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, itemName)
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	defer os.Remove(path)

	s3sess := aws.ConnectAws()
	err = s3sess.DownloadFile(bucketName, itemName, file)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "aws-s3", itemName, data)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, fmt.Sprintf("object %s was successfully uploaded from AWS S3 bucket to Storj", itemName))
}

func handleStorjToS3(c echo.Context) error {
	bucketName := c.Param("bucketName")
	itemName := c.Param("itemName")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant.Value, "aws-s3", itemName)
	if err != nil {
		return c.String(http.StatusForbidden, "error downloading object from Storj"+err.Error())
	}
	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, itemName)
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	file.Write(data)
	file.Close()
	defer os.Remove(path)

	cachedFile, err := os.Open(path)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	s3sess := aws.ConnectAws()
	err = s3sess.UploadFile(bucketName, itemName, cachedFile)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	return c.String(http.StatusOK, fmt.Sprintf("object %s was successfully uploaded from Storj to AWS S3 %s bucket", itemName, bucketName))

}

// <<<<<------------ GITHUB ------------>>>>>

func handleGithubLogin(c echo.Context) error {
	return gthb.AuthenticateGithub(c)
}

func handleGithubCallback(c echo.Context) error {
	code := c.QueryParam("code")

	githubAccessToken := gthb.GetGithubAccessToken(code)
	cookie := new(http.Cookie)
	cookie.Name = "github-auth"
	cookie.Value = githubAccessToken
	c.SetCookie(cookie)

	return c.String(http.StatusOK, "you have been successfuly authenticated to github")
}

func handleListRepos(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}
	reps, err := gh.ListReps(accessToken.Value)
	if err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}
	var repositories []string
	for _, r := range reps {
		repositories = append(repositories, *r.FullName)
	}
	return c.JSON(http.StatusOK, repositories)
}

func handleGetRepository(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}
	owner := c.QueryParam("owner")
	if owner == "" {
		return c.String(http.StatusBadRequest, "owner is now specified")
	}
	repo := c.QueryParam("repo")
	if repo == "" {
		return c.String(http.StatusBadRequest, "repo name is now specified")
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}

	repoPath, err := gh.DownloadRepositoryToCache(owner, repo, accessToken.Value)
	dir, _ := filepath.Split(repoPath)
	defer os.RemoveAll(dir)

	return c.File(repoPath)
}

func handleGithubRepositoryToStorj(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}

	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	owner := c.QueryParam("owner")
	if owner == "" {
		return c.String(http.StatusBadRequest, "owner is now specified")
	}
	repo := c.QueryParam("repo")
	if repo == "" {
		return c.String(http.StatusBadRequest, "repo name is now specified")
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}

	repoPath, err := gh.DownloadRepositoryToCache(owner, repo, accessToken.Value)
	dir, repoName := filepath.Split(repoPath)
	defer os.RemoveAll(dir)
	file, err := os.Open(repoPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "github", repoName, data)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	file.Close()

	return c.String(http.StatusOK, fmt.Sprintf("repo %s was successfully uploaded from Github to Storj", repoName))
}

func handleRepositoryFromStorjToGithub(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}

	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	repo := c.QueryParam("repo")
	if repo == "" {
		return c.String(http.StatusBadRequest, "repo name is now specified")
	}

	repoData, err := storj.DownloadObject(context.Background(), accesGrant.Value, "github", repo)
	if err != nil {
		return c.String(http.StatusForbidden, "error downloading object from Storj"+err.Error())
	}
	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	basePath := filepath.Join(dirPath, repo+".zip")
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(basePath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	file.Write(repoData)
	file.Close()

	defer os.RemoveAll(dirPath)

	unzipPath := filepath.Join(dirPath, "unarchived")
	os.Mkdir(unzipPath, 0777)

	err = utils.Unzip(basePath, unzipPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.String(http.StatusUnauthorized, "UNAUTHENTICATED!")
	}
	username, err := gh.GetAuthenticatedUserName()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	url := "https://api.github.com/user/repos"

	jsonBody := []byte(`{"name": "` + repo + `","private": true,}`)
	bodyReader := bytes.NewReader(jsonBody)

	req, _ := http.NewRequest(http.MethodPost, url, bodyReader)
	req.Header.Add("Authorization", "bearer "+accessToken.Value)

	err = filepath.WalkDir(unzipPath, func(path string, di fs.DirEntry, err error) error {
		if !di.IsDir() {
			gitFile, err := os.Open(path)
			if err != nil {
				return c.String(http.StatusForbidden, err.Error())
			}
			gitFileData, err := io.ReadAll(gitFile)
			if err != nil {
				return c.String(http.StatusForbidden, err.Error())
			}
			gh.UploadFileToGithub(username, repo, path, gitFileData)
			gitFile.Close()
		}
		return nil
	})

	return c.String(http.StatusOK, "repository "+repo+" restored to Github from Storj")
}

// <<<<<<<--------- SHOPIFY --------->>>>>>>

func createShopifyCleint(c echo.Context, shopname string) *shopify.ShopifyClient {
	cookieToken, err := c.Cookie("shopify-auth")
	if err != nil {
		c.String(http.StatusUnauthorized, "Unauthorized")
		return nil
	}
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	token, err := database.ReadShopifyAuthToken(cookieToken.Value)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return nil
	}
	cleint := shopify.CreateClient(token, shopname)
	return cleint
}

func handleShopifyProductsToStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	products, err := client.GetProducts()
	if err != nil {
		return c.String(http.StatusNotFound, "Error getting products")
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "shopify", "shopify.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	for _, product := range products {
		err = db.WriteProductsToDB(&product)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "shopify", "shopify.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "DB with products data was successfully uploaded")
}

func handleShopifyCustomersToStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	customers, err := client.GetCustomers()
	if err != nil {
		return c.String(http.StatusNotFound, "Error getting customers")
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "shopify", "shopify.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	for _, customer := range customers {
		err = db.WriteCustomersToDB(&customer)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "shopify", "shopify.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "DB with customers data was successfully uploaded")

}

func handleShopifyOrdersToStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	orders, err := client.GetOrders()
	if err != nil {
		return c.String(http.StatusNotFound, "Error getting orders")
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "shopify", "shopify.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	for _, order := range orders {
		err = db.WriteOrdersToDB(&order)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "shopify", "shopify.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "DB with orders data was successfully uploaded")
}

// Create an oauth-authorize url for the app and redirect to it.
func handleShopifyAuth(c echo.Context) error {
	shopName := c.QueryParam("shop")
	state := c.QueryParam("state")

	authUrl := shopify.ShopifyInitApp.App.AuthorizeUrl(shopName, state)

	return c.Redirect(http.StatusFound, authUrl)
}

func handleShopifyAuthRedirect(c echo.Context) error {
	// Check that the callback signature is valid
	if ok, err := shopify.ShopifyInitApp.App.VerifyAuthorizationURL(c.Request().URL); !ok {
		return c.String(http.StatusUnauthorized, "Invalid Signature\n"+err.Error())
	}
	query := c.Request().URL.Query()
	shopName := query.Get("shop")
	code := query.Get("code")
	token, err := shopify.ShopifyInitApp.App.GetAccessToken(shopName, code)
	if err != nil {
		return c.String(http.StatusUnauthorized, "Invalid Signature\n"+err.Error())

	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

	cookieNew := new(http.Cookie)
	cookieNew.Name = "shopify-auth"
	cookieNew.Value = utils.RandStringRunes(50)
	database.WriteShopifyAuthToken(cookieNew.Value, token)

	c.SetCookie(cookieNew)

	return c.String(http.StatusOK, "Authorized!")
}

// <<<<<<<--------- QUICKBOOKS --------->>>>>>>

// func loginQuickbooksClient(c echo.Context) *quickbooks.QBClient {
// 	cookieToken, err := c.Cookie("quickbooks-auth")
// 	if err != nil {
// 		c.String(http.StatusUnauthorized, "Unauthorized")
// 		return nil
// 	}
// 	database := c.Get(dbContextKey).(*storage.PosgresStore)
// 	token, err := database.ReadQuickbooksAuthToken(cookieToken.Value)
// 	if err != nil {
// 		c.String(http.StatusBadRequest, err.Error())
// 		return nil
// 	}
// 	client, _ := quickbooks.CreateClient()

// 	return client
// }

// func AuthenticateQuickbooks(c echo.Context) error {
// 	// Get the environment variable
// 	client, _ := quickbooks.CreateClient()

// 	// Create the dynamic redirect URL for login
// 	redirectURL := "https://developer.intuit.com/v2/OAuth2Playground/RedirectUrl"

// 	return c.Redirect(http.StatusMovedPermanently, redirectURL)
// }

// func GetCompanyInfo(c echo.Context) error {
// 	client, _ := quickbooks.CreateClient()
// 	companyInfo, err := client.Client.FetchCompanyInfo()
// 	if err != nil {
// 		c.String(http.StatusForbidden, err.Error())
// 	}
// }

func handleQuickbooksCustomersToStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")

	client, _ := quickbooks.CreateClient()
	customers, err := client.Client.FetchCustomers()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	for _, n := range customers {
		err = db.WriteCustomersToDB(&n)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "customers are successfully uploaded from quickbooks to storj")
}

func handleQuickbooksItemsToStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")

	client, _ := quickbooks.CreateClient()
	items, err := client.Client.FetchItems()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	for _, n := range items {
		err = db.WriteItemsToDB(&n)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "items are successfully uploaded from quickbooks to storj")
}

func handleQuickbooksInvoicesToStorj(c echo.Context) error {
	accesGrant, err := c.Cookie("storj_access_token")

	client, _ := quickbooks.CreateClient()
	invoices, err := client.Client.FetchInvoices()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	for _, n := range invoices {
		err = db.WriteInvoicesToDB(&n)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "invoices are successfully uploaded from quickbooks to storj")
}

// Shows list of user's Google Photos albums.
func handleListGPhotosAlbums(c echo.Context) error {
	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	albs, err := client.ListAlbums(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	strList := ""
	for _, v := range albs {
		strList = strList + "Name: " + v.Title + " ID: " + v.ID + "\n"
	}
	return c.String(http.StatusOK, strList)

}

// Shows list of user's Google Photos items in given album.
func handleListPhotosInAlbum(c echo.Context) error {
	id := c.Param("ID")

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	files, err := client.ListFilesFromAlbum(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	strList := ""
	for _, v := range files {
		strList = strList + "Name: " + v.Filename + " ID: " + v.ID + "\n"
	}
	return c.String(http.StatusOK, strList)
}

// Sends photo item from Storj to Google Photos.
func handleSendFileFromStorjToGooglePhotos(c echo.Context) error {
	name := c.Param("name")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant.Value, "google-photos", name)
	if err != nil {
		return c.String(http.StatusForbidden, "error downloading object from Storj"+err.Error())
	}

	path := filepath.Join("./cache", name)
	file, err := os.Create(path)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	file.Write(data)
	file.Close()
	defer os.Remove(path)

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	err = client.UploadFileToGPhotos(c, name, "Storj Album")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "file "+name+" was successfully uploaded from Storj to Google Photos")
}

// Sends photo item from Google Photos to Storj.
func handleSendFileFromGooglePhotosToStorj(c echo.Context) error {
	id := c.Param("ID")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	client, err := google.NewGPhotosClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	item, err := client.GetPhoto(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	resp, err := http.Get(item.BaseURL)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "google-photos", item.Filename, body)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "file "+item.Filename+" was successfully uploaded from Google Photos to Storj")
}

func handleGmailGetThreads(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	threads, err := GmailClient.GetUserThreads("")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// TODO: implement next page token (now only first page is avialable)
	respStr := ""
	for _, v := range threads.Threads {
		respStr = fmt.Sprintf("%sID: %s Snippet: %s\n", respStr, v.ID, v.Snippet)
	}

	return c.String(http.StatusOK, respStr)
}

func handleGmailGetMessages(c echo.Context) error {
	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	msgs, err := GmailClient.GetUserMessages("")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// TODO: implement next page token (now only first page is avialable)
	respStr := ""
	for _, v := range msgs.Messages {
		respStr = fmt.Sprintf("%sID: %s ThreadID: %s\n", respStr, v.ID, v.ThreadID)
	}

	return c.String(http.StatusOK, respStr)
}

func handleGmailGetMessage(c echo.Context) error {
	id := c.Param("ID")

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	res, _ := json.Marshal(msg)

	return c.String(http.StatusOK, string(res))
}

func handleGmailMessageToStorj(c echo.Context) error {
	id := c.Param("ID")
	accesGrant, err := c.Cookie("storj_access_token")

	// FETCH THE EMAIL TO GOLANG STRUCT

	GmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	msg, err := GmailClient.GetMessage(id)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	msgToSave := storage.GmailMessageSQL{
		ID:      msg.ID,
		Date:    msg.Date,
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Body:    msg.Body,
	}

	// SAVE ATTACHMENTS TO THE STORJ BUCKET AND WRITE THEIR NAMES TO STRUCT

	if len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			err = storj.UploadObject(context.Background(), accesGrant.Value, "gmail", att.FileName, att.Data)
			if err != nil {
				return c.String(http.StatusForbidden, err.Error())
			}
			msgToSave.Attachments = msgToSave.Attachments + "|" + att.FileName
		}
	}

	// CHECK IF EMAIL DATABASE ALREADY EXISTS AND DOWNLOAD IT, IF NOT - CREATE NEW ONE

	dbPath := "./cache/gmails.db"

	byteDB, err := storj.DownloadObject(context.Background(), accesGrant.Value, "gmail", "gmails.db")
	// Copy file from storj to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(dbPath)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.String(http.StatusForbidden, err.Error())
		}
	}

	db, err := storage.ConnectToEmailDB()
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// WRITE ALL EMAILS TO THE DATABASE LOCALLY

	err = db.WriteEmailToDB(&msgToSave)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// DELETE OLD DB COPY FROM STORJ UPLOAD UP TO DATE DB FILE BACK TO STORJ AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(dbPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete old db copy from storj
	err = storj.DeleteObject(context.Background(), accesGrant.Value, "gmail", "gmails.db")
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// upload file to storj
	err = storj.UploadObject(context.Background(), accesGrant.Value, "gmail", "gmails.db", dbByte)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	// delete from local cache copy of database
	err = os.Remove(dbPath)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}

	return c.String(http.StatusOK, "Email was successfully uploaded")
}
