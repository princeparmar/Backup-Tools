package satellite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/dgrijalva/jwt-go"
	"github.com/labstack/echo/v4"
	"storj.io/common/grant"
	"storj.io/uplink"
)

var satelliteHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	},
}

type accountCacheEntry struct {
	userID    string
	expiresAt time.Time
}

var accountUserIDCache sync.Map

func cachedAccountUserID(tokenKey string) (string, bool) {
	if v, ok := accountUserIDCache.Load(tokenKey); ok {
		e := v.(accountCacheEntry)
		if time.Now().Before(e.expiresAt) {
			return e.userID, true
		}
		accountUserIDCache.Delete(tokenKey)
	}
	return "", false
}

func storeAccountUserID(tokenKey, userID string) {
	accountUserIDCache.Store(tokenKey, accountCacheEntry{
		userID:    userID,
		expiresAt: time.Now().Add(2 * time.Minute),
	})
}

const (
	ReserveBucket_Gmail      = "gmail"
	ReserveBucket_Outlook    = "outlook"
	ReserveBucket_Drive      = "google-drive"
	ReserveBucket_Cloud      = "google-cloud"
	ReserveBucket_Photos     = "google-photos"
	ReserveBucket_Contacts   = "google-contacts"
	ReserveBucket_Calendar   = "google-calendar"
	ReserveBucket_Dropbox    = "dropbox"
	ReserveBucket_S3         = "aws-s3"
	ReserveBucket_Github     = "github"
	ReserveBucket_Shopify    = "shopify"
	RestoreBucket_Quickbooks = "quickbooks"
)

var StorxSatelliteService string

// HandleSatelliteAuthentication authenticates app with satellite account
func HandleSatelliteAuthentication(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken := c.FormValue("satellite")
	if accessToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "satellite access token is required",
		})
	}

	c.SetCookie(&http.Cookie{
		Name:  "access_token",
		Value: accessToken,
	})

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "authentication was successful",
	})
}

// GetUploader creates an uploader for the specified bucket and object
func GetUploader(ctx context.Context, accessGrant, bucketName, objectKey string) (*uplink.Upload, error) {

	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("parse access grant: %w", err)
	}

	testAccessParse, err := grant.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("parse access grant: %w", err)
	}

	logger.Info(ctx, "access details",
		logger.String("satellite", testAccessParse.SatelliteAddress),
		logger.String("api_key", testAccessParse.APIKey.Serialize()))

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("open project: %w", err)
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		_, err = project.CreateBucket(ctx, bucketName)
		if err != nil {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	} else {
	}

	logger.Info(ctx, "Uploading object",
		logger.String("bucket", bucketName),
		logger.String("object", objectKey))

	upload, err := project.UploadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		return nil, fmt.Errorf("initiate upload: %w", err)
	}

	return upload, nil
}

// UploadObject uploads data to satellite storage
func UploadObject(ctx context.Context, accessGrant, bucketName, objectKey string, data []byte) error {
	return UploadObjectFromReader(ctx, accessGrant, bucketName, objectKey, bytes.NewReader(data))
}

// UploadObjectFromReader streams content to satellite storage without loading the full object into memory.
func UploadObjectFromReader(ctx context.Context, accessGrant, bucketName, objectKey string, r io.Reader) error {
	upload, err := GetUploader(ctx, accessGrant, bucketName, objectKey)
	if err != nil {
		return err
	}

	_, err = io.Copy(upload, r)
	if err != nil {
		_ = upload.Abort()
		return fmt.Errorf("upload data: %w", err)
	}

	err = upload.Commit()
	if err != nil {
		return fmt.Errorf("commit object: %w", err)
	}

	return nil
}

// DownloadObjectTo streams an object from satellite storage into w.
func DownloadObjectTo(ctx context.Context, accessGrant, bucketName, objectKey string, w io.Writer) error {
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return fmt.Errorf("parse access grant: %w", err)
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}

	download, err := project.DownloadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		return fmt.Errorf("open object: %w", err)
	}
	defer download.Close()

	if _, err := io.Copy(w, download); err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	return nil
}

