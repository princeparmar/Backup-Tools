package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var (
	allowedMethods = map[string]bool{
		"gmail": true, "outlook": true, "psql_database": true, "mysql_database": true,
		"google_drive": true, "google_photos": true, "google_calendar": true, "google_contacts": true,
	}
	allowedSyncTypes = map[string]bool{
		"one_time": true, "daily": true,
	}
)

// onboardingIntervalValues — Satellite onboarding JSON `interval` + GET /auto-sync/job/interval.
var onboardingIntervalValues = map[string][]string{
	"1h":    {""},
	"6h":    {""},
	"daily": {"12am"}, // nightly (once per calendar day; on = 12am)
}

// intervalValues — validation for job PUT and legacy schedules (weekly/monthly kept for existing jobs).
var intervalValues = map[string][]string{
	"1h":    {""},
	"6h":    {""},
	"daily": {"12am"},
	// Legacy UI picker (not used for Satellite onboarding `interval` field):
	"weekly":  {"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	"monthly": {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26", "27", "28"},
}

// UI / Satellite service name → cron job method (DB).
var onboardingServiceToMethod = map[string]string{
	"gmail": "gmail", "drive": "google_drive", "photos": "google_photos",
	"calendar": "google_calendar", "contacts": "google_contacts",
	// Future: "outlook": "outlook", "psql": "psql_database", "mysql": "mysql_database",
}

// GoogleBackupOnboardingRequest is the Satellite → Backup-Tools job create body (POST /google/backup/onboarding/jobs or POST /auto-sync/job).
// sync_type is query only (?sync_type=daily). Future: outlook/psql via services[] in this JSON (legacy POST /auto-sync/job/:method is commented out).
type GoogleBackupOnboardingRequest struct {
	Services        []string `json:"services"`
	Interval        string   `json:"interval"`
	GoogleEmail     string   `json:"google_email"`
	AccountType     string   `json:"account_type"`
	ProjectID       string   `json:"project_id"`
	SatelliteUserID string   `json:"satellite_user_id"`
	RefreshToken    string   `json:"refresh_token"`
	Emails          []string `json:"emails"`
}

// Legacy unified create body (onboarding + outlook code + DB fields) — not used; onboarding uses GoogleBackupOnboardingRequest only.
/*
type AutoSyncCreateRequest struct {
	Services         []string `json:"services"`
	Interval         string   `json:"interval"`
	GoogleEmail      string   `json:"google_email"`
	AccountType      string   `json:"account_type"`
	ProjectID        string   `json:"project_id"`
	SatelliteUserID  string   `json:"satellite_user_id"`
	RefreshToken     string   `json:"refresh_token"`
	Emails           []string `json:"emails"`
	StorxAccessGrant string   `json:"storx_access_grant"`
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	DatabaseName     string   `json:"database_name"`
	Host             string   `json:"host"`
	Port             string   `json:"port"`
	Username         string   `json:"username"`
	Password         string   `json:"password"`
}

func (r *AutoSyncCreateRequest) isOnboarding() bool {
	return len(r.Services) > 0
}
*/

// Legacy: calendar/contacts returned "coming soon" — removed; create jobs like drive when enabled in cron.
/*
var onboardingServiceComingSoon = map[string]bool{
	"calendar": true,
	"contacts": true,
}
*/

type onboardingJobResult struct {
	Service string `json:"service,omitempty"`
	Email   string `json:"email,omitempty"`
	JobID   uint   `json:"job_id,omitempty"`
	TaskID  uint   `json:"task_id,omitempty"`
}

type onboardingFailedResult struct {
	Service string `json:"service,omitempty"`
	Email   string `json:"email,omitempty"`
	Error   string `json:"error"`
}

// nullSliceJSON marshals empty slices as JSON null (Satellite expects null, not []).
func nullSliceJSON[T any](s []T) any {
	if len(s) == 0 {
		return nil
	}
	return s
}

type onboardingSchedule struct {
	Interval string
	On       string
}

func (r *GoogleBackupOnboardingRequest) trim() {
	r.GoogleEmail = strings.TrimSpace(r.GoogleEmail)
	r.RefreshToken = strings.TrimSpace(r.RefreshToken)
	r.ProjectID = strings.TrimSpace(r.ProjectID)
	r.Interval = strings.TrimSpace(r.Interval)
	r.SatelliteUserID = strings.TrimSpace(r.SatelliteUserID)
}

func (r *GoogleBackupOnboardingRequest) validate(userID string) error {
	if r.RefreshToken == "" {
		return errors.New("refresh_token is required")
	}
	if r.GoogleEmail == "" {
		return errors.New("google_email is required")
	}
	if len(r.Services) == 0 {
		return errors.New("services is required")
	}
	if r.Interval == "" {
		return errors.New("interval is required")
	}
	if r.ProjectID == "" {
		return errors.New("project_id is required")
	}
	if r.SatelliteUserID != "" && r.SatelliteUserID != userID {
		return errors.New("satellite_user_id does not match token_key session")
	}
	return nil
}

type DatabaseConnection struct {
	Name         string `json:"name"`
	DatabaseName string `json:"database_name"`
	Host         string `json:"host"`
	Port         string `json:"port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

// AutoSyncStatsResponse represents the response structure for autosync stats
type AutoSyncStatsResponse struct {
	ActiveSyncs int    `json:"active_syncs"`
	FailedSyncs int    `json:"failed_syncs"`
	Status      string `json:"status"`
}

// CronJobResponse represents a cron job with next backup time.
// Connected-account tokens live on google_backup_credentials (input_data.credential_id); Satellite uses a separate connected-account section.
type CronJobResponse struct {
	repo.CronJobListingDB
	NextBackup *time.Time `json:"next_backup"`
}

// CronJobDetailResponse is a single job for GET detail / update responses.
type CronJobDetailResponse struct {
	repo.CronJobListingDB
}

// GoogleConnectedAccountUpdateRequest is the body for PUT /auto-sync/job/project (connected account; no job_id).
type GoogleConnectedAccountUpdateRequest struct {
	ProjectID    string  `json:"project_id"`
	GoogleEmail  string  `json:"google_email"`
	StorxToken   *string `json:"storx_token"`
	RefreshToken *string `json:"refresh_token"`
	Interval     *string `json:"interval"`
	On           *string `json:"on"`
	Active       *bool   `json:"active"`
}

func (r *GoogleConnectedAccountUpdateRequest) connectedEmail() string {
	return strings.TrimSpace(r.GoogleEmail)
}

func (r *GoogleConnectedAccountUpdateRequest) hasUpdateFields() bool {
	if r == nil {
		return false
	}
	return r.StorxToken != nil || r.RefreshToken != nil || r.Interval != nil || r.On != nil || r.Active != nil
}

// LEGACY(parent_account / is_admin) — removed from API; use credential_id + Satellite connected-account UI instead.
/*
type SharedCredentialAccountSummary struct {
	JobID          uint   `json:"job_id"`
	Email          string `json:"email"`
	StorxToken     string `json:"storx_token"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	StorjProjectID string `json:"storj_project_id,omitempty"`
}

type CronJobResponseWithParent struct {
	repo.CronJobListingDB
	NextBackup    *time.Time                      `json:"next_backup"`
	ParentAccount *SharedCredentialAccountSummary `json:"parent_account,omitempty"`
	IsAdmin       bool                            `json:"is_admin,omitempty"`
}

func sharedCredentialOAuthHolderIsAdmin(database *db.PostgresDb, job *repo.CronJobListingDB) bool { ... }
func enrichJobStorxFromSharedCredential(database *db.PostgresDb, job *repo.CronJobListingDB) { ... }
func buildSharedCredentialParentSummary(database *db.PostgresDb, job *repo.CronJobListingDB) *SharedCredentialAccountSummary { ... }
func stripDelegatedJobStorxFromAPIResponse(job *repo.CronJobListingDB, parentSummary *SharedCredentialAccountSummary) { ... }
func googleAutosyncAPIParentAndAdmin(database *db.PostgresDb, job *repo.CronJobListingDB) (*SharedCredentialAccountSummary, bool) { ... }
*/

func inputDataMapContainsRefreshToken(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok || m == nil {
		return false
	}
	rt, ok := m["refresh_token"].(string)
	return ok && strings.TrimSpace(rt) != ""
}

func applyAutomaticBackupUpdates(database *db.PostgresDb, job *repo.CronJobListingDB, jobID uint, updateRequest map[string]interface{}) error {
	if repo.JobCredentialID(job) > 0 {
		return applyAutomaticBackupUpdatesViaCredential(database, job, jobID, updateRequest)
	}
	if len(updateRequest) == 0 {
		return nil
	}
	return database.CronJobRepo.UpdateCronJobByID(jobID, updateRequest)
}

func applyAutomaticBackupUpdatesViaCredential(database *db.PostgresDb, job *repo.CronJobListingDB, jobID uint, updateRequest map[string]interface{}) error {
	cid := repo.JobCredentialID(job)
	if cid == 0 {
		return fmt.Errorf("credential_id not found on job")
	}
	var refreshPtr, storxPtr *string
	var projectID string
	jobUpdate := make(map[string]interface{})
	for k, v := range updateRequest {
		switch k {
		case "storj_project_id":
			if s, ok := v.(string); ok {
				projectID = strings.TrimSpace(s)
			}
		case "storx_token":
			if s, ok := v.(string); ok {
				storxPtr = &s
			}
		case "input_data":
			if m, ok := v.(map[string]interface{}); ok && inputDataMapContainsRefreshToken(m) {
				if rt, ok := m["refresh_token"].(string); ok {
					refreshPtr = &rt
				}
			}
			jobUpdate[k] = v
		default:
			jobUpdate[k] = v
		}
	}
	if refreshPtr != nil || storxPtr != nil {
		if err := database.CredentialRepo.UpdateTokens(cid, refreshPtr, storxPtr); err != nil {
			return err
		}
	}
	if projectID != "" {
		if err := database.CredentialRepo.UpdateStorjProjectID(cid, projectID); err != nil {
			return err
		}
	}
	delete(jobUpdate, "storx_token")
	delete(jobUpdate, "storj_project_id")
	if raw, ok := jobUpdate["input_data"]; ok {
		if m, ok := raw.(map[string]interface{}); ok {
			delete(m, "refresh_token")
			if len(m) == 0 {
				delete(jobUpdate, "input_data")
			} else {
				jobUpdate["input_data"] = m
			}
		}
	}
	if len(jobUpdate) == 0 {
		return nil
	}
	return database.CronJobRepo.UpdateCronJobByID(jobID, jobUpdate)
}

func applyStorxUpdateChoiceForLinkedGmailAccounts(job *repo.CronJobListingDB, reqBody AutomaticBackupUpdateRequest) error {
	if reqBody.StorxToken == nil || reqBody.ApplyStorxToAll == nil || !*reqBody.ApplyStorxToAll {
		return nil
	}
	if job == nil {
		return nil
	}
	if repo.JobCredentialID(job) > 0 {
		return nil
	}
	return httpErr(http.StatusBadRequest, "Invalid Request", "apply_storx_to_all_linked_accounts requires credential-linked jobs")
}

