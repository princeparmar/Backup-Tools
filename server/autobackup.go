package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/labstack/echo/v4"
)

var intervalValues = map[string][]string{
	"monthly": {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
		"14", "15", "16", "17", "18", "19", "20", "21", "22", "23",
		"24", "25", "26", "27", "28", "29", "30"},
	"weekly": {"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	"daily":  {"12am"},
}

// <<<<<------------ AUTOMATIC BACKUP ------------>>>>>
func handleAutomaticSyncListForUser(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "not able to authenticate user",
			"error":   err.Error(),
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
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

func handleIntervalOnConfig(c echo.Context) error {
	c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Interval Values",
		"data":    intervalValues,
	})
	return nil
}

func handleAutomaticSyncDetails(c echo.Context) error {
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
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

func handleAutomaticSyncCreateGmail(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	var reqBody struct {
		Code string `json:"code"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	if reqBody.Code == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Code is required",
		})
	}

	tok, err := google.GetRefreshTokenFromCodeForEmail(reqBody.Code)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Code. Not able to generate auth token from code",
			"error":   err.Error(),
		})
	}

	// Get User Email
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Code. May be it is expired or invalid",
			"error":   err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Code. May be it is expired or invalid",
			"error":   "getting empty email id from google token",
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	data, err := database.CreateCronJobForUser(userID, userDetails.Email, "gmail", map[string]interface{}{
		"refresh_token": tok.RefreshToken,
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key value") {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Email already exists",
				"error":   err.Error(),
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Created Successfully",
		"data":    data,
	})
}

func handleAutomaticSyncCreateOutlook(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	var reqBody struct {
		RefreshToken string `json:"refresh_token"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	if reqBody.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Refresh Token Required",
		})
	}

	// Get new access token using refresh token
	authToken, err := outlook.AuthTokenUsingRefreshToken(reqBody.RefreshToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. Not able to generate auth token from refresh token",
			"error":   err.Error(),
		})
	}

	// Create Outlook client and get user details
	client, err := outlook.NewOutlookClientUsingToken(authToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. May be it is expired or invalid",
			"error":   err.Error(),
		})
	}

	userDetails, err := client.GetCurrentUser()
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. May be it is expired or invalid",
			"error":   err.Error(),
		})
	}

	if userDetails.Mail == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. May be it is expired or invalid",
			"error":   "getting empty email id from outlook token",
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	data, err := database.CreateCronJobForUser(userID, userDetails.Mail, "outlook", map[string]interface{}{
		"refresh_token": reqBody.RefreshToken,
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key value") {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Email already exists",
				"error":   err.Error(),
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Created Successfully",
		"data":    data,
	})
}

func handleAutomaticSyncCreateDatabase(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	method := c.Param("method")

	if method != "psql_database" && method != "mysql_database" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "invalid method",
		})
	}

	var reqBody struct {
		Name         string `json:"name"`
		DatabaseName string `json:"database_name"`
		Host         string `json:"host"`
		Port         string `json:"port"`
		Username     string `json:"username"`
		Password     string `json:"password"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	if reqBody.Name == "" || reqBody.DatabaseName == "" || reqBody.Host == "" || reqBody.Port == "" || reqBody.Username == "" || reqBody.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "all fields are required",
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	data, err := database.CreateCronJobForUser(userID, reqBody.Name, method, map[string]interface{}{
		"database_name": reqBody.DatabaseName,
		"host":          reqBody.Host,
		"port":          reqBody.Port,
		"username":      reqBody.Username,
		"password":      reqBody.Password,
	})

	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Created Successfully",
		"data":    data,
	})
}

func handleAutomaticBackupUpdate(c echo.Context) error {

	ctx := c.Request().Context()
	logger.Info(ctx, "Starting automatic backup update request")

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

	userID, err := getUserDetailsFromSatellite(c)
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

	database := c.Get(dbContextKey).(*storage.PosgresStore)

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

func handleAutomaticSyncDelete(c echo.Context) error {
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

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

func handleAutomaticSyncTaskList(c echo.Context) error {
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

	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

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

func getUserDetailsFromSatellite(c echo.Context) (string, error) {
	tokenKey := c.Request().Header.Get("token_key")
	return satellite.GetUserdetails(tokenKey)
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

// handleDeleteJobsByEmail deletes all jobs and tasks for a user by email with password protection
func handleDeleteJobsByEmail(c echo.Context) error {
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
	database := c.Get(dbContextKey).(*storage.PosgresStore)

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
