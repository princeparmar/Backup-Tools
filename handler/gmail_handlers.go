package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"golang.org/x/sync/errgroup"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/gmail/v1"
)

type MessageListJSON struct {
	gmail.Message
	Synced bool `json:"synced"`
}

// UploadResult represents the result of uploading messages to Satellite
type UploadResult struct {
	ProcessedIDs []string `json:"processed_ids"`
	FailedIDs    []string `json:"failed_ids"`
	Message      string   `json:"message"`
}

// DownloadResult represents the result of downloading messages from Satellite
type DownloadResult struct {
	ProcessedIDs []string `json:"processed_ids"`
	FailedIDs    []string `json:"failed_ids"`
	Message      string   `json:"message"`
}

type gmailAccountCount struct {
	Email      string `json:"email"`
	EmailCount int64  `json:"email_count"`
}

type gmailGroupedEmails struct {
	AdminEmail      string              `json:"admin_email"`
	EmailCount      int64               `json:"email_count"`
	ConnectedEmails []gmailAccountCount `json:"connected_emails"`
}

type gmailCorporateAdminResponse struct {
	Account     string             `json:"account"`
	AccountType string             `json:"account_type"`
	Count       int                `json:"count"`
	Grouped     gmailGroupedEmails `json:"grouped_emails"`
}

// GmailService provides consolidated Gmail operations
type GmailService struct {
	client      *google.GmailClient
	accessGrant string
	userEmail   string
}

// NewGmailService creates a new GmailService instance
func NewGmailService(client *google.GmailClient, accessGrant, userEmail string) *GmailService {
	return &GmailService{
		client:      client,
		accessGrant: accessGrant,
		userEmail:   userEmail,
	}
}