// DownloadObject downloads data from satellite storage
func DownloadObject(ctx context.Context, accessGrant, bucketName, objectKey string) ([]byte, error) {
	var buf bytes.Buffer
	if err := DownloadObjectTo(ctx, accessGrant, bucketName, objectKey, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ListObjects lists all objects in a bucket
func ListObjects(ctx context.Context, accessGrant, bucketName string) (map[string]bool, error) {
	return ListObjectsWithPrefix(ctx, accessGrant, bucketName, "")
}

// ListObjectsWithPrefix lists objects with a specific prefix
func ListObjectsWithPrefix(ctx context.Context, accessGrant, bucketName, prefix string) (map[string]bool, error) {
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("parse access grant: %w", err)
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("open project: %w", err)
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("ensure bucket: %w", err)
	}

	listIter := project.ListObjects(ctx, bucketName, &uplink.ListObjectsOptions{
		Prefix: prefix,
	})

	objects := make(map[string]bool)
	for listIter.Next() {
		objects[listIter.Item().Key] = true
	}

	if err := listIter.Err(); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}

	return objects, nil
}

// ListObjectsDetailed returns detailed object information
func ListObjectsDetailed(ctx context.Context, accessGrant, bucketName string) ([]uplink.Object, error) {
	return listObjectsWithOptions(ctx, accessGrant, bucketName, &uplink.ListObjectsOptions{})
}

// GetFilesInFolder lists objects with a specific prefix
func GetFilesInFolder(ctx context.Context, accessGrant, bucketName, prefix string) ([]uplink.Object, error) {
	return listObjectsWithOptions(ctx, accessGrant, bucketName, &uplink.ListObjectsOptions{
		Prefix: prefix,
	})
}

// ListObjectsRecursive lists all objects recursively
func ListObjectsRecursive(ctx context.Context, accessGrant, bucketName string) ([]uplink.Object, error) {
	return listObjectsWithOptions(ctx, accessGrant, bucketName, &uplink.ListObjectsOptions{
		Recursive: true,
	})
}

// listObjectsWithOptions helper function for listing objects with options
func listObjectsWithOptions(ctx context.Context, accessGrant, bucketName string, options *uplink.ListObjectsOptions) ([]uplink.Object, error) {
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("parse access grant: %w", err)
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("open project: %w", err)
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, fmt.Errorf("ensure bucket: %w", err)
	}

	listIter := project.ListObjects(ctx, bucketName, options)
	var objects []uplink.Object

	for listIter.Next() {
		objects = append(objects, *listIter.Item())
	}

	if err := listIter.Err(); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}

	return objects, nil
}

// DeleteObject deletes an object from satellite storage
func DeleteObject(ctx context.Context, accessGrant, bucketName, objectKey string) error {
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return fmt.Errorf("parse access grant: %w", err)
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}

	_, err = project.DeleteObject(ctx, bucketName, objectKey)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}

	return nil
}

// GetUserdetails retrieves user details from satellite service (cached by token_key).
func GetUserdetails(c echo.Context) (string, error) {
	tokenKey := strings.TrimSpace(c.Request().Header.Get("token_key"))
	if tokenKey == "" {
		return "", fmt.Errorf("token_key header is required")
	}
	if userID, ok := cachedAccountUserID(tokenKey); ok {
		return userID, nil
	}

	url := StorxSatelliteService + "/api/v0/auth/account"
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("cookie", "_tokenKey="+tokenKey)

	res, err := satelliteHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var response struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if response.Error != "" {
		return "", fmt.Errorf("api error: %s", response.Error)
	}
	userID := strings.TrimSpace(response.ID)
	if userID == "" {
		return "", fmt.Errorf("empty account id")
	}

	storeAccountUserID(tokenKey, userID)
	return userID, nil
}