// propagateCredentialLinkedActive activates all cron jobs sharing credential_id (all Google services).
func propagateCredentialLinkedActive(database *db.PostgresDb, job *repo.CronJobListingDB, editedJobID uint, specificJobActive *bool) error {
	if database == nil || job == nil {
		return nil
	}
	cid := repo.JobCredentialID(job)
	if cid == 0 {
		return nil
	}
	scheduleTemplate, err := database.CronJobRepo.GetCronJobByID(editedJobID)
	if err != nil {
		return err
	}
	linked, err := database.CronJobRepo.ListJobsByCredentialID(job.UserID, cid)
	if err != nil {
		return err
	}
	for i := range linked {
		child := linked[i]
		if err := database.CronJobRepo.UpdateCronJobByID(child.ID, mergeBulkGmailActivatePatch(&child, scheduleTemplate)); err != nil {
			return err
		}
	}
	if specificJobActive != nil {
		override := activeStateUpdateFields(*specificJobActive)
		if err := database.CronJobRepo.UpdateCronJobByID(editedJobID, override); err != nil {
			return err
		}
	}
	return nil
}

// mergeBulkGmailActivatePatch is activeStateUpdateFields(true) plus interval/on from scheduleTemplate when the target row is missing them.
func mergeBulkGmailActivatePatch(target *repo.CronJobListingDB, scheduleTemplate *repo.CronJobListingDB) map[string]interface{} {
	patch := activeStateUpdateFields(true)
	if target == nil || scheduleTemplate == nil || scheduleTemplate.SyncType == "one_time" {
		return patch
	}
	if strings.TrimSpace(scheduleTemplate.Interval) == "" || strings.TrimSpace(scheduleTemplate.On) == "" {
		return patch
	}
	if strings.TrimSpace(target.Interval) == "" {
		patch["interval"] = scheduleTemplate.Interval
	}
	if strings.TrimSpace(target.On) == "" {
		patch["on"] = scheduleTemplate.On
	}
	return patch
}

func HandleAutomaticSyncListForUser(c echo.Context) error {
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

	// Parse filter from query parameter
	var filter *repo.CronJobFilter
	if filterParam := c.QueryParam("filter"); filterParam != "" {
		var decodedFilter repo.CronJobFilter
		if err := decodeFilterJSON(filterParam, &decodedFilter); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "invalid filter parameter",
				"error":   err.Error(),
			})
		}
		filter = &decodedFilter
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	automaticSyncList, err := database.CronJobRepo.GetAllCronJobsForUser(userID, filter)
	if err != nil {
		logger.Error(ctx, "Failed to get cron jobs for user", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	for i := range automaticSyncList {
		database.CronJobRepo.EnrichCronJobFromCredential(&automaticSyncList[i])
	}
	maskedJobs := repo.MaskTokenForCronJobListingDB(automaticSyncList)
	response := make([]CronJobResponse, len(maskedJobs))
	for i := range maskedJobs {
		j := maskedJobs[i]
		response[i] = CronJobResponse{
			CronJobListingDB: j,
			NextBackup:       calculateNextBackup(j),
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Accounts List",
		"success": response,
		"failed":  []interface{}{},
	})
}
func calculateNextBackup(job repo.CronJobListingDB) *time.Time {
	if !job.Active || job.Interval == "one_time" {
		return nil
	}

	now := time.Now()
	// Use today at 00:00:00 as the reference for date comparisons
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// If the job has already run today, we must look for the next occurrence starting from tomorrow.
	// Otherwise, we can include today in our search.
	startSearchingFrom := today
	if job.LastRun != nil {
		lastRunDate := time.Date(job.LastRun.Year(), job.LastRun.Month(), job.LastRun.Day(), 0, 0, 0, 0, job.LastRun.Location())
		if !lastRunDate.Before(today) {
			startSearchingFrom = today.AddDate(0, 0, 1)
		}
	}

	var next time.Time

	switch job.Interval {
	case "1h":
		next = now.Truncate(time.Hour).Add(time.Hour)
	case "6h":
		block := (now.Hour() / 6) * 6
		next = time.Date(now.Year(), now.Month(), now.Day(), block, 0, 0, 0, now.Location()).Add(6 * time.Hour)
		if !next.After(now) {
			next = next.Add(6 * time.Hour)
		}
	case "daily":
		next = startSearchingFrom

	case "weekly":
		if job.On == "" {
			return nil
		}
		target := parseWeekday(job.On)
		if target < 0 {
			return nil
		}
		next = startSearchingFrom
		for next.Weekday() != time.Weekday(target) {
			next = next.AddDate(0, 0, 1)
		}

	case "monthly":
		if job.On == "" {
			return nil
		}
		day, err := strconv.Atoi(job.On)
		if err != nil || day < 1 || day > 31 {
			return nil
		}

		// Start with the 'day' in the month of 'startSearchingFrom'
		next = time.Date(startSearchingFrom.Year(), startSearchingFrom.Month(), day, 0, 0, 0, 0, startSearchingFrom.Location())

		// If this date is before our startSearchingFrom, move to next month
		if next.Before(startSearchingFrom) {
			next = addOneMonthSameDay(next, day)
		}

	default:
		return nil
	}

	// Set a default time (e.g., 00:00:00) for the next backup
	return &next
}

func addOneMonthSameDay(t time.Time, day int) time.Time {
	year, month := t.Year(), t.Month()+1
	if month > 12 {
		month = 1
		year++
	}
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(
		year, month, day,
		t.Hour(), t.Minute(), t.Second(), 0,
		t.Location(),
	)
}

func parseWeekday(weekday string) time.Weekday {
	weekdayMap := map[string]time.Weekday{
		"Sunday":    time.Sunday,
		"Monday":    time.Monday,
		"Tuesday":   time.Tuesday,
		"Wednesday": time.Wednesday,
		"Thursday":  time.Thursday,
		"Friday":    time.Friday,
		"Saturday":  time.Saturday,
	}
	if wd, ok := weekdayMap[weekday]; ok {
		return wd
	}
	return -1
}

func HandleAutomaticSyncActiveJobsForUser(c echo.Context) error {
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
	activeJobs, err := database.CronJobRepo.GetAllActiveCronJobsForUser(userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Active Automatic Backup Accounts List",
		"data":    activeJobs,
	})
}

func HandleHideTask(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "authentication failed"})
	}

	var reqBody struct {
		CronJobID uint `json:"cron_job_id"`
	}
	if err := c.Bind(&reqBody); err != nil || reqBody.CronJobID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "invalid cron_job_id"})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	job, err := database.CronJobRepo.GetCronJobByID(reqBody.CronJobID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "cron job not found"})
	}

	if job.UserID != userID {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"message": "access denied"})
	}

	if job.Status != repo.JobStatusFailed {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "cron job has no failed tasks to hide",
		})
	}

	if err := database.CronJobRepo.UpdateCronJobByID(reqBody.CronJobID, map[string]interface{}{"hidden": true}); err != nil {
		logger.Error(ctx, "Failed to hide cron job", logger.Int("job_id", int(reqBody.CronJobID)), logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "failed to hide cron job"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":     "cron job hidden successfully",
		"cron_job_id": reqBody.CronJobID,
	})
}

func HandleIntervalOnConfig(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Interval Values",
		"data":    onboardingIntervalValues,
	})
	return nil
}

func HandleAutomaticSyncDetails(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	jobDetails, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		if strings.Contains(err.Error(), "record not found") {
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"message": "Invalid Request",
				"error":   err.Error(),
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Internal Server Error",
			"error":   err.Error(),
		})
	}

	jobCopy := *jobDetails
	database.CronJobRepo.EnrichCronJobFromCredential(&jobCopy)
	repo.MaskTokenForCronJobDB(&jobCopy)
	detail := CronJobDetailResponse{CronJobListingDB: jobCopy}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Account Details",
		"success": []CronJobDetailResponse{detail},
		"failed":  []interface{}{},
	})
}

// HandleAutomaticSyncCreate creates backup jobs from JSON only (no :method in URL).
func HandleAutomaticSyncCreate(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	var req GoogleBackupOnboardingRequest
	if err := c.Bind(&req); err != nil {
		return jsonError(http.StatusBadRequest, "Invalid Request", err)
	}
	req.trim()
	return handleSatelliteOnboardingCreate(c, ctx, userID, &req)
}

func satelliteUserIDFromRequest(c echo.Context) (string, error) {
	if strings.TrimSpace(c.Request().Header.Get("token_key")) == "" {
		return "", jsonErrorMsg(http.StatusUnauthorized, "Invalid Request", "token_key header is required")
	}
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return "", jsonError(http.StatusUnauthorized, "Invalid Request", err)
	}
	return userID, nil
}

func syncTypeFromQuery(c echo.Context) (string, error) {
	syncType := strings.TrimSpace(c.QueryParam("sync_type"))
	if syncType == "" {
		syncType = "daily"
	}
	if !allowedSyncTypes[syncType] {
		return "", jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid sync_type")
	}
	return syncType, nil
}

func handleSatelliteOnboardingCreate(c echo.Context, ctx context.Context, userID string, req *GoogleBackupOnboardingRequest) error {
	if err := req.validate(userID); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	syncType, err := syncTypeFromQuery(c)
	if err != nil {
		return err
	}
	schedule, err := parseOnboardingInterval(req.Interval)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	emails, normErr := normalizeGmailEmails(req.Emails, req.GoogleEmail)
	if normErr != nil {
		return normErr
	}
	if err := validateGmailAdminDomainForOnboarding(emails, req.GoogleEmail, req.AccountType); err != nil {
		return err
	}
	cred, err := database.CredentialRepo.FindOrCreateForUser(userID, req.GoogleEmail, req.ProjectID, req.RefreshToken, "")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}

	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	var servicesOut []string
	seenSvc := make(map[string]struct{})

	for _, raw := range req.Services {
		svc := strings.ToLower(strings.TrimSpace(raw))
		if svc == "" {
			continue
		}
		if _, dup := seenSvc[svc]; dup {
			continue
		}
		seenSvc[svc] = struct{}{}
		servicesOut = append(servicesOut, svc)

		j, f := onboardingCreateForService(ctx, c, userID, syncType, svc, schedule, req, cred.ID, emails, database)
		jobs = append(jobs, j...)
		failed = append(failed, f...)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":  len(failed) == 0,
		"message":  syncCreateMessage(syncType),
		"jobs":     nullSliceJSON(jobs),
		"failed":   nullSliceJSON(failed),
		"services": nullSliceJSON(servicesOut),
	})
}

