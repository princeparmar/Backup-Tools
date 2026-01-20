package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

type OutlookMessageListJSON struct {
	utils.OutlookMinimalMessage
	Synced bool `json:"synced"`
}

// OutlookService provides consolidated Outlook operations
type OutlookService struct {
	client      *outlook.OutlookClient
	accessGrant string
	userEmail   string
}

// NewOutlookService creates a new OutlookService instance
func NewOutlookService(client *outlook.OutlookClient, accessGrant, userEmail string) *OutlookService {
	return &OutlookService{
		client:      client,
		accessGrant: accessGrant,
		userEmail:   userEmail,
	}
}

// DownloadMessagesFromSatellite downloads messages from Satellite and inserts them into Outlook
func (s *OutlookService) DownloadMessagesFromSatellite(ctx context.Context, keys []string) (*DownloadResult, error) {
	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, key := range keys {
		if key == "" {
			continue
		}

		// Download file from Satellite using the key directly
		data, err := satellite.DownloadObject(ctx, s.accessGrant, satellite.ReserveBucket_Outlook, key)
		if err != nil {
			logger.Error(ctx, "error downloading message from satellite",
				logger.ErrorField(err), logger.String("key", key))
			failedIDs.Add(key)
			continue
		}

		// Parse the email data and insert into Outlook
		var outlookMsg outlook.OutlookMessage
		if err := json.Unmarshal(data, &outlookMsg); err != nil {
			logger.Error(ctx, "error unmarshalling message data",
				logger.ErrorField(err), logger.String("key", key))
			failedIDs.Add(key)
			continue
		}

		// Insert message into Outlook
		if _, err := s.client.InsertMessage(&outlookMsg); err != nil {
			logger.Error(ctx, "error inserting message into Outlook",
				logger.ErrorField(err), logger.String("key", key))
			failedIDs.Add(key)
		} else {
			processedIDs.Add(key)
		}
	}

	return &DownloadResult{
		ProcessedIDs: processedIDs.Get(),
		FailedIDs:    failedIDs.Get(),
		Message:      "all outlook messages processed",
	}, nil
}

// getAccessTokens extracts and validates access tokens from request
func getAccessTokens(c echo.Context) (accessGrant, accessToken string, err error) {
	accessGrant = c.Request().Header.Get("ACCESS_TOKEN")
	accessToken = c.Request().Header.Get("Authorization")

	if accessGrant == "" || accessToken == "" {
		return "", "", echo.NewHTTPError(http.StatusForbidden, "ACCESS_TOKEN and Authorization headers are required")
	}

	// Remove "Bearer " prefix if present
	accessToken = strings.TrimPrefix(accessToken, "Bearer ")
	return accessGrant, accessToken, nil
}

// parseMessageIDs parses message IDs from request body or form
func parseMessageIDs(c echo.Context) ([]string, error) {
	var ids []string

	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		if err := json.NewDecoder(c.Request().Body).Decode(&ids); err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid JSON format")
		}
	} else {
		formIDs := c.FormValue("ids")
		ids = strings.Split(formIDs, ",")
	}

	// Clean and decode IDs
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])

		// Base64 decode
		if decoded, err := base64.StdEncoding.DecodeString(ids[i]); err == nil {
			ids[i] = string(decoded)
		}
	}

	return ids, nil
}

// createOutlookClient creates a new Outlook client using the access token
func createOutlookClient(accessToken string) (*outlook.OutlookClient, error) {
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return client, nil
}

// DecodeOutlookURLFilter decodes a URL-encoded JSON filter parameter and returns an OutlookFilter
func DecodeOutlookURLFilter(urlEncodedFilter string) (*outlook.OutlookFilter, error) {
	var filter outlook.OutlookFilter
	if err := decodeFilterJSON(urlEncodedFilter, &filter); err != nil {
		return nil, err
	}
	return &filter, nil
}

