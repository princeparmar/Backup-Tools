package handler

import (
	"net/http"
	"time"

	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/labstack/echo/v4"
)

// HandleCreateScheduledTask creates a new scheduled task
func HandleCreateScheduledTask(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Get user details from token
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	method := c.Param("method")

	// Parse request body
	var reqBody struct {
		LoginId      string   `json:"login_id" validate:"required"`
		StorxToken   string   `json:"storx_token" validate:"required"`
		EmailIds     []string `json:"email_ids" validate:"required"`
		Code         string   `json:"code"`
		RefreshToken string   `json:"refresh_token"`
	}

	if err := c.Bind(&reqBody); err != nil {
		logger.Error(ctx, "Failed to bind request body", logger.ErrorField(err))
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}

	// Validate required fields
	if reqBody.LoginId == "" || method == "" || reqBody.StorxToken == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "login_id, method, and storx_token are required",
		})
	}

	var inputData *database.DbJson[map[string]interface{}]
	var memory *database.DbJson[map[string]string]

	if reqBody.Code != "" || reqBody.RefreshToken != "" {
		//store into input_data
		inputDataMap := make(map[string]any)
		if reqBody.Code != "" {
			inputDataMap["code"] = reqBody.Code
		}
		if reqBody.RefreshToken != "" {
			inputDataMap["refresh_token"] = reqBody.RefreshToken
		}
		inputData = database.NewDbJsonFromValue(inputDataMap)
	}

	if reqBody.EmailIds != nil {
		// Create a map of email_ids with "pending" status
		emailStatusMap := make(map[string]string)
		for _, emailID := range reqBody.EmailIds {
			emailStatusMap[emailID] = "pending" // Initial status for each email
		}
		memory = database.NewDbJsonFromValue(emailStatusMap)
	}

	// Get database connection
	db := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	// Create scheduled task
	task := &repo.ScheduledTasks{
		UserID:     userID,
		LoginId:    reqBody.LoginId,
		Method:     method,
		StorxToken: reqBody.StorxToken,
		Memory:     memory,
		Status:     "created",
		InputData:  inputData,
		Errors:     *database.NewDbJsonFromValue([]string{}),
	}

	// Save to database
	if err := task.Create(db.DB); err != nil {
		logger.Error(ctx, "Failed to create scheduled task", logger.ErrorField(err))
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to create scheduled task",
			"error":   err.Error(),
		})
	}

	logger.Info(ctx, "Scheduled task created successfully",
		logger.String("user_id", userID),
		logger.String("login_id", reqBody.LoginId),
		logger.String("method", method))

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Scheduled task created successfully",
		"task_id": task,
	})
}

// HandleGetScheduledTasksByUserID retrieves all scheduled tasks for the authenticated user
func HandleGetScheduledTasksByUserID(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	filter := parseFilterParams(c)

	// Get database connection
	db := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Scheduled tasks retrieved successfully",
		"tasks":   tasks,
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