// Legacy job create via POST /auto-sync/job/:method — commented out; use JSON onboarding (services[]).
// Re-enable when outlook/psql are added to GoogleBackupOnboardingRequest / services[] like Google services.
/*
func handleLegacySyncCreate(c echo.Context, ctx context.Context, userID, method string) error {
	syncType, err := syncTypeFromQuery(c)
	if err != nil {
		return err
	}
	if !allowedMethods[method] {
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid method")
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	switch method {
	case "gmail":
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "use POST /google/backup/onboarding/jobs or POST /auto-sync/job with services[] in JSON body")
	case "outlook":
		var body struct {
			Code string `json:"code"`
		}
		if err := c.Bind(&body); err != nil {
			return jsonError(http.StatusBadRequest, "Invalid Request", err)
		}
		name, config, err := ProcessOutlookMethod(strings.TrimSpace(body.Code))
		if err != nil {
			return err
		}
		return createSingleSyncJobAndRespond(ctx, c, userID, method, syncType, name, config, database)
	case "psql_database", "mysql_database":
		var conn DatabaseConnection
		if err := c.Bind(&conn); err != nil {
			return jsonError(http.StatusBadRequest, "Invalid Request", err)
		}
		name, config, err := ProcessDatabaseMethod(conn)
		if err != nil {
			return err
		}
		return createSingleSyncJobAndRespond(ctx, c, userID, method, syncType, name, config, database)
	default:
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid method")
	}
}
*/

// Legacy: optional storx on onboarding create (Satellite grant API + DB copy). Prefer PUT storx_token after create.
/*
func storxTokenFromExistingProjectJobs(projectID string, services []string, database *db.PostgresDb) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	methods := make([]string, 0, len(services)+4)
	seen := make(map[string]struct{})
	for _, raw := range services {
		svc := strings.ToLower(strings.TrimSpace(raw))
		m, ok := onboardingServiceToMethod[svc]
		if !ok {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		methods = append(methods, m)
	}
	for _, m := range []string{"gmail", "google_drive", "google_photos", "google_calendar", "google_contacts"} {
		if _, dup := seen[m]; dup {
			continue
		}
		methods = append(methods, m)
	}
	for _, method := range methods {
		grant, err := database.CronJobRepo.GetAccessGrantByProjectID(projectID, method)
		if err == nil && strings.TrimSpace(grant) != "" {
			return strings.TrimSpace(grant)
		}
	}
	return ""
}

func resolveStorxTokenForOnboarding(ctx context.Context, c echo.Context, projectID string, services []string, database *db.PostgresDb) (string, error) {
	tokenKey := strings.TrimSpace(c.Request().Header.Get("token_key"))
	if grant, err := satellite.GetAccessGrantFromProjectID(ctx, tokenKey, projectID); err == nil && grant != "" {
		return grant, nil
	}
	if grant := storxTokenFromExistingProjectJobs(projectID, services, database); grant != "" {
		return grant, nil
	}
	return "", fmt.Errorf("could not resolve storx access grant for project_id")
}
*/

func onboardingCreateForService(
	ctx context.Context, c echo.Context, userID, syncType, svc string, schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest, credentialID uint, emails []string, database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	method, ok := onboardingServiceToMethod[svc]
	if !ok {
		return nil, []onboardingFailedResult{{Service: svc, Error: "unknown service"}}
	}
	if !allowedMethods[method] {
		return nil, []onboardingFailedResult{{Service: svc, Error: "method not enabled"}}
	}
	return createGoogleJobsForServiceEmails(ctx, c, userID, method, svc, syncType, schedule, req, credentialID, emails, database)

	// LEGACY(credential-migration): per-service create before unified credential_id flow.
	/*
		switch method {
		case "gmail":
			return onboardingCreateGmailJobs(ctx, c, userID, syncType, svc, schedule, req, database)
		default:
			entry, fail := onboardingCreateMediaJob(ctx, c, userID, method, svc, syncType, schedule, req, database)
			if fail != nil {
				return nil, []onboardingFailedResult{*fail}
			}
			if entry == nil {
				return nil, nil
			}
			return []onboardingJobResult{*entry}, nil
		}
	*/
}

func createGoogleJobsForServiceEmails(
	ctx context.Context, c echo.Context, userID, method, svc, syncType string, schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest, credentialID uint, emails []string, database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	emails = dedupeEmailsPreservingOrder(emails)
	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	for _, targetEmail := range emails {
		cronJob, createErr := createSyncJobWithCredential(userID, targetEmail, method, syncType, credentialID, c)
		if createErr != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: extractCreateJobError(createErr)})
			continue
		}
		if err := applyOnboardingJobSchedule(database, cronJob.ID, schedule, req.ProjectID); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: err.Error()})
			continue
		}
		entry := onboardingJobResult{Service: svc, Email: targetEmail, JobID: cronJob.ID}
		if syncType == "one_time" {
			if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
				entry.TaskID = task.ID
			}
		}
		jobs = append(jobs, entry)
	}
	return jobs, failed
}

func createSyncJobWithCredential(userID, name, method, syncType string, credentialID uint, c echo.Context) (*repo.CronJobListingDB, error) {
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if err := checkExistingJobs(userID, name, syncType, method, database); err != nil {
		return nil, err
	}
	return database.CronJobRepo.CreateCronJobForUserWithCredential(userID, name, method, syncType, credentialID, nil)
}

func parseOnboardingInterval(raw string) (onboardingSchedule, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	var interval, on string
	switch raw {
	case "1h", "1hour":
		interval, on = "1h", ""
	case "6h", "6hour":
		interval, on = "6h", ""
	case "nightly", "night", "24h", "1d", "daily", "12am":
		interval, on = "daily", "12am"
	default:
		return onboardingSchedule{}, fmt.Errorf("unsupported interval: %s (use 1h, 6h, or nightly)", raw)
	}
	// Legacy onboarding mapping (weekly / monthly / catch-all hours → daily) — not used for Satellite JSON.
	/*
		switch raw {
		case "24h", "1d", "daily", "12h":
			interval, on = "daily", "12am"
		case "7d", "168h", "weekly":
			interval, on = "weekly", "Monday"
		default:
			if strings.HasSuffix(raw, "h") {
				interval, on = "daily", "12am"
			}
		}
	*/
	if !validateOnboardingInterval(interval, on) {
		return onboardingSchedule{}, fmt.Errorf("invalid interval schedule: %s", raw)
	}
	return onboardingSchedule{Interval: interval, On: on}, nil
}

func validateOnboardingInterval(interval, on string) bool {
	allowed, ok := onboardingIntervalValues[interval]
	if !ok {
		return false
	}
	for _, v := range allowed {
		if v == on {
			return true
		}
	}
	return false
}

func onboardingGoogleJobConfig(googleEmail, refreshToken string) map[string]interface{} {
	return map[string]interface{}{
		"email":         googleEmail,
		"refresh_token": refreshToken,
	}
}

// applyOnboardingJobSchedule sets interval/on on create. project_id lives on google_backup_credentials only.
func applyOnboardingJobSchedule(database *db.PostgresDb, jobID uint, schedule onboardingSchedule, projectID string) error {
	_ = projectID
	return database.CronJobRepo.UpdateCronJobByID(jobID, map[string]interface{}{
		"interval": schedule.Interval,
		"on":       schedule.On,
	})
}

// Legacy: set storx_token on create and auto-activate daily jobs when grant was resolved on onboarding.
/*
func applyOnboardingJobScheduleWithStorx(ctx context.Context, database *db.PostgresDb, jobID uint, schedule onboardingSchedule, storxToken, projectID, syncType string) error {
	patch := map[string]interface{}{
		"interval": schedule.Interval,
		"on":       schedule.On,
	}
	if projectID != "" {
		patch["storj_project_id"] = projectID
	}
	if storxToken != "" {
		patch["storx_token"] = storxToken
		if pid, err := satellite.GetProjectIDFromAccessGrant(ctx, storxToken); err == nil && pid != "" {
			patch["storj_project_id"] = pid
		}
		if syncType == "daily" {
			patch["active"] = true
		}
	}
	return database.CronJobRepo.UpdateCronJobByID(jobID, patch)
}
*/

// LEGACY(credential-migration): replaced by createGoogleJobsForServiceEmails — kept for reference, do not remove.
/*
func onboardingCreateGmailJobs(
	ctx context.Context, c echo.Context, userID, syncType, svc string, schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest, database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	toCreate, normErr := normalizeGmailEmails(req.Emails, req.GoogleEmail)
	if normErr != nil {
		return nil, onboardingFailFromError(svc, "", normErr)
	}
	if err := validateGmailAdminDomainForOnboarding(toCreate, req.GoogleEmail, req.AccountType); err != nil {
		return nil, onboardingFailFromError(svc, "", err)
	}
	parentID := getCorporateParentID(req.GoogleEmail, toCreate)
	success, createFailed := createJobsForEmails(ctx, c, userID, "gmail", syncType, req.GoogleEmail, req.RefreshToken, toCreate, parentID, database)
	return onboardingResultsFromCreate(database, svc, schedule, req.ProjectID, success, createFailed)
}

func onboardingCreateMediaJob(
	ctx context.Context, c echo.Context, userID, method, svc, syncType string, schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest, database *db.PostgresDb,
) (*onboardingJobResult, *onboardingFailedResult) {
	data, createErr := createSyncJob(userID, req.GoogleEmail, method, syncType, onboardingGoogleJobConfig(req.GoogleEmail, req.RefreshToken), c)
	if createErr != nil {
		return nil, &onboardingFailedResult{Service: svc, Email: req.GoogleEmail, Error: extractCreateJobError(createErr)}
	}
	cronJob, ok := data.(*repo.CronJobListingDB)
	if !ok {
		return nil, &onboardingFailedResult{Service: svc, Email: req.GoogleEmail, Error: "invalid job response"}
	}
	if err := applyOnboardingJobSchedule(database, cronJob.ID, schedule, req.ProjectID); err != nil {
		return nil, &onboardingFailedResult{Service: svc, Email: req.GoogleEmail, Error: err.Error()}
	}
	entry := &onboardingJobResult{Service: svc, Email: req.GoogleEmail, JobID: cronJob.ID}
	if syncType == "one_time" {
		if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
			entry.TaskID = task.ID
		}
	}
	return entry, nil
}
*/

// LEGACY(credential-migration): used only by commented onboardingCreateGmailJobs — not called by active flow.
/*
func onboardingResultsFromCreate(
	database *db.PostgresDb, svc string, schedule onboardingSchedule,
	projectID string, success, createFailed []map[string]interface{},
) ([]onboardingJobResult, []onboardingFailedResult) {
	jobs := make([]onboardingJobResult, 0, len(success))
	failed := make([]onboardingFailedResult, 0, len(createFailed))
	for _, item := range success {
		entry := onboardingJobResult{Service: svc}
		if email, ok := item["email"].(string); ok {
			entry.Email = email
		}
		entry.JobID = jobIDFromCreateItem(item)
		if tid, ok := item["task_id"].(uint); ok {
			entry.TaskID = tid
		} else if tidf, ok := item["task_id"].(float64); ok {
			entry.TaskID = uint(tidf)
		}
		if entry.JobID > 0 {
			if patchErr := applyOnboardingJobSchedule(database, entry.JobID, schedule, projectID); patchErr != nil {
				failed = append(failed, onboardingFailedResult{
					Service: svc, Email: entry.Email,
					Error: fmt.Sprintf("job %d created but activation failed: %v", entry.JobID, patchErr),
				})
				continue
			}
		}
		jobs = append(jobs, entry)
	}
	for _, item := range createFailed {
		fe := onboardingFailedResult{Service: svc}
		if email, ok := item["email"].(string); ok {
			fe.Email = email
		}
		if msg, ok := item["error"].(string); ok {
			fe.Error = msg
		} else {
			fe.Error = fmt.Sprintf("%v", item["error"])
		}
		failed = append(failed, fe)
	}
	return jobs, failed
}

func jobIDFromCreateItem(item map[string]interface{}) uint {
	if id, ok := item["job_id"].(uint); ok {
		return id
	}
	if idf, ok := item["job_id"].(float64); ok {
		return uint(idf)
	}
	return 0
}

func onboardingFailFromError(svc, email string, err error) []onboardingFailedResult {
	if he, ok := err.(*echo.HTTPError); ok {
		return []onboardingFailedResult{{Service: svc, Email: email, Error: fmt.Sprintf("%v", he.Message)}}
	}
	return []onboardingFailedResult{{Service: svc, Email: email, Error: err.Error()}}
}
*/