func HandleOutlookGetMessages(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessGrant, accessToken, err := getAccessTokens(c)
	if err != nil {
		return err
	}

	// Extract access grant early for webhook processing
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if accessGrant != "" {
		go func() {
			processCtx := context.Background()
			if processErr := ProcessWebhookEvents(processCtx, database, accessGrant, 100); processErr != nil {
				logger.Warn(processCtx, "Failed to process webhook events from listing route",
					logger.ErrorField(processErr))
			}
		}()
	}

	skip, _ := strconv.Atoi(c.QueryParam("skip"))
	limit, _ := strconv.Atoi(c.QueryParam("num"))

	// Parse filter from JWT-encoded query parameter
	var filter *outlook.OutlookFilter
	if filterParam := c.QueryParam("filter"); filterParam != "" {
		decodedFilter, err := DecodeOutlookURLFilter(filterParam)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid filter parameter: " + err.Error(),
			})
		}
		filter = decodedFilter
	}

	client, err := createOutlookClient(accessToken)
	if err != nil {
		return err
	}

	messages, err := client.GetUserMessagesControlled(int32(skip), int32(limit), filter)
	if err != nil {
		logger.Error(ctx, "Failed to get user messages from Outlook", logger.ErrorField(err))
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil {
		logger.Error(ctx, "Failed to get current user details", logger.ErrorField(err))
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return echo.NewHTTPError(http.StatusUnauthorized, "authentication failed")
	}

	// Get synced objects from database instead of listing from Satellite
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Outlook, "outlook", "outlook")
	if err != nil {
		logger.Warn(ctx, "Failed to get synced objects from database, continuing with empty map",
			logger.String("user_id", userID),
			logger.String("bucket", satellite.ReserveBucket_Outlook),
			logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}

	// Create map for fast lookup (same format as ListObjectsWithPrefix returns)
	emailListFromBucket := make(map[string]bool)
	for _, obj := range syncedObjects {
		emailListFromBucket[obj.ObjectKey] = true
	}

	outlookMessages := make([]*OutlookMessageListJSON, 0, len(messages.Messages))
	for _, msg := range messages.Messages {
		message := &utils.OutlookMinimalMessage{
			ID:               msg.ID,
			Subject:          msg.Subject,
			From:             msg.From,
			ReceivedDateTime: msg.ReceivedDateTime,
			IsRead:           msg.IsRead,
			HasAttachments:   msg.HasAttachments,
		}
		_, synced := emailListFromBucket[userDetails.Mail+"/"+utils.GenerateTitleFromOutlookMessage(message)]
		outlookMessages = append(outlookMessages, &OutlookMessageListJSON{
			OutlookMinimalMessage: *message,
			Synced:                synced,
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"messages":       outlookMessages,
		"skip":           messages.Skip,
		"limit":          messages.Limit,
		"total_count":    messages.TotalCount,
		"response_count": messages.ResponseCount,
		"has_more":       messages.HasMore,
	})
}

func HandleOutlookGetMessageById(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken := c.Request().Header.Get("Authorization")

	msgID := c.Param("id")
	client, err := createOutlookClient(accessToken)
	if err != nil {
		return err
	}

	message, err := client.GetMessage(msgID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": message,
	})
}

func HandleListOutlookMessagesToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessGrant, accessToken, err := getAccessTokens(c)
	if err != nil {
		return err
	}

	allIDs, err := parseMessageIDs(c)
	if err != nil {
		return err
	}

	client, err := createOutlookClient(accessToken)
	if err != nil {
		return err
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil {
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}

	// Get database and userID for syncing
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "authentication failed")
	}

	return processMessagesConcurrently(c, allIDs, func(echoCtx echo.Context, id string) error {
		// FIX: Use the echo context parameter
		reqCtx := echoCtx.Request().Context()
		msg, err := client.GetMessage(id)
		if err != nil {
			logger.Error(reqCtx, "Failed to get message from Outlook",
				logger.ErrorField(err), logger.String("id", id))
			return err
		}

		b, err := json.Marshal(msg)
		if err != nil {
			logger.Error(reqCtx, "Failed to marshal message to JSON",
				logger.ErrorField(err), logger.String("id", id))
			return err
		}

		message := &utils.OutlookMinimalMessage{
			ID:               msg.ID,
			Subject:          msg.Subject,
			From:             msg.From,
			ReceivedDateTime: msg.ReceivedDateTime,
		}

		messagePath := userDetails.Mail + "/" + utils.GenerateTitleFromOutlookMessage(message)
		err = UploadObjectAndSync(reqCtx, database, accessGrant, satellite.ReserveBucket_Outlook, messagePath, b, userID)
		if err != nil {
			logger.Error(reqCtx, "Failed to upload message to satellite",
				logger.ErrorField(err), logger.String("id", id), logger.String("path", messagePath))
			return err
		}

		return nil
	})
}