// GetProjectIDFromAccessGrant extracts project_id from access grant
func GetProjectIDFromAccessGrant(ctx context.Context, accessGrant string) (string, error) {
	if StorxSatelliteService == "" {
		return "", nil
	}

	url := strings.TrimSuffix(StorxSatelliteService, "/") + "/api/v0/public/projects/project-id-from-access-grant"

	payload := struct {
		AccessGrant string `json:"access_grant"`
	}{
		AccessGrant: accessGrant,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", nil
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", nil
	}

	var response struct {
		ProjectID string `json:"project_id"`
		Error     string `json:"error"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return "", nil
	}

	if response.Error != "" {
		return "", nil
	}

	return response.ProjectID, nil
}

// RefreshStorxToken requests a new uplink access grant from Satellite (Backup-Tools internal route).
func RefreshStorxToken(ctx context.Context, userID, projectID, email string) (string, error) {
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	email = strings.TrimSpace(email)
	if userID == "" || projectID == "" {
		return "", fmt.Errorf("user_id and project_id are required for storx token refresh")
	}
	if StorxSatelliteService == "" {
		return "", fmt.Errorf("STORX_SATELLITE_SERVICE not set")
	}
	apiKey := utils.GetEnvWithKey("BACKUP_TOOLS_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("BACKUP_TOOLS_API_KEY not set")
	}

	payload := struct {
		UserID    string `json:"user_id"`
		ProjectID string `json:"project_id"`
		Email     string `json:"email,omitempty"`
	}{
		UserID:    userID,
		ProjectID: projectID,
		Email:     email,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal refresh payload: %w", err)
	}

	url := strings.TrimSuffix(StorxSatelliteService, "/") + "/api/v0/internal/storx-token/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-User-Id", userID)
	if email != "" {
		req.Header.Set("X-User-Email", email)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("storx refresh request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("read storx refresh response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("storx refresh status %d: %s", res.StatusCode, string(body))
	}

	var response struct {
		AccessGrant string `json:"access_grant"`
		ProjectID   string `json:"project_id"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("parse storx refresh response: %w", err)
	}
	if response.Error != "" {
		return "", fmt.Errorf("storx refresh: %s", response.Error)
	}
	grant := strings.TrimSpace(response.AccessGrant)
	if grant == "" {
		return "", fmt.Errorf("storx refresh returned empty access_grant")
	}
	return grant, nil
}