// --- Sync create helpers (shared for Gmail and future Outlook) ---
func syncCreateMessage(syncType string) string {
	if syncType == "one_time" {
		return "One-time Automatic Backup Created Successfully"
	}
	return "Daily Automatic Backup Created Successfully"
}

// normalizeGmailEmails dedupes and validates; empty list => [connectedEmail].
func normalizeGmailEmails(emails []string, connectedEmail string) ([]string, error) {
	if len(emails) == 0 {
		return []string{connectedEmail}, nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] || !isValidEmail(e) {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"error": "No valid emails provided"})
	}
	return out, nil
}

// validateGmailAdminDomainForOnboarding uses Satellite account_type (no Google token exchange on create).
func validateGmailAdminDomainForOnboarding(toCreate []string, connectedEmail, accountType string) error {
	adminDomain := google.ExtractDomainFromEmail(connectedEmail)
	needsAdminCheck := false
	for _, e := range toCreate {
		if !strings.EqualFold(e, connectedEmail) {
			needsAdminCheck = true
			break
		}
	}
	if !needsAdminCheck {
		return nil
	}
	if adminDomain == "" {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": "Only admins can backup other users' accounts"})
	}
	for _, e := range toCreate {
		if !strings.EqualFold(e, connectedEmail) && google.ExtractDomainFromEmail(e) != adminDomain {
			return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": "Only admins can backup accounts from their own domain"})
		}
	}
	if !strings.EqualFold(strings.TrimSpace(accountType), "admin_workspace") {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": "Only Google Workspace admins can backup other users' accounts"})
	}
	return nil
}

// validateGmailAdminDomain — legacy: called Google IsUserAdmin with access_token on job create. Not used; onboarding uses validateGmailAdminDomainForOnboarding (account_type from Satellite).
/*
func validateGmailAdminDomain(ctx context.Context, toCreate []string, connectedEmail, accessToken string) error {
	adminDomain := google.ExtractDomainFromEmail(connectedEmail)
	needsAdminCheck := false
	for _, e := range toCreate {
		if !strings.EqualFold(e, connectedEmail) {
			needsAdminCheck = true
			break
		}
	}
	if !needsAdminCheck {
		return nil
	}
	if adminDomain == "" {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": "Only admins can backup other users' accounts"})
	}
	for _, e := range toCreate {
		if !strings.EqualFold(e, connectedEmail) && google.ExtractDomainFromEmail(e) != adminDomain {
			return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": "Only admins can backup accounts from their own domain"})
		}
	}
	adminOK, err := google.IsUserAdmin(ctx, accessToken, connectedEmail)
	if err != nil || !adminOK {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": "Only Google Workspace admins can backup other users' accounts"})
	}
	return nil
}
*/

// buildCronNotification returns the standard notification payload for cron_created events.
func buildCronNotification(method, name, syncType string, jobID uint) map[string]interface{} {
	m := map[string]interface{}{
		"event": "cron_created", "level": 2, "method": method, "name": name, "type": syncType, "timestamp": "now",
	}
	if jobID > 0 {
		m["job_id"] = jobID
	}
	return m
}

// dedupeEmailsPreservingOrder drops duplicate addresses (case-insensitive), empty strings, and keeps the first trimmed spelling for API responses.
func dedupeEmailsPreservingOrder(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	out := make([]string, 0, len(emails))
	for _, raw := range emails {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

// LEGACY(credential-migration): corporate parent_id / placeholder create path — kept for reference, do not remove.
/*
// getCorporateParentID returns connected admin email for corporate selections (at least one target differs from connectedEmail).
// Personal/self backups keep parent_id null.
func getCorporateParentID(connectedEmail string, targets []string) *string {
	for _, e := range targets {
		if !strings.EqualFold(strings.TrimSpace(e), strings.TrimSpace(connectedEmail)) {
			parent := strings.TrimSpace(connectedEmail)
			return &parent
		}
	}
	return nil
}

// ensureGmailPlaceholderAdminJob creates or updates a hidden admin row (placeholder=true) that holds OAuth for delegated mailboxes when the admin did not select their own mailbox for backup.
func ensureGmailPlaceholderAdminJob(database *db.PostgresDb, userID, syncType, adminEmail, refreshToken string) error {
	adminEmail = strings.TrimSpace(adminEmail)
	if adminEmail == "" {
		return nil
	}
	existing, ok, err := database.CronJobRepo.FindGmailJobByUserNameSyncType(userID, adminEmail, syncType)
	if err != nil {
		return err
	}
	if ok && existing != nil {
		if !existing.Placeholder {
			return nil
		}
		merged := mergeJobInputData(existing, map[string]interface{}{"refresh_token": refreshToken})
		return database.CronJobRepo.UpdateCronJobByID(existing.ID, map[string]interface{}{"input_data": merged})
	}
	_, err = database.CronJobRepo.CreateCronJobForUserWithPlaceholder(userID, adminEmail, "gmail", syncType, map[string]interface{}{
		"email":         adminEmail,
		"refresh_token": refreshToken,
	}, true)
	return err
}

// createJobsForEmails creates one job per email. Refresh token is stored only on the admin mailbox row; corporate children omit it and resolve from parent. StorX access grant follows the same pattern: set storx_token on the admin job (or via any child update — it is persisted on the parent row).
func createJobsForEmails(ctx context.Context, c echo.Context, userID, method, syncType string, connectedEmail, refreshToken string, emails []string, parentID *string, database *db.PostgresDb) (success, failed []map[string]interface{}) {
	emails = dedupeEmailsPreservingOrder(emails)
	success = make([]map[string]interface{}, 0, len(emails))
	failed = make([]map[string]interface{}, 0)
	priority := "normal"
	batchNotify := len(emails) > 1
	if strings.TrimSpace(refreshToken) == "" {
		for _, targetEmail := range emails {
			failed = append(failed, map[string]interface{}{"email": targetEmail, "error": "OAuth refresh token required"})
		}
		return success, failed
	}
	connTrim := strings.TrimSpace(connectedEmail)
	connKey := strings.ToLower(connTrim)
	adminInList := false
	for _, e := range emails {
		if strings.ToLower(e) == connKey {
			adminInList = true
			break
		}
	}
	if parentID != nil && !adminInList && method == "gmail" {
		if err := ensureGmailPlaceholderAdminJob(database, userID, syncType, connTrim, refreshToken); err != nil {
			for _, targetEmail := range emails {
				failed = append(failed, map[string]interface{}{"email": targetEmail, "error": err.Error()})
			}
			return success, failed
		}
	}

	var gmailByLower map[string]*repo.CronJobListingDB
	if method == "gmail" {
		var batchErr error
		gmailByLower, batchErr = database.CronJobRepo.FindGmailJobsByUserSyncTypeAndNames(userID, syncType, emails)
		if batchErr != nil {
			for _, targetEmail := range emails {
				failed = append(failed, map[string]interface{}{"email": targetEmail, "error": batchErr.Error()})
			}
			return success, failed
		}
	}

	for _, targetEmail := range emails {
		tEmail := targetEmail
		if method == "gmail" {
			existing := gmailByLower[strings.ToLower(tEmail)]
			if existing != nil && existing.Placeholder && strings.EqualFold(tEmail, connTrim) {
				merged := mergeJobInputData(existing, map[string]interface{}{"refresh_token": refreshToken})
				upd := map[string]interface{}{"placeholder": false, "input_data": merged}
				if syncType == "one_time" {
					upd["active"] = true
				}
				if err := database.CronJobRepo.UpdateCronJobByID(existing.ID, upd); err != nil {
					failed = append(failed, map[string]interface{}{"email": targetEmail, "error": err.Error()})
					continue
				}
				existing.Placeholder = false
				item := map[string]interface{}{"email": targetEmail, "job_id": existing.ID}
				if syncType == "one_time" {
					task, taskErr := database.TaskRepo.CreateTaskForCronJob(existing.ID)
					if taskErr != nil {
						logger.Warn(ctx, "CreateTaskForCronJob failed after placeholder job was promoted",
							logger.String("user_id", userID),
							logger.String("email", targetEmail),
							logger.String("method", method),
							logger.String("sync_type", syncType),
							logger.ErrorField(taskErr))
						success = append(success, item)
						if !batchNotify {
							notificationData := buildCronNotification(method, targetEmail, syncType, existing.ID)
							satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %s has been created successfully", targetEmail), &priority, notificationData, nil)
						}
						continue
					}
					item["task_id"] = task.ID
				}
				success = append(success, item)
				if !batchNotify {
					notificationData := buildCronNotification(method, targetEmail, syncType, existing.ID)
					satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %s has been created successfully", targetEmail), &priority, notificationData, nil)
				}
				continue
			}
		}
		config := map[string]interface{}{
			"email": targetEmail,
		}
		// Corporate: store refresh_token only on the admin row (real job or placeholder). All delegated mailboxes omit it and resolve via parent_id + GmailResolvedRefreshToken.
		isAdminMailbox := strings.EqualFold(tEmail, connTrim)
		skipLocalToken := parentID != nil && !isAdminMailbox
		if !skipLocalToken {
			config["refresh_token"] = refreshToken
		}
		data, createErr := createSyncJob(userID, targetEmail, method, syncType, config, c)
		if createErr != nil {
			logger.Warn(ctx, "createSyncJob failed",
				logger.String("user_id", userID),
				logger.String("email", targetEmail),
				logger.String("method", method),
				logger.String("sync_type", syncType),
				logger.ErrorField(createErr))
			failed = append(failed, map[string]interface{}{"email": targetEmail, "error": extractCreateJobError(createErr)})
			continue
		}
		cronJob, ok := data.(*repo.CronJobListingDB)
		if !ok {
			failed = append(failed, map[string]interface{}{"email": targetEmail, "error": "invalid job response"})
			continue
		}
		item := map[string]interface{}{"email": targetEmail, "job_id": cronJob.ID}
		if syncType == "one_time" {
			task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID)
			if taskErr != nil {
				logger.Warn(ctx, "CreateTaskForCronJob failed after job was created",
					logger.String("user_id", userID),
					logger.String("email", targetEmail),
					logger.String("method", method),
					logger.String("sync_type", syncType),
					logger.Int("job_id", int(cronJob.ID)),
					logger.ErrorField(taskErr))
				success = append(success, item)
				if !batchNotify {
					notificationData := buildCronNotification(method, targetEmail, syncType, cronJob.ID)
					satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %s has been created successfully", targetEmail), &priority, notificationData, nil)
				}
				continue
			}
			item["task_id"] = task.ID
		}
		success = append(success, item)
		if !batchNotify {
			notificationData := buildCronNotification(method, targetEmail, syncType, cronJob.ID)
			satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %s has been created successfully", targetEmail), &priority, notificationData, nil)
		}
	}
	if batchNotify && len(success) > 0 {
		notificationData := buildCronNotification(method, "", syncType, 0)
		notificationData["count"] = len(success)
		satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %d account(s) has been created successfully", len(success)), &priority, notificationData, nil)
	}
	return success, failed
}
*/

func extractCreateJobError(createErr error) string {
	var httpErr *echo.HTTPError
	if errors.As(createErr, &httpErr) && httpErr.Message != nil {
		if m, ok := httpErr.Message.(map[string]interface{}); ok && m["message"] != nil {
			return fmt.Sprintf("%v", m["message"])
		}
		return fmt.Sprintf("%v", httpErr.Message)
	}
	return createErr.Error()
}

// respondSyncCreate sends unified response: message, success, failed. For single-job (Outlook/DB) the job is placed in success[0]; no separate "data".
// Gmail bulk path passes singleJobData=nil; success/failed contain only email, job_id, task_id.
func respondSyncCreate(c echo.Context, syncType string, success, failed []map[string]interface{}, singleJobData interface{}, task interface{}) error {
	resp := map[string]interface{}{
		"message": syncCreateMessage(syncType),
		"success": success,
		"failed":  failed,
	}
	if singleJobData != nil {
		if job, ok := singleJobData.(*repo.CronJobListingDB); ok {
			repo.MaskTokenForCronJobDB(job)
			resp["success"] = []*repo.CronJobListingDB{job}
		}
	}
	if task != nil {
		resp["task"] = task
	}
	return c.JSON(http.StatusOK, resp)
}

// createSingleSyncJobAndRespond creates one job (legacy Outlook/DB create — kept for future services[] onboarding).
func createSingleSyncJobAndRespond(ctx context.Context, c echo.Context, userID, method, syncType, name string, config map[string]interface{}, database *db.PostgresDb) error {
	data, err := createSyncJob(userID, name, method, syncType, config, c)
	if err != nil {
		return err
	}
	priority := "normal"
	var jobID uint
	if cronJob, ok := data.(*repo.CronJobListingDB); ok {
		jobID = cronJob.ID
	}
	notificationData := buildCronNotification(method, name, syncType, jobID)
	satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %s has been created successfully", name), &priority, notificationData, nil)

	cronJob, _ := data.(*repo.CronJobListingDB)
	success := []map[string]interface{}{}
	failed := []map[string]interface{}{}
	var task interface{}
	if cronJob != nil {
		item := map[string]interface{}{"email": cronJob.Name, "job_id": cronJob.ID}
		if syncType == "one_time" {
			t, err := database.TaskRepo.CreateTaskForCronJob(cronJob.ID)
			if err != nil {
				return jsonError(http.StatusInternalServerError, "Failed to create task for one-time backup", err)
			}
			item["task_id"] = t.ID
			task = t
		}
		success = append(success, item)
	}
	return respondSyncCreate(c, syncType, success, failed, data, task)
}

// ProcessOutlookMethod exchanges OAuth code for refresh_token (legacy create — future: outlook in services[] + refresh_token in JSON).
func ProcessOutlookMethod(code string) (string, map[string]interface{}, error) {
	if code == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Code is required")
	}

	tok, err := outlook.AuthTokenUsingCode(code)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Code. Not able to generate auth token from code", err)
	}

	client, err := outlook.NewOutlookClientUsingToken(tok.AccessToken)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Code. May be it is expired or invalid", err)
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil || userDetails.Mail == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Invalid Code. May be it is expired or invalid")
	}

	config := map[string]interface{}{
		"refresh_token": tok.RefreshToken,
		"email":         userDetails.Mail,
	}

	return userDetails.Mail, config, nil
}

