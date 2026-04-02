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

var Err error

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

// CronJobResponse represents a cron job with next backup time
type CronJobResponse struct {
	repo.CronJobListingDB
	NextBackup *time.Time `json:"next_backup"`
}

// <<<<<------------ AUTOMATIC BACKUP ------------>>>>>
func HandleAutomaticSyncListForUser(c echo.Context) error {
	ctx := c.Request().Context()
	defer monitor.Mon.Task()(&ctx)(&Err)
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

	maskedJobs := repo.MaskTokenForCronJobListingDB(automaticSyncList)
	response := make([]CronJobResponse, len(maskedJobs))
	for i, job := range maskedJobs {
		response[i] = CronJobResponse{
			CronJobListingDB: job,
			NextBackup:       calculateNextBackup(job),
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

	repo.MaskTokenForCronJobDB(jobDetails)
	enrichGmailJobInputDataWithOAuthRefresh(database, userID, jobDetails)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Account Details",
		"success": []*repo.CronJobListingDB{jobDetails},
		"failed":  []interface{}{},
	})
}

// enrichGmailJobInputDataWithOAuthRefresh sets input_data.refresh_token (masked) from oauth_credentials when credential_id is set.
func enrichGmailJobInputDataWithOAuthRefresh(database *db.PostgresDb, userID string, job *repo.CronJobListingDB) {
	if job == nil || job.Method != "gmail" {
		return
	}
	credID, ok := gmailCredentialIDFromJob(job)
	if !ok {
		return
	}
	cred, err := database.OAuthCredentialRepo.GetByIDAndUser(credID, userID)
	if err != nil {
		return
	}
	(*job.InputData.Json())["refresh_token"] = utils.MaskString(cred.RefreshToken)
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
		logger.Info(ctx, "Creating backup jobs", logger.String("user_id", userID), logger.String("method", method), logger.Int("accounts", len(toCreate)))
		if err := validateGmailAdminDomain(ctx, toCreate, connectedEmail, accessToken); err != nil {
			return err
		}
		cred, err := getOrCreateOAuthCredential(database, userID, "gmail", connectedEmail, refreshToken)
		if err != nil {
			return err
		}
		parentID := getCorporateParentID(connectedEmail, toCreate)
		success, failed := createJobsForEmails(ctx, c, userID, method, syncType, cred.ID, toCreate, parentID, database)
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

// getOrCreateOAuthCredential returns existing credential for (userID, source, email) or creates one. One row per connection.
func getOrCreateOAuthCredential(database *db.PostgresDb, userID, source, email, refreshToken string) (*repo.OAuthCredentialDB, error) {
	cred, err := database.OAuthCredentialRepo.GetByUserIDAndSourceAndEmail(userID, source, email)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to lookup credentials"})
	}
	if cred != nil {
		return cred, nil
	}
	cred = &repo.OAuthCredentialDB{
		UserID:       userID,
		Email:        email,
		Source:       source,
		RefreshToken: refreshToken,
	}
	if err := database.OAuthCredentialRepo.Create(cred); err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to store credentials"})
	}
	return cred, nil
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
		if e != connectedEmail {
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
		if e != connectedEmail && google.ExtractDomainFromEmail(e) != adminDomain {
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
		if e != connectedEmail {
			parent := connectedEmail
			return &parent
		}
	}
	return nil
}

// createJobsForEmails creates one job per email; collects success/failed. Sends one batch notification when multiple accounts.
// Refresh token is resolved from oauth_credentials via credID at cron run time; no need to store it in job input_data.
func createJobsForEmails(ctx context.Context, c echo.Context, userID, method, syncType string, credID uint, emails []string, parentID *string, database *db.PostgresDb) (success, failed []map[string]interface{}) {
	success = make([]map[string]interface{}, 0, len(emails))
	failed = make([]map[string]interface{}, 0)
	priority := "normal"
	batchNotify := len(emails) > 1
	for _, targetEmail := range emails {
		config := map[string]interface{}{
			"credential_id": credID,
			"email":         targetEmail,
		}
		data, createErr := createSyncJob(userID, targetEmail, method, syncType, config, parentID, c)
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
	existingJobs, err := db.CronJobRepo.GetAllCronJobsForUser(userID, nil)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "Failed to check existing jobs", err)
	}

	serviceName := getServiceName(method)

	// Check for exact duplicate (same name + syncType + userID)
	for _, job := range existingJobs {
		if job.Name == name && job.SyncType == syncType {
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

// gmailCredentialIDFromJob returns oauth_credentials.id from job input_data (JSON numbers decode as float64).
func gmailCredentialIDFromJob(job *repo.CronJobListingDB) (uint, bool) {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return 0, false
	}
	v, ok := (*job.InputData.Json())["credential_id"].(float64)
	if !ok || v <= 0 {
		return 0, false
	}
	return uint(v), true
}

func gmailInputDataAfterCodeReauth(database *db.PostgresDb, userID string, job *repo.CronJobListingDB, code string) (map[string]interface{}, error) {
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

	if credID, ok := gmailCredentialIDFromJob(job); ok {
		cred, err := database.OAuthCredentialRepo.GetByIDAndUser(credID, userID)
		if err != nil {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "oauth credential not found for this job")
		}
		if !strings.EqualFold(tokenEmail, strings.TrimSpace(cred.Email)) {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "email id mismatch"})
		}
		if err := database.OAuthCredentialRepo.UpdateRefreshTokenForUser(credID, userID, tok.RefreshToken); err != nil {
			return nil, httpErr(http.StatusInternalServerError, "Failed to update credentials", err.Error())
		}
		out := mergeJobInputData(job, nil)
		delete(out, "refresh_token")
		return out, nil
	}

	if !strings.EqualFold(tokenEmail, strings.TrimSpace(job.Name)) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "email id mismatch"})
	}
	return mergeJobInputData(job, map[string]interface{}{
		"refresh_token": tok.RefreshToken,
		"email":         userDetails.Email,
	}), nil
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

