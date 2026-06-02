package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	googlepack "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

type restoreAllRequest struct {
	Service string `json:"service"`
	LoginID string `json:"login_id"`
}

// HandleRestoreAll creates a queued restore job for one service + account.
func HandleRestoreAll(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
	}

	storxToken := strings.TrimSpace(c.Request().Header.Get("ACCESS_TOKEN"))
	if storxToken == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "access token not found"})
	}

	var req restoreAllRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "invalid request body"})
	}
	req.Service = strings.TrimSpace(strings.ToLower(req.Service))
	req.LoginID = strings.TrimSpace(req.LoginID)
	if req.Service == "" || req.LoginID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "service and login_id are required"})
	}

	method, ok := restore.APIServiceToMethod[restore.APIService(req.Service)]
	if !ok {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "unsupported service"})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	googleToken := strings.TrimSpace(c.Request().Header.Get("GOOGLE_ACCESS_TOKEN"))
	if googleToken == "" {
		jwtKey, jwtErr := googlepack.GetGoogleTokenFromJWT(c)
		if jwtErr != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "google access token not found"})
		}
		googleToken, err = database.AuthRepo.ReadGoogleAuthToken(jwtKey)
		if err != nil || googleToken == "" {
			return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "google access token not found"})
		}
	}

	loginID, err := validateRestoreLoginID(googleToken, req.LoginID)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	}

	job, err := restore.CreateRestoreJob(database, userID, method, req.Service, loginID, storxToken, googleToken)
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

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
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

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
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

	userID, err := satellite.GetUserdetails(c)
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
	return map[string]interface{}{
		"job_id":           job.ID,
		"status":           job.Status,
		"service":          job.Service,
		"login_id":         job.LoginID,
		"total":            job.TotalCount,
		"processed":        job.ProcessedCount,
		"failed":           job.FailedCount,
		"cursor_id":        job.CursorID,
		"tasks_total":      job.TasksTotal,
		"tasks_completed":  job.TasksCompleted,
		"tasks_failed":     job.TasksFailed,
		"progress_percent": repo.RestoreProgressPercent(job.TotalCount, job.ProcessedCount, job.FailedCount),
		"message":          job.Message,
	}
}

// HandleCancelRestoreJob cancels a restore job.
func HandleCancelRestoreJob(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
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
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
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

// validateRestoreLoginID mirrors manual restore: verify Google token and ensure login_id matches token email.
func validateRestoreLoginID(googleAccessToken, loginID string) (string, error) {
	details, err := googlepack.GetGoogleAccountDetailsFromAccessToken(googleAccessToken)
	if err != nil {
		return "", fmt.Errorf("google access token invalid or expired")
	}
	tokenEmail := strings.TrimSpace(details.Email)
	if tokenEmail == "" {
		return "", fmt.Errorf("failed to get user email from google token")
	}
	want := strings.TrimSpace(loginID)
	if want == "" {
		return tokenEmail, nil
	}
	if !strings.EqualFold(want, tokenEmail) {
		return "", fmt.Errorf("login_id does not match the connected Google account")
	}
	return tokenEmail, nil
}

func parseUintParam(c echo.Context, name string) (uint, error) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
