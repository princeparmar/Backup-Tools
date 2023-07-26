package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"storj-integrations/apps/aws"
	"storj-integrations/apps/dropbox"
	gthb "storj-integrations/apps/github"
	google "storj-integrations/apps/google"
	"storj-integrations/storage"
	"storj-integrations/storj"
	"storj-integrations/utils"

	"github.com/labstack/echo/v4"
)

// <<<<<------------ GOOGLE DRIVE ------------>>>>>

// Sends file from Google Drive to Storj
func handleSendFileFromGoogleDriveToStorj(c echo.Context) error {
	id := c.Param("ID")

	name, data, err := google.GetFile(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, "error")
	}
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	err = storj.UploadObject(context.Background(), accesGrant.Value, "google-drive", name, data)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	return c.String(http.StatusOK, "file "+name+" was successfully uploaded from Google Drive to Storj")
}

// Sends file from Storj to Google Drive
func handleSendFileFromStorjToGoogleDrive(c echo.Context) error {
	name := c.Param("name")
	accesGrant, err := c.Cookie("storj_access_token")
	if err != nil {
		return c.String(http.StatusForbidden, "storj is unauthenticated")
	}

	data, err := storj.DownloadObject(context.Background(), accesGrant.Value, "google-drive", name)
	if err != nil {
		return c.String(http.StatusForbidden, "error downloading object from Storj"+err.Error())
	}

	err = google.UploadFile(c, name, data)
	if err != nil {
		return c.String(http.StatusForbidden, "error uploading file to Google Drive")
	}

	return c.String(http.StatusOK, "file "+name+" was successfully uploaded from Storj to Google Drive")
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
	path := filepath.Join(dirPath, repo+".zip")
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return c.String(http.StatusForbidden, err.Error())
	}
	file.Write(repoData)
	file.Close()
	dir, _ := filepath.Split(path)
	defer os.RemoveAll(dir)

	url := "https://api.github.com/user/repos"

	jsonBody := []byte(`{"name": "` + repo + `","private": true,}`)
	bodyReader := bytes.NewReader(jsonBody)

	req, _ := http.NewRequest(http.MethodPost, url, bodyReader)
	req.Header.Add("Authorization", "bearer "+accessToken.Value)

	// TODO Unzip archived repo
	// TODO upload all files to github repo

	return c.String(http.StatusOK, "work in progress")
}
