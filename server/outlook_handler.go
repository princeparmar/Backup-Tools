package server

import (
	"net/http"
	"strconv"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/labstack/echo/v4"
)

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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"messages": messages,
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