// UploadMessagesToSatellite uploads Gmail messages to Satellite and updates synced_objects
func (s *GmailService) UploadMessagesToSatellite(ctx context.Context, database *db.PostgresDb, messageIDs []string) (*UploadResult, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, id := range messageIDs {
		// Skip empty or whitespace-only IDs
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		func(id string) {
			g.Go(func() error {
				msg, err := s.client.GetMessageDirect(id)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				b, err := json.Marshal(msg)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				messagePath := s.userEmail + "/" + utils.GenerateTitleFromGmailMessage(msg)

				// Use helper function to upload and sync
				// Source and Type are automatically derived from bucket name ("gmail" -> source: "google", type: "gmail")
				err = UploadObjectAndSync(ctx, database, s.accessGrant, "gmail", messagePath, b, s.userEmail)
				if err != nil {
					logger.Info(ctx, "error uploading to satellite", logger.ErrorField(err))
					failedIDs.Add(id)
					return nil
				}

				processedIDs.Add(id)
				return nil
			})
		}(id)
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &UploadResult{
		ProcessedIDs: processedIDs.Get(),
		FailedIDs:    failedIDs.Get(),
		Message:      "all mails were successfully uploaded from Google mail to Satellite",
	}, nil
}

// DownloadMessagesFromSatellite downloads messages from Satellite and inserts them into Gmail
func (s *GmailService) DownloadMessagesFromSatellite(ctx context.Context, keys []string) (*DownloadResult, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, key := range keys {
		key := key
		if key == "" {
			continue
		}
		g.Go(func() error {
			// Download file from Satellite
			data, err := satellite.DownloadObject(ctx, s.accessGrant, satellite.ReserveBucket_Gmail, key)
			if err != nil {
				failedIDs.Add(key)
				return nil
			}

			// Parse the email data and insert into Gmail
			var gmailMsg gmail.Message
			if err := json.Unmarshal(data, &gmailMsg); err != nil {
				failedIDs.Add(key)
				return nil
			}

			// Insert message into Gmail
			if err := s.client.InsertMessage(&gmailMsg); err != nil {
				logger.Info(ctx, "error inserting message into Gmail", logger.ErrorField(err))
				failedIDs.Add(key)
			} else {
				processedIDs.Add(key)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &DownloadResult{
		ProcessedIDs: processedIDs.Get(),
		FailedIDs:    failedIDs.Get(),
		Message:      "all gmail messages processed",
	}, nil
}

// decodeURLFilter decodes a URL-encoded JSON filter parameter and returns a GmailFilter
func DecodeURLFilter(urlEncodedFilter string) (*google.GmailFilter, error) {
	var filter google.GmailFilter
	if err := decodeFilterJSON(urlEncodedFilter, &filter); err != nil {
		return nil, err
	}
	return &filter, nil
}

// decodeFilterJSON is a generic helper to decode URL-encoded JSON filter parameters
func decodeFilterJSON(urlEncodedFilter string, target interface{}) error {
	// URL decode the filter string
	decodedFilter, err := url.QueryUnescape(urlEncodedFilter)
	if err != nil {
		return fmt.Errorf("failed to URL decode filter: %v", err)
	}

	// Parse the JSON string into target struct
	if err := json.Unmarshal([]byte(decodedFilter), target); err != nil {
		return fmt.Errorf("failed to parse filter JSON: %v", err)
	}

	return nil
}

// Helper function to parse request IDs from JSON or form data
func parseRequestIDs(c echo.Context) ([]string, error) {
	var allIDs []string
	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		// Decode JSON array from request body
		if err := json.NewDecoder(c.Request().Body).Decode(&allIDs); err != nil {
			return nil, errors.New("invalid JSON format")
		}
	} else {
		// Handle form data
		formIDs := c.FormValue("ids")
		allIDs = strings.Split(formIDs, ",")
	}
	return allIDs, nil
}

// Helper function to validate and process request IDs
func validateAndProcessRequestIDs(c echo.Context) ([]string, error) {
	allIDs, err := parseRequestIDs(c)
	if err != nil {
		return nil, err
	}

	// Process base64 decoding for download operations
	for i := range allIDs {
		allIDs[i] = strings.TrimSpace(allIDs[i])
		decodedID, err := base64.StdEncoding.DecodeString(allIDs[i])
		if err != nil {
			return nil, errors.New("invalid base64 format")
		}
		allIDs[i] = string(decodedID)
	}

	// Validate request
	if len(allIDs) == 0 || allIDs[0] == "" {
		return nil, errors.New("no keys provided")
	}
	if len(allIDs) > 10 {
		return nil, errors.New("maximum 10 keys allowed")
	}

	return allIDs, nil
}

// // Helper function to setup Gmail handler with all common validations
// func setupGmailHandler(c echo.Context) (string, *google.GmailClient, error) {
// 	// Validate access token
// 	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
// 	if accessGrant == "" {
// 		return "", nil, errors.New("access token not found")
// 	}

// 	// Create Gmail client
// 	gmailClient, err := google.NewGmailClient(c)
// 	if err != nil {
// 		return "", nil, err
// 	}

// 	return accessGrant, gmailClient, nil
// }

// func HandleListGmailMessagesToSatellite(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	// Setup Gmail handler with all common validations
// 	accessGrant, gmailClient, err := setupGmailHandler(c)
// 	if err != nil {
// 		if err.Error() == "access token not found" {
// 			return c.JSON(http.StatusForbidden, map[string]interface{}{
// 				"error": err.Error(),
// 			})
// 		}
// 		return err
// 	}

// 	// Get user details
// 	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
// 	if err != nil {
// 		return err
// 	}

// 	if userDetails.Email == "" {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error": "user email not found, please check access handling",
// 		})
// 	}

// 	// Parse request IDs
// 	allIDs, err := parseRequestIDs(c)
// 	if err != nil {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"error": err.Error(),
// 		})
// 	}

// 	// Get database from context
// 	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

// 	// Create Gmail service and upload messages
// 	gmailService := NewGmailService(gmailClient, accessGrant, userDetails.Email)
// 	result, err := gmailService.UploadMessagesToSatellite(c.Request().Context(), database, allIDs)
// 	if err != nil {
// 		return c.JSON(http.StatusForbidden, map[string]interface{}{
// 			"error":         err.Error(),
// 			"failed_ids":    result.FailedIDs,
// 			"processed_ids": result.ProcessedIDs,
// 		})
// 	}

// 	return c.JSON(http.StatusOK, result)
// }

// handleGmailGetThreadsIDsControlled - fetches threads IDs from Gmail and returns them in JSON format.
// It uses pagination to fetch threads in chunks of 500.
func HandleGmailGetThreadsIDsControlled(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Extract access grant early for webhook processing
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "authentication failed",
		})
	}

	go func() {
		processCtx := context.Background()
		if processErr := ProcessWebhookEvents(processCtx, database, accessGrant, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from listing route",
				logger.ErrorField(processErr))
		}
	}()

	num := c.QueryParam("num")
	var numInt int64
	if num != "" {
		var err error
		if numInt, err = strconv.ParseInt(num, 10, 64); err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	} else {
		numInt = 500
	}
	nextPageToken := c.QueryParam("nextPageToken")

	// Parse filter from JWT-encoded query parameter
	var filter *google.GmailFilter
	if filterParam := c.QueryParam("filter"); filterParam != "" {
		decodedFilter, err := DecodeURLFilter(filterParam)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid filter parameter: " + err.Error(),
			})
		}
		filter = decodedFilter
	}

	gmailClient, err := google.NewGmailClient(c)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "googleapi: error 401") || strings.Contains(errMsg, "googleapi: error 403") || strings.Contains(errMsg, "invalid_grant") {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "Google account access has been revoked. Please reconnect your Google account.",
			})
		}
		return err
	}

	var threads []any

	res, err := gmailClient.GetUserMessagesControlled(nextPageToken, "", numInt, filter)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "googleapi: error 401") || strings.Contains(errMsg, "googleapi: error 403") || strings.Contains(errMsg, "invalid_grant") {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "Google account access has been revoked. Please reconnect your Google account.",
			})
		}
		return err
	}

	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "googleapi: error 401") || strings.Contains(errMsg, "googleapi: error 403") || strings.Contains(errMsg, "invalid_grant") {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "Google account access has been revoked. Please reconnect your Google account.",
			})
		}
		return err
	}

	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Gmail, "google", "gmail")
	if err != nil {
		logger.Error(ctx, "Failed to get synced objects from database", logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}
	syncedMap := make(map[string]bool)
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	for _, message := range res.Messages {
		messagePath := userDetails.Email + "/" + utils.GenerateTitleFromGmailMessage(message)
		synced := syncedMap[messagePath]
		threads = append(threads, MessageListJSON{Message: *message, Synced: synced})
	}
	nextPageToken = res.NextPageToken

	return c.JSON(http.StatusOK, map[string]any{"messages": threads, "nextPageToken": nextPageToken})
}

