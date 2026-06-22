package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// resolveManualRestoreStorx loads storx grant from DB for manual item restore (one Satellite refresh on first uplink error).
func resolveManualRestoreStorx(c echo.Context, method, loginID string) (*restore.StorxGrantSession, error) {
	loginID = strings.TrimSpace(loginID)
	if loginID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"error": "login_id required"})
	}
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, map[string]interface{}{"error": "authentication failed"})
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	sess, err := restore.NewStorxGrantSession(c.Request().Context(), database, userID, method, loginID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	}
	return sess, nil
}

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
		logger.Warn(ctx, "Restore prepare check failed",
			logger.String("service", service),
			logger.String("login_id", loginID),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	if result.Ready {
		logger.Info(ctx, "Restore prepare ready",
			logger.String("service", service),
			logger.String("login_id", loginID),
			logger.String("auth_mode", result.AuthMode),
			logger.Int("backup_items", int(result.BackupItemCount)))
	} else {
		logger.Warn(ctx, "Restore prepare not ready",
			logger.String("service", service),
			logger.String("login_id", loginID),
			logger.String("reason", result.Reason),
			logger.String("message", result.Message))
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
		logger.Warn(ctx, "Restore all readiness check failed",
			logger.String("service", req.Service),
			logger.String("login_id", req.LoginID),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	if !prep.Ready {
		logger.Warn(ctx, "Restore all rejected — account not ready",
			logger.String("service", req.Service),
			logger.String("login_id", req.LoginID),
			logger.String("reason", prep.Reason),
			logger.String("message", prep.Message))
		return c.JSON(http.StatusUnprocessableEntity, prep)
	}

	job, err := restore.CreateRestoreJobFromReadiness(ctx, database, userID, prep)
	if err != nil {
		if restore.IsActiveRestoreConflict(err) {
			logger.Warn(ctx, "Restore all rejected — job already active",
				logger.String("service", req.Service),
				logger.String("login_id", req.LoginID),
				logger.ErrorField(err))
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
		}
		logger.Error(ctx, "Failed to create restore job",
			logger.String("service", req.Service),
			logger.String("login_id", req.LoginID),
			logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	logger.Info(ctx, "Restore job queued",
		logger.Int("job_id", int(job.ID)),
		logger.String("service", req.Service),
		logger.String("login_id", req.LoginID),
		logger.String("method", job.Method))

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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Restore Account Details",
		"success": []RestoreJobDetailResponse{toRestoreJobDetailResponse(database, job)},
		"failed":  []interface{}{},
	})
}

// RestoreJobListResponse is the job metadata shape for GET /restore/jobs (progress via /restore/live).
type RestoreJobListResponse struct {
	ID            uint                   `json:"ID"`
	Method        string                 `json:"method"`
	LoginID       string                 `json:"login_id"`
	Status        string                 `json:"status"`
	Message       string                 `json:"message"`
	MessageStatus string                 `json:"message_status"`
	AccountType   string                 `json:"account_type"`
	AuthMode      string                 `json:"auth_mode"`
	InputData     map[string]interface{} `json:"input_data"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

// RestoreJobDetailResponse is GET /restore/job/:job_id (autosync detail shape + progress).
type RestoreJobDetailResponse struct {
	ID              uint                   `json:"ID"`
	Method          string                 `json:"method"`
	LoginID         string                 `json:"login_id"`
	Status          string                 `json:"status"`
	Message         string                 `json:"message"`
	MessageStatus   string                 `json:"message_status"`
	AccountType     string                 `json:"account_type"`
	AuthMode        string                 `json:"auth_mode"`
	InputData       map[string]interface{} `json:"input_data"`
	Total           uint                   `json:"total"`
	Processed       uint                   `json:"processed"`
	Failed          uint                   `json:"failed"`
	CursorID        uint                   `json:"cursor_id"`
	ProgressPercent float64                `json:"progress_percent"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	CancelledAt     *time.Time             `json:"cancelled_at,omitempty"`
}

// HandleListRestoreJobs returns recent restore jobs for the user (metadata; poll /restore/live for progress).
func HandleListRestoreJobs(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	filter, err := parseRestoreJobListFilter(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	jobs, err := database.RestoreJobRepo.ListByUser(userID, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	success := make([]RestoreJobListResponse, 0, len(jobs))
	for i := range jobs {
		success = append(success, toRestoreJobListResponse(database, &jobs[i]))
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Restore jobs list",
		"success": success,
		"failed":  []interface{}{},
	})
}

// HandleRestoreLive returns running restore jobs with progress (poll like /auto-sync/live).
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
		"success": activeJobs,
		"failed":  []interface{}{},
	})
}