func ProcessOutlookAccessToken(accessToken string) (string, map[string]interface{}, error) {
	if accessToken == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Access Token Required")
	}

	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Access Token. May be it is expired or invalid", err)
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil || userDetails.Mail == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid")
	}

	config := map[string]interface{}{
		"access_token": accessToken,
		"email":        userDetails.Mail,
	}

	return userDetails.Mail, config, nil
}

func ProcessDatabaseMethod(reqBody DatabaseConnection) (string, map[string]interface{}, error) {
	if reqBody.Name == "" || reqBody.DatabaseName == "" || reqBody.Host == "" ||
		reqBody.Port == "" || reqBody.Username == "" || reqBody.Password == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "All fields are required")
	}

	config := map[string]interface{}{
		"database_name": reqBody.DatabaseName,
		"host":          reqBody.Host,
		"port":          reqBody.Port,
		"username":      reqBody.Username,
		"password":      reqBody.Password,
		"email":         reqBody.Name,
	}

	return reqBody.Name, config, nil
}

// Helper functions
func createSyncJob(userID, name, method, syncType string, config map[string]interface{}, c echo.Context) (interface{}, error) {
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Check for existing jobs using original name (before adding timestamp)
	if err := checkExistingJobs(userID, name, syncType, method, database); err != nil {
		return nil, err
	}

	data, err := database.CronJobRepo.CreateCronJobForUser(userID, name, method, syncType, config)
	if err != nil {
		return nil, handleDBError(err)
	}

	return data, nil
}

func checkExistingJobs(userID, name, syncType, method string, db *db.PostgresDb) error {
	existingJobs, err := db.CronJobRepo.GetAllCronJobsForUserUnfiltered(userID)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "Failed to check existing jobs", err)
	}

	serviceName := getServiceName(method)

	// Exact duplicate: same user, mailbox name, sync_type, and method (separate jobs per service, same email OK).
	for _, job := range existingJobs {
		if job.Name != name || job.SyncType != syncType || job.Method != method {
			continue
		}
		if method == "gmail" && job.Placeholder {
			continue
		}
		return jsonErrorMsg(http.StatusBadRequest,
			fmt.Sprintf("A %s %s backup for %s already exists for your account", syncType, serviceName, name))
	}

	for _, job := range existingJobs {
		// Only check conflicts for jobs of the same method and name
		if job.Method != method || job.Name != name {
			continue
		}

		// Check for name conflicts between daily and one_time syncs
		if syncType == "one_time" && job.SyncType == "daily" {
			// Daily sync blocks one_time sync creation (regardless of active status)
			return jsonErrorMsg(http.StatusBadRequest, "A daily sync already exists with this "+name+". Cannot create one-time sync.")
		}

		if syncType == "daily" && job.SyncType == "one_time" {
			// One_time sync blocks daily sync unless it's completed (success) or failed
			if job.Status != repo.JobStatusSuccess && job.Status != repo.JobStatusFailed {
				return jsonErrorMsg(http.StatusBadRequest, "A one-time sync with this name is still in progress. Wait for it to complete or fail before creating a daily sync.", "A one-time sync with this name is still in progress. Wait for it to complete or fail before creating a daily sync.")
			}
		}

		// Check if there are running tasks for this job (for same sync type)
		if job.SyncType == syncType {
			hasRunningTasks, err := hasRunningTasksForJob(db.TaskRepo, job.ID)
			if err != nil {
				return jsonError(http.StatusInternalServerError, "Failed to check task status", err)
			}

			if hasRunningTasks {
				errorMsg := fmt.Sprintf("A backup job for this %s is currently running. Cannot create %s backup.", serviceName, syncType)
				return jsonErrorMsg(http.StatusBadRequest, errorMsg, errorMsg)
			}
		}
	}

	return nil
}

func hasRunningTasksForJob(taskRepo *repo.TaskRepository, jobID uint) (bool, error) {
	// Get all tasks for the job and check if any are running or pushed
	tasks, err := taskRepo.ListAllTasksByJobID(jobID, 100, 0)
	if err != nil {
		return false, err
	}

	for _, task := range tasks {
		if task.Status == "running" || task.Status == "pushed" {
			return true, nil
		}
	}
	return false, nil
}

func getServiceName(method string) string {
	switch method {
	case "gmail":
		return "Gmail"
	case "outlook":
		return "Outlook"
	case "google_drive":
		return "Google Drive"
	case "google_photos":
		return "Google Photos"
	case "google_calendar":
		return "Google Calendar"
	case "google_contacts":
		return "Google Contacts"
	case "psql_database", "mysql_database":
		return "database"
	default:
		return "backup"
	}
}

// Common error functions
func jsonErrorMsg(status int, message string, details ...string) error {
	detailMsg := ""
	if len(details) > 0 {
		detailMsg = details[0]
	}
	return echo.NewHTTPError(status, map[string]interface{}{
		"error":   message,
		"details": detailMsg,
	})
}

func jsonError(code int, message string, err error) *echo.HTTPError {
	return echo.NewHTTPError(code, map[string]interface{}{
		"message": message,
		"error":   err.Error(),
	})
}

func handleDBError(err error) *echo.HTTPError {
	if strings.Contains(err.Error(), "duplicate key value") {
		if strings.Contains(err.Error(), "idx_name_sync_type_user") {
			return jsonError(http.StatusBadRequest, "A backup job for this email, service, and sync type already exists for your account", err)
		}
		return jsonError(http.StatusBadRequest, "Email already exists", err)
	}
	return jsonError(http.StatusInternalServerError, "Internal Server Error", err)
}

// mergeJobInputData returns a new map with existing job.InputData merged with updates.
func mergeJobInputData(job *repo.CronJobListingDB, updates map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{})
	if job.InputData != nil && job.InputData.Json() != nil {
		for k, v := range *job.InputData.Json() {
			out[k] = v
		}
	}
	if updates != nil {
		for k, v := range updates {
			out[k] = v
		}
	}
	return out
}

type AutomaticBackupUpdateRequest struct {
	Interval           *string             `json:"interval"`
	On                 *string             `json:"on"`
	Code               *string             `json:"code"`
	RefreshToken       *string             `json:"refresh_token"`
	DatabaseConnection *DatabaseConnection `json:"database_connection"`
	StorxToken         *string             `json:"storx_token"`
	Active             *bool               `json:"active"`
	ApplyStorxToAll    *bool               `json:"apply_storx_to_all_linked_accounts"`
}

