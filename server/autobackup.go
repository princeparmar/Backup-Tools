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

// <<<<<------------ AUTOMATIC SYNC ------------>>>>>
func handleAutomaticSyncListForUser(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized", "error": err.Error()})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	automaticSyncList, err := database.GetAllCronJobsForUser(userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Cron Jobs List",
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
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Bad Request", "error": err.Error()})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	jobDetails, err := database.GetCronJobByID(uint(jobID))
	if err != nil {
		if strings.Contains(err.Error(), "record not found") {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Cron Job Not Found", "error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	storage.MastTokenForCronJobDB(jobDetails)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Cron Job Details",
		"data":    jobDetails,
	})
}

func handleAutomaticSyncCreate(c echo.Context) error {
	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	var reqBody struct {
		Method       string `json:"method"`
		RefreshToken string `json:"refresh_token"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Bad Request"})
	}

	if reqBody.Method != "gmail" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Method"})
	}

	if reqBody.RefreshToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Refresh Token Required"})
	}

	authToken, err := google.AuthTokenUsingRefreshToken(reqBody.RefreshToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token. Not able to generate auth token from refresh token"})
	}

	// Get User Email
	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(authToken)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token. Not able to get user details from access token"})
	}

	if userDetails.Email == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token. Not able to get user email from access token"})
	}
	name := userDetails.Email

	database := c.Get(dbContextKey).(*storage.PosgresStore)
	data, err := database.CreateCronJobForUser(userID, name, reqBody.Method, reqBody.RefreshToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Cron Job Created", "data": data})
}

func handleAutomaticSyncUpdate(c echo.Context) error {
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Job ID"})
	}

	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

	job, err := database.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	var reqBody struct {
		Interval *string `json:"interval"`
		On       *string `json:"on"`

		RefreshToken *string `json:"refresh_token"`

		StorxToken *string `json:"storx_token"`

		Active *bool `json:"active"`
	}

	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Bad Request"})
	}

	updateRequest := map[string]interface{}{}

	if reqBody.Interval != nil {
		if reqBody.On == nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "On is required with Interval"})
		}
		if !validateInterval(*reqBody.Interval, *reqBody.On) {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Interval or On"})
		}

		updateRequest["interval"] = *reqBody.Interval
		updateRequest["on"] = *reqBody.On
	}

	if reqBody.RefreshToken != nil {
		authToken, err := google.AuthTokenUsingRefreshToken(*reqBody.RefreshToken)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token"})
		}

		// Get User Email
		userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(authToken)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token"})
		}

		if userDetails.Email == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token"})
		}

		if userDetails.Email != job.Name {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Refresh Token"})
		}
		updateRequest["refresh_token"] = *reqBody.RefreshToken
	}

	if reqBody.StorxToken != nil {
		updateRequest["storx_token"] = *reqBody.StorxToken
	}

	if reqBody.Active != nil {
		// TODO: Add validation if storx_token is present and auth_token is present
		updateRequest["active"] = *reqBody.Active
	}

	err = database.UpdateCronJobByID(uint(jobID), updateRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	data, err := database.GetCronJobByID(uint(jobID))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Cron Job Updated", "data": data})
}

func handleAutomaticSyncDelete(c echo.Context) error {
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Job ID"})
	}

	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

	if _, err := database.GetJobByIDForUser(userID, uint(jobID)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	err = database.DeleteCronJobByID(uint(jobID))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Cron Job Deleted"})
}

func handleAutomaticSyncTaskList(c echo.Context) error {
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"message": "Invalid Job ID"})
	}

	userID, err := getUserDetailsFromSatellite(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	database := c.Get(dbContextKey).(*storage.PosgresStore)

	if _, err := database.GetJobByIDForUser(userID, uint(jobID)); err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"message": "Unauthorized"})
	}

	data, err := database.ListAllTasksByJobID(uint(jobID))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Internal Server Error", "error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Cron Job Tasks", "data": data})
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
