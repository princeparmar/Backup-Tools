package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
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

		// URL decode
		if urlDecoded, err := url.QueryUnescape(ids[i]); err == nil {
			ids[i] = urlDecoded
		}

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

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(ctx,
		accessGrant, satellite.ReserveBucket_Outlook, userDetails.Mail+"/")
	if err != nil {
		logger.Error(ctx, "Failed to list objects from satellite", logger.ErrorField(err))
		userFriendlyError := satellite.FormatSatelliteError(err)
		return echo.NewHTTPError(http.StatusForbidden, userFriendlyError)
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
		err = satellite.UploadObject(reqCtx, accessGrant, satellite.ReserveBucket_Outlook, messagePath, b)
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

	// Create Outlook service and download messages
	outlookService := NewOutlookService(outlookClient, accessGrant, "")
	result, err := outlookService.DownloadMessagesFromSatellite(c.Request().Context(), allIDs)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    result.FailedIDs,
			"processed_ids": result.ProcessedIDs,
		})
	}

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

	authURL, err := outlook.BuildAuthURL()
	if err != nil {
		logger.Error(ctx, "Failed to build Outlook auth URL", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": "Failed to build authorization URL: " + err.Error(),
		})
	}

	return c.Redirect(http.StatusTemporaryRedirect, authURL)
}
