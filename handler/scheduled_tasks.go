package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// HandleCreateScheduledTask creates a new scheduled task
func HandleCreateScheduledTask(c echo.Context) error {
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

	method := c.Param("method")

	// Get email_ids as a comma-separated string
	emailIdsStr := c.FormValue("email_ids")
	if emailIdsStr == "" {
		return jsonErrorMsg(http.StatusBadRequest, "email_ids are required")
	}

	storxToken := c.FormValue("storx_token")
	if storxToken == "" {
		return jsonErrorMsg(http.StatusBadRequest, "storx_token is required")
	}

	// Parse the comma-separated string
	emailIds := strings.Split(emailIdsStr, ",")
	// Trim whitespace from each email ID
	for i := range emailIds {
		emailIds[i] = strings.TrimSpace(emailIds[i])
	}

	if len(emailIds) == 0 {
		return jsonErrorMsg(http.StatusBadRequest, "email_ids cannot be empty")
	}

	// Get access_token from header
	accessToken := c.Request().Header.Get("ACCESS_TOKEN")
	if accessToken == "" {
		return jsonErrorMsg(http.StatusBadRequest, "ACCESS_TOKEN header is required")
	}

	if method == "" {
		return jsonErrorMsg(http.StatusBadRequest, "method is required")
	}

	var email string
	var config map[string]interface{}
	switch method {
	case "gmail":
		email, config, err = processGmailAccessToken(accessToken)
	case "outlook":
		email, config, err = ProcessOutlookAccessToken(accessToken)
	default:
		return jsonErrorMsg(http.StatusBadRequest, "Unsupported method. Supported methods: gmail")
	}
	if err != nil {
		return err
	}

	statusEmailsMap := make(map[string][]string)
	statusEmailsMap["pending"] = emailIds

	db := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	task := &repo.ScheduledTasks{
		UserID:     userID,
		LoginId:    email,
		Method:     method,
		StorxToken: storxToken,
		Memory:     database.NewDbJsonFromValue(statusEmailsMap),
		Status:     "created",
		InputData:  database.NewDbJsonFromValue(config),
		Errors:     *database.NewDbJsonFromValue([]string{}),
	}

	if err := task.Create(db.DB); err != nil {
		logger.Error(ctx, "Failed to create scheduled task", logger.ErrorField(err))
		return jsonError(http.StatusInternalServerError, "Failed to create scheduled task", err)
	}

	logger.Info(ctx, "Scheduled task created successfully",
		logger.String("user_id", userID),
		logger.String("login_id", email),
		logger.String("method", method))

	// Send notification for scheduled task creation
	priority := "normal"
	data := map[string]interface{}{
		"event":     "scheduled_task_created",
		"level":     2,
		"task_id":   task.ID,
		"method":    method,
		"login_id":  email,
		"email_ids": emailIds,
	}
	satellite.SendNotificationAsync(ctx, userID, "Scheduled Task Created", fmt.Sprintf("Scheduled task for %s has been created successfully", email), &priority, data, nil)

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"success": true,
		"message": "Scheduled task created successfully",
		"task_id": task.ID,
		"email":   email,
	})
}

// HandleGetRunningScheduledTasks retrieves all running scheduled tasks for the authenticated user
func HandleGetRunningScheduledTasks(c echo.Context) error {
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

	// Get database connection
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Get running scheduled tasks by user ID
	runningTasks, err := database.ScheduledTasksRepo.GetAllRunningScheduledTasksForUser(userID)
	if err != nil {
		logger.Error(ctx, "Failed to get running scheduled tasks", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "internal server error",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Running Scheduled Tasks List",
		"data":    runningTasks,
	})
}

// HandleGetScheduledTasksByUserID retrieves all scheduled tasks for the authenticated user
func HandleGetScheduledTasksByUserID(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	filter := parseFilterParams(c)

	// Get database connection
	db := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	// Get scheduled tasks by user ID
	taskRepo := &repo.ScheduledTasks{}
	tasks, err := taskRepo.GetTasksForCurrentUser(db.DB, *filter)
	if err != nil {
		logger.Error(ctx, "Failed to get scheduled tasks", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to retrieve scheduled tasks",
			"error":   err.Error(),
		})
	}

	logger.Info(ctx, "Retrieved scheduled tasks",
		logger.String("user_id", filter.UserID),
		logger.Int("task_count", len(tasks)))

	// Mask storx_token before returning
	maskedTasks := maskStorxTokens(tasks)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Scheduled tasks retrieved successfully",
		"tasks":   maskedTasks,
		"count":   len(tasks),
	})
}

func parseFilterParams(c echo.Context) *repo.ScheduledTasksFilter {
	filter := &repo.ScheduledTasksFilter{
		Order: "desc", // default order
	}

	// Get user ID from authenticated user
	if userID, err := satellite.GetUserdetails(c); err == nil {
		filter.UserID = userID
	}

	if loginID := c.QueryParam("login_id"); loginID != "" {
		filter.LoginID = loginID
	}

	if method := c.QueryParam("method"); method != "" {
		filter.Method = method
	}

	if status := c.QueryParam("status"); status != "" {
		filter.Status = status
	}

	if startTimeStr := c.QueryParam("start_time"); startTimeStr != "" {
		if startTime, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			filter.StartTime = &startTime
		}
	}

	if order := c.QueryParam("order"); order != "" {
		// Validate order parameter
		if order == "asc" || order == "desc" {
			filter.Order = order
		}
	}

	return filter
}

// maskStorxTokens masks the storx_token field in scheduled tasks before returning them
func maskStorxTokens(tasks []repo.ScheduledTasks) []repo.ScheduledTasks {
	masked := make([]repo.ScheduledTasks, len(tasks))
	for i, task := range tasks {
		masked[i] = task
		if task.StorxToken != "" {
			masked[i].StorxToken = utils.MaskString(task.StorxToken)
		}
	}
	return masked
}

// Method processing functions
func processGmailAccessToken(accessToken string) (string, map[string]interface{}, error) {
	if accessToken == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Access Token is required")
	}

	userDetails, err := google.GetGoogleAccountDetailsFromAccessToken(accessToken)
	if err != nil {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Invalid Access Token: "+err.Error())
	}
	if userDetails == nil || userDetails.Email == "" {
		return "", nil, jsonErrorMsg(http.StatusBadRequest, "Email not found in token validation")
	}

	config := map[string]interface{}{
		"access_token": accessToken,
		"email":        userDetails.Email,
	}

	return userDetails.Email, config, nil
}