// jobGmailMailbox returns input_data.email when set, otherwise job name.
func jobGmailMailbox(job *repo.CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if job.InputData != nil && job.InputData.Json() != nil {
		if s, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return strings.TrimSpace(job.Name)
}

// gmailOAuthMergeBaseJob — DISABLED(parent_id): refresh_token updates go to google_backup_credentials.
/*
func gmailOAuthMergeBaseJob(...) (*repo.CronJobListingDB, error) {
	...
}
*/

// oauthInputDataFromBackupRequest stores refresh_token from Satellite on job update (no OAuth code exchange).
func oauthInputDataFromBackupRequest(database *db.PostgresDb, job *repo.CronJobListingDB, req *AutomaticBackupUpdateRequest) (map[string]interface{}, error) {
	// Legacy: OAuth code → new refresh_token
	// if req.Code != nil {
	// 	return gmailInputDataAfterCodeReauth(database, job, *req.Code)
	// }
	if req.Code != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "code update is disabled; send refresh_token in JSON from Satellite"})
	}
	if req.RefreshToken != nil {
		rt := strings.TrimSpace(*req.RefreshToken)
		if rt == "" {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "refresh_token cannot be empty")
		}
		switch job.Method {
		case "outlook", "gmail", "google_drive", "google_photos", "google_calendar", "google_contacts":
			return storeRefreshTokenInputData(database, job, rt)
		default:
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"message": fmt.Sprintf("refresh_token update is not supported for method %s", job.Method),
			})
		}
	}
	return nil, nil
}

// storeRefreshTokenInputData persists Satellite-provided refresh_token without Google/Outlook token exchange.
func storeRefreshTokenInputData(database *db.PostgresDb, job *repo.CronJobListingDB, refreshToken string) (map[string]interface{}, error) {
	if cid := repo.JobCredentialID(job); cid > 0 && repo.IsGoogleMediaOrGmailMethod(job.Method) {
		rt := strings.TrimSpace(refreshToken)
		if err := database.CredentialRepo.UpdateTokens(cid, &rt, nil); err != nil {
			return nil, err
		}
		out := mergeJobInputData(job, map[string]interface{}{})
		delete(out, "refresh_token")
		if out == nil {
			out = map[string]interface{}{}
		}
		out["credential_id"] = cid
		if e := strings.TrimSpace(job.Name); e != "" {
			out["email"] = e
		}
		return out, nil
	}
	// DISABLED(parent_id): gmailOAuthMergeBaseJob for admin row merge.
	out := mergeJobInputData(job, map[string]interface{}{"refresh_token": refreshToken})
	delete(out, "credential_id")
	delete(out, "connected_email")
	return out, nil
}

// Legacy: ExchangeCodeForTokenWithAdminScope → refresh_token
//
//	func gmailInputDataAfterCodeReauth(database *db.PostgresDb, job *repo.CronJobListingDB, code string) (map[string]interface{}, error) { ... }
//
// Legacy: AuthTokenUsingRefreshToken validation before store
//
//	func gmailInputDataAfterRefreshToken(...) { ... }
//	func outlookInputDataAfterRefreshToken(...) { ... }

func HandleAutomaticSyncCreateTask(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return sendJSONError(c, http.StatusBadRequest, "Invalid Job ID", err)
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return sendJSONError(c, http.StatusUnauthorized, "Invalid Request", err)
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	job, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return sendJSONError(c, http.StatusNotFound, "Job not found", err)
	}

	if job.SyncType != "one_time" {
		return sendJSONError(c, http.StatusBadRequest, "Job is not a one-time job", nil)
	}

	hasRunningTasks, err := hasRunningTasksForJob(database.TaskRepo, job.ID)
	if err != nil {
		return sendJSONError(c, http.StatusInternalServerError, "Failed to check task status", err)
	}

	if hasRunningTasks {
		return sendJSONError(c, http.StatusBadRequest, "one time backup is already running wait for it to complete", nil)
	}

	// One job = one account; create a single task for this job.
	task, err := database.TaskRepo.CreateTaskForCronJob(job.ID)
	if err != nil {
		return sendJSONError(c, http.StatusInternalServerError, "Failed to create task", err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Task created successfully",
		"data":    task,
	})
}

func isValidEmail(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 || !strings.Contains(s, "@") {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1
}

func sendJSONError(c echo.Context, status int, message string, err error) error {
	response := map[string]interface{}{
		"message": message,
	}
	if err != nil {
		response["error"] = err.Error()
	}
	return c.JSON(status, response)
}

func httpErr(status int, message string, errText string) error {
	return echo.NewHTTPError(status, map[string]interface{}{"message": message, "error": errText})
}

func activeStateUpdateFields(active bool) map[string]interface{} {
	update := map[string]interface{}{
		"active": active,
	}
	if active {
		update["message"] = "Your automatic backup is activated. It will start processing the first backup soon."
		update["message_status"] = repo.JobMessageStatusInfo
		update["auto_deactivated"] = false
		update["failure_periods"] = uint(0)
		return update
	}
	update["message"] = "Your automatic backup is deactivated. It will not process any backups."
	update["message_status"] = repo.JobMessageStatusInfo
	return update
}

func credentialEmailMatches(cred *repo.GoogleBackupCredentialDB, email string) bool {
	if cred == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cred.Email), strings.TrimSpace(email))
}

func resolveUserCredentialByProjectAndEmail(database *db.PostgresDb, userID, projectID, email string) (*repo.GoogleBackupCredentialDB, error) {
	projectID = strings.TrimSpace(projectID)
	email = strings.TrimSpace(email)
	if projectID == "" {
		return nil, httpErr(http.StatusBadRequest, "Invalid Request", "project_id is required")
	}
	if email == "" {
		return nil, httpErr(http.StatusBadRequest, "Invalid Request", "google_email is required")
	}
	credID, ok, err := database.CredentialRepo.FindIDForUserAndProjectID(userID, projectID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, httpErr(http.StatusNotFound, "Invalid Request", "no backup credential found for this project_id; complete onboarding first")
	}
	cred, err := database.CredentialRepo.GetByID(credID)
	if err != nil {
		return nil, fmt.Errorf("load credential: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(cred.StorjProjectID), projectID) {
		return nil, httpErr(http.StatusNotFound, "Invalid Request", "project_id does not match stored credential")
	}
	if !credentialEmailMatches(cred, email) {
		return nil, httpErr(http.StatusBadRequest, "Invalid Request", "google_email does not match the connected account for this project_id")
	}
	return cred, nil
}

func parseConnectedAccountSchedule(interval, on *string) (intervalVal, onVal string, err error) {
	if (interval == nil && on != nil) || (interval != nil && on == nil) {
		return "", "", httpErr(http.StatusBadRequest, "Invalid Request", "interval and on must be sent together")
	}
	if interval == nil {
		return "", "", nil
	}
	intervalVal = strings.TrimSpace(*interval)
	onVal = ""
	if on != nil {
		onVal = strings.TrimSpace(*on)
	}
	if onVal == "" && intervalVal != "1h" && intervalVal != "6h" {
		return "", "", httpErr(http.StatusBadRequest, "Invalid Request", "on is required for this interval (use empty on for 1h or 6h)")
	}
	if intervalVal == "monthly" {
		day, convErr := strconv.Atoi(onVal)
		if convErr != nil {
			return "", "", httpErr(http.StatusBadRequest, "Invalid Request", "on must be a valid day number for monthly interval")
		}
		onVal = strconv.Itoa(day)
		if day == 29 || day == 30 || day == 31 {
			return "", "", httpErr(http.StatusBadRequest, "Invalid Request", "monthly backups cannot be scheduled on the 29th, 30th or 31st; use 1-28")
		}
	}
	if !validateInterval(intervalVal, onVal) {
		return "", "", httpErr(http.StatusBadRequest, "Invalid Request", "on is not valid for the given interval")
	}
	return intervalVal, onVal, nil
}

func extractAndStoreProjectIDForCredential(ctx context.Context, database *db.PostgresDb, credentialID uint, storxToken string) {
	if database == nil || credentialID == 0 || strings.TrimSpace(storxToken) == "" {
		return
	}
	projectID, err := satellite.GetProjectIDFromAccessGrant(ctx, storxToken)
	if err != nil || projectID == "" {
		return
	}
	if err := database.CredentialRepo.UpdateStorjProjectID(credentialID, projectID); err != nil {
		logger.Warn(ctx, "failed to update storj_project_id on credential",
			logger.Int("credential_id", int(credentialID)), logger.ErrorField(err))
	}
}

func applyConnectedAccountCredentialUpdate(ctx context.Context, database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, req *GoogleConnectedAccountUpdateRequest) error {
	if cred == nil || database == nil || req == nil {
		return nil
	}
	var refreshPtr, storxPtr *string
	if req.RefreshToken != nil {
		rt := strings.TrimSpace(*req.RefreshToken)
		if rt == "" {
			return httpErr(http.StatusBadRequest, "Invalid Request", "refresh_token cannot be empty")
		}
		refreshPtr = &rt
	}
	if req.StorxToken != nil {
		st := strings.TrimSpace(*req.StorxToken)
		if st == "" {
			return httpErr(http.StatusBadRequest, "Invalid Request", "storx_token cannot be empty")
		}
		storxPtr = &st
		extractAndStoreProjectIDForCredential(ctx, database, cred.ID, st)
	}
	if refreshPtr == nil && storxPtr == nil {
		return nil
	}
	return database.CredentialRepo.UpdateTokens(cred.ID, refreshPtr, storxPtr)
}

func linkedJobPatchFromConnectedAccountUpdate(job *repo.CronJobListingDB, intervalVal, onVal string, active *bool, activateAllOnStorx bool) map[string]interface{} {
	if job == nil {
		return nil
	}
	patch := map[string]interface{}{}
	if job.SyncType != "one_time" && intervalVal != "" {
		patch["interval"] = intervalVal
		patch["on"] = onVal
	}
	applyActive := false
	if active != nil {
		applyActive = *active
	} else if activateAllOnStorx {
		applyActive = true
	}
	if active != nil || activateAllOnStorx {
		for k, v := range activeStateUpdateFields(applyActive) {
			patch[k] = v
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}

// HandleAutomaticBackupUpdateByProject updates google_backup_credentials and all linked cron jobs (no job_id).
// PUT /auto-sync/job/project — body: project_id, google_email, storx_token, refresh_token, interval, on, active.
func HandleAutomaticBackupUpdateByProject(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	var reqBody GoogleConnectedAccountUpdateRequest
	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}
	projectID := strings.TrimSpace(reqBody.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(c.Param("project_id")) // optional legacy URL param
	}
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "project_id is required in request body",
		})
	}
	if !reqBody.hasUpdateFields() {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "at least one of storx_token, refresh_token, interval, on, or active is required",
		})
	}

	connectedEmail := reqBody.connectedEmail()
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	cred, err := resolveUserCredentialByProjectAndEmail(database, userID, projectID, connectedEmail)
	if err != nil {
		var he *echo.HTTPError
		if errors.As(err, &he) {
			return c.JSON(he.Code, he.Message)
		}
		logger.Error(ctx, "resolve credential for connected account update", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	intervalVal, onVal, schedErr := parseConnectedAccountSchedule(reqBody.Interval, reqBody.On)
	if schedErr != nil {
		var he *echo.HTTPError
		if errors.As(schedErr, &he) {
			return c.JSON(he.Code, he.Message)
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Request", "error": schedErr.Error()})
	}

	if err := applyConnectedAccountCredentialUpdate(ctx, database, cred, &reqBody); err != nil {
		var he *echo.HTTPError
		if errors.As(err, &he) {
			return c.JSON(he.Code, he.Message)
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to update credential",
			"error":   err.Error(),
		})
	}

	activateOnStorx := reqBody.StorxToken != nil
	jobs, err := database.CronJobRepo.ListJobsByCredentialID(userID, cred.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to list linked jobs",
			"error":   err.Error(),
		})
	}
	if len(jobs) == 0 {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "No jobs found for credential",
			"error":   "no autosync jobs linked to this project_id",
		})
	}

	success := make([]CronJobResponse, 0, len(jobs))
	failed := make([]map[string]interface{}, 0)
	for i := range jobs {
		job := jobs[i]
		patch := linkedJobPatchFromConnectedAccountUpdate(&job, intervalVal, onVal, reqBody.Active, activateOnStorx)
		if patch == nil {
			continue
		}
		if updErr := database.CronJobRepo.UpdateCronJobByID(job.ID, patch); updErr != nil {
			failed = append(failed, map[string]interface{}{
				"job_id": job.ID,
				"email":  job.Name,
				"error":  updErr.Error(),
			})
			continue
		}
		updated, getErr := database.CronJobRepo.GetCronJobByID(job.ID)
		if getErr != nil {
			failed = append(failed, map[string]interface{}{
				"job_id": job.ID,
				"email":  job.Name,
				"error":  getErr.Error(),
			})
			continue
		}
		database.CronJobRepo.EnrichCronJobFromCredential(updated)
		repo.MaskTokenForCronJobDB(updated)
		j := *updated
		success = append(success, CronJobResponse{
			CronJobListingDB: j,
			NextBackup:       calculateNextBackup(j),
		})
	}

	if len(success) == 0 && len(failed) == 0 {
		for i := range jobs {
			updated, getErr := database.CronJobRepo.GetCronJobByID(jobs[i].ID)
			if getErr != nil {
				continue
			}
			database.CronJobRepo.EnrichCronJobFromCredential(updated)
			repo.MaskTokenForCronJobDB(updated)
			j := *updated
			success = append(success, CronJobResponse{
				CronJobListingDB: j,
				NextBackup:       calculateNextBackup(j),
			})
		}
	}

	resp := map[string]interface{}{
		"message":       "Connected account backup updated successfully",
		"project_id":    projectID,
		"google_email":  connectedEmail,
		"credential_id": cred.ID,
		"success":       nullSliceJSON(success),
		"failed":        nullSliceJSON(failed),
	}
	return c.JSON(http.StatusOK, resp)
}

