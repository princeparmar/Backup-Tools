package google

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"storj-integrations/storage"
	"storj-integrations/utils"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/gphotosuploader/googlemirror/api/photoslibrary/v1"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
)

// for middleware database purposes
const dbContextKey = "__db"

type CustomClaims struct {
	GoogleAuthToken string `json:"google_token"`
	// TODO: add more tokens
	Service2Token string `json:"service2_token"`
	jwt.StandardClaims
}

var (
	jwtSecretKey    = "your-secret-key"
	tokenExpiration = time.Now().Add(24 * time.Hour) // Example: expires in 24 hours
)

func CreateJWToken(googleToken string) string {
	// Create the claims
	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		CustomClaims{
			GoogleAuthToken: googleToken,
			StandardClaims: jwt.StandardClaims{
				ExpiresAt: tokenExpiration.Unix(),
			},
		},
	)

	// Sign the token with the secret key and get the complete, encoded token as a string
	tokenString, err := token.SignedString([]byte(jwtSecretKey))
	if err != nil {
		fmt.Println("Error generating token:", err)
		return ""
	}

	return tokenString
}

func GetGoogleTokenFromJWT(c echo.Context) (string, error) {
	tokenString := c.Request().Header.Get("Authorization")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Check the signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte("your-secret-key"), nil
	})
	if err != nil {
		log.Print("Error parsing token:", err)
		return "", err
	}

	// Check if the token is valid
	if token.Valid {
		// Extract claims
		claims, ok := token.Claims.(CustomClaims)
		if !ok {
			log.Print("Invalid token claims")
			return "", err
		}

		// Extract specific information from claims
		googleAuth := claims.GoogleAuthToken

		// Output extracted informationr
		return googleAuth, nil
	} else {
		log.Print(err)
		return "", err
	}
}

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

	var redirectAddr = os.Getenv("FRONTEND_URL") // Add Frontend URL for redirect to file .env

	fmt.Println(err.Error())
	if err != nil {
		if code == "" {
			authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
			c.Redirect(http.StatusTemporaryRedirect, authURL)

		} else {
			tok, err := config.Exchange(context.TODO(), code)
			if err != nil {
				log.Fatalf("Unable to retrieve token from web %v", err)
			}

			googleExternalToken := utils.RandStringRunes(50)
			database.WriteGoogleAuthToken(googleExternalToken, tok)
			jwtString := CreateJWToken(googleExternalToken)
			c.Response().Header().Add("Authorization", "Bearer "+jwtString)
		}
	} else {
		return c.String(http.StatusAccepted, "you are already authenticated!") // if code 202 - means already authentificated
	}

	return c.Redirect(http.StatusTemporaryRedirect, redirectAddr)
}