// HandleOutlookDownloadAndInsert - downloads emails from Satellite and inserts them into Outlook.
// It uses pagination to download emails in chunks of 10.
func HandleOutlookDownloadAndInsert(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Get access token from header
	accessGrant, accessToken, err := getAccessTokens(c)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// Validate and process request IDs
	allIDs, err := parseMessageIDs(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Get Outlook client
	outlookClient, err := createOutlookClient(accessToken)
	if err != nil {
		return err
	}

	// Get user details for notification
	userDetails, err := outlookClient.GetCurrentUser()
	if err != nil {
		logger.Warn(ctx, "Failed to get user details for notification", logger.ErrorField(err))
		userDetails = &outlook.OutlookUser{}
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to get userID from Satellite service", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
	}

	// Send start notification
	priority := "normal"
	startData := map[string]interface{}{
		"event":      "outlook_restore_started",
		"level":      2,
		"login_id":   userDetails.Mail,
		"method":     "outlook",
		"type":       "restore",
		"timestamp":  "now",
		"item_count": len(allIDs),
	}
	satellite.SendNotificationAsync(ctx, userID, "Outlook Restore Started", fmt.Sprintf("Restore of %d messages for %s has started", len(allIDs), userDetails.Mail), &priority, startData, nil)

	// Create Outlook service and download messages
	outlookService := NewOutlookService(outlookClient, accessGrant, "")
	result, err := outlookService.DownloadMessagesFromSatellite(c.Request().Context(), allIDs)
	if err != nil {
		// Send failure notification
		failPriority := "high"
		failData := map[string]interface{}{
			"event":     "outlook_restore_failed",
			"level":     4,
			"login_id":  userDetails.Mail,
			"method":    "outlook",
			"type":      "restore",
			"timestamp": "now",
			"error":     err.Error(),
		}
		satellite.SendNotificationAsync(context.Background(), userID, "Outlook Restore Failed", fmt.Sprintf("Restore for %s failed: %v", userDetails.Mail, err), &failPriority, failData, nil)

		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    result.FailedIDs,
			"processed_ids": result.ProcessedIDs,
		})
	}

	// Send completion notification
	compPriority := "normal"
	compData := map[string]interface{}{
		"event":           "outlook_restore_completed",
		"level":           2,
		"login_id":        userDetails.Mail,
		"method":          "outlook",
		"type":            "restore",
		"timestamp":       "now",
		"processed_count": len(result.ProcessedIDs),
		"failed_count":    len(result.FailedIDs),
	}
	satellite.SendNotificationAsync(ctx, userID, "Outlook Restore Completed", fmt.Sprintf("Restore for %s completed. %d succeeded, %d failed", userDetails.Mail, len(result.ProcessedIDs), len(result.FailedIDs)), &compPriority, compData, nil)

	return c.JSON(http.StatusOK, result)
}

// processMessagesConcurrently handles concurrent message processing with error tracking
func processMessagesConcurrently(c echo.Context, ids []string, processor func(echo.Context, string) error) error {
	g, _ := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	for _, id := range ids {
		if id == "" {
			continue
		}

		id := id
		g.Go(func() error {
			if err := processor(c, id); err != nil {
				failedIDs.Add(id)
				return nil // Don't return error to continue processing other messages
			}
			processedIDs.Add(id)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	message := "all messages processed successfully"
	if len(failedIDs.Get()) > 0 {
		message = "some messages failed to process"
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       message,
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}

func HandleMicrosoftAuthRedirect(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	authURL, err := outlook.BuildAuthURL(ctx)
	if err != nil {
		logger.Error(ctx, "Failed to build Outlook auth URL", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": "Failed to build authorization URL: " + err.Error(),
		})
	}

	// Validate that the URL is absolute and points to Microsoft's OAuth endpoint
	parsedURL, err := url.Parse(authURL)
	if err != nil {
		logger.Error(ctx, "Invalid auth URL format", logger.ErrorField(err), logger.String("auth_url", authURL))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": "Invalid authorization URL format",
		})
	}

	// Ensure the URL points to Microsoft's OAuth endpoint, not our own server
	if parsedURL.Host != "login.microsoftonline.com" {
		logger.Error(ctx, "Auth URL host mismatch", 
			logger.String("expected_host", "login.microsoftonline.com"),
			logger.String("actual_host", parsedURL.Host),
			logger.String("auth_url", authURL))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": "Invalid authorization URL host",
		})
	}

	// Use http.Redirect directly to ensure the Location header is set correctly
	// and not modified by any middleware or proxy
	c.Response().Header().Set("Location", authURL)
	c.Response().WriteHeader(http.StatusTemporaryRedirect)
	return nil
}
