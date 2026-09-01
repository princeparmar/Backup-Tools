package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

const (
	backupRestoreLogTypeBackup     = "backup"
	backupRestoreLogTypeRestore    = "restore"
	backupRestoreLogsDefaultLimit  = 10
	backupRestoreLogsMaxLimit      = 100
)

var validBackupRestoreLogTypes = map[string]struct{}{
	backupRestoreLogTypeBackup:  {},
	backupRestoreLogTypeRestore: {},
}

var validBackupRestoreMessageStatuses = map[string]struct{}{
	repo.JobMessageStatusInfo:    {},
	repo.JobMessageStatusWarning: {},
	repo.JobMessageStatusError:   {},
}

// BackupRestoreLogsPagination is pagination metadata for GET /backup-restore/logs.
type BackupRestoreLogsPagination struct {
	Limit      int   `json:"limit"`
	Offset     int   `json:"offset"`
	TotalCount int64 `json:"total_count"`
}

// BackupRestoreLogsResponse is the success body for GET /backup-restore/logs.
type BackupRestoreLogsResponse struct {
	Message    string                        `json:"message"`
	Logs       []repo.BackupRestoreLogEntry  `json:"logs"`
	Pagination BackupRestoreLogsPagination   `json:"pagination"`
}

// parseBackupRestoreLogTypes parses types query param (default backup + restore).
func parseBackupRestoreLogTypes(raw string) (includeBackup, includeRestore bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true, true, nil
	}
	includeBackup = false
	includeRestore = false
	for _, part := range strings.Split(raw, ",") {
		t := strings.ToLower(strings.TrimSpace(part))
		if t == "" {
			continue
		}
		if _, ok := validBackupRestoreLogTypes[t]; !ok {
			return false, false, echo.NewHTTPError(http.StatusBadRequest, "invalid types value: "+t)
		}
		switch t {
		case backupRestoreLogTypeBackup:
			includeBackup = true
		case backupRestoreLogTypeRestore:
			includeRestore = true
		}
	}
	if !includeBackup && !includeRestore {
		return false, false, echo.NewHTTPError(http.StatusBadRequest, "types must include backup and/or restore")
	}
	return includeBackup, includeRestore, nil
}

func parseBackupRestoreLogsLimitOffset(c echo.Context) (limit, offset int) {
	limit = backupRestoreLogsDefaultLimit
	if q := strings.TrimSpace(c.QueryParam("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			if n > backupRestoreLogsMaxLimit {
				n = backupRestoreLogsMaxLimit
			}
			limit = n
		}
	}
	offset = 0
	if q := strings.TrimSpace(c.QueryParam("offset")); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

func backupRestoreLogsBadRequest(c echo.Context, err error) error {
	if he, ok := err.(*echo.HTTPError); ok {
		return c.JSON(he.Code, map[string]interface{}{
			"message": "Invalid Request",
			"error":   he.Message,
		})
	}
	return c.JSON(http.StatusBadRequest, map[string]interface{}{
		"message": "Invalid Request",
		"error":   err.Error(),
	})
}

// HandleBackupRestoreLogs returns merged backup + restore activity logs for the dashboard widget.
func HandleBackupRestoreLogs(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	includeBackup, includeRestore, err := parseBackupRestoreLogTypes(c.QueryParam("types"))
	if err != nil {
		return backupRestoreLogsBadRequest(c, err)
	}

	search := strings.TrimSpace(c.QueryParam("search"))
	method := strings.TrimSpace(c.QueryParam("method"))
	messageStatus := strings.TrimSpace(c.QueryParam("message_status"))
	if messageStatus != "" {
		if _, ok := validBackupRestoreMessageStatuses[messageStatus]; !ok {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "invalid message_status",
			})
		}
	}

	limit, offset := parseBackupRestoreLogsLimitOffset(c)

	database, ok := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if !ok || database == nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Database connection unavailable",
		})
	}

	result, err := database.BackupRestoreLogsRepo.ListBackupRestoreLogs(userID, repo.BackupRestoreLogsFilter{
		IncludeBackup:  includeBackup,
		IncludeRestore: includeRestore,
		Search:         search,
		Method:         method,
		MessageStatus:  messageStatus,
		Limit:          limit,
		Offset:         offset,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load backup and restore logs",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, BackupRestoreLogsResponse{
		Message: "Backup and restore logs",
		Logs:    result.Logs,
		Pagination: BackupRestoreLogsPagination{
			Limit:      limit,
			Offset:     offset,
			TotalCount: result.TotalCount,
		},
	})
}
