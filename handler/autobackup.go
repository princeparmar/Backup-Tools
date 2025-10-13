package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/labstack/echo/v4"
)

var Err error

var intervalValues = map[string][]string{
	"monthly": {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
		"14", "15", "16", "17", "18", "19", "20", "21", "22", "23",
		"24", "25", "26", "27", "28", "29", "30"},
	"weekly": {"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	"daily":  {"12am"},
}

type DatabaseConnection struct {
	Name         string `json:"name"`
	DatabaseName string `json:"database_name"`
	Host         string `json:"host"`
	Port         string `json:"port"`
	Username     string `json:"username"`
	Password     string `json:"password"`
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)
	automaticSyncList, err := database.GetAllCronJobsForUser(userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Accounts List",
		"data":    storage.MastTokenForCronJobListingDB(automaticSyncList),
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)
	activeJobs, err := database.GetAllActiveCronJobsForUser(userID)
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)
	jobDetails, err := database.GetCronJobByID(uint(jobID))
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

	storage.MastTokenForCronJobDB(jobDetails)

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
		name, config, err = processGmailMethod(reqBody.Code, syncType)
	case "outlook":
		name, config, err = processOutlookMethod(reqBody.RefreshToken, syncType)
	case "psql_database", "mysql_database":
		name, config, err = processDatabaseMethod(DatabaseConnection{
			Name:         reqBody.Name,
			DatabaseName: reqBody.DatabaseName,
			Host:         reqBody.Host,
			Port:         reqBody.Port,
			Username:     reqBody.Username,
			Password:     reqBody.Password,
		}, syncType)
	}

	if err != nil {
		return err
	}

	// Create the sync job
	data, err := createSyncJob(userID, name, method, syncType, config, c)
	if err != nil {
		return err
	}

	return sendSyncResponse(c, syncType, data)
}

// Method processing functions
func processGmailMethod(code string, syncType string) (string, map[string]interface{}, error) {
	if code == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Code is required")
	}

	tok, err := google.GetRefreshTokenFromCodeForEmail(code)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Code. Not able to generate auth token from code", err)
	}

	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
	if err != nil || userDetails.Email == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Invalid Code. May be it is expired or invalid")
	}

	config := map[string]interface{}{
		"refresh_token": tok.RefreshToken,
		"sync_type":     syncType,
		"email":         userDetails.Email,
	}

	return userDetails.Email, config, nil
}

func processOutlookMethod(refreshToken, syncType string) (string, map[string]interface{}, error) {
	if refreshToken == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Refresh Token Required")
	}

	authToken, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Refresh Token. Not able to generate auth token", err)
	}

	client, err := outlook.NewOutlookClientUsingToken(authToken)
	if err != nil {
		return "", nil, jsonError(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid", err)
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil || userDetails.Mail == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Invalid Refresh Token. May be it is expired or invalid")
	}

	config := map[string]interface{}{
		"refresh_token": refreshToken,
		"sync_type":     syncType,
		"email":         userDetails.Mail,
	}

	return userDetails.Mail, config, nil
}

func processDatabaseMethod(reqBody DatabaseConnection, syncType string) (string, map[string]interface{}, error) {
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
		"sync_type":     syncType,
		"email":         reqBody.Name,
	}

	return reqBody.Name, config, nil
}

// Helper functions
func createSyncJob(userID, name, method, syncType string, config map[string]interface{}, c echo.Context) (interface{}, error) {
	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	// Check for existing jobs using original name (before adding timestamp)
	if err := checkExistingJobs(userID, syncType, method, database); err != nil {
		return nil, err
	}

	data, err := database.CreateCronJobForUser(userID, name, method, syncType, config)
	if err != nil {
		return nil, handleDBError(err)
	}

	return data, nil
}

func checkExistingJobs(userID, syncType, method string, db *storage.PosgresStore) error {
	existingJobs, err := db.GetAllCronJobsForUser(userID)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "Failed to check existing jobs", err)
	}

	serviceName := getServiceName(method)

	for _, job := range existingJobs {
		// Check if there are running tasks for this job
		hasRunningTasks, err := hasRunningTasksForJob(db, job.ID)
		if err != nil {
			return jsonError(http.StatusInternalServerError, "Failed to check task status", err)
		}

		// Handle job type conflicts
		if err := handleJobTypeConflicts(&job, syncType, serviceName, hasRunningTasks); err != nil {
			return err
		}
	}

	return nil
}

