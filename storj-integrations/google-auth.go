package main
/*
import (
	"context"
	"log"
	"net/http"
	"os"
	"storj-integrations/storage"
	"storj-integrations/utils"

	"github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
)

// for middleware database purposes
const dbContextKey = "__db"

// Google authentication module, checks if you have auth token in database, if not - redirects to Google auth page.
func Autentificate(c echo.Context) error {
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	code := c.FormValue("code")
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, drive.DriveScope, photoslibrary.PhotoslibraryScope, gmail.MailGoogleComScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}
	_, err = c.Cookie("google-auth")
	if err != nil {
		if code == "" {
			authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
			c.Redirect(http.StatusTemporaryRedirect, authURL)

		} else {
			tok, err := config.Exchange(context.TODO(), code)
			if err != nil {
				log.Fatalf("Unable to retrieve token from web %v", err)
			}
			cookieNew := new(http.Cookie)
			cookieNew.Name = "google-auth"
			cookieNew.Value = utils.RandStringRunes(50)
			database.WriteGoogleAuthToken(cookieNew.Value, tok)

			c.SetCookie(cookieNew)

			// Redirect to frontend application
			frontendURL := os.Getenv("FRONTEND_URL") // Add Frontend URL for redirect to file .env
			return c.Redirect(http.StatusTemporaryRedirect, frontendURL)
		}
	} else {
		return c.String(http.StatusAccepted, "you are already authenticated!") // if code 202 - means already authentificated
	}

	frontendURL := os.Getenv("FRONTEND_URL") // Add Frontend URL for redirect to file .env
	return c.Redirect(http.StatusTemporaryRedirect, frontendURL)
}
*/