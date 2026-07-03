package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
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
	Service     string `json:"service"`
	ProjectID   string `json:"project_id"`
	LoginID     string `json:"login_id"`
	TargetEmail string `json:"target_email,omitempty"`
}

// RestoreCredentialItem is one personal Google account for the restore list (no workspace tab).
type RestoreCredentialItem struct {
	Email                string `json:"email"`
	HasBackup            bool   `json:"has_backup"`
	NeedsGoogleReconnect bool   `json:"needs_google_reconnect"`
}

// RestoreListPagination is shared pagination metadata for restore account lists.
type RestoreListPagination struct {
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
}

const (
	restoreListDefaultLimit = 20
	restoreListMaxLimit     = 100
)

// HandleRestoreWorkspaces lists workspace domain tabs or paginated mailbox emails for one domain.
// Auth via token_key → user_id; admin credential and project are resolved internally.
// Without domain → { workspaces: ["company.com", ...] }.
// With domain → { mailboxes: ["user@company.com", ...], pagination }.
func HandleRestoreWorkspaces(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	domain := strings.TrimSpace(c.QueryParam("domain"))
	if domain == "" {
		domains, listErr := listRestoreAdminWorkspaceDomains(database, userID)
		if listErr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": listErr.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"workspaces": domains,
		})
	}

	limit, offset, search, err := parseRestoreListParams(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	adminCred, err := findRestoreAdminWorkspaceCred(database, userID, domain)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "workspace not found"})
	}

	adminEmail := strings.TrimSpace(adminCred.Email)
	adminDomain := restore.EmailDomain(adminEmail)

	adminJobs, err := database.CronJobRepo.ListJobsByCredentialID(userID, adminCred.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	adminNeedsGoogle, _ := credentialReconnectFlagsFromJobs(database.CronJobRepo, adminCred, adminJobs)
	if adminNeedsGoogle || strings.TrimSpace(adminCred.RefreshToken) == "" {
		return c.JSON(http.StatusUnprocessableEntity, map[string]interface{}{
			"error": "admin Google reconnect required to list workspace mailboxes",
		})
	}

	emails, err := google.ListAllDomainUsersWithToken(ctx, adminCred.RefreshToken, adminDomain)
	if err != nil {
		logger.Warn(ctx, "restore workspace list domain users failed",
			logger.String("admin_email", adminEmail),
			logger.String("domain", adminDomain),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadGateway, map[string]interface{}{
			"error": "failed to list workspace mailboxes from Google",
		})
	}

	loginID := strings.TrimSpace(c.QueryParam("login_id"))
	loginExclude := strings.ToLower(loginID)

	mailboxes := make([]string, 0, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		if email == "" {
			continue
		}
		if loginExclude != "" && strings.ToLower(email) == loginExclude {
			continue
		}
		mailboxes = append(mailboxes, email)
	}

	mailboxes = filterRestoreEmailsBySearch(mailboxes, search)
	mailboxes, pagination := paginateRestoreSlice(mailboxes, limit, offset)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"mailboxes":  mailboxes,
		"pagination": pagination,
	})
}

// HandleRestoreCredentials lists personal Google credentials for the restore list.
// Auth via token_key → user_id; credentials are loaded from DB by user (no project_id).
func HandleRestoreCredentials(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	loginID := strings.TrimSpace(c.QueryParam("login_id"))

	limit, offset, search, err := parseRestoreListParams(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	rows, err := database.CredentialRepo.ListByUserID(userID, loginID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	backupSet, err := restoreBackupEmailSet(database, userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	credentials := make([]RestoreCredentialItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		email := strings.TrimSpace(row.Email)
		if restoreAccountKind(strings.TrimSpace(row.AccountType)) != "personal" {
			continue
		}
		jobs, jobsErr := database.CronJobRepo.ListJobsByCredentialID(userID, row.ID)
		if jobsErr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": jobsErr.Error()})
		}
		needsGoogle, _ := credentialReconnectFlagsFromJobs(database.CronJobRepo, row, jobs)
		_, hasBackup := backupSet[strings.ToLower(email)]
		credentials = append(credentials, RestoreCredentialItem{
			Email:                email,
			HasBackup:            hasBackup,
			NeedsGoogleReconnect: needsGoogle,
		})
	}

	credentials = filterRestoreCredentialsBySearch(credentials, search)
	credentials, pagination := paginateRestoreCredentials(credentials, limit, offset)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"credentials": credentials,
		"pagination":  pagination,
	})
}

func restoreAccountKind(accountType string) string {
	switch google.NormalizeAccountType(accountType) {
	case google.AccountTypeAdminWorkspace, google.AccountTypeEmployeeWorkspace:
		return "workspace"
	default:
		return "personal"
	}
}

func restoreBackupEmailSet(database *db.PostgresDb, userID string) (map[string]struct{}, error) {
	emails, err := database.CronJobRepo.ListBackupMailboxEmailsForUser(userID, "")
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		key := strings.ToLower(strings.TrimSpace(email))
		if key != "" {
			set[key] = struct{}{}
		}
	}
	return set, nil
}

