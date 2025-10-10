package handler

import (
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

func HandleOutlookGetMessages(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessGrant, accessToken, err := getAccessTokens(c)
	if err != nil {
		return err
	}

	skip, _ := strconv.Atoi(c.QueryParam("offset"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))

	client, err := createOutlookClient(accessToken)
	if err != nil {
		return err
	}

	messages, err := client.GetUserMessages(int32(skip), int32(limit))
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
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		logger.Error(ctx, "Failed to list objects from satellite", logger.ErrorField(err))
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}

	outlookMessages := make([]*OutlookMessageListJSON, 0, len(messages))
	for _, msg := range messages {
		message := &utils.OutlookMinimalMessage{
			ID:               msg.ID,
			Subject:          msg.Subject,
			From:             msg.From,
			ReceivedDateTime: msg.ReceivedDateTime,
		}
		_, synced := emailListFromBucket[userDetails.Mail+"/"+utils.GenerateTitleFromOutlookMessage(message)]
		outlookMessages = append(outlookMessages, &OutlookMessageListJSON{
			OutlookMinimalMessage: *message,
			Synced:                synced,
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"messages": outlookMessages,
	})
}

func HandleOutlookGetMessageById(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// FIX: Use blank identifier for unused accessGrant
	_, accessToken, err := getAccessTokens(c)
	if err != nil {
		return err
	}

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

func HandleOutlookDownloadAndInsert(c echo.Context) error {
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

	if len(allIDs) == 0 || allIDs[0] == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "no keys provided")
	}
	if len(allIDs) > 10 {
		return echo.NewHTTPError(http.StatusBadRequest, "maximum 10 keys allowed")
	}

	client, err := createOutlookClient(accessToken)
	if err != nil {
		return err
	}

	return processMessagesConcurrently(c, allIDs, func(echoCtx echo.Context, key string) error {
		// FIX: Use the echo context parameter
		reqCtx := echoCtx.Request().Context()
		userDetails, err := client.GetCurrentUser()
		if err != nil {
			logger.Error(reqCtx, "Failed to get user details", logger.ErrorField(err))
			return err
		}

		msg, err := client.GetMessage(key)
		if err != nil {
			logger.Error(reqCtx, "Failed to get message details for key generation",
				logger.ErrorField(err), logger.String("key", key))
			return err
		}

		message := &utils.OutlookMinimalMessage{
			ID:               msg.ID,
			Subject:          msg.Subject,
			From:             msg.From,
			ReceivedDateTime: msg.ReceivedDateTime,
		}

		satelliteKey := userDetails.Mail + "/" + utils.GenerateTitleFromOutlookMessage(message)
		data, err := satellite.DownloadObject(reqCtx, accessGrant, satellite.ReserveBucket_Outlook, satelliteKey)
		if err != nil {
			logger.Error(reqCtx, "error downloading message from satellite",
				logger.ErrorField(err), logger.String("key", key))
			return err
		}

		var outlookMsg outlook.OutlookMessage
		if err := json.Unmarshal(data, &outlookMsg); err != nil {
			logger.Error(reqCtx, "error unmarshalling message data",
				logger.ErrorField(err), logger.String("key", key))
			return err
		}

		_, err = client.InsertMessage(&outlookMsg)
		if err != nil {
			logger.Error(reqCtx, "error inserting message into Outlook",
				logger.ErrorField(err), logger.String("key", key))
			return err
		}

		return nil
	})
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