// ClearGoogleRefreshToken tells Satellite to clear the stored Google OAuth refresh token for a user + mailbox email.
func ClearGoogleRefreshToken(ctx context.Context, userID, email string) error {
	userID = strings.TrimSpace(userID)
	email = strings.TrimSpace(email)
	if userID == "" || email == "" {
		return fmt.Errorf("user_id and email are required to clear google refresh token")
	}
	if StorxSatelliteService == "" {
		return fmt.Errorf("STORX_SATELLITE_SERVICE not set")
	}
	apiKey := utils.GetEnvWithKey("BACKUP_TOOLS_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("BACKUP_TOOLS_API_KEY not set")
	}

	payload := struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}{
		UserID: userID,
		Email:  email,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal clear google token payload: %w", err)
	}

	url := strings.TrimSuffix(StorxSatelliteService, "/") + "/api/v0/internal/google-token/clear"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("create clear google token request: %w", err)
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-User-Email", email)

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("clear google token request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read clear google token response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("clear google token status %d: %s", res.StatusCode, string(body))
	}

	var response struct {
		Error string `json:"error"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("parse clear google token response: %w", err)
		}
	}
	if response.Error != "" {
		return fmt.Errorf("clear google token: %s", response.Error)
	}
	return nil
}

// createJWTToken creates a JWT token for email notifications
func createJWTToken(email, errorMsg, method, secretKey string) (string, error) {
	claims := jwt.MapClaims{
		"email":  email,
		"error":  errorMsg,
		"method": method,
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(7 * time.Minute).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return tokenString, nil
}

// SendEmailForBackupFailure sends email notification for backup failures
func SendEmailForBackupFailure(ctx context.Context, email, errorMsg, method string) error {
	emailCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if StorxSatelliteService == "" {
		return fmt.Errorf("STORX_SATELLITE_SERVICE not set")
	}

	emailAPIKey := utils.GetEnvWithKey("EMAIL_API_KEY")
	if emailAPIKey == "" {
		return fmt.Errorf("EMAIL_API_KEY not set")
	}

	jwtToken, err := createJWTToken(email, errorMsg, method, emailAPIKey)
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}

	payload := struct {
		Token string `json:"token"`
	}{
		Token: jwtToken,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := strings.TrimSuffix(StorxSatelliteService, "/") + "/api/v0/auth/send-email"

	req, err := http.NewRequestWithContext(emailCtx, http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", res.StatusCode, string(body))
	}

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if response.Error != "" {
		return fmt.Errorf("server error: %s", response.Error)
	}

	if !response.Success {
		return fmt.Errorf("request failed: %s", response.Message)
	}

	return nil
}

// createNotificationJWTToken creates a JWT token for generic notifications
func createNotificationJWTToken(userID, title, body, secretKey string, priority *string, data map[string]interface{}, imageURL *string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"title":   title,
		"body":    body,
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(7 * time.Minute).Unix(),
	}

	if priority != nil && (*priority == "high" || *priority == "normal") {
		claims["priority"] = *priority
	}
	if len(data) > 0 {
		claims["data"] = data
	}
	if imageURL != nil && *imageURL != "" {
		claims["image_url"] = *imageURL
	}

	tokenString, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secretKey))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return tokenString, nil
}

// SendNotificationAsync sends a notification asynchronously and logs any errors
func SendNotificationAsync(ctx context.Context, userID, title, body string, priority *string, data map[string]interface{}, imageURL *string) {
	// Create a new background context to avoid cancellation when the original context is canceled
	// This ensures the notification can complete independently of the request lifecycle
	notificationCtx := context.Background()
	go func() {
		if err := SendNotification(notificationCtx, userID, title, body, priority, data, imageURL); err != nil {
			logger.Error(ctx, "Failed to send notification",
				logger.String("user_id", userID),
				logger.String("title", title),
				logger.ErrorField(err),
			)
		}
	}()
}

// SendNotification sends a generic notification for any type of event
func SendNotification(ctx context.Context, userID, title, body string, priority *string, data map[string]interface{}, imageURL *string) error {
	if userID == "" || title == "" || body == "" {
		return fmt.Errorf("userID, title, and body are required")
	}
	if StorxSatelliteService == "" {
		return fmt.Errorf("STORX_SATELLITE_SERVICE not set")
	}

	emailAPIKey := utils.GetEnvWithKey("EMAIL_API_KEY")
	if emailAPIKey == "" {
		return fmt.Errorf("EMAIL_API_KEY not set")
	}

	jwtToken, err := createNotificationJWTToken(userID, title, body, emailAPIKey, priority, data, imageURL)
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}

	notificationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	payloadBytes, err := json.Marshal(struct{ Token string }{Token: jwtToken})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	url := strings.TrimSuffix(StorxSatelliteService, "/") + "/api/v0/auth/send-notification"

	req, err := http.NewRequestWithContext(notificationCtx, http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer res.Body.Close()

	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", res.StatusCode, string(responseBody))
	}

	var response struct {
		Success bool   `json:"success"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}

	if err := json.Unmarshal(responseBody, &response); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	switch {
	case response.Error != "":
		return fmt.Errorf("server error: %s", response.Error)
	case response.Success || strings.Contains(strings.ToLower(response.Status), "success"):
		return nil
	case response.Status != "":
		return fmt.Errorf("request failed: %s", response.Status)
	case response.Message != "":
		return fmt.Errorf("request failed: %s", response.Message)
	default:
		return nil
	}
}

// FormatSatelliteError converts technical satellite errors to user-friendly messages
func FormatSatelliteError(err error) string {
	if err == nil {
		return ""
	}
	errMsg := err.Error()

	if strings.Contains(errMsg, "uplink:") {
		return errMsg
	}

	// Check for permission denied / unauthorized API credentials
	if strings.Contains(errMsg, "permission denied") &&
		strings.Contains(errMsg, "Unauthorized API credentials") {
		return "Please reconnect your account to proceed"
	}

	// Check for other permission denied patterns
	if strings.Contains(errMsg, "permission denied") {
		return "Please reconnect your account to proceed"
	}

	// Check for ensure bucket permission errors
	if strings.Contains(errMsg, "ensure bucket") && strings.Contains(errMsg, "permission denied") {
		return "Please reconnect your account to proceed"
	}

	// Return original error for other cases
	return errMsg
}
