package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"

	"github.com/google/go-github/v53/github"
	"github.com/labstack/echo/v4"
)

func AuthenticateGithub(c echo.Context) error {
	start := time.Now()

	// Get the environment variable
	githubClientID := getGithubClientID()

	// Create the dynamic redirect URL for login
	redirectURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&scope=repo",
		githubClientID,
	)

	duration := time.Since(start)
	prometheus.RecordTimer("github_auth_redirect_duration", duration, "service", "github")
	prometheus.RecordCounter("github_auth_redirect_total", 1, "service", "github", "status", "success")

	return c.Redirect(http.StatusMovedPermanently, redirectURL)
}

type Github struct {
	Client *github.Client
}

func NewGithubClient(c echo.Context) (Github, error) {
	start := time.Now()

	cookieToken, err := c.Cookie("github-auth")
	if err != nil {
		prometheus.RecordError("github_client_creation_failed", "github")
		return Github{}, err
	}
	accessToken := cookieToken.Value
	clnt := github.NewTokenClient(context.Background(), accessToken)

	duration := time.Since(start)
	prometheus.RecordTimer("github_client_creation_duration", duration, "service", "github")
	prometheus.RecordCounter("github_client_creation_total", 1, "service", "github", "status", "success")

	return Github{Client: clnt}, nil
}

func (gh Github) ListReps(accessToken string) ([]*github.Repository, error) {
	start := time.Now()

	url := "https://api.github.com/user/repos"

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Add("Authorization", "bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		prometheus.RecordError("github_list_repos_failed", "github")
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		prometheus.RecordError("github_read_response_failed", "github")
		return nil, err
	}
	var reps []*github.Repository
	err = json.Unmarshal(data, &reps)
	if err != nil {
		prometheus.RecordError("github_unmarshal_failed", "github")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("github_list_repos_duration", duration, "service", "github")
	prometheus.RecordCounter("github_list_repos_total", 1, "service", "github", "status", "success")
	prometheus.RecordCounter("github_repos_listed_total", int64(len(reps)), "service", "github")

	return reps, nil
}

func (gh Github) DownloadRepositoryToCache(owner, repo, accessToken string) (string, error) {
	start := time.Now()

	redirectURL := "https://api.github.com/repos/" + owner + "/" + repo + "/zipball"

	req, _ := http.NewRequest(http.MethodGet, redirectURL, nil)
	req.Header.Add("Authorization", "bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		prometheus.RecordError("github_download_repo_failed", "github")
		return "", err
	}

	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, repo+".zip")

	file, err := utils.CreateFile(path)
	if err != nil {
		prometheus.RecordError("github_create_file_failed", "github")
		return "", err
	}
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		prometheus.RecordError("github_copy_file_failed", "github")
		return "", err
	}
	file.Close()

	duration := time.Since(start)
	prometheus.RecordTimer("github_download_repo_duration", duration, "owner", owner, "repo", repo)
	prometheus.RecordCounter("github_download_repo_total", 1, "owner", owner, "repo", repo, "status", "success")
	prometheus.RecordCounter("github_repos_downloaded_total", 1, "owner", owner, "repo", repo)

	return path, nil
}

func (gh Github) UploadFileToGithub(owner, repo, filePath string, data []byte) error {
	start := time.Now()

	msg := fmt.Sprintf("file %s recovered from backup", filePath)
	_, _, err := gh.Client.Repositories.CreateFile(context.Background(), owner, repo, filePath, &github.RepositoryContentFileOptions{
		Message: &msg,
		Content: data,
	})
	if err != nil {
		prometheus.RecordError("github_upload_file_failed", "github")
		return err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("github_upload_file_duration", duration, "owner", owner, "repo", repo)
	prometheus.RecordCounter("github_upload_file_total", 1, "owner", owner, "repo", repo, "status", "success")
	prometheus.RecordCounter("github_files_uploaded_total", 1, "owner", owner, "repo", repo)

	return nil
}

func (gh Github) GetAuthenticatedUserName() (string, error) {
	start := time.Now()

	user, _, err := gh.Client.Users.Get(context.Background(), "")
	if err != nil {
		prometheus.RecordError("github_get_user_failed", "github")
		return "", err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("github_get_user_duration", duration, "service", "github")
	prometheus.RecordCounter("github_get_user_total", 1, "service", "github", "status", "success")

	return *user.Name, nil
}

func GetGithubAccessToken(code string) string {
	start := time.Now()

	clientID := getGithubClientID()
	clientSecret := getGithubClientSecret()

	// Set us the request body as JSON
	requestBodyMap := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
	}
	requestJSON, _ := json.Marshal(requestBodyMap)

	// POST request to set URL
	req, reqerr := http.NewRequest(
		"POST",
		"https://github.com/login/oauth/access_token",
		bytes.NewBuffer(requestJSON),
	)
	if reqerr != nil {
		prometheus.RecordError("github_token_request_creation_failed", "github")
		log.Panic("Request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Get the response
	resp, resperr := http.DefaultClient.Do(req)
	if resperr != nil {
		prometheus.RecordError("github_token_request_failed", "github")
		log.Panic("Request failed")
	}

	// Response body converted to stringified JSON
	respbody, _ := ioutil.ReadAll(resp.Body)

	// Represents the response received from Github
	type githubAccessTokenResponse struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}

	// Convert stringified JSON to a struct object of type githubAccessTokenResponse
	var ghresp githubAccessTokenResponse
	json.Unmarshal(respbody, &ghresp)

	duration := time.Since(start)
	prometheus.RecordTimer("github_token_exchange_duration", duration, "service", "github")
	prometheus.RecordCounter("github_token_exchange_total", 1, "service", "github", "status", "success")

	// Return the access token (as the rest of the
	// details are relatively unnecessary for us)
	return ghresp.AccessToken
}

// utility function
func getGithubClientID() string {

	githubClientID, exists := os.LookupEnv("GITHUB_CLIENT")
	if !exists {
		log.Fatal("Github Client ID not defined in .env file")
	}

	return githubClientID
}

// utility function
func getGithubClientSecret() string {

	githubClientSecret, exists := os.LookupEnv("GITHUB_SECRET")
	if !exists {
		log.Fatal("Github Client ID not defined in .env file")
	}

	return githubClientSecret
}