func buildUpdateRequestForJob(ctx context.Context, database *db.PostgresDb, job *repo.CronJobListingDB, reqBody AutomaticBackupUpdateRequest, jobID int) (map[string]interface{}, error) {
	updateRequest := map[string]interface{}{}

	if job.SyncType == "one_time" {
		if reqBody.Interval != nil || reqBody.On != nil || reqBody.DatabaseConnection != nil || reqBody.Active != nil ||
			reqBody.ApplyStorxToAll != nil {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "For one-time sync jobs, only storx_token and refresh_token updates are allowed")
		}
		oauthInputCount := 0
		if reqBody.Code != nil {
			oauthInputCount++
		}
		if reqBody.RefreshToken != nil {
			oauthInputCount++
		}
		if oauthInputCount > 1 {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "Only one of code or refresh_token may be set in a single request")
		}
		if reqBody.StorxToken != nil {
			if strings.TrimSpace(*reqBody.StorxToken) == "" {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "storx_token cannot be empty")
			}
			updateRequest["storx_token"] = *reqBody.StorxToken
			extractAndStoreProjectID(ctx, database, job, *reqBody.StorxToken, updateRequest, jobID, "one-time")
		}
		in, err := oauthInputDataFromBackupRequest(database, job, &reqBody)
		if err != nil {
			return nil, err
		}
		if in != nil {
			updateRequest["input_data"] = in
		}
		if len(updateRequest) == 0 {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "No valid update fields provided. Only storx_token and refresh_token are allowed for one-time jobs")
		}
		return updateRequest, nil
	}

	if (reqBody.Interval != nil && reqBody.On == nil) || (reqBody.On != nil && reqBody.Interval == nil) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "Both interval and on are required together"})
	}
	if reqBody.Interval != nil {
		onValue := ""
		if reqBody.On != nil {
			onValue = strings.TrimSpace(*reqBody.On)
		}
		intervalVal := strings.TrimSpace(*reqBody.Interval)
		if onValue == "" && intervalVal != "1h" && intervalVal != "6h" {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "on is required for this interval (use empty on for 1h or 6h)")
		}
		if intervalVal == "monthly" {
			day, err := strconv.Atoi(onValue)
			if err != nil {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "On value must be a valid number for monthly intervals")
			}
			onValue = strconv.Itoa(day)
			if day == 29 || day == 30 || day == 31 {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "Monthly backups cannot be scheduled on the 29th, 30th or 31st day. Please select a date between 1-28.")
			}
		}
		if !validateInterval(intervalVal, onValue) {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "on is not valid for the given interval")
		}
		updateRequest["interval"] = intervalVal
		updateRequest["on"] = onValue
	}
	inputKinds := 0
	if reqBody.Code != nil {
		inputKinds++
	}
	if reqBody.RefreshToken != nil {
		inputKinds++
	}
	if reqBody.DatabaseConnection != nil {
		inputKinds++
	}
	if inputKinds > 1 {
		return nil, httpErr(http.StatusBadRequest, "Invalid Request", "Only one of code, refresh_token, or database_connection may be set in a single request")
	}
	if reqBody.Code != nil {
		in, err := oauthInputDataFromBackupRequest(database, job, &reqBody)
		if err != nil {
			return nil, err
		}
		if in != nil {
			updateRequest["input_data"] = in
		}
	} else if reqBody.DatabaseConnection != nil {
		if job.Method != "psql_database" && job.Method != "mysql_database" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "database connection is not allowed for this method"})
		}
		dc := reqBody.DatabaseConnection
		if strings.TrimSpace(dc.Host) == "" || strings.TrimSpace(dc.DatabaseName) == "" || strings.TrimSpace(dc.Username) == "" {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "database_connection requires non-empty host, database_name, and username")
		}
		updateRequest["input_data"] = mergeJobInputData(job, map[string]interface{}{
			"host":          dc.Host,
			"port":          dc.Port,
			"username":      dc.Username,
			"password":      dc.Password,
			"database_name": dc.DatabaseName,
		})
	} else if reqBody.RefreshToken != nil {
		in, err := oauthInputDataFromBackupRequest(database, job, &reqBody)
		if err != nil {
			return nil, err
		}
		if in != nil {
			updateRequest["input_data"] = in
		}
	}
	if reqBody.StorxToken != nil {
		if strings.TrimSpace(*reqBody.StorxToken) == "" {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "storx_token cannot be empty")
		}
		updateRequest["storx_token"] = *reqBody.StorxToken
		extractAndStoreProjectID(ctx, database, job, *reqBody.StorxToken, updateRequest, jobID, "daily")
	}
	if reqBody.Active != nil {
		for k, v := range activeStateUpdateFields(*reqBody.Active) {
			updateRequest[k] = v
		}
	}
	if len(updateRequest) == 0 {
		return nil, httpErr(http.StatusBadRequest, "Invalid Request", "No valid update fields provided")
	}
	return updateRequest, nil
}

// func HandleAutomaticBackupUpdate(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	jobID, parseErr := strconv.Atoi(c.Param("job_id"))
// 	if parseErr != nil || jobID <= 0 {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"message": "Invalid Job ID",
// 		})
// 	}

// 	userID, err := satellite.GetUserdetails(c)
// 	if err != nil {
// 		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
// 			"message": "Authentication required",
// 			"error":   err.Error(),
// 		})
// 	}

// 	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

// 	job, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
// 	if err != nil {
// 		return c.JSON(http.StatusNotFound, map[string]interface{}{
// 			"message": "Job not found",
// 			"error":   err.Error(),
// 		})
// 	}

// 	var reqBody AutomaticBackupUpdateRequest

// 	if err := c.Bind(&reqBody); err != nil {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"message": "Invalid request body",
// 			"error":   err.Error(),
// 		})
// 	}

// 	updateRequest, err := buildUpdateRequestForJob(ctx, database, job, reqBody, jobID)
// 	if err != nil {
// 		var httpErr *echo.HTTPError
// 		if errors.As(err, &httpErr) {
// 			return c.JSON(httpErr.Code, httpErr.Message)
// 		}
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Request", "error": err.Error()})
// 	}

// 	if err := applyStorxUpdateChoiceForLinkedGmailAccounts(job, reqBody); err != nil {
// 		var httpErrObj *echo.HTTPError
// 		if errors.As(err, &httpErrObj) {
// 			return c.JSON(httpErrObj.Code, httpErrObj.Message)
// 		}
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Request", "error": err.Error()})
// 	}

// 	err = applyAutomaticBackupUpdates(database, job, uint(jobID), updateRequest)
// 	if err != nil {
// 		if strings.Contains(err.Error(), "not found") {
// 			return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 				"message": "Failed to update job",
// 				"error":   err.Error(),
// 			})
// 		}
// 		logger.Error(ctx, "Failed to update cron job", logger.Int("job_id", jobID), logger.ErrorField(err))
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"message": "Failed to update job",
// 			"error":   err.Error(),
// 		})
// 	}

// 	if reqBody.StorxToken != nil && reqBody.ApplyStorxToAll != nil && *reqBody.ApplyStorxToAll {
// 		var propagateErr error
// 		propagateErr = propagateCredentialLinkedActive(database, job, uint(jobID), reqBody.Active)
// 		if propagateErr != nil {
// 			logger.Error(ctx, "Failed to propagate linked active state after StorX update", logger.Int("job_id", jobID), logger.ErrorField(propagateErr))
// 			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 				"message": "Failed to update linked accounts",
// 				"error":   propagateErr.Error(),
// 			})
// 		}
// 	}

// 	data, err := database.CronJobRepo.GetCronJobByID(uint(jobID))
// 	if err != nil {
// 		logger.Error(ctx, "Failed to load job after update", logger.Int("job_id", jobID), logger.ErrorField(err))
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"message": "internal server error",
// 			"error":   err.Error(),
// 		})
// 	}

// 	jobCopy := *data
// 	database.CronJobRepo.EnrichCronJobFromCredential(&jobCopy)
// 	repo.MaskTokenForCronJobDB(&jobCopy)
// 	detail := CronJobDetailResponse{CronJobListingDB: jobCopy}
// 	return c.JSON(http.StatusOK, map[string]interface{}{
// 		"message": "Automatic backup updated successfully",
// 		"success": []CronJobDetailResponse{detail},
// 		"failed":  []interface{}{},
// 	})
// }

func bulkGmailUpdateFailure(jobID uint, name string, err error) map[string]interface{} {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return map[string]interface{}{"job_id": jobID, "email": name, "error": fmt.Sprintf("%v", he.Message)}
	}
	return map[string]interface{}{"job_id": jobID, "email": name, "error": err.Error()}
}

// HandleAutomaticBackupBulkUpdateByParent updates all jobs for a credential (project_id or google_email). LEGACY route name; not parent_id.
// Uses same buildUpdateRequestForJob / refresh_token path as PUT /auto-sync/job/:job_id (Satellite sends refresh_token in JSON).
// func HandleAutomaticBackupBulkUpdateByParent(c echo.Context) error {
// 	ctx := c.Request().Context()
// 	var err error
// 	defer monitor.Mon.Task()(&ctx)(&err)

