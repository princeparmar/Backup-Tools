package handler

import (
	"fmt"
	"net/http"
	"reflect"
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

	// Enrich tasks with execution_time_formatted, progress, and operation
	enrichedTasks := enrichTasksForUI(maskedTasks)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Scheduled tasks retrieved successfully",
		"tasks":   enrichedTasks,
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

func enrichTasksForUI(tasks []repo.ScheduledTasks) []map[string]interface{} {
	enriched := make([]map[string]interface{}, len(tasks))

	for i, task := range tasks {
		taskMap := structToMap(task)

		taskMap["execution_time_formatted"] = formatExecutionTime(task.CreatedAt, task.UpdatedAt)
		taskMap["progress"] = calculateProgressFromMemory(task)
		taskMap["operation"] = getOperationByMethod(task.Method)

		enriched[i] = taskMap
	}

	return enriched
}

func structToMap(v interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	val := reflect.ValueOf(v)
	typ := reflect.TypeOf(v)

	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return result
		}
		val = val.Elem()
		typ = typ.Elem()
	}

	if val.Kind() != reflect.Struct {
		return result
	}

	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		fieldVal := val.Field(i)

		if !fieldVal.CanInterface() {
			continue
		}

		jsonKey := field.Tag.Get("json")
		if jsonKey == "" || jsonKey == "-" {
			jsonKey = field.Name
		} else {
			if idx := strings.Index(jsonKey, ","); idx != -1 {
				jsonKey = jsonKey[:idx]
			}
		}

		if fieldVal.Kind() == reflect.Ptr {
			if fieldVal.IsNil() {
				result[jsonKey] = nil
				continue
			}

			ptrVal := fieldVal.Interface()
			if dbJsonPtr, ok := ptrVal.(*database.DbJson[map[string][]string]); ok {
				if jsonVal := dbJsonPtr.Json(); jsonVal != nil {
					result[jsonKey] = *jsonVal
				} else {
					result[jsonKey] = map[string][]string{}
				}
				continue
			}
			if dbJsonPtr, ok := ptrVal.(*database.DbJson[map[string]interface{}]); ok {
				if jsonVal := dbJsonPtr.Json(); jsonVal != nil {
					result[jsonKey] = *jsonVal
				} else {
					result[jsonKey] = map[string]interface{}{}
				}
				continue
			}
			if dbJsonPtr, ok := ptrVal.(*database.DbJson[[]string]); ok {
				if jsonVal := dbJsonPtr.Json(); jsonVal != nil {
					result[jsonKey] = *jsonVal
				} else {
					result[jsonKey] = []string{}
				}
				continue
			}

			fieldVal = fieldVal.Elem()
		}

		result[jsonKey] = fieldVal.Interface()
	}

	return result
}

func formatExecutionTime(createdAt, updatedAt time.Time) string {
	if updatedAt.IsZero() || createdAt.IsZero() {
		return "-"
	}

	seconds := int(updatedAt.Sub(createdAt).Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}

	minutes := seconds / 60
	if remainingSeconds := seconds % 60; remainingSeconds > 0 {
		return fmt.Sprintf("%dm %ds", minutes, remainingSeconds)
	}
	return fmt.Sprintf("%dm", minutes)
}

func calculateProgressFromMemory(task repo.ScheduledTasks) int {
	memPtr := task.Memory.Json()
	if memPtr == nil {
		return 0
	}

	memory := *memPtr

	getLen := func(key string) int {
		if arr, ok := memory[key]; ok {
			return len(arr)
		}
		return 0
	}

	getErrorLen := func() int {
		total := 0
		for key, arr := range memory {
			if strings.HasPrefix(key, "error") {
				total += len(arr)
			}
		}
		return total
	}

	synced := getLen("synced")
	total := synced + getLen("pending") + getLen("skipped") + getErrorLen()

	if total == 0 {
		if task.Status == "completed" {
			return 100
		}
		return 0
	}

	return int(float64(synced) / float64(total) * 100)
}

var operationMappings = map[string]string{
	"gmail":          "Email Backup",
	"outlook":        "Email Backup",
	"google_photos":  "Photos Upload",
	"google_drive":   "Folder Upload",
	"psql_database":  "Database Backup",
	"mysql_database": "Database Backup",
}

func getOperationByMethod(method string) string {
	if operation, ok := operationMappings[method]; ok {
		return operation
	}
	return "Backup"
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
