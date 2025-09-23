package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
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

	job, err := database.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	type DatabaseConnection struct {
		DatabaseName string `json:"database_name"`
		Host         string `json:"host"`
		Port         string `json:"port"`
		Username     string `json:"username"`
		Password     string `json:"password"`
	}

	var reqBody struct {
		Interval *string `json:"interval"`
		On       *string `json:"on"`

		Code *string `json:"code"`

		RefreshToken *string `json:"refresh_token"`

		DatabaseConnection *DatabaseConnection `json:"database_connection"`

		StorxToken *string `json:"storx_token"`

		Active *bool `json:"active"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	updateRequest := map[string]interface{}{}

	if reqBody.Interval != nil {
		if reqBody.On == nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "On is required with Interval",
			})
		}
		if !validateInterval(*reqBody.Interval, *reqBody.On) {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "On is not valid for the given interval",
			})
		}

		updateRequest["interval"] = *reqBody.Interval
		updateRequest["on"] = *reqBody.On
	}

	if reqBody.Code != nil {
		if job.Method != "gmail" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "refresh token is not allowed for this method",
			})
		}
		tok, err := google.GetRefreshTokenFromCodeForEmail(*reqBody.Code)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   err.Error(),
			})
		}

		// Get User Email
		userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(tok.AccessToken)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   err.Error(),
			})
		}

		if userDetails.Email == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "getting empty email id from google token",
			})
		}

		if userDetails.Email != job.Name {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "email id mismatch",
			})
		}
		updateRequest["input_data"] = map[string]interface{}{
			"refresh_token": tok.RefreshToken,
		}
	} else if reqBody.DatabaseConnection != nil {
		if job.Method != "database" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "database connection is not allowed for this method",
			})
		}

		updateRequest["input_data"] = map[string]interface{}{
			"host":          reqBody.DatabaseConnection.Host,
			"port":          reqBody.DatabaseConnection.Port,
			"username":      reqBody.DatabaseConnection.Username,
			"password":      reqBody.DatabaseConnection.Password,
			"database_name": reqBody.DatabaseConnection.DatabaseName,
		}
	} else if reqBody.RefreshToken != nil {
		if job.Method != "outlook" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "refresh token is not allowed for this method",
			})
		}

		// Get new access token using refresh token
		authToken, err := outlook.AuthTokenUsingRefreshToken(*reqBody.RefreshToken)
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

		updateRequest["input_data"] = map[string]interface{}{
			"refresh_token": *reqBody.RefreshToken,
		}
	}

	if reqBody.StorxToken != nil {
		updateRequest["storx_token"] = *reqBody.StorxToken
	}

	if reqBody.Active != nil {
		updateRequest["active"] = *reqBody.Active
		if *reqBody.Active {
			// Validate required fields when activating backup
			if reqBody.StorxToken == nil {
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   "storx_token is required when activating backup",
				})
			}

			if reqBody.Interval == nil {
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   "interval is required when activating backup",
				})
			}
			if reqBody.On == nil {
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   "on is required when activating backup",
				})
			}

			// Validate authentication token based on job method
			if err := validateExistingAuthToken(job); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"message": "Invalid Request",
					"error":   err.Error(),
				})
			}
			updateRequest["message"] = "You Automatic backup is activated. it will start processing first backup soon"
			updateRequest["message_status"] = storage.JobMessageStatusInfo
		} else {
			updateRequest["message"] = "You Automatic backup is deactivated. it will not process any backup"
			updateRequest["message_status"] = storage.JobMessageStatusInfo
		}
	}

	err = database.UpdateCronJobByID(uint(jobID), updateRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	data, err := database.GetCronJobByID(uint(jobID))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Automatic Backup Updated Successfully",
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

// validateExistingAuthToken checks if the job has the required authentication token in input_data
func validateExistingAuthToken(job *storage.CronJobListingDB) error {
	// Parse existing input_data to check for authentication tokens
	inputData := job.InputData

	switch job.Method {
	case "gmail":
		// Check if refresh_token exists in input_data
		if refreshToken, exists := inputData["refresh_token"]; !exists || refreshToken == "" {
			return fmt.Errorf("refresh_token is required in input_data for gmail method")
		}
	case "outlook":
		// Check if refresh_token exists in input_data
		if refreshToken, exists := inputData["refresh_token"]; !exists || refreshToken == "" {
			return fmt.Errorf("refresh_token is required in input_data for outlook method")
		}
	case "database", "psql_database", "mysql_database":
		// Check if database connection details exist in input_data
		requiredFields := []string{"host", "port", "username", "password", "database_name"}
		for _, field := range requiredFields {
			if value, exists := inputData[field]; !exists || value == "" {
				return fmt.Errorf("%s is required in input_data for database method", field)
			}
		}
	}
	return nil
}
