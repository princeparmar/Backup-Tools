package outlook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/dgrijalva/jwt-go"
	"github.com/labstack/echo/v4"
)

// MicrosoftAuthClaims is the Backup-Tools JWT issued by POST /microsoft-auth.
// Covers all Microsoft Graph restore (mail, calendar, contacts, OneDrive, SharePoint, Teams, Groups) — not Outlook-only.
type MicrosoftAuthClaims struct {
	MicrosoftAuthToken string `json:"microsoft_token"`
	jwt.StandardClaims
}

// CreateMicrosoftAuthJWT wraps a DB lookup key (not the Graph token itself).
func CreateMicrosoftAuthJWT(lookupKey string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, MicrosoftAuthClaims{
		MicrosoftAuthToken: lookupKey,
		StandardClaims: jwt.StandardClaims{
			ExpiresAt: time.Now().Add(middleware.TokenExpiration).Unix(),
		},
	})
	tokenString, err := token.SignedString([]byte(middleware.JwtSecretKey))
	if err != nil {
		logger.Info(context.Background(), "Error generating microsoft-auth token:", logger.ErrorField(err))
		return ""
	}
	return tokenString
}

// LookupKeyFromMicrosoftAuthJWT parses Authorization (raw JWT or Bearer JWT) into the DB lookup key.
func LookupKeyFromMicrosoftAuthJWT(authHeader string) (string, error) {
	tokenString := strings.TrimSpace(authHeader)
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return "", fmt.Errorf("Authorization header is empty")
	}
	token, err := jwt.ParseWithClaims(tokenString, &MicrosoftAuthClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(middleware.JwtSecretKey), nil
	})
	if err != nil {
		return "", err
	}
	if !token.Valid {
		return "", fmt.Errorf("invalid microsoft-auth jwt")
	}
	claims, ok := token.Claims.(*MicrosoftAuthClaims)
	if !ok || strings.TrimSpace(claims.MicrosoftAuthToken) == "" {
		return "", fmt.Errorf("invalid microsoft-auth claims")
	}
	return claims.MicrosoftAuthToken, nil
}

// ResolveGraphAccessToken turns an Authorization header into a Graph access token.
// Prefer Backup-Tools microsoft-auth JWT (POST /microsoft-auth); fall back to a raw Graph token.
func ResolveGraphAccessToken(c echo.Context, authHeader string) (string, error) {
	authHeader = strings.TrimSpace(authHeader)
	authHeader = strings.TrimPrefix(authHeader, "Bearer ")
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return "", fmt.Errorf("Authorization header is required")
	}
	lookup, err := LookupKeyFromMicrosoftAuthJWT(authHeader)
	if err == nil {
		database, ok := c.Get(middleware.DbContextKey).(*db.PostgresDb)
		if !ok || database == nil || database.AuthRepo == nil {
			return "", fmt.Errorf("database not available for microsoft-auth lookup")
		}
		graphToken, rerr := database.AuthRepo.ReadMicrosoftAuthToken(lookup)
		if rerr != nil {
			return "", fmt.Errorf("unable to retrieve microsoft-auth token from database: %w", rerr)
		}
		if strings.TrimSpace(graphToken) == "" {
			return "", fmt.Errorf("microsoft-auth token empty in database")
		}
		return graphToken, nil
	}
	// Not our JWT — treat as direct Graph access token (legacy / debugging).
	return authHeader, nil
}

func validateGraphAccessToken(ctx context.Context, accessToken string) error {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return fmt.Errorf("microsoft-key is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, graphBaseURL+"/me?$select=id,mail,userPrincipalName", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("microsoft-key rejected by Graph http %d: %s", resp.StatusCode, truncateForErr(b))
	}
	return nil
}

// Authenticate exchanges a Microsoft Graph access token for a Backup-Tools microsoft-auth JWT.
// Used for all Microsoft restore (mail/calendar/contacts/OneDrive/SharePoint/Teams/Groups), not Outlook-only.
// Mirrors Google Autentificate (POST /google-auth). Form or JSON: microsoft-key / microsoft_key.
func Authenticate(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	authToken := strings.TrimSpace(c.FormValue("microsoft-key"))
	if authToken == "" {
		authToken = strings.TrimSpace(c.FormValue("microsoft_key"))
	}
	if authToken == "" {
		var body struct {
			MicrosoftKey    string `json:"microsoft_key"`
			MicrosoftKeyAlt string `json:"microsoft-key"`
		}
		_ = c.Bind(&body)
		authToken = strings.TrimSpace(body.MicrosoftKey)
		if authToken == "" {
			authToken = strings.TrimSpace(body.MicrosoftKeyAlt)
		}
	}
	if authToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "microsoft-key is missing",
		})
	}

	if err = validateGraphAccessToken(ctx, authToken); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "error validating microsoft auth token: " + err.Error(),
		})
	}

	externalToken := utils.RandStringRunes(50)
	if err = database.AuthRepo.WriteMicrosoftAuthToken(externalToken, authToken); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": "failed to store microsoft-auth token",
		})
	}
	jwtString := CreateMicrosoftAuthJWT(externalToken)
	if jwtString == "" {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": "failed to create microsoft-auth jwt",
		})
	}
	c.Response().Header().Add("Authorization", "Bearer "+jwtString)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"microsoft-auth": jwtString,
	})
}