// handleGmailDownloadAndInsert - downloads emails from Satellite and inserts them into Gmail.
// It uses pagination to download emails in chunks of 10.
func HandleGmailDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Get access token from header
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// Validate and process request IDs
	allIDs, err := validateAndProcessRequestIDs(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Get Gmail client
	gmailClient, err := google.NewGmailClient(c)
	if err != nil {
		return err
	}

	// Get user details for notification
	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil || userDetails.Email == "" {
		logger.Warn(ctx, "Failed to get user email for notification", logger.ErrorField(err))
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
	}

	// Send start notification
	priority := "normal"
	startData := map[string]interface{}{
		"event":      "gmail_restore_started",
		"level":      2,
		"login_id":   userDetails.Email,
		"method":     "gmail",
		"type":       "restore",
		"timestamp":  "now",
		"item_count": len(allIDs),
	}
	satellite.SendNotificationAsync(ctx, userID, "Gmail Restore Started", fmt.Sprintf("Restore of %d messages for %s has started", len(allIDs), userDetails.Email), &priority, startData, nil)

	// Create Gmail service and download messages
	gmailService := NewGmailService(gmailClient, accessGrant, "")
	result, err := gmailService.DownloadMessagesFromSatellite(c.Request().Context(), allIDs)
	if err != nil {
		// Send failure notification
		failPriority := "high"
		failData := map[string]interface{}{
			"event":     "gmail_restore_failed",
			"level":     4,
			"login_id":  userDetails.Email,
			"method":    "gmail",
			"type":      "restore",
			"timestamp": "now",
			"error":     err.Error(),
		}
		satellite.SendNotificationAsync(context.Background(), userID, "Gmail Restore Failed", fmt.Sprintf("Restore for %s failed: %v", userDetails.Email, err), &failPriority, failData, nil)

		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    result.FailedIDs,
			"processed_ids": result.ProcessedIDs,
		})
	}

	// Send completion notification
	compPriority := "normal"
	compData := map[string]interface{}{
		"event":           "gmail_restore_completed",
		"level":           2,
		"login_id":        userDetails.Email,
		"method":          "gmail",
		"type":            "restore",
		"timestamp":       "now",
		"processed_count": len(result.ProcessedIDs),
		"failed_count":    len(result.FailedIDs),
	}
	satellite.SendNotificationAsync(ctx, userID, "Gmail Restore Completed", fmt.Sprintf("Restore for %s completed. %d succeeded, %d failed", userDetails.Email, len(result.ProcessedIDs), len(result.FailedIDs)), &compPriority, compData, nil)

	return c.JSON(http.StatusOK, result)
}