// New helper function to reduce duplication in error responses
func sendJSONError(c echo.Context, status int, message string, err error) error {
	response := map[string]interface{}{
		"message": message,
	}
	if err != nil {
		response["error"] = err.Error()
	}
	return c.JSON(status, response)
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

func httpErr(status int, message string, errText string) error {
	return echo.NewHTTPError(status, map[string]interface{}{"message": message, "error": errText})
}

func buildUpdateRequestForJob(ctx context.Context, database *db.PostgresDb, userID string, job *repo.CronJobListingDB, reqBody AutomaticBackupUpdateRequest, jobID int) (map[string]interface{}, error) {
	updateRequest := map[string]interface{}{}

	if job.SyncType == "one_time" {
		if reqBody.Interval != nil || reqBody.On != nil || reqBody.DatabaseConnection != nil || reqBody.Active != nil {
			return nil, httpErr(http.StatusBadRequest, "Invalid Request", "For one-time sync jobs, only storx_token and code for outlook/gmail updates are allowed")
		}
		if reqBody.StorxToken != nil {
			if *reqBody.StorxToken == "" {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "storx_token cannot be empty")
			}
			updateRequest["storx_token"] = *reqBody.StorxToken
			extractAndStoreProjectID(ctx, *reqBody.StorxToken, updateRequest, jobID, "one-time")
		}
		if reqBody.Code != nil {
			if job.Method != "gmail" {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "code update is only allowed for gmail method")
			}
			in, err := gmailInputDataAfterCodeReauth(database, userID, job, *reqBody.Code)
			if err != nil {
				return nil, err
			}
			updateRequest["input_data"] = in
		}
		if reqBody.RefreshToken != nil {
			if job.Method != "outlook" {
				return nil, httpErr(http.StatusBadRequest, "Invalid Request", "refresh_token update is only allowed for outlook method")
			}
			in, err := outlookInputDataAfterRefreshToken(job, *reqBody.RefreshToken)
			if err != nil {
				return nil, err
			}
			updateRequest["input_data"] = in
		}
		if len(updateRequest) == 0 {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "No valid update fields provided. Only storx_token, code (gmail), and refresh_token (outlook) are allowed"})
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
		if job.Method != "gmail" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "refresh token is not allowed for this method"})
		}
		in, err := gmailInputDataAfterCodeReauth(database, userID, job, *reqBody.Code)
		if err != nil {
			return nil, err
		}
		updateRequest["input_data"] = in
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
		if job.Method != "outlook" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{"message": "refresh token is not allowed for this method"})
		}
		in, err := outlookInputDataAfterRefreshToken(job, *reqBody.RefreshToken)
		if err != nil {
			return nil, err
		}
		updateRequest["input_data"] = in
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
	logger.Info(ctx, "Starting automatic backup update request")
	defer monitor.Mon.Task()(&ctx)(&Err)

	// Validate jobID
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil || jobID <= 0 {
		logger.Error(ctx, "Invalid job ID provided",
			logger.String("job_id_param", c.Param("job_id")),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Job ID",
		})
	}
	logger.Info(ctx, "Job ID validated", logger.Int("job_id", jobID))

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		logger.Error(ctx, "Authentication failed for backup update",
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}
	logger.Info(ctx, "User authenticated",
		logger.String("user_id", userID),
		logger.Int("job_id", jobID))

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Verify job exists and belongs to user
	job, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		logger.Error(ctx, "Job not found or access denied",
			logger.String("user_id", userID),
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Job not found",
			"error":   err.Error(),
		})
	}
	logger.Info(ctx, "Job retrieved successfully",
		logger.Int("job_id", jobID),
		logger.String("job_name", job.Name),
		logger.String("job_method", job.Method),
		logger.Bool("job_active", job.Active))

	var reqBody AutomaticBackupUpdateRequest

	if err := c.Bind(&reqBody); err != nil {
		logger.Error(ctx, "Failed to bind request body",
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}

	updateRequest, err := buildUpdateRequestForJob(ctx, database, userID, job, reqBody, jobID)
	if err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) {
			return c.JSON(httpErr.Code, httpErr.Message)
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Request", "error": err.Error()})
	}

	logger.Info(ctx, "Updating job in database",
		logger.Int("job_id", jobID),
		logger.Int("update_fields_count", len(updateRequest)))

	err = database.CronJobRepo.UpdateCronJobByID(uint(jobID), updateRequest)
	if err != nil {
		logger.Error(ctx, "Failed to update job in database",
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to update job",
			"error":   err.Error(),
		})
	}

	data, err := database.CronJobRepo.GetCronJobByID(uint(jobID))
	if err != nil {
		logger.Error(ctx, "Failed to retrieve updated job data",
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	logger.Info(ctx, "Automatic backup update completed successfully",
		logger.Int("job_id", jobID),
		logger.String("job_name", data.Name),
		logger.Bool("job_active", data.Active))

	repo.MaskTokenForCronJobDB(data)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic backup updated successfully",
		"success": []*repo.CronJobListingDB{data},
		"failed":  []interface{}{},
	})
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
	for _, job := range jobs {
		jobUpdate, buildErr := buildUpdateRequestForJob(ctx, database, userID, &job, reqBody, int(job.ID))
		if buildErr != nil {
			var httpErr *echo.HTTPError
			if errors.As(buildErr, &httpErr) {
				failed = append(failed, map[string]interface{}{"job_id": job.ID, "email": job.Name, "error": fmt.Sprintf("%v", httpErr.Message)})
			} else {
				failed = append(failed, map[string]interface{}{"job_id": job.ID, "email": job.Name, "error": buildErr.Error()})
			}
			continue
		}
		if updErr := database.CronJobRepo.UpdateCronJobByID(job.ID, jobUpdate); updErr != nil {
			failed = append(failed, map[string]interface{}{"job_id": job.ID, "email": job.Name, "error": updErr.Error()})
			continue
		}
		updatedJob, getErr := database.CronJobRepo.GetCronJobByID(job.ID)
		if getErr != nil {
			failed = append(failed, map[string]interface{}{"job_id": job.ID, "email": job.Name, "error": getErr.Error()})
			continue
		}
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

// extractAndStoreProjectID extracts project_id from storx_token and adds it to updateRequest
// This is a helper function to avoid code duplication
func extractAndStoreProjectID(ctx context.Context, storxToken string, updateRequest map[string]interface{}, jobID int, syncType string) {
	projectID, err := satellite.GetProjectIDFromAccessGrant(ctx, storxToken)
	if err != nil {
		logger.Warn(ctx, fmt.Sprintf("Failed to extract project_id from storx_token for %s sync, continuing without it", syncType),
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		// Continue without project_id (backward compatible)
		// Note: storj_project_id will be populated from webhook events when they arrive
	} else if projectID != "" {
		updateRequest["storj_project_id"] = projectID
		logger.Info(ctx, fmt.Sprintf("Extracted and stored storj_project_id from storx_token for %s sync", syncType),
			logger.Int("job_id", jobID),
			logger.String("storj_project_id", projectID))
	} else {
		// Extraction returned empty (API endpoint not available or project_id not in response)
		logger.Info(ctx, fmt.Sprintf("Could not extract project_id from access grant for %s sync. It will be populated from webhook events.", syncType),
			logger.Int("job_id", jobID))
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
			Where("user_id = ?", userID).
			Count(&totalAccounts).Error
	}()

	go func() {
		defer wg.Done()
		errs[1] = database.DB.Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND active = ?", userID, true).
			Count(&activeBackups).Error
	}()

	go func() {
		defer wg.Done()
		errs[2] = database.DB.Model(&repo.CronJobListingDB{}).
			Where("user_id = ?", userID).
			Distinct("method").
			Count(&providers).Error
	}()

	go func() {
		defer wg.Done()
		errs[3] = database.DB.Model(&repo.TaskListingDB{}).
			Joins("JOIN cron_job_listing_dbs ON task_listing_dbs.cron_job_id = cron_job_listing_dbs.id").
			Where("cron_job_listing_dbs.user_id = ? AND task_listing_dbs.status = ? AND DATE(task_listing_dbs.start_time) = ?",
				userID, repo.TaskStatusSuccess, today).
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
	db := database.DB.WithContext(ctx)

	var totalAccounts, activeSyncs, failedSyncs int64
	var errTotal, errActive, errFailed error

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		errTotal = db.Session(&gorm.Session{}).
			Model(&repo.CronJobListingDB{}).
			Where("user_id = ?", userID).
			Count(&totalAccounts).Error
	}()

	go func() {
		defer wg.Done()
		errActive = db.Session(&gorm.Session{}).
			Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND active = ?", userID, true).
			Count(&activeSyncs).Error
	}()

	go func() {
		defer wg.Done()
		errFailed = db.Session(&gorm.Session{}).
			Model(&repo.CronJobListingDB{}).
			Where("user_id = ? AND ((active = ? AND message_status = ?) OR auto_deactivated = ?)",
				userID, true, repo.JobMessageStatusError, true).
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
