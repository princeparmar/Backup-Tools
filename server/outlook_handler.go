package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/utils"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

type OutlookMessageListJSON struct {
	outlook.OutlookMinimalMessage
	Synced bool `json:"synced"`
}

func handleOutlookGetMessages(c echo.Context) error {

	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	accessToken := c.Request().Header.Get("Authorization")
	if accessToken == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	skip, _ := strconv.Atoi(c.QueryParam("offset"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))

	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
	}

	messages, err := client.GetUserMessages(int32(skip), int32(limit))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	emailListFromBucket, err := satellite.ListObjectsWithPrefix(context.Background(),
		accessGrant, satellite.ReserveBucket_Outlook, userDetails.Mail+"/")
	if err != nil && !strings.Contains(err.Error(), "object not found") {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	outlookMessages := make([]*OutlookMessageListJSON, 0, len(messages))
	for _, message := range messages {
		_, synced := emailListFromBucket[userDetails.Mail+"/"+utils.GenerateTitleFromOutlookMessage(message)]
		outlookMessages = append(outlookMessages, &OutlookMessageListJSON{OutlookMinimalMessage: *message, Synced: synced})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"messages": outlookMessages,
	})
}

func handleOutlookGetMessageById(c echo.Context) error {

	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	accessToken := c.Request().Header.Get("Authorization")
	if accessToken == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	msgID := c.Param("id")

	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
	}

	message, err := client.GetMessage(msgID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": message,
	})
}

func handleListOutlookMessagesToSatellite(c echo.Context) error {
	// Get access tokens
	accessGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accessGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	accessToken := c.Request().Header.Get("Authorization")
	if accessToken == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	// Parse message IDs from request
	var allIDs []string
	if strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		if err := json.NewDecoder(c.Request().Body).Decode(&allIDs); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "invalid JSON format",
			})
		}
	} else {
		formIDs := c.FormValue("ids")
		allIDs = strings.Split(formIDs, ",")
	}

	// Create Outlook client
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Get user details
	userDetails, err := client.GetCurrentUser()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Create error group for concurrent processing
	g, ctx := errgroup.WithContext(c.Request().Context())
	g.SetLimit(10)

	processedIDs, failedIDs := utils.NewLockedArray(), utils.NewLockedArray()

	// Process each message
	for _, id := range allIDs {
		func(id string) {
			g.Go(func() error {
				// Get full message with attachments
				msg, err := client.GetMessage(id)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				// Marshal message to JSON
				b, err := json.Marshal(msg)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				// Create message path
				messagePath := userDetails.Mail + "/" + utils.GenerateTitleFromOutlookMessage(&msg.OutlookMinimalMessage)

				// Upload to Satellite
				err = satellite.UploadObject(ctx, accessGrant, satellite.ReserveBucket_Outlook, messagePath, b)
				if err != nil {
					failedIDs.Add(id)
					return nil
				}

				processedIDs.Add(id)
				return nil
			})
		}(id)
	}

	message := "all mails were successfully uploaded from Outlook to Satellite"
	if failedIDs.Get() != nil {
		message = "some mails were not uploaded from Outlook to Satellite"
	}

	if err := g.Wait(); err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":         err.Error(),
			"failed_ids":    failedIDs.Get(),
			"processed_ids": processedIDs.Get(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       message,
		"failed_ids":    failedIDs.Get(),
		"processed_ids": processedIDs.Get(),
	})
}