func listRestoreAdminWorkspaceDomains(database *db.PostgresDb, userID string) ([]string, error) {
	rows, err := database.CredentialRepo.ListByUserID(userID, "")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	domains := make([]string, 0)
	for i := range rows {
		row := &rows[i]
		if google.NormalizeAccountType(row.AccountType) != google.AccountTypeAdminWorkspace {
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(restore.EmailDomain(strings.TrimSpace(row.Email))))
		if domain == "" || restore.IsConsumerMailboxDomain(domain) {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains, nil
}

func pickRestoreAdminWorkspaceCred(rows []repo.GoogleBackupCredentialDB, domain string) (*repo.GoogleBackupCredentialDB, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || restore.IsConsumerMailboxDomain(domain) {
		return nil, fmt.Errorf("invalid workspace domain")
	}
	for i := range rows {
		row := &rows[i]
		if google.NormalizeAccountType(row.AccountType) != google.AccountTypeAdminWorkspace {
			continue
		}
		adminDomain := strings.ToLower(strings.TrimSpace(restore.EmailDomain(strings.TrimSpace(row.Email))))
		if adminDomain == domain {
			return row, nil
		}
	}
	return nil, fmt.Errorf("workspace not found")
}

func findRestoreAdminWorkspaceCred(database *db.PostgresDb, userID, domain string) (*repo.GoogleBackupCredentialDB, error) {
	rows, err := database.CredentialRepo.ListByUserID(userID, "")
	if err != nil {
		return nil, err
	}
	return pickRestoreAdminWorkspaceCred(rows, domain)
}

func parseRestoreListParams(c echo.Context) (limit, offset int, search string, err error) {
	limit, offset, err = parseRestorePagination(c)
	if err != nil {
		return 0, 0, "", err
	}
	search = strings.TrimSpace(c.QueryParam("search"))
	return limit, offset, search, nil
}

func filterRestoreCredentialsBySearch(items []RestoreCredentialItem, search string) []RestoreCredentialItem {
	search = strings.TrimSpace(strings.ToLower(search))
	if search == "" {
		return items
	}
	out := make([]RestoreCredentialItem, 0, len(items))
	for i := range items {
		if strings.Contains(strings.ToLower(strings.TrimSpace(items[i].Email)), search) {
			out = append(out, items[i])
		}
	}
	return out
}

func filterRestoreEmailsBySearch(emails []string, search string) []string {
	search = strings.TrimSpace(strings.ToLower(search))
	if search == "" {
		return emails
	}
	out := make([]string, 0, len(emails))
	for _, email := range emails {
		if strings.Contains(strings.ToLower(strings.TrimSpace(email)), search) {
			out = append(out, email)
		}
	}
	return out
}

func parseRestorePagination(c echo.Context) (limit, offset int, err error) {
	limit = restoreListDefaultLimit
	offset = 0

	if l := strings.TrimSpace(c.QueryParam("limit")); l != "" {
		limit, err = strconv.Atoi(l)
		if err != nil || limit < 1 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
		if limit > restoreListMaxLimit {
			limit = restoreListMaxLimit
		}
	}
	if o := strings.TrimSpace(c.QueryParam("offset")); o != "" {
		offset, err = strconv.Atoi(o)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

func paginateRestoreCredentials(all []RestoreCredentialItem, limit, offset int) ([]RestoreCredentialItem, RestoreListPagination) {
	return paginateRestoreSlice(all, limit, offset)
}

func paginateRestoreSlice[T any](all []T, limit, offset int) ([]T, RestoreListPagination) {
	total := len(all)
	totalPages := 0
	if limit > 0 && total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	page := 1
	if limit > 0 {
		page = offset/limit + 1
	}
	meta := RestoreListPagination{
		Limit:      limit,
		Offset:     offset,
		Page:       page,
		TotalPages: totalPages,
		TotalCount: total,
	}
	if total == 0 || offset >= total {
		return []T{}, meta
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], meta
}

// HandleRestorePrepare checks whether restore-all can run for project_id + login_id + service.
// Optional target_email selects migration write account (from GET /restore/credentials).
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
	targetEmail := strings.TrimSpace(c.QueryParam("target_email"))

	if projectID == "" || loginID == "" || service == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "project_id, login_id, and service are required",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	result, err := restore.EvaluateReadinessWithOptions(ctx, database, restore.ReadinessRequest{
		UserID: userID, ProjectID: projectID, LoginID: loginID, Service: service,
		TargetEmail: targetEmail,
	})
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
	req.TargetEmail = strings.TrimSpace(req.TargetEmail)
	if req.Service == "" || req.ProjectID == "" || req.LoginID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "service, project_id, and login_id are required"})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	prep, err := restore.EvaluateReadinessWithOptions(ctx, database, restore.ReadinessRequest{
		UserID: userID, ProjectID: req.ProjectID, LoginID: req.LoginID, Service: req.Service,
		TargetEmail: req.TargetEmail,
	})
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
	if req.TargetEmail != "" && !strings.EqualFold(req.TargetEmail, prep.TargetEmail) {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "target_email does not match prepare result; re-run GET /restore/prepare",
		})
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
		"job_id":        job.ID,
		"status":        job.Status,
		"message":       "restore job queued",
		"credential_id": job.CredentialID,
		"cron_job_id":   job.CronJobID,
		"target_email":  job.TargetEmail,
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
	if te := strings.TrimSpace(job.TargetEmail); te != "" {
		inputData["target_email"] = te
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
