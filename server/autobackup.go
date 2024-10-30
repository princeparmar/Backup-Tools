package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
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

func handleAutomaticSyncCreate(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	var reqBody struct {
		Method       string `json:"method"`
		RefreshToken string `json:"refresh_token"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	if reqBody.Method != "gmail" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Automatic Sync Method not supported",
		})
	}

	if reqBody.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Refresh Token Required",
		})
	}

	authToken, err := google.AuthTokenUsingRefreshToken(reqBody.RefreshToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. Not able to generate auth token from refresh token",
			"error":   err.Error(),
		})
	}

	// Get User Email
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(authToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. May be it is expired or invalid",
			"error":   err.Error(),
		})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Refresh Token. May be it is expired or invalid",
			"error":   "getting empty email id from google token",
		})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	data, err := database.CreateCronJobForUser(userID, userDetails.Email, reqBody.Method, reqBody.RefreshToken)
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

	var reqBody struct {
		Interval *string `json:"interval"`
		On       *string `json:"on"`

		RefreshToken *string `json:"refresh_token"`

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

	if reqBody.RefreshToken != nil {
		authToken, err := google.AuthTokenUsingRefreshToken(*reqBody.RefreshToken)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   err.Error(),
			})
		}

		// Get User Email
		userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(authToken)
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
		updateRequest["refresh_token"] = *reqBody.RefreshToken
	}

	if reqBody.StorxToken != nil {
		updateRequest["storx_token"] = *reqBody.StorxToken
	}

	if reqBody.Active != nil {
		// TODO: Add validation if storx_token is present and auth_token is present
		updateRequest["active"] = *reqBody.Active
		if *reqBody.Active {
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
