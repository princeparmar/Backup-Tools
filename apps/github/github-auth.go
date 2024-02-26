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
	"storj-integrations/utils"

	"github.com/google/go-github/v53/github"
	"github.com/labstack/echo/v4"
)

func AuthenticateGithub(c echo.Context) error {
	// Get the environment variable
	githubClientID := getGithubClientID()

	// Create the dynamic redirect URL for login
	redirectURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&scope=repo",
		githubClientID,
	)
	return c.Redirect(http.StatusMovedPermanently, redirectURL)
}

type Github struct {
	Client *github.Client
}

func NewGithubClient(c echo.Context) (Github, error) {
	cookieToken, err := c.Cookie("github-auth")
	if err != nil {
		return Github{}, err
	}
	accessToken := cookieToken.Value
	clnt := github.NewTokenClient(context.Background(), accessToken)
	return Github{Client: clnt}, nil
}

func (gh Github) ListReps(accessToken string) ([]*github.Repository, error) {
	url := "https://api.github.com/user/repos"

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Add("Authorization", "bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	var reps []*github.Repository
	err = json.Unmarshal(data, &reps)
	if err != nil {
		return nil, err
	}

	return reps, nil
}

func (gh Github) DownloadRepositoryToCache(owner, repo, accessToken string) (string, error) {
	redirectURL := "https://api.github.com/repos/" + owner + "/" + repo + "/zipball"

	req, _ := http.NewRequest(http.MethodGet, redirectURL, nil)
	req.Header.Add("Authorization", "bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}

	dirPath := filepath.Join("./cache", utils.CreateUserTempCacheFolder())
	path := filepath.Join(dirPath, repo+".zip")
	os.Mkdir(dirPath, 0777)

	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}
	file.Close()
	return path, nil
}

func (gh Github) UploadFileToGithub(owner, repo, filePath string, data []byte) error {
	msg := fmt.Sprintf("file %s recovered from backup", filePath)
	_, _, err := gh.Client.Repositories.CreateFile(context.Background(), owner, repo, filePath, &github.RepositoryContentFileOptions{
		Message: &msg,
		Content: data,
	})
	if err != nil {
		return err
	}
	return nil
}

func (gh Github) GetAuthenticatedUserName() (string, error) {
	user, _, err := gh.Client.Users.Get(context.Background(), "")
	if err != nil {
		return "", err
	}
	return *user.Name, nil
}

func GetGithubAccessToken(code string) string {

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
		log.Panic("Request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Get the response
	resp, resperr := http.DefaultClient.Do(req)
	if resperr != nil {
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
