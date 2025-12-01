package satellite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/dgrijalva/jwt-go"
	"github.com/labstack/echo/v4"
	"storj.io/uplink"
)

const (
	ReserveBucket_Gmail      = "gmail"
	ReserveBucket_Outlook    = "outlook"
	ReserveBucket_Drive      = "google-drive"
	ReserveBucket_Cloud      = "google-cloud"
	ReserveBucket_Photos     = "google-photos"
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
// Note: The caller is responsible for calling Commit() or Abort() on the returned upload
func GetUploader(ctx context.Context, accessGrant, bucketName, objectKey string) (*uplink.Upload, error) {
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("parse access grant: %w", err)
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("open project: %w", err)
	}

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		if _, err = project.CreateBucket(ctx, bucketName); err != nil {
			project.Close()
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}

	logger.Info(ctx, "Uploading object",
		logger.String("bucket", bucketName),
		logger.String("object", objectKey))

	upload, err := project.UploadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		project.Close()
		return nil, fmt.Errorf("initiate upload: %w", err)
	}

	// Note: Project will be closed when upload is committed or aborted
	return upload, nil
}

// UploadObject uploads data to satellite storage
func UploadObject(ctx context.Context, accessGrant, bucketName, objectKey string, data []byte) error {
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
		if _, err = project.CreateBucket(ctx, bucketName); err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
	}

	logger.Info(ctx, "Uploading object",
		logger.String("bucket", bucketName),
		logger.String("object", objectKey))

	upload, err := project.UploadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		return fmt.Errorf("initiate upload: %w", err)
	}

	buf := bytes.NewBuffer(data)
	if _, err = io.Copy(upload, buf); err != nil {
		_ = upload.Abort()
		return fmt.Errorf("upload data: %w", err)
	}

	if err = upload.Commit(); err != nil {
		return fmt.Errorf("commit object: %w", err)
	}

	return nil
}

// DownloadObject downloads data from satellite storage
func DownloadObject(ctx context.Context, accessGrant, bucketName, objectKey string) ([]byte, error) {
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

	download, err := project.DownloadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	defer download.Close()

	receivedContents, err := io.ReadAll(download)
	if err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}

	return receivedContents, nil
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
	objects, err := listObjectsWithOptions(ctx, accessGrant, bucketName, &uplink.ListObjectsOptions{
		Prefix: prefix,
	})
	if err != nil {
		return nil, err
	}

	return objects, nil
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

// GetUserdetails retrieves user details from satellite service
func GetUserdetails(c echo.Context) (string, error) {
	tokenKey := c.Request().Header.Get("token_key")
	url := StorxSatelliteService + "/api/v0/auth/account"

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("cookie", "_tokenKey="+tokenKey)

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
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

	return response.ID, nil
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

// UploadItem represents a single upload item
type UploadItem struct {
	ObjectKey string
	Data      []byte
}

// UploadBatch uploads multiple objects in parallel
func UploadBatch(ctx context.Context, accessGrant, bucketName string, uploads []UploadItem, workerCount int) error {
	if len(uploads) == 0 {
		return nil
	}

	if workerCount <= 0 {
		workerCount = 5 // Default to 5 workers
	}
	if workerCount > len(uploads) {
		workerCount = len(uploads)
	}

	var wg sync.WaitGroup
	uploadChan := make(chan UploadItem, len(uploads))
	errChan := make(chan error, len(uploads))

	// Send all uploads to channel
	for _, item := range uploads {
		uploadChan <- item
	}
	close(uploadChan)

	// Start workers
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range uploadChan {
				if err := UploadObject(ctx, accessGrant, bucketName, item.ObjectKey, item.Data); err != nil {
					logger.Error(ctx, "Failed to upload object",
						logger.String("object_key", item.ObjectKey),
						logger.ErrorField(err),
					)
					errChan <- fmt.Errorf("failed to upload %s: %w", item.ObjectKey, err)
				}
			}
		}()
	}

	// Wait for all workers to complete
	wg.Wait()
	close(errChan)

	// Collect errors
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("batch upload completed with %d errors: %v", len(errors), errors[0])
	}

	return nil
}
