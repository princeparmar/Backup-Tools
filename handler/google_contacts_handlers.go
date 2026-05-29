package handler

import (
	"context"
	"net/http"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/labstack/echo/v4"
)

// HandleListContacts lists Google contacts with pagination (same logic as cron).
func HandleListContacts(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	go func() {
		processCtx := context.Background()
		if processErr := ProcessWebhookEvents(processCtx, database, accesGrant, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from contacts listing route", logger.ErrorField(processErr))
		}
	}()

	pageToken := strings.TrimSpace(c.QueryParam("nextPageToken"))
	if pageToken == "" {
		pageToken = strings.TrimSpace(c.QueryParam("pageToken"))
	}
	if pageToken == "" {
		pageToken = strings.TrimSpace(c.QueryParam("page_token"))
	}

	resp, err := google.ListAllContactsFlat(c, pageToken)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "token expired"})
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	}

	userDetails, err := google.GetGoogleAccountDetailsFromContext(c)
	if err != nil || strings.TrimSpace(userDetails.Email) == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "failed to get user email",
		})
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
	}

	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Contacts, "google", "contacts")
	if err != nil {
		logger.Warn(ctx, "Failed to get synced objects from database", logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}
	syncedMap := make(map[string]bool, len(syncedObjects))
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	for i := range resp.Contacts {
		item := &resp.Contacts[i]
		item.Synced = google.IsContactSynced(syncedMap, userDetails.Email, item.ID)
	}

	return c.JSON(http.StatusOK, resp)
}
