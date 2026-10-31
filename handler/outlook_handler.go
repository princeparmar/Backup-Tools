package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
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
	accessGrant string
	accessToken string
}

// NewOutlookService creates a new OutlookService instance
func NewOutlookService(accessGrant, accessToken string) *OutlookService {
	return &OutlookService{
		accessGrant: accessGrant,
		accessToken: accessToken,
	}
}

// DownloadMessagesFromSatellite downloads messages from Satellite and restores them into Outlook
// via the same folder-aware path used by official restore jobs.
func (s *OutlookService) DownloadMessagesFromSatellite(ctx context.Context, keys []string) (*DownloadResult, error) {
	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	userBase, err := s.restoreUserBase()
	if err != nil {
		return nil, err
	}
	download := func(k string) ([]byte, error) {
		return satellite.DownloadObject(ctx, s.accessGrant, satellite.ReserveBucket_Outlook, k)
	}

	for _, key := range keys {
		if key == "" {
			continue
		}

		var restoreErr error
		if outlook.IsOutlookMailBackupKey(key) {
			restoreErr = outlook.RestoreMailFromBackup(ctx, s.accessToken, userBase, key, download)
		} else {
			// Manual / scheduled flat keys: restore into Inbox with MAPI received appearance.
			data, err := download(key)
			if err != nil {
				logger.Error(ctx, "error downloading message from satellite",
					logger.ErrorField(err), logger.String("key", key))
				failedIDs.Add(key)
				continue
			}
			restoreErr = outlook.RestoreMailMessage(ctx, s.accessToken, userBase, outlook.MailRestoreFolder{}, data)
		}
		if restoreErr != nil {
			logger.Error(ctx, "error restoring outlook message", logger.ErrorField(restoreErr), logger.String("key", key))
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

// restoreUserBase is the signed-in mailbox. Manual restore writes /me, the same mailbox the
// Graph token belongs to. Folder caches are already scoped by token, so /users/{mail} is unnecessary.
func (s *OutlookService) restoreUserBase() (string, error) {
	return outlook.UserBaseURL("", "", "", false)
}

// getAccessTokens extracts StorX grant + Microsoft Graph token.
// Authorization may be a Backup-Tools microsoft-auth JWT (from POST /microsoft-auth) or a raw Graph access token.
func getAccessTokens(c echo.Context) (accessGrant, accessToken string, err error) {
	accessGrant = c.Request().Header.Get("ACCESS_TOKEN")
	authHeader := c.Request().Header.Get("Authorization")

	if accessGrant == "" || authHeader == "" {
		return "", "", echo.NewHTTPError(http.StatusForbidden, "ACCESS_TOKEN and Authorization headers are required")
	}

	accessToken, err = outlook.ResolveGraphAccessToken(c, authHeader)
	if err != nil {
		return "", "", echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
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

	if len(ids) > microsoftManualRestoreMaxKeys {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "maximum 10 keys allowed")
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
	if microsoftManualKeysNeedTenantCheck(allIDs) {
		if _, err := checkMicrosoftManualRestoreKeys(c, accessToken, allIDs); err != nil {
			return microsoftManualRestoreHTTPError(c, err)
		}
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
	note := &microsoftManualRestoreSession{loginID: userDetails.Mail, userID: userID}
	notifyMicrosoftRestore(ctx, note, "outlook", "started", len(allIDs), nil, nil)

	// Restore into the signed-in mailbox (/me), same as calendar, contacts, and OneDrive.
	outlookService := NewOutlookService(accessGrant, accessToken)
	result, err := outlookService.DownloadMessagesFromSatellite(c.Request().Context(), allIDs)
	if err != nil {
		notifyMicrosoftRestore(context.Background(), note, "outlook", "failed", len(allIDs), nil, err)
		failed, processed := []string{}, []string{}
		if result != nil {
			failed, processed = result.FailedIDs, result.ProcessedIDs
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failed,
			"processed_ids": processed,
		})
	}

	notifyMicrosoftRestore(ctx, note, "outlook", "completed", len(allIDs), result, nil)
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

	logger.Info(ctx, "Returning Microsoft OAuth URL to client",
		logger.String("auth_url", authURL),
		logger.String("request_host", c.Request().Host),
		logger.String("request_url", c.Request().URL.String()),
	)

	// Return URL as JSON instead of redirecting server-side
	// This prevents proxy from rewriting the Microsoft OAuth URL
	return c.JSON(http.StatusOK, map[string]interface{}{
		"auth_url": authURL,
		"message":  "Redirect to this URL for Microsoft OAuth",
	})
}
