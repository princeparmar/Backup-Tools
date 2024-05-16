package google

import (
	"context"
	"encoding/json"
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
	gs "google.golang.org/api/storage/v1"
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
	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
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
		claims, ok := token.Claims.(*CustomClaims)
		log.Printf("Token claims: %+v", token.Claims)

		if !ok {
			log.Print("Invalid token claims")
			return "", err
		}

		// Extract specific information from claims
		googleAuth := claims.GoogleAuthToken
		fmt.Println(googleAuth)

		// Output extracted informationr
		return googleAuth, nil
	} else {
		log.Print(err)
		return "", err
	}
}

type TokenInfo struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Expiry        int64  `json:"exp"`
	IssuedAt      int64  `json:"iat"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	UserID        string `json:"sub"`
}

func verifyToken(idToken string) (*TokenInfo, error) {
	resp, err := http.Get(fmt.Sprintf("https://www.googleapis.com/oauth2/v3/tokeninfo?id_token=%s", idToken))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tokenInfo TokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&tokenInfo); err != nil {
		return nil, err
	}

	return &tokenInfo, nil
}

func Autentificate(c echo.Context) error {
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	authToken := c.FormValue("google-key")
	// refreshToken := c.FormValue("refresh-key")

	if authToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "google-key is missing",
		})
	}
	// if refreshToken == "" {
	// 	return c.JSON(http.StatusBadRequest, map[string]interface{}{
	// 		"error": "refresh token is missing",
	// 	})
	// }

	_, err := verifyToken(authToken)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error validating google auth token": err.Error(),
		})
	}
	fmt.Println("token validated")

	// _, err := idtoken.Validate(context.Background(), googleKey, "")
	// if err != nil {
	// 	return c.JSON(http.StatusUnauthorized, map[string]interface{}{
	// 		"error": "google-key can not be validated. check google authentication token",
	// 	})
	// }

	googleExternalToken := utils.RandStringRunes(50)
	database.WriteGoogleAuthToken(googleExternalToken, authToken)
	jwtString := CreateJWToken(googleExternalToken)
	c.Response().Header().Add("Authorization", "Bearer "+jwtString)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"google-auth": jwtString,
	})
}

// Google authentication module, checks if you have auth token in database, if not - redirects to Google auth page.
func Autentificateg(c echo.Context) error {
	database := c.Get(dbContextKey).(*storage.PosgresStore)
	code := c.FormValue("code")
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Printf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, drive.DriveScope, photoslibrary.PhotoslibraryScope, gmail.MailGoogleComScope, gs.CloudPlatformScope, gs.CloudPlatformReadOnlyScope, gs.DevstorageFullControlScope, gs.DevstorageReadWriteScope)
	if err != nil {
		log.Printf("Unable to parse client secret file to config: %v", err)
	}

	var redirectAddr = os.Getenv("FRONTEND_URL") // Add Frontend URL for redirect to file .env
	if AuthRequestChecker(c) {
		return c.String(http.StatusAccepted, "you are already authenticated!") // if code 202 - means already authentificated
	} else {
		if code == "" {
			authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
			c.Redirect(http.StatusTemporaryRedirect, authURL)
		} else {
			tok, err := config.Exchange(context.TODO(), code)
			if err != nil {
				log.Printf("Unable to retrieve token from web %v", err)
			}

			googleExternalToken := utils.RandStringRunes(50)
			database.WriteGoogleAuthToken(googleExternalToken, tok.AccessToken)
			jwtString := CreateJWToken(googleExternalToken)
			c.Response().Header().Add("Authorization", "Bearer "+jwtString)
		}
	}

	return c.Redirect(http.StatusTemporaryRedirect, redirectAddr)
}

func AuthRequestChecker(c echo.Context) bool {
	// Check if the request contains the "Authorization" header
	authHeader := c.Request().Header.Get("Authorization")

	if authHeader != "" {
		// Header exists, handle accordingly
		// Example: Perform authentication based on the token in the header
		return true
	} else {
		// Header does not exist
		return false
	}
}
