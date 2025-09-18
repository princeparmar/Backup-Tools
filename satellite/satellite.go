package satellite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/labstack/echo/v4"
	"storj.io/common/grant"
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

// Authenticates app with your satellite accout.
func HandleSatelliteAuthentication(c echo.Context) error {
	accessToken := c.FormValue("satellite")
	c.SetCookie(&http.Cookie{
		Name:  "access_token",
		Value: accessToken,
	})
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "authentication was successful",
	})
}

func GetUploader(ctx context.Context, accessGrant, bucketName, objectKey string) (*uplink.Upload, error) {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}

	testAccessParse, err := grant.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}
	fmt.Println("testAccessParse", testAccessParse.SatelliteAddress)
	fmt.Println("access", testAccessParse.APIKey.Serialize())
	fmt.Println("encAccess", testAccessParse.EncAccess)

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		_, err := project.CreateBucket(ctx, bucketName)
		if err != nil {
			return nil, fmt.Errorf("could not create bucket: %v", err)
		}
	}

	fmt.Println("Uploading object to bucket:", bucketName, "with key:", objectKey)
	// Intitiate the upload of our Object to the specified bucket and key.
	upload, err := project.UploadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		return nil, fmt.Errorf("could not initiate upload: %v", err)
	}

	return upload, nil
}

func UploadObject(ctx context.Context, accessGrant, bucketName, objectKey string, data []byte) error {
	upload, err := GetUploader(ctx, accessGrant, bucketName, objectKey)
	if err != nil {
		return err
	}

	// Copy the data to the upload.
	buf := bytes.NewBuffer(data)
	_, err = io.Copy(upload, buf)
	if err != nil {
		_ = upload.Abort()
		return fmt.Errorf("could not upload data: %v", err)
	}

	// Commit the uploaded object.
	err = upload.Commit()
	if err != nil {
		return fmt.Errorf("could not commit uploaded object: %v", err)
	}

	return nil
}

func DownloadObject(ctx context.Context, accessGrant, bucketName, objectKey string) ([]byte, error) {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	download, err := project.DownloadObject(ctx, bucketName, objectKey, nil)
	if err != nil {
		return nil, fmt.Errorf("could not open object: %v", err)
	}
	defer download.Close()

	// Read everything from the download stream
	receivedContents, err := io.ReadAll(download)
	if err != nil {
		return nil, fmt.Errorf("could not read data: %v", err)
	}
	return receivedContents, nil
}

func ListObjects(ctx context.Context, accessGrant, bucketName string) (map[string]bool, error) {
	return ListObjectsWithPrefix(ctx, accessGrant, bucketName, "")
}

func ListObjectsWithPrefix(ctx context.Context, accessGrant, bucketName, prefix string) (map[string]bool, error) {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	listIter := project.ListObjects(ctx, bucketName, &uplink.ListObjectsOptions{
		Prefix: prefix,
	})
	/*if err != nil {
		return nil, fmt.Errorf("could not open object: %v", err)
	}*/

	objects := map[string]bool{}
	for listIter.Next() {
		objects[listIter.Item().Key] = true
	}

	if listIter.Err() != nil {
		return nil, fmt.Errorf("could not list objects: %v", listIter.Err())
	}

	return objects, nil
}

func DeleteObject(ctx context.Context, accessGrant, bucketName, objectKey string) error {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return fmt.Errorf("could not parse access grant: %v", err)
	}

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return err
	}

	// Delete object
	_, err = project.DeleteObject(ctx, bucketName, objectKey)
	if err != nil {
		return err
	}
	return nil

}

func ListObjects1(ctx context.Context, accessGrant, bucketName string) ([]uplink.Object, error) {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	listIter := project.ListObjects(ctx, bucketName, nil)
	/*if err != nil {
		return nil, fmt.Errorf("could not open object: %v", err)
	}*/

	objects := []uplink.Object{}
	for listIter.Next() {
		//objects[listIter.Item().Key] = true
		objects = append(objects, *listIter.Item())
	}

	if listIter.Err() != nil {
		return nil, fmt.Errorf("could not list objects: %v", listIter.Err())
	}

	return objects, nil
}

func GetFilesInFolder(ctx context.Context, accessGrant, bucketName, prefix string) ([]uplink.Object, error) {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	listIter := project.ListObjects(ctx, bucketName, &uplink.ListObjectsOptions{Prefix: prefix})

	objects := []uplink.Object{}
	for listIter.Next() {
		//objects[listIter.Item().Key] = true
		objects = append(objects, *listIter.Item())
	}

	if listIter.Err() != nil {
		return nil, fmt.Errorf("could not list objects: %v", listIter.Err())
	}

	return objects, nil
}

func ListObjectsRecurisive(ctx context.Context, accessGrant, bucketName string) ([]uplink.Object, error) {
	// Parse the Access Grant.
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return nil, fmt.Errorf("could not parse access grant: %v", err)
	}

	// Open up the Project we will be working with.
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("could not open project: %v", err)
	}
	defer project.Close()

	// Ensure the desired Bucket within the Project is created.
	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		return nil, err
	}
	listIter := project.ListObjects(ctx, bucketName, &uplink.ListObjectsOptions{Recursive: true, Prefix: ""})
	/*if err != nil {
		return nil, fmt.Errorf("could not open object: %v", err)
	}*/

	objects := []uplink.Object{}
	for listIter.Next() {
		objects = append(objects, *listIter.Item())
	}

	if listIter.Err() != nil {
		return nil, fmt.Errorf("could not list objects: %v", listIter.Err())
	}

	return objects, nil
}

func GetUserdetails(token string) (string, error) {

	url := StorxSatelliteService + "/api/v0/auth/account"

	client := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, url, nil)

	if err != nil {
		return "", err
	}
	req.Header.Add("accept", "application/json")
	req.Header.Add("cookie", "_tokenKey="+token)

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	var userDetailResponse struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}

	err = json.Unmarshal(body, &userDetailResponse)
	if err != nil {
		return "", err
	}

	if userDetailResponse.Error != "" {
		return "", fmt.Errorf(userDetailResponse.Error)
	}

	return userDetailResponse.ID, nil
}

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
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

type BackupFailureRequest struct {
	Email  string `json:"email"`
	Error  string `json:"error"`
	Method string `json:"method"`
}

func SendEmailForBackupFailure(ctx context.Context, email, errorMsg, method string) error {
	// Create a new context with timeout (respect caller context)
	emailCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if StorxSatelliteService == "" {
		return fmt.Errorf("STORX_SATELLITE_SERVICE environment variable is not set")
	}

	emailAPIKey := os.Getenv("EMAIL_API_KEY")
	if emailAPIKey == "" {
		return fmt.Errorf("EMAIL_API_KEY environment variable is not set")
	}

	jwtToken, err := createJWTToken(email, errorMsg, method, emailAPIKey)
	if err != nil {
		return fmt.Errorf("failed to create JWT token: %w", err)
	}

	payload := struct {
		Token string `json:"token"`
	}{
		Token: jwtToken,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := strings.TrimSuffix(StorxSatelliteService, "/") + "/api/v0/auth/send-email"

	req, err := http.NewRequestWithContext(emailCtx, http.MethodPost, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("request failed with status %d: %s", res.StatusCode, string(body))
	}

	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Error != "" {
		return fmt.Errorf("server error: %s", response.Error)
	}

	if !response.Success {
		return fmt.Errorf("request was not successful: %s", response.Message)
	}

	return nil
}