func restoreJobInputData(job *repo.RestoreJobListingDB) map[string]interface{} {
	inputData := map[string]interface{}{}
	if job.CredentialID > 0 {
		inputData["credential_id"] = job.CredentialID
	}
	if job.CronJobID > 0 {
		inputData["cron_job_id"] = job.CronJobID
	}
	if job.StorjProjectID != "" {
		inputData["project_id"] = job.StorjProjectID
	}
	return inputData
}

func toRestoreJobListResponse(store *db.PostgresDb, job *repo.RestoreJobListingDB) RestoreJobListResponse {
	return RestoreJobListResponse{
		ID:            job.ID,
		Method:        job.Method,
		LoginID:       job.LoginID,
		Status:        job.Status,
		Message:       job.Message,
		MessageStatus: repo.EffectiveRestoreMessageStatus(job),
		AccountType:   job.AccountType,
		AuthMode:      restore.AuthModeForJob(store, job),
		InputData:     restoreJobInputData(job),
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
	}
}

func toRestoreJobDetailResponse(store *db.PostgresDb, job *repo.RestoreJobListingDB) RestoreJobDetailResponse {
	return RestoreJobDetailResponse{
		ID:              job.ID,
		Method:          job.Method,
		LoginID:         job.LoginID,
		Status:          job.Status,
		Message:         job.Message,
		MessageStatus:   repo.EffectiveRestoreMessageStatus(job),
		AccountType:     job.AccountType,
		AuthMode:        restore.AuthModeForJob(store, job),
		InputData:       restoreJobInputData(job),
		Total:           job.TotalCount,
		Processed:       job.ProcessedCount,
		Failed:          job.FailedCount,
		CursorID:        job.CursorID,
		ProgressPercent: repo.RestoreProgressPercent(job.TotalCount, job.ProcessedCount, job.FailedCount),
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		CancelledAt:     job.CancelledAt,
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
	if err := restore.CancelRestoreJob(ctx, database, userID, jobID); err != nil {
		logger.Warn(ctx, "Restore cancel failed",
			logger.Int("job_id", int(jobID)),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	logger.Info(ctx, "Restore job cancelled via API", logger.Int("job_id", int(jobID)))
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

// parseRestoreJobListFilter reads optional query filters for GET /restore/jobs.
//
// Query params:
//   - service: gmail | drive | photos | calendar | contacts (also accepts internal method names)
//   - status: queued | running | completed | partial_completed | failed | cancelled
//   - search: partial match on login_id (email)
//   - from_time / to_time: RFC3339 or YYYY-MM-DD (created_at range)
//   - limit, offset: pagination (default limit 20, max 100)
func parseRestoreJobListFilter(c echo.Context) (*repo.RestoreJobListFilter, error) {
	filter := &repo.RestoreJobListFilter{}

	if svc := strings.TrimSpace(c.QueryParam("service")); svc != "" {
		method := repo.RestoreMethodFromAPIService(svc)
		if method == "" {
			return nil, fmt.Errorf("invalid service")
		}
		filter.Method = method
	} else if method := strings.TrimSpace(c.QueryParam("method")); method != "" {
		mapped := repo.RestoreMethodFromAPIService(method)
		if mapped == "" {
			return nil, fmt.Errorf("invalid method")
		}
		filter.Method = mapped
	}

	if status := strings.TrimSpace(c.QueryParam("status")); status != "" {
		if !repo.ValidRestoreJobStatus(status) {
			return nil, fmt.Errorf("invalid status")
		}
		filter.Status = status
	}

	if search := strings.TrimSpace(c.QueryParam("search")); search != "" {
		filter.Search = search
	} else if email := strings.TrimSpace(c.QueryParam("email")); email != "" {
		filter.Search = email
	}

	if fromRaw := strings.TrimSpace(c.QueryParam("from_time")); fromRaw != "" {
		from, err := parseRestoreJobListTimeQuery(fromRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid from_time: %w", err)
		}
		filter.FromTime = from
	}

	if toRaw := strings.TrimSpace(c.QueryParam("to_time")); toRaw != "" {
		to, inclusiveEnd, err := parseRestoreJobListTimeBound(toRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid to_time: %w", err)
		}
		if inclusiveEnd {
			end := to.Add(24*time.Hour - time.Nanosecond)
			filter.ToTime = &end
		} else {
			filter.ToTime = &to
		}
	}

	if q := strings.TrimSpace(c.QueryParam("limit")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid limit")
		}
		filter.Limit = n
	}
	if q := strings.TrimSpace(c.QueryParam("offset")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid offset")
		}
		filter.Offset = n
	}

	return filter, nil
}

func parseRestoreJobListTimeQuery(raw string) (*time.Time, error) {
	t, _, err := parseRestoreJobListTimeBound(raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func parseRestoreJobListTimeBound(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, false, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("use RFC3339 or YYYY-MM-DD")
}