func handleJobTypeConflicts(job *storage.CronJobListingDB, syncType, serviceName string, hasRunningTasks bool) error {
	// Check for running tasks first (applies to both job types)
	if hasRunningTasks {
		errorMsg := fmt.Sprintf("A backup job for this %s is currently running. Cannot create %s backup.", serviceName, syncType)
		return jsonErrorMsg(http.StatusBadRequest, errorMsg, errorMsg)
	}

	// Handle daily job conflicts
	if job.SyncType == "daily" {
		if syncType == "daily" {
			errorMsg := fmt.Sprintf("A daily backup job for this %s already exists", serviceName)
			return jsonErrorMsg(http.StatusBadRequest, errorMsg, errorMsg)
		} else if syncType == "one_time" && job.Active {
			errorMsg := fmt.Sprintf("A daily backup job for this %s is already active. Cannot create one-time backup.", serviceName)
			return jsonErrorMsg(http.StatusBadRequest, errorMsg, errorMsg)
		}
	}

	// Handle one-time job conflicts
	if job.SyncType == "one_time" {
		if syncType == "one_time" {
			errorMsg := fmt.Sprintf("A one-time backup job for this %s already exists", serviceName)
			return jsonErrorMsg(http.StatusBadRequest, errorMsg, errorMsg)
		} else if syncType == "daily" && job.Active {
			errorMsg := fmt.Sprintf("A one-time backup job for this %s is already active. Cannot create daily backup.", serviceName)
			return jsonErrorMsg(http.StatusBadRequest, errorMsg, errorMsg)
		}
	}

	return nil
}

func hasRunningTasksForJob(db *storage.PosgresStore, jobID uint) (bool, error) {
	var count int64
	err := db.DB.Model(&storage.TaskListingDB{}).
		Where("cron_job_id = ? AND status IN (?, ?)", jobID, "running", "pushed").
		Count(&count).Error
	return count > 0, err
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
		cronJobData, ok := data.(*storage.CronJobListingDB)
		if !ok {
			return jsonErrorMsg(http.StatusInternalServerError, "Invalid data type returned")
		}

		database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)
		task, err := database.CreateTaskForCronJob(cronJobData.ID)
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	job, err := database.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return sendJSONError(c, http.StatusNotFound, "Job not found", err)
	}

	if job.SyncType != "one_time" {
		return sendJSONError(c, http.StatusBadRequest, "Job is not a one-time job", nil)
	}

	hasRunningTasks, err := hasRunningTasksForJob(database, job.ID)
	if err != nil {
		return sendJSONError(c, http.StatusInternalServerError, "Failed to check task status", err)
	}

	if hasRunningTasks {
		return sendJSONError(c, http.StatusBadRequest, "one time backup is already running wait for it to complete", nil)
	}

	task, err := database.CreateTaskForCronJob(job.ID)
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	// Verify job exists and belongs to user
	job, err := database.GetJobByIDForUser(userID, uint(jobID))
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
	logger.Info(ctx, "Request body parsed successfully",
		logger.Int("job_id", jobID),
		logger.Bool("has_interval", reqBody.Interval != nil),
		logger.Bool("has_on", reqBody.On != nil),
		logger.Bool("has_code", reqBody.Code != nil),
		logger.Bool("has_refresh_token", reqBody.RefreshToken != nil),
		logger.Bool("has_database_connection", reqBody.DatabaseConnection != nil),
		logger.Bool("has_storx_token", reqBody.StorxToken != nil),
		logger.Bool("has_active", reqBody.Active != nil))

	// Validate interval and on together
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
		tok, err := google.GetRefreshTokenFromCodeForEmail(*reqBody.Code)
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
			updateRequest["message_status"] = storage.JobMessageStatusInfo
			logger.Info(ctx, "Job activated", logger.Int("job_id", jobID))
		} else {
			updateRequest["message"] = "You Automatic backup is deactivated. it will not process any backup"
			updateRequest["message_status"] = storage.JobMessageStatusInfo
			logger.Info(ctx, "Job deactivated", logger.Int("job_id", jobID))
		}
	}

	logger.Info(ctx, "Updating job in database",
		logger.Int("job_id", jobID),
		logger.Int("update_fields_count", len(updateRequest)))

	err = database.UpdateCronJobByID(uint(jobID), updateRequest)
	if err != nil {
		logger.Error(ctx, "Failed to update job in database",
			logger.Int("job_id", jobID),
			logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to update job",
			"error":   err.Error(),
		})
	}

	data, err := database.GetCronJobByID(uint(jobID))
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	if _, err := database.GetJobByIDForUser(userID, uint(jobID)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	err = database.DeleteCronJobByID(uint(jobID))
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

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	if _, err := database.GetJobByIDForUser(userID, uint(jobID)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	data, err := database.ListAllTasksByJobID(uint(jobID), uint(limit), uint(offset))
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
	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	// Delete all jobs and tasks for the user by email
	deletedJobIDs, deletedTaskIDs, err := database.DeleteAllJobsAndTasksByEmail(req.Email)
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