// --- Google token (encrypted payload): connect returns it; job create and domain-users consume it ---
type googleTokenPayload struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Email        string `json:"email,omitempty"`
	AccountType  string `json:"account_type,omitempty"`
}

const (
	googleTokenEncryptedPrefix = "gts1."
	googleAuthScheme           = "Bearer "
)

// getGoogleTokenFromRequest returns the token from Authorization: Bearer <token>. Scheme is standard; token is our encrypted value, not JWT.
func getGoogleTokenFromRequest(c echo.Context) (string, error) {
	h := c.Request().Header.Get("Authorization")
	if len(h) < len(googleAuthScheme) || h[:len(googleAuthScheme)] != googleAuthScheme {
		return "", errors.New("Google token required in Authorization header (Bearer <token>)")
	}
	token := strings.TrimSpace(h[len(googleAuthScheme):])
	if token == "" {
		return "", errors.New("Google token required in Authorization header (Bearer <token>)")
	}
	return token, nil
}

func getGoogleTokenEncryptionKey() ([]byte, error) {
	secret := utils.GetEnvWithKey("GOOGLE_TOKEN_SECRET")
	if secret == "" {
		return nil, errors.New("GOOGLE_TOKEN_SECRET not set")
	}
	h := sha256.Sum256([]byte(secret))
	return h[:], nil
}

func encryptGoogleTokenPayload(p *googleTokenPayload) (string, error) {
	key, err := getGoogleTokenEncryptionKey()
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return googleTokenEncryptedPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func decryptGoogleTokenPayload(token string) (*googleTokenPayload, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, googleTokenEncryptedPrefix) {
		return nil, nil
	}
	key, err := getGoogleTokenEncryptionKey()
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, googleTokenEncryptedPrefix))
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(b) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, b[:nonceSize], b[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed: %w", err)
	}
	var p googleTokenPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.AccessToken == "" {
		return nil, errors.New("decrypted payload invalid or missing access_token")
	}
	return &p, nil
}

// parseGoogleToken returns access_token and refresh_token from raw token (encrypted gts1.* or base64 JSON).
func parseGoogleToken(token string) (accessToken, refreshToken string, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", errors.New("empty token")
	}
	if p, err := decryptGoogleTokenPayload(token); err == nil && p != nil {
		return p.AccessToken, p.RefreshToken, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(token)
	}
	if err == nil && len(decoded) > 0 {
		var p googleTokenPayload
		if json.Unmarshal(decoded, &p) == nil && p.AccessToken != "" {
			return p.AccessToken, p.RefreshToken, nil
		}
	}
	return token, "", nil
}

// resolveAccessToken returns a valid access token; if current is expired and refreshToken is set, refreshes.
func resolveAccessToken(accessToken, refreshToken string) (string, error) {
	if _, err := google.GetGoogleAccountDetailsFromAccessToken(accessToken); err == nil {
		return accessToken, nil
	}
	if refreshToken == "" {
		return "", errors.New("access token invalid and no refresh token")
	}
	return google.AuthTokenUsingRefreshToken(refreshToken)
}

