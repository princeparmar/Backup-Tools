package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/labstack/echo/v4"
)

type restoreAllRequest struct {
	Service   string `json:"service"`
	ProjectID string `json:"project_id"`
	LoginID   string `json:"login_id"`
}

// HandleRestorePrepare checks whether restore-all can run for project_id + login_id + service.
func HandleRestorePrepare(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	loginID := strings.TrimSpace(c.QueryParam("login_id"))
	service := strings.TrimSpace(strings.ToLower(c.QueryParam("service")))
	if projectID == "" || loginID == "" || service == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "project_id, login_id, and service are required",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	result, err := restore.EvaluateReadiness(ctx, database, userID, projectID, loginID, service)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// HandleRestoreAll creates a queued restore job for one service + account (Satellite-proxied).
func HandleRestoreAll(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	var req restoreAllRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "invalid request body"})
	}
	req.Service = strings.TrimSpace(strings.ToLower(req.Service))
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.LoginID = strings.TrimSpace(req.LoginID)
	if req.Service == "" || req.ProjectID == "" || req.LoginID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "service, project_id, and login_id are required"})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	prep, err := restore.EvaluateReadiness(ctx, database, userID, req.ProjectID, req.LoginID, req.Service)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	if !prep.Ready {
		return c.JSON(http.StatusUnprocessableEntity, prep)
	}

	job, err := restore.CreateRestoreJobFromReadiness(database, userID, prep)
	if err != nil {
		if restore.IsActiveRestoreConflict(err) {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "restore job queued",
	})
}

// HandleGetRestoreJob returns progress for a restore job.
func HandleGetRestoreJob(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	jobID, err := parseUintParam(c, "job_id")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "invalid job_id"})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	job, err := database.RestoreJobRepo.GetByIDForUser(userID, jobID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "job not found"})
	}

	return c.JSON(http.StatusOK, restoreJobProgressResponse(job))
}

// HandleListRestoreJobs returns recent restore jobs for the user (history; no tokens).
func HandleListRestoreJobs(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	limit := 20
	if q := strings.TrimSpace(c.QueryParam("limit")); q != "" {
		if n, parseErr := strconv.Atoi(q); parseErr == nil && n > 0 {
			limit = n
		}
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	jobs, err := database.RestoreJobRepo.ListByUser(userID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	data := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		data = append(data, restoreJobProgressResponse(&jobs[i]))
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Restore jobs list",
		"data":    data,
	})
}

// HandleRestoreLive returns running restore jobs for the user (poll like /auto-sync/live; queued omitted).
func HandleRestoreLive(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "not able to authenticate user",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	activeJobs, err := database.RestoreJobRepo.GetAllActiveRestoreJobsForUser(userID)
	if err != nil {
		logger.Error(ctx, "Failed to get active restore jobs", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Active Restore Jobs List",
		"data":    activeJobs,
	})
}

func restoreJobProgressResponse(job *repo.RestoreJobListingDB) map[string]interface{} {
	service := repo.APIServiceFromMethod(job.Method)
	return map[string]interface{}{
		"job_id":           job.ID,
		"status":           job.Status,
		"service":          service,
		"method":           job.Method,
		"login_id":         job.LoginID,
		"project_id":       job.StorjProjectID,
		"account_type":     job.AccountType,
		"auth_mode":        job.AuthMode,
		"total":            job.TotalCount,
		"processed":        job.ProcessedCount,
		"failed":           job.FailedCount,
		"cursor_id":        job.CursorID,
		"progress_percent": repo.RestoreProgressPercent(job.TotalCount, job.ProcessedCount, job.FailedCount),
		"message":          job.Message,
	}
}

// HandleCancelRestoreJob cancels a restore job.
func HandleCancelRestoreJob(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}
	jobID, err := parseUintParam(c, "job_id")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "invalid job_id"})
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if err := restore.CancelRestoreJob(database, userID, jobID); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": "restore cancelled", "job_id": jobID})
}

// HandleListRestoreDeadItems lists DLQ entries for a job.
func HandleListRestoreDeadItems(c echo.Context) error {
	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}
	jobID, err := parseUintParam(c, "job_id")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "invalid job_id"})
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if _, err := database.RestoreJobRepo.GetByIDForUser(userID, jobID); err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "job not found"})
	}
	items, err := database.RestoreJobRepo.ListDeadItemsByJobID(jobID, 200)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": items})
}

func parseUintParam(c echo.Context, name string) (uint, error) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
