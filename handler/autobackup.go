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
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var (
	allowedMethods = map[string]bool{
		"gmail": true, "outlook": true, "psql_database": true, "mysql_database": true,
	}
	allowedSyncTypes = map[string]bool{
		"one_time": true, "daily": true,
	}
)

var intervalValues = map[string][]string{
	"monthly": {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
		"14", "15", "16", "17", "18", "19", "20", "21", "22", "23",
		"24", "25", "26", "27", "28"},
	"weekly": {"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	"daily":  {"12am"},
	// "one_time": {},
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

// GmailParentAccountSummary is the admin mailbox job that holds shared StorX (and OAuth) for corporate Gmail children.
type GmailParentAccountSummary struct {
	JobID          uint   `json:"job_id"`
	Email          string `json:"email"`
	StorxToken     string `json:"storx_token"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	StorjProjectID string `json:"storj_project_id,omitempty"`
}

// CronJobResponse represents a cron job with next backup time
type CronJobResponse struct {
	repo.CronJobListingDB
	NextBackup    *time.Time                 `json:"next_backup"`
	ParentAccount *GmailParentAccountSummary `json:"parent_account,omitempty"`
}

// CronJobDetailResponse is a single job for GET detail / update responses with optional parent summary.
type CronJobDetailResponse struct {
	repo.CronJobListingDB
	ParentAccount *GmailParentAccountSummary `json:"parent_account,omitempty"`
}

// gmailParentRowForCorporateChild loads the admin Gmail job when job is a corporate child; nil otherwise.
func gmailParentRowForCorporateChild(database *db.PostgresDb, job *repo.CronJobListingDB) *repo.CronJobListingDB {
	if database == nil || job == nil || job.Method != "gmail" {
		return nil
	}
	p, err := database.CronJobRepo.GmailParentRowForCorporateChild(job)
	if err != nil || p == nil {
		return nil
	}
	return p
}

func copyGmailCorporateChildStorxFromParent(database *db.PostgresDb, job *repo.CronJobListingDB) {
	if job == nil {
		return
	}
	if strings.TrimSpace(job.StorxToken) != "" {
		return
	}
	parent := gmailParentRowForCorporateChild(database, job)
	if parent == nil {
		return
	}
	job.StorxToken = parent.StorxToken
	if strings.TrimSpace(job.StorjProjectID) == "" {
		job.StorjProjectID = parent.StorjProjectID
	}
}

func buildGmailParentAccountSummary(database *db.PostgresDb, job *repo.CronJobListingDB) *GmailParentAccountSummary {
	if job == nil {
		return nil
	}
	parent := gmailParentRowForCorporateChild(database, job)
	if parent == nil {
		return nil
	}
	summary := &GmailParentAccountSummary{
		JobID:          parent.ID,
		Email:          parent.Name,
		StorxToken:     utils.MaskString(parent.StorxToken),
		StorjProjectID: parent.StorjProjectID,
	}
	if parent.InputData != nil && parent.InputData.Json() != nil {
		if rt, ok := (*parent.InputData.Json())["refresh_token"].(string); ok && strings.TrimSpace(rt) != "" {
			summary.RefreshToken = utils.MaskString(rt)
		}
	}
	return summary
}

// stripGmailCorporateChildStorxFromAPIResponse clears StorX grant fields on a Gmail child job for JSON only when parent_account
// is present, so the UI shows tokens once on the admin row instead of duplicating the same masked values on each mailbox.
func stripGmailCorporateChildStorxFromAPIResponse(job *repo.CronJobListingDB, parentSummary *GmailParentAccountSummary) {
	if job == nil || parentSummary == nil || job.Method != "gmail" {
		return
	}
	job.StorxToken = ""
	job.StorjProjectID = ""
}

func inputDataMapContainsRefreshToken(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok || m == nil {
		return false
	}
	rt, ok := m["refresh_token"].(string)
	return ok && strings.TrimSpace(rt) != ""
}

// splitGmailCorporateChildCredentialUpdate routes storx_token, storj_project_id, and Gmail OAuth input_data (refresh_token)
// to the admin (parent) cron row when this job is a Workspace connected account (parent_id set). Personal Gmail jobs
// (no parent_id / self-parent) keep the full updateRequest on the same row. Placeholder admin rows are found the same way as normal admin jobs.
func splitGmailCorporateChildCredentialUpdate(job *repo.CronJobListingDB, updateRequest map[string]interface{}) (parentUpdate map[string]interface{}, childUpdate map[string]interface{}) {
	if job == nil || job.Method != "gmail" || repo.GmailConnectedAccountEmail(job) == "" {
		return nil, updateRequest
	}
	hasStorx := false
	if _, ok := updateRequest["storx_token"]; ok {
		hasStorx = true
	}
	if _, ok := updateRequest["storj_project_id"]; ok {
		hasStorx = true
	}
	hasOAuthInput := false
	if raw, ok := updateRequest["input_data"]; ok && inputDataMapContainsRefreshToken(raw) {
		hasOAuthInput = true
	}
	if !hasStorx && !hasOAuthInput {
		return nil, updateRequest
	}
	parentUpdate = make(map[string]interface{})
	childUpdate = make(map[string]interface{})
	for k, v := range updateRequest {
		switch k {
		case "storx_token", "storj_project_id":
			parentUpdate[k] = v
		case "input_data":
			if hasOAuthInput {
				parentUpdate[k] = v
			} else {
				childUpdate[k] = v
			}
		default:
			childUpdate[k] = v
		}
	}
	return parentUpdate, childUpdate
}

func applyAutomaticBackupUpdates(database *db.PostgresDb, job *repo.CronJobListingDB, jobID uint, updateRequest map[string]interface{}) error {
	parentPatch, childUpdate := splitGmailCorporateChildCredentialUpdate(job, updateRequest)
	if len(parentPatch) > 0 {
		parent, err := database.CronJobRepo.GmailParentRowForCorporateChild(job)
		if err != nil {
			return err
		}
		if parent == nil {
			return fmt.Errorf("gmail corporate parent job not found for shared credential update")
		}
		if err := database.CronJobRepo.UpdateCronJobByID(parent.ID, parentPatch); err != nil {
			return err
		}
	}
	if len(childUpdate) > 0 {
		return database.CronJobRepo.UpdateCronJobByID(jobID, childUpdate)
	}
	return nil
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
		copyGmailCorporateChildStorxFromParent(database, &automaticSyncList[i])
	}
	maskedJobs := repo.MaskTokenForCronJobListingDB(automaticSyncList)
	response := make([]CronJobResponse, len(maskedJobs))
	for i := range maskedJobs {
		j := maskedJobs[i]
		parent := buildGmailParentAccountSummary(database, &maskedJobs[i])
		stripGmailCorporateChildStorxFromAPIResponse(&j, parent)
		response[i] = CronJobResponse{
			CronJobListingDB: j,
			NextBackup:       calculateNextBackup(j),
			ParentAccount:    parent,
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
		"data":    intervalValues,
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
	copyGmailCorporateChildStorxFromParent(database, &jobCopy)
	repo.MaskTokenForCronJobDB(&jobCopy)
	parent := buildGmailParentAccountSummary(database, &jobCopy)
	stripGmailCorporateChildStorxFromAPIResponse(&jobCopy, parent)
	detail := CronJobDetailResponse{
		CronJobListingDB: jobCopy,
		ParentAccount:    parent,
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Account Details",
		"success": []CronJobDetailResponse{detail},
		"failed":  []interface{}{},
	})
}

// HandleAutomaticSyncCreate creates backup jobs. Gmail: emails[] + Bearer token (from POST /auth/google/connect). Same response shape for Outlook later.
func HandleAutomaticSyncCreate(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return jsonError(http.StatusUnauthorized, "Invalid Request", err)
	}

	syncType := c.QueryParam("sync_type")
	if syncType == "" {
		syncType = "daily"
	}
	if !allowedSyncTypes[syncType] {
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid sync type")
	}

	method := c.Param("method")
	if !allowedMethods[method] {
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid method")
	}

	var reqBody struct {
		Code         string   `json:"code"`
		Name         string   `json:"name"`
		Emails       []string `json:"emails"`
		DatabaseName string   `json:"database_name"`
		Host         string   `json:"host"`
		Port         string   `json:"port"`
		Username     string   `json:"username"`
		Password     string   `json:"password"`
	}
	if err := c.Bind(&reqBody); err != nil {
		return jsonError(http.StatusBadRequest, "Invalid Request", err)
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	switch method {
	case "gmail":
		connectedEmail, accessToken, refreshToken, credErr := GetGoogleCredentialsFromRequest(c)
		if credErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": credErr.Error()})
		}
		toCreate, err := normalizeGmailEmails(reqBody.Emails, connectedEmail)
		if err != nil {
			return err
		}
		if err := validateGmailAdminDomain(ctx, toCreate, connectedEmail, accessToken); err != nil {
			return err
		}
		parentID := getCorporateParentID(connectedEmail, toCreate)
		success, failed := createJobsForEmails(ctx, c, userID, method, syncType, connectedEmail, refreshToken, toCreate, parentID, database)
		return respondSyncCreate(c, syncType, success, failed, nil, nil)
	case "outlook":
		name, config, err := ProcessOutlookMethod(reqBody.Code)
		if err != nil {
			return err
		}
		return createSingleSyncJobAndRespond(ctx, c, userID, method, syncType, name, config, database)
	case "psql_database", "mysql_database":
		name, config, err := ProcessDatabaseMethod(DatabaseConnection{
			Name:         reqBody.Name,
			DatabaseName: reqBody.DatabaseName,
			Host:         reqBody.Host,
			Port:         reqBody.Port,
			Username:     reqBody.Username,
			Password:     reqBody.Password,
		})
		if err != nil {
			return err
		}
		return createSingleSyncJobAndRespond(ctx, c, userID, method, syncType, name, config, database)
	default:
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid method")
	}
}

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

// validateGmailAdminDomain ensures admin can backup other accounts (same domain). Single IsUserAdmin call when needed.
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
	}, nil, true)
	return err
}

// createJobsForEmails creates one job per email. Refresh token is stored only on the admin mailbox row; corporate children omit it and resolve from parent. StorX access grant follows the same pattern: set storx_token on the admin job (or via any child update — it is persisted on the parent row).
func createJobsForEmails(ctx context.Context, c echo.Context, userID, method, syncType string, connectedEmail, refreshToken string, emails []string, parentID *string, database *db.PostgresDb) (success, failed []map[string]interface{}) {
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
	adminInList := false
	conn := strings.TrimSpace(connectedEmail)
	for _, e := range emails {
		if strings.EqualFold(strings.TrimSpace(e), conn) {
			adminInList = true
			break
		}
	}
	if parentID != nil && !adminInList && method == "gmail" {
		if err := ensureGmailPlaceholderAdminJob(database, userID, syncType, conn, refreshToken); err != nil {
			for _, targetEmail := range emails {
				failed = append(failed, map[string]interface{}{"email": targetEmail, "error": err.Error()})
			}
			return success, failed
		}
	}
	for _, targetEmail := range emails {
		tEmail := strings.TrimSpace(targetEmail)
		if method == "gmail" {
			existing, ok, ferr := database.CronJobRepo.FindGmailJobByUserNameSyncType(userID, tEmail, syncType)
			if ferr != nil {
				failed = append(failed, map[string]interface{}{"email": targetEmail, "error": ferr.Error()})
				continue
			}
			if ok && existing != nil && existing.Placeholder && strings.EqualFold(tEmail, conn) {
				merged := mergeJobInputData(existing, map[string]interface{}{"refresh_token": refreshToken})
				upd := map[string]interface{}{"placeholder": false, "input_data": merged}
				if syncType == "one_time" {
					upd["active"] = true
				}
				if err := database.CronJobRepo.UpdateCronJobByID(existing.ID, upd); err != nil {
					failed = append(failed, map[string]interface{}{"email": targetEmail, "error": err.Error()})
					continue
				}
				item := map[string]interface{}{"email": targetEmail, "job_id": existing.ID}
				if syncType == "one_time" {
					task, taskErr := database.TaskRepo.CreateTaskForCronJob(existing.ID)
					if taskErr != nil {
						failed = append(failed, map[string]interface{}{"email": targetEmail, "error": "job updated but task failed: " + taskErr.Error()})
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
		isAdminMailbox := strings.EqualFold(strings.TrimSpace(targetEmail), conn)
		skipLocalToken := parentID != nil && !isAdminMailbox
		if !skipLocalToken {
			config["refresh_token"] = refreshToken
		}
		// Only delegated mailbox rows point at the admin (parent_id = admin email). The admin / token-holder row must have parent_id nil — not a child of itself.
		rowParentID := parentID
		if isAdminMailbox {
			rowParentID = nil
		}
		data, createErr := createSyncJob(userID, targetEmail, method, syncType, config, rowParentID, c)
		if createErr != nil {
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
				failed = append(failed, map[string]interface{}{"email": targetEmail, "error": "job created but task failed: " + taskErr.Error()})
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

// createSingleSyncJobAndRespond creates one job (Outlook/DB), sends notification, returns unified success/failed response.
func createSingleSyncJobAndRespond(ctx context.Context, c echo.Context, userID, method, syncType, name string, config map[string]interface{}, database *db.PostgresDb) error {
	data, err := createSyncJob(userID, name, method, syncType, config, nil, c)
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
func createSyncJob(userID, name, method, syncType string, config map[string]interface{}, parentID *string, c echo.Context) (interface{}, error) {
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Check for existing jobs using original name (before adding timestamp)
	if err := checkExistingJobs(userID, name, syncType, method, database); err != nil {
		return nil, err
	}

	data, err := database.CronJobRepo.CreateCronJobForUser(userID, name, method, syncType, config, parentID)
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

	// Check for exact duplicate (same name + syncType + userID)
	for _, job := range existingJobs {
		if job.Name == name && job.SyncType == syncType {
			if method == "gmail" && job.Placeholder {
				continue
			}
			return jsonErrorMsg(http.StatusBadRequest,
				fmt.Sprintf("A %s backup with this email (%s) already exists for your account", syncType, name))
		}
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
		return "Gmail account"
	case "outlook":
		return "Outlook account"
	case "psql_database", "mysql_database":
		return "database backup"
	default:
		return "service"
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
			return jsonError(http.StatusBadRequest, "A backup job with this name and sync type already exists for your account", err)
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

// gmailOAuthMergeBaseJob returns the cron row whose input_data should receive Google OAuth tokens for this job:
// the admin/parent row for Workspace connected accounts (including placeholder-only admin), otherwise the job itself.
func gmailOAuthMergeBaseJob(database *db.PostgresDb, job *repo.CronJobListingDB) (*repo.CronJobListingDB, error) {
	if job == nil || database == nil {
		return job, nil
	}
	adminEmail := repo.GmailConnectedAccountEmail(job)
	if adminEmail == "" {
		return job, nil
	}
	parent, ok, err := database.CronJobRepo.FindGmailJobByUserNameSyncType(job.UserID, adminEmail, job.SyncType)
	if err != nil {
		return nil, err
	}
	if !ok || parent == nil {
		return job, nil
	}
	return parent, nil
}

// oauthInputDataFromBackupRequest builds merged input_data from code (Gmail) or refresh_token (Gmail/Outlook).
func oauthInputDataFromBackupRequest(database *db.PostgresDb, job *repo.CronJobListingDB, req *AutomaticBackupUpdateRequest) (map[string]interface{}, error) {
	if req.Code != nil {
		if job.Method != "gmail" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "code update is only allowed for gmail method"})
		}
		return gmailInputDataAfterCodeReauth(database, job, *req.Code)
	}
	if req.RefreshToken != nil {
		switch job.Method {
		case "outlook":
			return outlookInputDataAfterRefreshToken(job, *req.RefreshToken)
		case "gmail":
			return gmailInputDataAfterRefreshToken(database, job, *req.RefreshToken)
		default:
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "refresh_token update is only supported for gmail and outlook methods"})
		}
	}
	return nil, nil
}

func gmailInputDataAfterCodeReauth(database *db.PostgresDb, job *repo.CronJobListingDB, code string) (map[string]interface{}, error) {
	tok, err := google.ExchangeCodeForTokenWithAdminScope(code)
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Code. Not able to generate auth token from code", err.Error())
	}
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Code. May be it is expired or invalid", err.Error())
	}
	if userDetails.Email == "" {
		return nil, httpErr(http.StatusBadRequest, "Invalid Code. May be it is expired or invalid", "getting empty email id from google token")
	}

	tokenEmail := strings.TrimSpace(userDetails.Email)
	mailbox := jobGmailMailbox(job)
	connected := repo.GmailConnectedAccountEmail(job)
	if !strings.EqualFold(tokenEmail, mailbox) && (connected == "" || !strings.EqualFold(tokenEmail, connected)) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "email id mismatch"})
	}

	mergeBase, err := gmailOAuthMergeBaseJob(database, job)
	if err != nil {
		return nil, err
	}
	out := mergeJobInputData(mergeBase, map[string]interface{}{"refresh_token": tok.RefreshToken})
	delete(out, "credential_id")
	delete(out, "connected_email")
	return out, nil
}

func gmailInputDataAfterRefreshToken(database *db.PostgresDb, job *repo.CronJobListingDB, refreshToken string) (map[string]interface{}, error) {
	accessToken, err := google.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. Not able to generate auth token from refresh token", err.Error())
	}
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(accessToken)
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid", err.Error())
	}
	if userDetails.Email == "" {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid", "getting empty email id from google token")
	}
	tokenEmail := strings.TrimSpace(userDetails.Email)
	mailbox := jobGmailMailbox(job)
	connected := repo.GmailConnectedAccountEmail(job)
	if !strings.EqualFold(tokenEmail, mailbox) && (connected == "" || !strings.EqualFold(tokenEmail, connected)) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "email id mismatch"})
	}
	mergeBase, err := gmailOAuthMergeBaseJob(database, job)
	if err != nil {
		return nil, err
	}
	out := mergeJobInputData(mergeBase, map[string]interface{}{"refresh_token": refreshToken})
	delete(out, "credential_id")
	delete(out, "connected_email")
	return out, nil
}

// outlookInputDataAfterRefreshToken validates the refresh token matches the job mailbox, returns merged input_data.
func outlookInputDataAfterRefreshToken(job *repo.CronJobListingDB, refreshToken string) (map[string]interface{}, error) {
	authToken, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. Not able to generate auth token from refresh token", err.Error())
	}
	client, err := outlook.NewOutlookClientUsingToken(authToken)
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid", err.Error())
	}
	userDetails, err := client.GetCurrentUser()
	if err != nil {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid", err.Error())
	}
	if strings.TrimSpace(userDetails.Mail) == "" {
		return nil, httpErr(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid", "getting empty email id from outlook token")
	}
	if !strings.EqualFold(strings.TrimSpace(userDetails.Mail), strings.TrimSpace(job.Name)) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "email id mismatch"})
	}
	return mergeJobInputData(job, map[string]interface{}{"refresh_token": refreshToken}), nil
}

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

func buildUpdateRequestForJob(ctx context.Context, database *db.PostgresDb, job *repo.CronJobListingDB, reqBody AutomaticBackupUpdateRequest, jobID int) (map[string]interface{}, error) {
	updateRequest := map[string]interface{}{}

	if job.SyncType == "one_time" {
		if reqBody.Interval != nil || reqBody.On != nil || reqBody.DatabaseConnection != nil || reqBody.Active != nil {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "For one-time sync jobs, only storx_token and code or refresh_token for gmail/outlook updates are allowed")
		}
		if reqBody.StorxToken != nil {
			if *reqBody.StorxToken == "" {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "storx_token cannot be empty")
			}
			updateRequest["storx_token"] = *reqBody.StorxToken
			extractAndStoreProjectID(ctx, *reqBody.StorxToken, updateRequest, jobID, "one-time")
		}
		in, err := oauthInputDataFromBackupRequest(database, job, &reqBody)
		if err != nil {
			return nil, err
		}
		if in != nil {
			updateRequest["input_data"] = in
		}
		if len(updateRequest) == 0 {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "No valid update fields provided. Only storx_token, code (gmail), and refresh_token (gmail or outlook) are allowed"})
		}
		return updateRequest, nil
	}

	if (reqBody.Interval != nil && reqBody.On == nil) || (reqBody.On != nil && reqBody.Interval == nil) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "Both interval and on are required together"})
	}
	if reqBody.Interval != nil {
		onValue := strings.TrimSpace(*reqBody.On)
		if onValue == "" {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "On value cannot be empty")
		}
		if *reqBody.Interval == "monthly" {
			day, err := strconv.Atoi(onValue)
			if err != nil {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "On value must be a valid number for monthly intervals")
			}
			onValue = strconv.Itoa(day)
			if day == 29 || day == 30 || day == 31 {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "Monthly backups cannot be scheduled on the 29th, 30th or 31st day. Please select a date between 1-28.")
			}
		}
		if !validateInterval(*reqBody.Interval, onValue) {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "On is not valid for the given interval")
		}
		updateRequest["interval"] = *reqBody.Interval
		updateRequest["on"] = onValue
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
		if job.Method != "database" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "database connection is not allowed for this method"})
		}
		updateRequest["input_data"] = mergeJobInputData(job, map[string]interface{}{
			"host":          reqBody.DatabaseConnection.Host,
			"port":          reqBody.DatabaseConnection.Port,
			"username":      reqBody.DatabaseConnection.Username,
			"password":      reqBody.DatabaseConnection.Password,
			"database_name": reqBody.DatabaseConnection.DatabaseName,
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
		updateRequest["storx_token"] = *reqBody.StorxToken
		extractAndStoreProjectID(ctx, *reqBody.StorxToken, updateRequest, jobID, "daily")
	}
	if reqBody.Active != nil {
		updateRequest["active"] = *reqBody.Active
		if *reqBody.Active {
			updateRequest["message"] = "You Automatic backup is activated. it will start processing first backup soon"
			updateRequest["message_status"] = repo.JobMessageStatusInfo
			updateRequest["auto_deactivated"] = false
		} else {
			updateRequest["message"] = "You Automatic backup is deactivated. it will not process any backup"
			updateRequest["message_status"] = repo.JobMessageStatusInfo
		}
	}
	return updateRequest, nil
}

func HandleAutomaticBackupUpdate(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	jobID, parseErr := strconv.Atoi(c.Param("job_id"))
	if parseErr != nil || jobID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Job ID",
		})
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	job, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Job not found",
			"error":   err.Error(),
		})
	}

	var reqBody AutomaticBackupUpdateRequest

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}

	updateRequest, err := buildUpdateRequestForJob(ctx, database, job, reqBody, jobID)
	if err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			return c.JSON(httpErr.Code, httpErr.Message)
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Request", "error": err.Error()})
	}

	err = applyAutomaticBackupUpdates(database, job, uint(jobID), updateRequest)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Failed to update job",
				"error":   err.Error(),
			})
		}
		logger.Error(ctx, "Failed to update cron job", logger.Int("job_id", jobID), logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to update job",
			"error":   err.Error(),
		})
	}

	data, err := database.CronJobRepo.GetCronJobByID(uint(jobID))
	if err != nil {
		logger.Error(ctx, "Failed to load job after update", logger.Int("job_id", jobID), logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	jobCopy := *data
	copyGmailCorporateChildStorxFromParent(database, &jobCopy)
	repo.MaskTokenForCronJobDB(&jobCopy)
	parent := buildGmailParentAccountSummary(database, &jobCopy)
	stripGmailCorporateChildStorxFromAPIResponse(&jobCopy, parent)
	detail := CronJobDetailResponse{
		CronJobListingDB: jobCopy,
		ParentAccount:    parent,
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic backup updated successfully",
		"success": []CronJobDetailResponse{detail},
		"failed":  []interface{}{},
	})
}

func bulkGmailUpdateFailure(jobID uint, name string, err error) map[string]interface{} {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return map[string]interface{}{"job_id": jobID, "email": name, "error": fmt.Sprintf("%v", he.Message)}
	}
	return map[string]interface{}{"job_id": jobID, "email": name, "error": err.Error()}
}

// HandleAutomaticBackupBulkUpdateByParent updates all Gmail jobs for a corporate admin parent_id
// resolved from the same encrypted Google token used in create flow.
func HandleAutomaticBackupBulkUpdateByParent(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	method := c.Param("method")
	if method != "gmail" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "bulk update by parent_id is currently supported only for gmail method",
		})
	}

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	connectedEmail, _, _, credErr := GetGoogleCredentialsFromRequest(c)
	if credErr != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   credErr.Error(),
		})
	}

	var reqBody AutomaticBackupUpdateRequest
	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	jobs, err := database.CronJobRepo.GetJobsByUserAndParentIDAndMethod(userID, connectedEmail, "gmail")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to fetch jobs for parent",
			"error":   err.Error(),
		})
	}
	if len(jobs) == 0 {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "No jobs found for parent_id",
			"error":   "no gmail jobs found for this parent",
		})
	}

	success := make([]*repo.CronJobListingDB, 0, len(jobs))
	failed := make([]map[string]interface{}, 0)
	for i := range jobs {
		job := &jobs[i]
		jobUpdate, buildErr := buildUpdateRequestForJob(ctx, database, job, reqBody, int(job.ID))
		if buildErr != nil {
			failed = append(failed, bulkGmailUpdateFailure(job.ID, job.Name, buildErr))
			continue
		}
		if updErr := applyAutomaticBackupUpdates(database, job, job.ID, jobUpdate); updErr != nil {
			failed = append(failed, bulkGmailUpdateFailure(job.ID, job.Name, updErr))
			continue
		}
		updatedJob, getErr := database.CronJobRepo.GetCronJobByID(job.ID)
		if getErr != nil {
			failed = append(failed, bulkGmailUpdateFailure(job.ID, job.Name, getErr))
			continue
		}
		copyGmailCorporateChildStorxFromParent(database, updatedJob)
		repo.MaskTokenForCronJobDB(updatedJob)
		success = append(success, updatedJob)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":   "Automatic backup bulk update completed successfully",
		"parent_id": connectedEmail,
		"success":   success,
		"failed":    failed,
	})
}

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

func extractAndStoreProjectID(ctx context.Context, storxToken string, updateRequest map[string]interface{}, jobID int, syncType string) {
	projectID, err := satellite.GetProjectIDFromAccessGrant(ctx, storxToken)
	if err != nil {
		logger.Warn(ctx, "storj_project_id extraction failed; continuing without it",
			logger.Int("job_id", jobID), logger.String("sync_type", syncType), logger.ErrorField(err))
		return
	}
	if projectID != "" {
		updateRequest["storj_project_id"] = projectID
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
