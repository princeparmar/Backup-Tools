package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	gthb "github.com/StorX2-0/Backup-Tools/apps/github"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"io/fs"

	"github.com/labstack/echo/v4"
)

// HandleGithubLogin initiates GitHub authentication
func HandleGithubLogin(c echo.Context) error {
	return gthb.AuthenticateGithub(c)
}

// HandleGithubCallback handles GitHub OAuth callback
func HandleGithubCallback(c echo.Context) error {
	code := c.QueryParam("code")

	githubAccessToken := gthb.GetGithubAccessToken(code)
	cookie := new(http.Cookie)
	cookie.Name = "github-auth"
	cookie.Value = githubAccessToken
	c.SetCookie(cookie)

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "you have been successfuly authenticated to github"})
}

// HandleListRepos lists all repositories for the authenticated user
func HandleListRepos(c echo.Context) error {
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

// HandleGetRepository downloads a specific repository
func HandleGetRepository(c echo.Context) error {
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

// HandleGithubRepositoryToSatellite uploads a GitHub repository to Satellite
func HandleGithubRepositoryToSatellite(c echo.Context) error {
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

// HandleRepositoryFromSatelliteToGithub downloads a repository from Satellite and uploads it to GitHub
func HandleRepositoryFromSatelliteToGithub(c echo.Context) error {
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

	file, err := utils.CreateFile(basePath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	_, err = file.Write(repoData)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	file.Close()

	defer os.RemoveAll(dirPath)

	unzipPath := filepath.Join(dirPath, "unarchived")
	err = os.MkdirAll(unzipPath, 0755)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

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
