package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/storage"

	rm "cloud.google.com/go/resourcemanager/apiv3"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
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

type GoogleAuthResponse struct {
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	ExpiresIn     string `json:"expires_in"`
	Error         string `json:"error"`
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
		logger.Info(context.Background(), "Error generating token:", logger.ErrorField(err))
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
		logger.Info(context.Background(), googleAuth)

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
	logger.Info(context.Background(), "token validated")

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
	scopes := []string{drive.DriveScope, photoslibrary.PhotoslibraryScope, gmail.MailGoogleComScope, gs.DevstorageFullControlScope, gs.DevstorageReadWriteScope}
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

func GetRefreshTokenFromCodeForEmail(code string) (*oauth2.Token, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Printf("Unable to read client secret file: %v", err)
	}

	// get refresh token from code
	config, err := google.ConfigFromJSON(b, gmail.GmailReadonlyScope, "https://www.googleapis.com/auth/userinfo.email")
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), code)
	if err != nil {
		return nil, err
	}

	return tok, nil
}

func AuthTokenUsingRefreshToken(refreshToken string) (string, error) {
	type credentials struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		TokenURI     string `json:"token_uri"`
	}

	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token,omitempty"`
	}

	byteValue, err := os.ReadFile("credentials.json")
	if err != nil {
		return "", fmt.Errorf("error reading credentials file: %v", err)
	}

	credMap := make(map[string]credentials)
	// Unmarshal the JSON into the Credentials struct
	if err := json.Unmarshal(byteValue, &credMap); err != nil {
		return "", fmt.Errorf("error parsing credentials JSON: %v", err)
	}

	if len(credMap) == 0 {
		return "", fmt.Errorf("no credentials found in JSON")
	}

	data := map[string]string{}
	tokenURI := ""
	for _, v := range credMap {
		// Create the request body
		data = map[string]string{
			"client_id":     v.ClientID,
			"client_secret": v.ClientSecret,
			"refresh_token": refreshToken,
			"grant_type":    "refresh_token",
		}
		tokenURI = v.TokenURI
		break
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("error encoding JSON: %v", err)
	}

	// Create the HTTP request
	req, err := http.NewRequest("POST", tokenURI, bytes.NewBuffer(jsonData))
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
	body, err := io.ReadAll(resp.Body)
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

func GetGoogleAccountDetailsFromContext(c echo.Context) (*GoogleAuthResponse, error) {
	database := c.Get(dbContextKey).(*storage.PosgresStore)

	googleToken, err := GetGoogleTokenFromJWT(c)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from JWT: %v", err)
	}
	token, err := database.ReadGoogleAuthToken(googleToken)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from database: %v", err)
	}
	// Get User Email
	userDetails, err := GetGoogleAccountDetailsFromAccessToken(token)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve google-auth token from database: %v", err)
	}
	return userDetails, nil
}

func GetGoogleAccountDetailsFromAccessToken(accessToken string) (*GoogleAuthResponse, error) {
	// Token info endpoint with the provided token
	url := fmt.Sprintf("https://oauth2.googleapis.com/tokeninfo?access_token=%s", accessToken)

	// Send an HTTP GET request to the token info endpoint
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error sending HTTP request: %v", err)
	}
	defer resp.Body.Close()

	var tokenInfo GoogleAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenInfo); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	// Check if the response contains an error
	if tokenInfo.Error != "" {
		return nil, fmt.Errorf("error in response: %v", tokenInfo.Error)
	}

	return &tokenInfo, nil
}

// IsGoogleTokenExpired checks if the provided Google access token is valid or expired
func IsGoogleTokenExpired(token string) bool {

	ctx := context.Background()

	tokenInfo, err := GetGoogleAccountDetailsFromAccessToken(token)
	if err != nil {
		logger.Info(ctx, "Error getting account details:", logger.ErrorField(err))
		return true
	}

	expireIn, err := strconv.Atoi(tokenInfo.ExpiresIn)
	if err != nil {
		logger.Info(ctx, "Error converting expires_in to int:", logger.ErrorField(err))
		return true
	}

	// Check if the token has expired (expires_in should be greater than 0)
	if expireIn > 0 {
		logger.Info(ctx, "Token expires in:"+tokenInfo.ExpiresIn+"seconds")
		return false
	}

	// Token is expired
	return true
}