// GetGoogleCredentialsFromRequest decrypts token from request (from connect) and returns email, accessToken, refreshToken. Used by job create.
func GetGoogleCredentialsFromRequest(c echo.Context) (email, accessToken, refreshToken string, err error) {
	token, err := getGoogleTokenFromRequest(c)
	if err != nil {
		return "", "", "", err
	}
	p, err := decryptGoogleTokenPayload(token)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid or expired Google token: %w", err)
	}
	if p == nil {
		return "", "", "", errors.New("invalid or expired Google token")
	}
	return p.Email, p.AccessToken, p.RefreshToken, nil
}

// HandleGoogleConnect exchanges OAuth code for tokens, encrypts them, returns only the token. UI uses it for job create and domain-users.
func HandleGoogleConnect(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "code is required in request body"})
	}
	tok, err := google.ExchangeCodeForTokenWithAdminScope(req.Code)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid or expired code; please sign in with Google again"})
	}
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
	if err != nil || userDetails.Email == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Could not get Google account details from token"})
	}
	encrypted, err := encryptGoogleTokenPayload(&googleTokenPayload{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Email:        userDetails.Email,
	})
	if err != nil {
		logger.Error(ctx, "Failed to encrypt Google token", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Server configuration error (GOOGLE_TOKEN_SECRET required)"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"token": encrypted})
}

// HandleGmailCorporateDomainUsers uses token from Authorization header. Personal/employee: account_type + email. Admin: + domain users with per-account email counts.
func HandleGmailCorporateDomainUsers(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	token, err := getGoogleTokenFromRequest(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": err.Error()})
	}
	accessToken, refreshToken, err := parseGoogleToken(token)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Invalid token format"})
	}
	accessToken, err = resolveAccessToken(accessToken, refreshToken)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Could not get Google account details; please reconnect"})
	}
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(accessToken)
	if err != nil || userDetails.Email == "" {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Could not get Google account details; please reconnect"})
	}

	accountType := "personal"
	if adminOK, adminErr := google.IsUserAdmin(ctx, accessToken, userDetails.Email); adminErr == nil && adminOK {
		accountType = "admin_workspace"
	} else if adminErr == nil {
		accountType = "employee_workspace"
	}

	if accountType != "admin_workspace" {
		return c.JSON(http.StatusOK, map[string]interface{}{"account_type": accountType, "email": userDetails.Email})
	}

	domain := google.ExtractDomainFromEmail(userDetails.Email)
	resp := gmailCorporateAdminResponse{
		Account:     userDetails.Email,
		AccountType: accountType,
		Count:       0,
		Grouped: gmailGroupedEmails{
			AdminEmail:      userDetails.Email,
			EmailCount:      0,
			ConnectedEmails: []gmailAccountCount{},
		},
	}
	if domain == "" {
		return c.JSON(http.StatusOK, resp)
	}

	users, err := google.ListAllDomainUsers(ctx, accessToken, domain)
	if err != nil {
		logger.Warn(ctx, "List domain users failed", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list domain users"})
	}
	gmailClient, err := google.NewGmailClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to create Gmail client"})
	}

	accounts := make([]gmailAccountCount, 0, len(users))
	connectedAccounts := make([]gmailAccountCount, 0, len(users))
	adminCount, adminCountErr := gmailClient.GetUserMessageCount(userDetails.Email)
	if adminCountErr != nil {
		logger.Warn(ctx, "Get message count failed for admin user", logger.String("email", userDetails.Email), logger.ErrorField(adminCountErr))
	}
	for _, email := range users {
		if email == userDetails.Email {
			continue
		}
		count, countErr := gmailClient.GetUserMessageCount(email)
		if countErr != nil {
			logger.Warn(ctx, "Get message count failed for user", logger.String("email", email), logger.ErrorField(countErr))
		}
		entry := gmailAccountCount{Email: email, EmailCount: count}
		accounts = append(accounts, entry)
		connectedAccounts = append(connectedAccounts, entry)
	}
	resp.Count = len(accounts)
	resp.Grouped.EmailCount = adminCount
	resp.Grouped.ConnectedEmails = connectedAccounts
	return c.JSON(http.StatusOK, resp)
}
