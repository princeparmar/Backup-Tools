package googledrive

import (
	"context"
	"log"
	"net/http"
	"os"
	"storj-integrations/storage"
	"storj-integrations/utils"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

const dbContextKey = "__db"

func Autentificate(c echo.Context) error {
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	code := c.FormValue("code")
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, drive.DriveScope)
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
			database.WriteGoogleAuthToken(cookieNew.Name, tok)

			c.SetCookie(cookieNew)
		}
	} else {
		return c.String(http.StatusOK, "you are already authenticated!")
	}

	return c.String(http.StatusOK, "authentication is successful!")
}
