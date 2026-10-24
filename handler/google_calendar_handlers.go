package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	googlestore "github.com/StorX2-0/Backup-Tools/restore/google"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
)

// HandleListCalendars lists Google calendars (same API as cron).
func HandleListCalendars(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	resp, err := google.ListCalendarsFlat(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "token expired"})
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, resp)
}

// HandleListCalendarEvents lists events for one calendar with pagination (same helper as cron).
func HandleListCalendarEvents(c echo.Context) error {
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
			logger.Warn(processCtx, "Failed to process webhook events from calendar listing route", logger.ErrorField(processErr))
		}
	}()

	calendarID := strings.TrimSpace(c.Param("calendarId"))
	if calendarID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "calendarId is required",
		})
	}

	pageToken := strings.TrimSpace(c.QueryParam("nextPageToken"))
	if pageToken == "" {
		pageToken = strings.TrimSpace(c.QueryParam("pageToken"))
	}
	if pageToken == "" {
		pageToken = strings.TrimSpace(c.QueryParam("page_token"))
	}
	syncToken := strings.TrimSpace(c.QueryParam("syncToken"))

	service, err := google.NewCalendarServiceFromContext(c)
	if err != nil {
		if err.Error() == "token error" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "token expired"})
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	}

	page, err := google.ListCalendarEventsWithService(service, calendarID, pageToken, syncToken)
	if err != nil {
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

	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, satellite.ReserveBucket_Calendar, "google", "calendar")
	if err != nil {
		logger.Warn(ctx, "Failed to get synced calendar objects from database", logger.ErrorField(err))
		syncedObjects = []repo.SyncedObject{}
	}
	syncedMap := make(map[string]bool, len(syncedObjects))
	for _, obj := range syncedObjects {
		syncedMap[obj.ObjectKey] = true
	}

	for i := range page.Events {
		item := &page.Events[i]
		item.Synced = google.IsCalendarEventSynced(syncedMap, userDetails.Email, calendarID, item.ID, "", item.Summary)
	}

	return c.JSON(http.StatusOK, page)
}

// HandleGoogleCalendarRestore downloads calendar events from Satellite and inserts them into Google Calendar.
func HandleGoogleCalendarRestore(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	allKeys, err := validateAndProcessRequestIDs(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": err.Error(),
		})
	}

	service, err := google.NewCalendarServiceFromContext(c)
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

	storxSess, err := resolveManualRestoreStorx(c, "google_calendar", userDetails.Email)
	if err != nil {
		return err
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
	}

	priority := "normal"
	startData := map[string]interface{}{
		"event":      "google_calendar_restore_started",
		"level":      2,
		"login_id":   userDetails.Email,
		"method":     "google_calendar",
		"type":       "restore",
		"timestamp":  "now",
		"item_count": len(allKeys),
	}
	satellite.SendNotificationAsync(ctx, userID, "Google Calendar Restore Started", fmt.Sprintf("Restore of %d events for %s has started", len(allKeys), userDetails.Email), &priority, startData, nil)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10)
	processedKeys, failedKeys := utils.NewLockedArray(), utils.NewLockedArray()

	for _, key := range allKeys {
		key := key
		if !google.IsCalendarEventRestoreObjectKey(key) {
			failedKeys.Add(key)
			continue
		}
		g.Go(func() error {
			if restoreErr := googlestore.RestoreCalendarKeyWithSession(ctx, storxSess, service, key); restoreErr != nil {
				logger.Warn(ctx, "Failed to restore calendar event", logger.String("key", key), logger.ErrorField(restoreErr))
				failedKeys.Add(key)
			} else {
				processedKeys.Add(key)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		failPriority := "high"
		failData := map[string]interface{}{
			"event": "google_calendar_restore_failed", "level": 4, "login_id": userDetails.Email,
			"method": "google_calendar", "type": "restore", "timestamp": "now", "error": err.Error(),
		}
		satellite.SendNotificationAsync(context.Background(), userID, "Google Calendar Restore Failed", err.Error(), &failPriority, failData, nil)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(), "failed_keys": failedKeys.Get(), "processed_keys": processedKeys.Get(),
		})
	}

	compPriority := "normal"
	compData := map[string]interface{}{
		"event": "google_calendar_restore_completed", "level": 2, "login_id": userDetails.Email,
		"method": "google_calendar", "type": "restore", "timestamp": "now",
		"processed_count": len(processedKeys.Get()), "failed_count": len(failedKeys.Get()),
	}
	satellite.SendNotificationAsync(ctx, userID, "Google Calendar Restore Completed",
		fmt.Sprintf("Restore for %s completed. %d succeeded, %d failed", userDetails.Email, len(processedKeys.Get()), len(failedKeys.Get())),
		&compPriority, compData, nil)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":        "Google Calendar restore completed",
		"processed_keys": processedKeys.Get(),
		"failed_keys":    failedKeys.Get(),
	})
}
