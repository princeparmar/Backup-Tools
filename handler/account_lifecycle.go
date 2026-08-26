package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/labstack/echo/v4"
)

// AccountLifecycleRequest is the Satellite → Backup-Tools body for account deletion lifecycle.
type AccountLifecycleRequest struct {
	SatelliteUserID string   `json:"satellite_user_id"`
	ProjectIDs      []string `json:"project_ids,omitempty"`
	DeleteAt        string   `json:"delete_at,omitempty"`
}

func requireBackupToolsAPIKey(c echo.Context) error {
	expected := strings.TrimSpace(utils.GetEnvWithKey("BACKUP_TOOLS_API_KEY"))
	if expected == "" {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "BACKUP_TOOLS_API_KEY not configured")
	}
	got := strings.TrimSpace(c.Request().Header.Get("X-API-Key"))
	if got == "" || got != expected {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or missing X-API-Key")
	}
	return nil
}

func parseAccountLifecycleRequest(c echo.Context) (*AccountLifecycleRequest, error) {
	var req AccountLifecycleRequest
	if err := c.Bind(&req); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid JSON body")
	}
	req.SatelliteUserID = strings.TrimSpace(req.SatelliteUserID)
	if req.SatelliteUserID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "satellite_user_id is required")
	}
	return &req, nil
}

func accountLifecycleDB(c echo.Context) (*db.PostgresDb, error) {
	database, ok := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if !ok || database == nil || database.AccountLifecycleRepo == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "database not available")
	}
	return database, nil
}

// RejectIfAccountPendingDelete returns 403 when the satellite user is tombstoned.
// Use on create/restore paths so grace-period accounts cannot enqueue new work.
func RejectIfAccountPendingDelete(c echo.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	database, ok := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if !ok || database == nil || database.AccountLifecycleRepo == nil {
		return nil
	}
	pending, err := database.AccountLifecycleRepo.IsPendingDelete(userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if pending {
		return echo.NewHTTPError(http.StatusForbidden, "account is scheduled for deletion; resume the account before creating backup or restore jobs")
	}
	return nil
}

// HandleAccountPendingDelete pauses all backup/restore for satellite_user_id and sets a tombstone.
// POST /internal/account/pending-delete
func HandleAccountPendingDelete(c echo.Context) error {
	if err := requireBackupToolsAPIKey(c); err != nil {
		return err
	}
	req, err := parseAccountLifecycleRequest(c)
	if err != nil {
		return err
	}
	database, err := accountLifecycleDB(c)
	if err != nil {
		return err
	}

	var deleteAt *time.Time
	if strings.TrimSpace(req.DeleteAt) != "" {
		t, parseErr := time.Parse(time.RFC3339, req.DeleteAt)
		if parseErr != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "delete_at must be RFC3339")
		}
		utc := t.UTC()
		deleteAt = &utc
	}

	if err := database.AccountLifecycleRepo.PendingDelete(req.SatelliteUserID, deleteAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":           true,
		"satellite_user_id": req.SatelliteUserID,
		"status":            "pending_delete",
	})
}

// HandleAccountResume clears the tombstone (jobs stay paused until user re-enables).
// POST /internal/account/resume
func HandleAccountResume(c echo.Context) error {
	if err := requireBackupToolsAPIKey(c); err != nil {
		return err
	}
	req, err := parseAccountLifecycleRequest(c)
	if err != nil {
		return err
	}
	database, err := accountLifecycleDB(c)
	if err != nil {
		return err
	}
	if err := database.AccountLifecycleRepo.Resume(req.SatelliteUserID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":           true,
		"satellite_user_id": req.SatelliteUserID,
		"status":            "resumed",
	})
}

// HandleAccountPurge hard-wipes Backup-Tools data for the satellite user (idempotent).
// POST /internal/account/purge
func HandleAccountPurge(c echo.Context) error {
	if err := requireBackupToolsAPIKey(c); err != nil {
		return err
	}
	req, err := parseAccountLifecycleRequest(c)
	if err != nil {
		return err
	}
	database, err := accountLifecycleDB(c)
	if err != nil {
		return err
	}
	if err := database.AccountLifecycleRepo.Purge(req.SatelliteUserID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":           true,
		"satellite_user_id": req.SatelliteUserID,
		"status":            "purged",
	})
}
