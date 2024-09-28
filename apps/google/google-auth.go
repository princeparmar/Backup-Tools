package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/StorX2-0/Backup-Tools/utils"

	rm "cloud.google.com/go/resourcemanager/apiv3"
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
	JwtSecretKey    = "your-secret-key"
	tokenExpiration = time.Duration(24 * time.Hour) // Example: expires in 24 hours
)

func CreateJWToken(googleToken string) string {
	// Create the claims
	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		CustomClaims{
			GoogleAuthToken: googleToken,
			StandardClaims: jwt.StandardClaims{
				ExpiresAt: time.Now().Add(tokenExpiration).Unix(),
			},
		},
	)

	// Sign the token with the secret key and get the complete, encoded token as a string
	tokenString, err := token.SignedString([]byte(JwtSecretKey))
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
		return []byte(JwtSecretKey), nil
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
	scopes := []string{drive.DriveScope, photoslibrary.PhotoslibraryScope, gmail.MailGoogleComScope, gs.CloudPlatformScope, gs.CloudPlatformReadOnlyScope, gs.DevstorageFullControlScope, gs.DevstorageReadWriteScope}
	scopes = append(scopes, rm.DefaultAuthScopes()...)
	config, err := google.ConfigFromJSON(b, scopes...)
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

func AuthTokenUsingRefreshToken(refreshToken string) (string, error) {
	var credentials struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}

	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token,omitempty"`
	}

	file, err := os.Open("./credentials.json")
	if err != nil {
		return "", fmt.Errorf("error opening credentials file: %v", err)
	}

	// Read the file
	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("error reading credentials file: %v", err)
	}

	// Unmarshal the JSON into the Credentials struct
	if err := json.Unmarshal(byteValue, &credentials); err != nil {
		return "", fmt.Errorf("error parsing credentials JSON: %v", err)
	}

	// Create the request body
	data := map[string]string{
		"client_id":     credentials.ClientID,
		"client_secret": credentials.ClientSecret,
		"refresh_token": credentials.RefreshToken,
		"grant_type":    "refresh_token",
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("error encoding JSON: %v", err)
	}

	const tokenURL = "https://oauth2.googleapis.com/token"

	// Create the HTTP request
	req, err := http.NewRequest("POST", tokenURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error making HTTP request: %v", err)
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	// Parse the response JSON
	err = json.Unmarshal(body, &tokenResponse)
	if err != nil {
		return "", fmt.Errorf("error parsing response JSON: %v", err)
	}

	// Return the access token
	return tokenResponse.AccessToken, nil
}
