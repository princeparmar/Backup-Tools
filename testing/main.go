package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/StorX2-0/Backup-Tools/logger"
	"github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v2"
	"google.golang.org/api/gmail/v1"
)

var (
	googleOauthConfig *oauth2.Config
	oauthStateString  = "pseudo-random"
)

func init() {
	googleOauthConfig = &oauth2.Config{
		RedirectURL:  "http://localhost:8080/callback",
		ClientID:     "123",
		ClientSecret: "123",
		Scopes:       []string{"openid", "email", drive.DriveScope, photoslibrary.PhotoslibraryScope, gmail.MailGoogleComScope},
		Endpoint:     google.Endpoint,
	}
}

func handleMain(w http.ResponseWriter, r *http.Request) {
	var htmlIndex = `<html><body><a href="/login">Google Log In</a></body></html>`
	fmt.Fprintf(w, htmlIndex)
}

func handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	url := googleOauthConfig.AuthCodeURL(oauthStateString)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("state")
	if state != oauthStateString {
		logger.Info(context.Background(), "invalid oauth state")
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	code := r.FormValue("code")
	token, err := googleOauthConfig.Exchange(context.Background(), code)
	if err != nil {
		logger.Info(context.Background(), "code exchange failed: ", logger.ErrorField(err))
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	tokenJSON, err := json.Marshal(token)
	if err != nil {
		logger.Info(context.Background(), "failed to marshal token: ", logger.ErrorField(err))
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(tokenJSON)
}

func main() {
	http.HandleFunc("/", handleMain)
	http.HandleFunc("/login", handleGoogleLogin)
	http.HandleFunc("/callback", handleGoogleCallback)
	fmt.Println(http.ListenAndServe(":8080", nil))
}
