package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/StorX2-0/Backup-Tools/apps/aws"
	"github.com/StorX2-0/Backup-Tools/apps/dropbox"
	gthb "github.com/StorX2-0/Backup-Tools/apps/github"
	"github.com/StorX2-0/Backup-Tools/apps/quickbooks"
	"github.com/StorX2-0/Backup-Tools/apps/shopify"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"

	"github.com/labstack/echo/v4"
)

// <<<<<------------ DROPBOX ------------>>>>>

func handleDropboxToSatellite(c echo.Context) error {
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

func handleSatelliteToDropbox(c echo.Context) error {
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

// <<<<<------------ AWS S3 ------------>>>>>

func handleListAWSs3BucketFiles(c echo.Context) error {
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

func handleS3toSatellite(c echo.Context) error {
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
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
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

func handleSatelliteToS3(c echo.Context) error {
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
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Write(data)
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

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "you have been successfuly authenticated to github"})
}

func handleListRepos(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}
	reps, err := gh.ListReps(accessToken.Value)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
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
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}
	owner := c.QueryParam("owner")
	if owner == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "owner is now specified"})
	}
	repo := c.QueryParam("repo")
	if repo == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "repo name is now specified"})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	repoPath, err := gh.DownloadRepositoryToCache(owner, repo, accessToken.Value)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	dir, _ := filepath.Split(repoPath)
	defer os.RemoveAll(dir)

	return c.File(repoPath)
}

func handleGithubRepositoryToSatellite(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	owner := c.QueryParam("owner")
	if owner == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "owner is now specified"})
	}
	repo := c.QueryParam("repo")
	if repo == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "repo name is now specified"})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	repoPath, err := gh.DownloadRepositoryToCache(owner, repo, accessToken.Value)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	dir, repoName := filepath.Split(repoPath)
	defer os.RemoveAll(dir)
	file, err := os.Open(repoPath)
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

	err = satellite.UploadObject(context.Background(), accesGrant, "github", repoName, data)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Close()

	return c.JSON(http.StatusOK, map[string]interface{}{"message": fmt.Sprintf("repo %s was successfully uploaded from Github to Satellite", repoName)})
}

func handleRepositoryFromSatelliteToGithub(c echo.Context) error {
	accessToken, err := c.Cookie("github-auth")
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	repo := c.QueryParam("repo")
	if repo == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "repo name is now specified"})
	}

	repoData, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Github, repo)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"message": "error downloading object from Satellite" + err.Error(), "error": err.Error()})
	}
	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	basePath := filepath.Join(dirPath, repo+".zip")
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(basePath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Write(repoData)
	file.Close()

	defer os.RemoveAll(dirPath)

	unzipPath := filepath.Join(dirPath, "unarchived")
	os.Mkdir(unzipPath, 0777)

	err = utils.Unzip(basePath, unzipPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	gh, err := gthb.NewGithubClient(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
	}
	username, err := gh.GetAuthenticatedUserName()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
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
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			gitFileData, err := io.ReadAll(gitFile)
			if err != nil {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error": err.Error(),
				})
			}
			gh.UploadFileToGithub(username, repo, path, gitFileData)
			gitFile.Close()
		}
		return nil
	})
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": "repository " + repo + " restored to Github from Satellite"})
}

// <<<<<<<--------- SHOPIFY --------->>>>>>>

func createShopifyCleint(c echo.Context, shopname string) *shopify.ShopifyClient {
	cookieToken, err := c.Cookie("shopify-auth")
	if err != nil {
		c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
		return nil
	}
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	token, err := database.ReadShopifyAuthToken(cookieToken.Value)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Error reading token from database",
			"error":   err.Error(),
		})
		return nil
	}
	client, err := shopify.CreateClient(token, shopname)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Error creating shopify client",
			"error":   err.Error(),
		})
		return nil
	}
	return client
}

func handleShopifyProductsToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	products, err := client.GetProducts()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting products", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Shopify, "shopify.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, product := range products {
		err = db.WriteProductsToDB(&product)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with products data was successfully uploaded"})
}

func handleShopifyCustomersToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	customers, err := client.GetCustomers()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting customers", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Shopify, "shopify.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, customer := range customers {
		err = db.WriteCustomersToDB(&customer)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with customers data was successfully uploaded"})

}

func handleShopifyOrdersToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	orders, err := client.GetOrders()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting orders", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Shopify, "shopify.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, order := range orders {
		err = db.WriteOrdersToDB(&order)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with orders data was successfully uploaded"})
}

// Create an oauth-authorize url for the app and redirect to it.
func handleShopifyAuth(c echo.Context) error {
	shopName := c.QueryParam("shop")
	state := c.QueryParam("state")

	authUrl, _ := shopify.ShopifyInitApp.App.AuthorizeUrl(shopName, state)

	return c.Redirect(http.StatusFound, authUrl)
}

func handleShopifyAuthRedirect(c echo.Context) error {
	// Check that the callback signature is valid
	if ok, err := shopify.ShopifyInitApp.App.VerifyAuthorizationURL(c.Request().URL); !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Signature",
			"error":   err.Error(),
		})
	}
	query := c.Request().URL.Query()
	shopName := query.Get("shop")
	code := query.Get("code")
	token, err := shopify.ShopifyInitApp.App.GetAccessToken(c.Request().Context(), shopName, code)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Signature",
			"error":   err.Error(),
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

	cookieNew := new(http.Cookie)
	cookieNew.Name = "shopify-auth"
	cookieNew.Value = utils.RandStringRunes(50)
	database.WriteShopifyAuthToken(cookieNew.Value, token)

	c.SetCookie(cookieNew)

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Authorized!"})
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
// 		c.JSON(http.StatusForbidden, map[string]interface{}{ "message":  err.Error(), "error": err.Error()})
// 	}
// }

func handleQuickbooksCustomersToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, _ := quickbooks.CreateClient()
	customers, err := client.Client.FetchCustomers()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.RestoreBucket_Quickbooks, "quickbooks.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range customers {
		err = db.WriteCustomersToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "customers are successfully uploaded from quickbooks to satellite"})
}

func handleQuickbooksItemsToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, _ := quickbooks.CreateClient()
	items, err := client.Client.FetchItems()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.RestoreBucket_Quickbooks, "quickbooks.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range items {
		err = db.WriteItemsToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "items are successfully uploaded from quickbooks to satellite"})
}

func handleQuickbooksInvoicesToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, _ := quickbooks.CreateClient()
	invoices, err := client.Client.FetchInvoices()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.RestoreBucket_Quickbooks, "quickbooks.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := os.Create(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	db, err := storage.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range invoices {
		err = db.WriteInvoicesToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "invoices are successfully uploaded from quickbooks to satellite"})
}
