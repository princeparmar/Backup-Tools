package handler

import (
	"context"
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

var Err error

var intervalValues = map[string][]string{
	"monthly": {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
		"14", "15", "16", "17", "18", "19", "20", "21", "22", "23",
		"24", "25", "26", "27", "28", "29", "30"},
	"weekly":   {"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	"daily":    {"12am"},
	"one_time": {},
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Accounts List",
		"data":    repo.MaskTokenForCronJobListingDB(automaticSyncList),
	})
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

	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	jobDetails, err := database.CronJobRepo.GetCronJobByID(uint(jobID))
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Account Details",
		"data":    jobDetails,
	})
}

func HandleAutomaticSyncCreate(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return jsonError(http.StatusUnauthorized, "Invalid Request", err)
	}

	syncType := c.QueryParam("sync_type")
	method := c.Param("method")

	// Set to daily if empty and validate
	if syncType == "" {
		syncType = "daily"
	}
	if syncType != "one_time" && syncType != "daily" {
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid sync type")
	}

	// Validate method
	if method != "gmail" && method != "outlook" && method != "psql_database" && method != "mysql_database" {
		return jsonErrorMsg(http.StatusBadRequest, "Invalid Request", "invalid method")
	}

	// Parse request body and extract common fields
	var reqBody struct {
		Code         string `json:"code"`
		RefreshToken string `json:"refresh_token"`
		Name         string `json:"name"`
		DatabaseName string `json:"database_name"`
		Host         string `json:"host"`
		Port         string `json:"port"`
		Username     string `json:"username"`
		Password     string `json:"password"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return jsonError(http.StatusBadRequest, "Invalid Request", err)
	}

	// Process based on method
	var name string
	var config map[string]interface{}

	switch method {
	case "gmail":
		name, config, err = ProcessGmailMethod(reqBody.Code)
	case "outlook":
		name, config, err = ProcessOutlookMethod(reqBody.Code)
	case "psql_database", "mysql_database":
		name, config, err = ProcessDatabaseMethod(DatabaseConnection{
			Name:         reqBody.Name,
			DatabaseName: reqBody.DatabaseName,
			Host:         reqBody.Host,
			Port:         reqBody.Port,
			Username:     reqBody.Username,
			Password:     reqBody.Password,
		})
	}

	if err != nil {
		return err
	}

	// Create the sync job
	data, err := createSyncJob(userID, name, method, syncType, config, c)
	if err != nil {
		return err
	}

	// Send notification for cron job creation
	priority := "normal"
	notificationData := map[string]interface{}{
		"event":     "cron_created",
		"level":     2,
		"method":    method,
		"name":      name,
		"type":      syncType,
		"timestamp": "now", // Required by notification template
	}
	if cronJob, ok := data.(*repo.CronJobListingDB); ok {
		notificationData["job_id"] = cronJob.ID
	}
	satellite.SendNotificationAsync(ctx, userID, "Automatic Backup Created for "+method, fmt.Sprintf("Your automatic backup for %s has been created successfully", name), &priority, notificationData, nil)

	return sendSyncResponse(c, syncType, data)
}

// Method processing functions
func ProcessGmailMethod(code string) (string, map[string]interface{}, error) {
	if code == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Code is required")
	}

	tok, err := google.ExchangeCodeForToken(code)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Code. Not able to generate auth token from code", err)
	}

	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
	if err != nil || userDetails.Email == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Invalid Code. May be it is expired or invalid")
	}

	config := map[string]interface{}{
		"refresh_token": tok.RefreshToken,
		"email":         userDetails.Email,
	}

	return userDetails.Email, config, nil
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

func sendSyncResponse(c echo.Context, syncType string, data interface{}) error {
	message := "Daily Automatic Backup Created Successfully"
	response := map[string]interface{}{
		"message": message,
		"data":    data,
	}

	if syncType == "one_time" {
		cronJobData, ok := data.(*repo.CronJobListingDB)
		if !ok {
			return jsonErrorMsg(http.StatusInternalServerError, "Invalid data type returned")
		}

		database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
		task, err := database.TaskRepo.CreateTaskForCronJob(cronJobData.ID)
		if err != nil {
			return jsonError(http.StatusInternalServerError, "Failed to create task for one-time backup", err)
		}

		response["message"] = "One-time Automatic Backup Created Successfully"
		response["task"] = task
	}

	return c.JSON(http.StatusOK, response)
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

	task, err := database.TaskRepo.CreateTaskForCronJob(job.ID)
	if err != nil {
		return sendJSONError(c, http.StatusInternalServerError, "Failed to create task", err)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Task created successfully",
		"data":    task,
	})
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

	type DatabaseConnection struct {
		DatabaseName string `json:"database_name"`
		Host         string `json:"host"`
		Port         string `json:"port"`
		Username     string `json:"username"`
		Password     string `json:"password"`
	}

	var reqBody struct {
		Interval           *string             `json:"interval"`
		On                 *string             `json:"on"`
		Code               *string             `json:"code"`
		RefreshToken       *string             `json:"refresh_token"`
		DatabaseConnection *DatabaseConnection `json:"database_connection"`
		StorxToken         *string             `json:"storx_token"`
		Active             *bool               `json:"active"`
	}

	if err := c.Bind(&reqBody); err != nil {
		logger.Error(ctx, "Failed to bind request body",
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}

	// For one-time syncs, only allow storx_token and refresh_token (outlook) updates
	if job.SyncType == "one_time" {
		// Block updates to interval, on, code, database_connection, active
		if reqBody.Interval != nil || reqBody.On != nil ||
			reqBody.DatabaseConnection != nil || reqBody.Active != nil {
			logger.Warn(ctx, "Attempt to update restricted fields for one-time sync",
				logger.Int("job_id", jobID))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "For one-time sync jobs, only storx_token and code for outlook/gmail updates are allowed",
			})
		}

		updateRequest := map[string]interface{}{}
		logger.Info(ctx, "Processing one-time sync update", logger.Int("job_id", jobID))

		// Handle storx_token update for one-time syncs
		if reqBody.StorxToken != nil {
			if *reqBody.StorxToken == "" {
				logger.Warn(ctx, "Empty storx_token provided for one-time sync",
					logger.Int("job_id", jobID))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   "storx_token cannot be empty",
				})
			}
			updateRequest["storx_token"] = *reqBody.StorxToken
			logger.Info(ctx, "Storx token updated for one-time sync",
				logger.Int("job_id", jobID))
		}

		// Handle code update for one-time syncs (gmail only)
		if reqBody.Code != nil {
			if job.Method != "gmail" {
				logger.Warn(ctx, "Code update attempted for non-gmail one-time sync",
					logger.Int("job_id", jobID),
					logger.String("current_method", job.Method))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   "code update is only allowed for gmail method",
				})
			}

			logger.Info(ctx, "Processing Google OAuth code for one-time sync", logger.Int("job_id", jobID))
			tok, err := google.ExchangeCodeForToken(*reqBody.Code)
			if err != nil {
				logger.Error(ctx, "Failed to get refresh token from code",
					logger.Int("job_id", jobID),
					logger.ErrorField(err))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Code. Not able to generate auth token from code",
					"error":   err.Error(),
				})
			}

			// Get User Email
			userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
			if err != nil {
				logger.Error(ctx, "Failed to get Google account details",
					logger.Int("job_id", jobID),
					logger.ErrorField(err))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Code. May be it is expired or invalid",
					"error":   err.Error(),
				})
			}

			if userDetails.Email == "" {
				logger.Error(ctx, "Empty email received from Google token", logger.Int("job_id", jobID))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Code. May be it is expired or invalid",
					"error":   "getting empty email id from google token",
				})
			}

			if userDetails.Email != job.Name {
				logger.Warn(ctx, "Email mismatch in Google OAuth for one-time sync",
					logger.Int("job_id", jobID),
					logger.String("token_email", userDetails.Email),
					logger.String("job_email", job.Name))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "email id mismatch",
				})
			}

			updateRequest["input_data"] = map[string]interface{}{
				"refresh_token": tok.RefreshToken,
			}
			logger.Info(ctx, "Google OAuth token updated successfully for one-time sync",
				logger.Int("job_id", jobID),
				logger.String("email", userDetails.Email))
		}

		// Handle refresh_token update for one-time syncs (outlook only)
		if reqBody.RefreshToken != nil {
			if job.Method != "outlook" {
				logger.Warn(ctx, "Refresh token update attempted for non-outlook one-time sync",
					logger.Int("job_id", jobID),
					logger.String("current_method", job.Method))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   "refresh_token update is only allowed for outlook method",
				})
			}

			logger.Info(ctx, "Processing Outlook refresh token for one-time sync", logger.Int("job_id", jobID))
			// Get new access token using refresh token
			authToken, err := outlook.AuthTokenUsingRefreshToken(*reqBody.RefreshToken)
			if err != nil {
				logger.Error(ctx, "Failed to get auth token from refresh token",
					logger.Int("job_id", jobID),
					logger.ErrorField(err))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Refresh Token. Not able to generate auth token from refresh token",
					"error":   err.Error(),
				})
			}

			// Create Outlook client and get user details
			client, err := outlook.NewOutlookClientUsingToken(authToken)
			if err != nil {
				logger.Error(ctx, "Failed to create Outlook client",
					logger.Int("job_id", jobID),
					logger.ErrorField(err))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Refresh Token. May be it is expired or invalid",
					"error":   err.Error(),
				})
			}

			userDetails, err := client.GetCurrentUser()
			if err != nil {
				logger.Error(ctx, "Failed to get Outlook user details",
					logger.Int("job_id", jobID),
					logger.ErrorField(err))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Refresh Token. May be it is expired or invalid",
					"error":   err.Error(),
				})
			}

			if userDetails.Mail == "" {
				logger.Error(ctx, "Empty email received from Outlook token", logger.Int("job_id", jobID))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Refresh Token. May be it is expired or invalid",
					"error":   "getting empty email id from outlook token",
				})
			}

			if userDetails.Mail != job.Name {
				logger.Warn(ctx, "Email mismatch in Outlook refresh token update",
					logger.Int("job_id", jobID),
					logger.String("token_email", userDetails.Mail),
					logger.String("job_email", job.Name))
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "email id mismatch",
				})
			}

			updateRequest["input_data"] = map[string]interface{}{
				"refresh_token": *reqBody.RefreshToken,
			}
			logger.Info(ctx, "Outlook refresh token updated successfully for one-time sync",
				logger.Int("job_id", jobID),
				logger.String("email", userDetails.Mail))
		}

		// If no valid updates were provided
		if len(updateRequest) == 0 {
			logger.Warn(ctx, "No valid update fields provided for one-time sync",
				logger.Int("job_id", jobID))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "No valid update fields provided. Only storx_token, code (gmail), and refresh_token (outlook) are allowed",
			})
		}

		logger.Info(ctx, "Updating one-time sync job in database",
			logger.Int("job_id", jobID),
			logger.Int("update_fields_count", len(updateRequest)))

		err = database.CronJobRepo.UpdateCronJobByID(uint(jobID), updateRequest)
		if err != nil {
			logger.Error(ctx, "Failed to update one-time sync job in database",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "Failed to update job",
				"error":   err.Error(),
			})
		}

		data, err := database.CronJobRepo.GetCronJobByID(uint(jobID))
		if err != nil {
			logger.Error(ctx, "Failed to retrieve updated one-time sync job data",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "internal server error",
				"error":   err.Error(),
			})
		}

		logger.Info(ctx, "One-time sync update completed successfully",
			logger.Int("job_id", jobID),
			logger.String("job_name", data.Name))

		return c.JSON(http.StatusOK, map[string]interface{}{
			"message": "Automatic backup updated successfully",
			"data":    data,
		})
	}

	// Validate interval and on together (for daily syncs)
	if (reqBody.Interval != nil && reqBody.On == nil) ||
		(reqBody.On != nil && reqBody.Interval == nil) {
		logger.Warn(ctx, "Invalid interval/on combination",
			logger.Int("job_id", jobID),
			logger.Bool("has_interval", reqBody.Interval != nil),
			logger.Bool("has_on", reqBody.On != nil))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Both interval and on are required together",
		})
	}

	updateRequest := map[string]interface{}{}
	logger.Info(ctx, "Starting update request processing", logger.Int("job_id", jobID))

	if reqBody.Interval != nil {
		if !validateInterval(*reqBody.Interval, *reqBody.On) {
			logger.Warn(ctx, "Invalid interval validation",
				logger.Int("job_id", jobID),
				logger.String("interval", *reqBody.Interval),
				logger.String("on", *reqBody.On))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "On is not valid for the given interval",
			})
		}

		updateRequest["interval"] = *reqBody.Interval
		updateRequest["on"] = *reqBody.On
		logger.Info(ctx, "Interval and on updated",
			logger.Int("job_id", jobID),
			logger.String("interval", *reqBody.Interval),
			logger.String("on", *reqBody.On))
	}

	if reqBody.Code != nil {
		if job.Method != "gmail" {
			logger.Warn(ctx, "Code update attempted for non-gmail method",
				logger.Int("job_id", jobID),
				logger.String("current_method", job.Method))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "refresh token is not allowed for this method",
			})
		}

		logger.Info(ctx, "Processing Google OAuth code", logger.Int("job_id", jobID))
		tok, err := google.ExchangeCodeForToken(*reqBody.Code)
		if err != nil {
			logger.Error(ctx, "Failed to get refresh token from code",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Code. Not able to generate auth token from code",
				"error":   err.Error(),
			})
		}

		// Get User Email
		userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
		if err != nil {
			logger.Error(ctx, "Failed to get Google account details",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Code. May be it is expired or invalid",
				"error":   err.Error(),
			})
		}

		if userDetails.Email == "" {
			logger.Error(ctx, "Empty email received from Google token", logger.Int("job_id", jobID))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Code. May be it is expired or invalid",
				"error":   "getting empty email id from google token",
			})
		}

		if userDetails.Email != job.Name {
			logger.Warn(ctx, "Email mismatch in Google OAuth",
				logger.Int("job_id", jobID),
				logger.String("token_email", userDetails.Email),
				logger.String("job_email", job.Name))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "email id mismatch",
			})
		}

		updateRequest["input_data"] = map[string]interface{}{
			"refresh_token": tok.RefreshToken,
		}
		logger.Info(ctx, "Google OAuth token updated successfully",
			logger.Int("job_id", jobID),
			logger.String("email", userDetails.Email))

	} else if reqBody.DatabaseConnection != nil {
		if job.Method != "database" {
			logger.Warn(ctx, "Database connection update attempted for non-database method",
				logger.Int("job_id", jobID),
				logger.String("current_method", job.Method))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "database connection is not allowed for this method",
			})
		}

		updateRequest["input_data"] = map[string]interface{}{
			"host":          reqBody.DatabaseConnection.Host,
			"port":          reqBody.DatabaseConnection.Port,
			"username":      reqBody.DatabaseConnection.Username,
			"password":      reqBody.DatabaseConnection.Password,
			"database_name": reqBody.DatabaseConnection.DatabaseName,
		}
		logger.Info(ctx, "Database connection updated",
			logger.Int("job_id", jobID),
			logger.String("host", reqBody.DatabaseConnection.Host),
			logger.String("database", reqBody.DatabaseConnection.DatabaseName))

	} else if reqBody.RefreshToken != nil {
		if job.Method != "outlook" {
			logger.Warn(ctx, "Refresh token update attempted for non-outlook method",
				logger.Int("job_id", jobID),
				logger.String("current_method", job.Method))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "refresh token is not allowed for this method",
			})
		}

		logger.Info(ctx, "Processing Outlook refresh token", logger.Int("job_id", jobID))
		// Get new access token using refresh token
		authToken, err := outlook.AuthTokenUsingRefreshToken(*reqBody.RefreshToken)
		if err != nil {
			logger.Error(ctx, "Failed to get auth token from refresh token",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Refresh Token. Not able to generate auth token from refresh token",
				"error":   err.Error(),
			})
		}

		// Create Outlook client and get user details
		client, err := outlook.NewOutlookClientUsingToken(authToken)
		if err != nil {
			logger.Error(ctx, "Failed to create Outlook client",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Refresh Token. May be it is expired or invalid",
				"error":   err.Error(),
			})
		}

		userDetails, err := client.GetCurrentUser()
		if err != nil {
			logger.Error(ctx, "Failed to get Outlook user details",
				logger.Int("job_id", jobID),
				logger.ErrorField(err))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Refresh Token. May be it is expired or invalid",
				"error":   err.Error(),
			})
		}

		if userDetails.Mail == "" {
			logger.Error(ctx, "Empty email received from Outlook token", logger.Int("job_id", jobID))
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Refresh Token. May be it is expired or invalid",
				"error":   "getting empty email id from outlook token",
			})
		}

		updateRequest["input_data"] = map[string]interface{}{
			"refresh_token": *reqBody.RefreshToken,
		}
		logger.Info(context.Background(), "Outlook token updated successfully",
			logger.Int("job_id", jobID),
			logger.String("email", userDetails.Mail))
	}

	if reqBody.StorxToken != nil {
		updateRequest["storx_token"] = *reqBody.StorxToken
		logger.Info(ctx, "Storx token updated",
			logger.Int("job_id", jobID),
			logger.Bool("has_storx_token", true))
	}

	if reqBody.Active != nil {
		updateRequest["active"] = *reqBody.Active
		if *reqBody.Active {
			updateRequest["message"] = "You Automatic backup is activated. it will start processing first backup soon"
			updateRequest["message_status"] = repo.JobMessageStatusInfo
			logger.Info(ctx, "Job activated", logger.Int("job_id", jobID))
		} else {
			updateRequest["message"] = "You Automatic backup is deactivated. it will not process any backup"
			updateRequest["message_status"] = repo.JobMessageStatusInfo
			logger.Info(ctx, "Job deactivated", logger.Int("job_id", jobID))
		}
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic backup updated successfully",
		"data":    data,
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

	if interval == "monthly" && (on == "30" || on == "29") {
		return false
	}

	for _, v := range intervalValues[interval] {
		if v == on {
			return true
		}
	}

	return false
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
			Where("user_id = ? AND active = ? AND message_status = ?", userID, true, repo.JobMessageStatusError).
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