// 	method := c.Param("method")
// 	if method != "gmail" {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"message": "Invalid Request",
// 			"error":   "bulk update by google_email is currently supported only for gmail method",
// 		})
// 	}

// 	userID, err := satellite.GetUserdetails(c)
// 	if err != nil {
// 		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
// 			"message": "Authentication required",
// 			"error":   err.Error(),
// 		})
// 	}

// 	var reqBody struct {
// 		AutomaticBackupUpdateRequest
// 		GoogleEmail string `json:"google_email"`
// 		ProjectID   string `json:"project_id"`
// 	}
// 	if err := c.Bind(&reqBody); err != nil {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"message": "Invalid request body",
// 			"error":   err.Error(),
// 		})
// 	}

// 	connectedEmail := strings.TrimSpace(reqBody.GoogleEmail)
// 	projectID := strings.TrimSpace(reqBody.ProjectID)
// 	if projectID == "" && connectedEmail == "" {
// 		return c.JSON(http.StatusBadRequest, map[string]interface{}{
// 			"message": "Invalid Request",
// 			"error":   "project_id or google_email is required in body for bulk update",
// 		})
// 	}
// 	// Legacy: getSatelliteGoogleCredentialsFromRequest(c)

// 	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
// 	var jobs []repo.CronJobListingDB
// 	var credID uint
// 	var ok bool
// 	var credErr error
// 	if projectID != "" {
// 		credID, ok, credErr = database.CredentialRepo.FindIDForUserAndProjectID(userID, projectID)
// 	} else {
// 		credID, ok, credErr = database.CredentialRepo.FindIDForUserAndEmail(userID, connectedEmail)
// 	}
// 	if credErr != nil {
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"message": "Failed to resolve credential",
// 			"error":   credErr.Error(),
// 		})
// 	} else if ok {
// 		jobs, err = database.CronJobRepo.ListJobsByCredentialID(userID, credID)
// 	} else {
// 		// DISABLED(parent_id): GetJobsByUserAndParentIDAndMethod
// 		return c.JSON(http.StatusNotFound, map[string]interface{}{
// 			"message": "No credential found",
// 			"error":   "re-onboard with credential-linked jobs (project_id or google_email)",
// 		})
// 	}
// 	if err != nil {
// 		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
// 			"message": "Failed to fetch jobs for parent",
// 			"error":   err.Error(),
// 		})
// 	}
// 	if len(jobs) == 0 {
// 		return c.JSON(http.StatusNotFound, map[string]interface{}{
// 			"message": "No jobs found for credential",
// 			"error":   "no gmail jobs found for this google_email",
// 		})
// 	}

// 	success := make([]*repo.CronJobListingDB, 0, len(jobs))
// 	failed := make([]map[string]interface{}, 0)
// 	for i := range jobs {
// 		job := &jobs[i]
// 		jobUpdate, buildErr := buildUpdateRequestForJob(ctx, database, job, reqBody.AutomaticBackupUpdateRequest, int(job.ID))
// 		if buildErr != nil {
// 			failed = append(failed, bulkGmailUpdateFailure(job.ID, job.Name, buildErr))
// 			continue
// 		}
// 		if updErr := applyAutomaticBackupUpdates(database, job, job.ID, jobUpdate); updErr != nil {
// 			failed = append(failed, bulkGmailUpdateFailure(job.ID, job.Name, updErr))
// 			continue
// 		}
// 		updatedJob, getErr := database.CronJobRepo.GetCronJobByID(job.ID)
// 		if getErr != nil {
// 			failed = append(failed, bulkGmailUpdateFailure(job.ID, job.Name, getErr))
// 			continue
// 		}
// 		database.CronJobRepo.EnrichCronJobFromCredential(updatedJob)
// 		repo.MaskTokenForCronJobDB(updatedJob)
// 		success = append(success, updatedJob)
// 	}

// 	resp := map[string]interface{}{
// 		"message": "Automatic backup bulk update completed successfully",
// 		"success": success,
// 		"failed":  failed,
// 	}
// 	if projectID != "" {
// 		resp["project_id"] = projectID
// 	}
// 	if connectedEmail != "" {
// 		resp["google_email"] = connectedEmail
// 	}
// 	return c.JSON(http.StatusOK, resp)
// }

func HandleAutomaticSyncDelete(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	if _, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	err = database.CronJobRepo.DeleteCronJobByID(uint(jobID))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Deleted Successfully",
	})
}

func HandleAutomaticSyncTaskList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 10
	}

	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if offset < 0 {
		offset = 0
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	if _, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	data, err := database.TaskRepo.ListAllTasksByJobID(uint(jobID), uint(limit), uint(offset))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Logs for Automatic Backup",
		"data":    data,
	})
}

func validateInterval(interval, on string) bool {
	// For one_time backups, interval doesn't need validation since scheduling doesn't apply
	if interval == "one_time" {
		return true
	}

	// Get allowed values for this interval
	allowedValues, exists := intervalValues[interval]
	if !exists {
		return false
	}

	// Check if the value matches any allowed value
	for _, v := range allowedValues {
		if v == on {
			return true
		}
	}

	return false
}

func extractAndStoreProjectID(ctx context.Context, database *db.PostgresDb, job *repo.CronJobListingDB, storxToken string, updateRequest map[string]interface{}, jobID int, syncType string) {
	projectID, err := satellite.GetProjectIDFromAccessGrant(ctx, storxToken)
	if err != nil {
		logger.Warn(ctx, "storj_project_id extraction failed; continuing without it",
			logger.Int("job_id", jobID), logger.String("sync_type", syncType), logger.ErrorField(err))
		return
	}
	if projectID == "" {
		return
	}
	delete(updateRequest, "storj_project_id")
	if database == nil || job == nil {
		return
	}
	cid := repo.JobCredentialID(job)
	if cid == 0 {
		return
	}
	if err := database.CredentialRepo.UpdateStorjProjectID(cid, projectID); err != nil {
		logger.Warn(ctx, "failed to update storj_project_id on credential",
			logger.Int("credential_id", int(cid)), logger.ErrorField(err))
	}
}

// Default password for admin operations
const DefaultAdminPassword = "admin123!@#"

// Request structure for delete jobs by email
type DeleteJobsByEmailRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// HandleDeleteJobsByEmail deletes all jobs and tasks for a user by email with password protection
func HandleDeleteJobsByEmail(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	var req DeleteJobsByEmailRequest

	// Parse request body
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request format",
			"error":   err.Error(),
		})
	}

	// Validate required fields
	if req.Email == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Email and password are required",
		})
	}

	// Validate email format
	if !strings.Contains(req.Email, "@") {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid email format",
		})
	}

	// Check password
	if req.Password != DefaultAdminPassword {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid password",
		})
	}

	// Get database instance
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Delete all jobs and tasks for the user by email
	deletedJobIDs, deletedTaskIDs, err := database.CronJobRepo.DeleteAllJobsAndTasksByEmail(req.Email)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to delete jobs and tasks",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":             "All jobs and tasks deleted successfully for user",
		"email":               req.Email,
		"deleted_job_ids":     deletedJobIDs,
		"deleted_task_ids":    deletedTaskIDs,
		"total_jobs_deleted":  len(deletedJobIDs),
		"total_tasks_deleted": len(deletedTaskIDs),
	})
}

func HandleAutomaticBackupSummary(c echo.Context) error {
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

	// Execute all counts in parallel - each goroutine creates its own query
	var totalAccounts, activeBackups, providers int64
	today := time.Now().Format("2006-01-02")
	var todaysBackups int64

	errs := make([]error, 4)
	var wg sync.WaitGroup
	wg.Add(4)

	// Each goroutine creates its own query from database.DB
	go func() {
		defer wg.Done()
		errs[0] = database.DB.Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
			Count(&totalAccounts).Error
	}()

	go func() {
		defer wg.Done()
		errs[1] = database.DB.Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND active = ? AND COALESCE(placeholder, false) = ?", userID, true, false).
			Count(&activeBackups).Error
	}()

	go func() {
		defer wg.Done()
		errs[2] = database.DB.Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
			Distinct("method").
			Count(&providers).Error
	}()

	go func() {
		defer wg.Done()
		errs[3] = database.DB.Model(&repo.TaskListingDB{}).
			Joins("JOIN cron_job_listing_dbs ON task_listing_dbs.cron_job_id = cron_job_listing_dbs.id").
			Where("cron_job_listing_dbs.user_id = ? AND COALESCE(cron_job_listing_dbs.placeholder, false) = ? AND task_listing_dbs.status = ? AND DATE(task_listing_dbs.start_time) = ?",
				userID, false, repo.TaskStatusSuccess, today).
			Count(&todaysBackups).Error
	}()

	wg.Wait()

	// Check for any errors
	for _, e := range errs {
		if e != nil {
			logger.Error(ctx, "Failed to get backup summary", logger.ErrorField(e))
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "internal server error",
				"error":   e.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Summary",
		"data": map[string]interface{}{
			"total_accounts": int(totalAccounts),
			"active_backups": int(activeBackups),
			"todays_backups": int(todaysBackups),
			"providers":      int(providers),
		},
	})
}

func HandleAutomaticSyncStats(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Failed to authenticate user", logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"message": "unauthorized",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	gdb := database.DB.WithContext(ctx)

	var totalAccounts, activeSyncs, failedSyncs int64
	var errTotal, errActive, errFailed error

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		errTotal = gdb.Session(&gorm.Session{}).
			Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
			Count(&totalAccounts).Error
	}()

	go func() {
		defer wg.Done()
		errActive = gdb.Session(&gorm.Session{}).
			Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND active = ? AND COALESCE(placeholder, false) = ?", userID, true, false).
			Count(&activeSyncs).Error
	}()

	go func() {
		defer wg.Done()
		errFailed = gdb.Session(&gorm.Session{}).
			Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND COALESCE(placeholder, false) = ? AND ((active = ? AND message_status = ?) OR auto_deactivated = ?)",
				userID, false, true, repo.JobMessageStatusError, true).
			Count(&failedSyncs).Error
	}()

	wg.Wait()

	if errTotal != nil || errActive != nil || errFailed != nil {
		if errTotal != nil {
			err = errTotal
		} else if errActive != nil {
			err = errActive
		} else {
			err = errFailed
		}
		logger.Error(ctx, "Failed to get autosync stats",
			logger.ErrorField(errTotal),
			logger.ErrorField(errActive),
			logger.ErrorField(errFailed),
		)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"message": "internal server error",
		})
	}

	var status string
	switch {
	case totalAccounts == 0:
		status = "add accounts"
	case activeSyncs == 0:
		status = "inactive"
	case failedSyncs == 0:
		status = "success"
	case failedSyncs == activeSyncs:
		status = "failed"
	default:
		status = "partial_success"
	}

	return c.JSON(http.StatusOK, AutoSyncStatsResponse{
		ActiveSyncs: int(activeSyncs),
		FailedSyncs: int(failedSyncs),
		Status:      status,
	})
}
