package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// TaskProcessorDeps contains all dependencies for task processing
type TaskProcessorDeps struct {
	Store *db.PostgresDb
	Repo  *repo.ScheduledTasksRepository
}

// ScheduledTaskProcessorInput defines the input for processor execution
type ScheduledTaskProcessorInput struct {
	Ctx           context.Context
	InputData     map[string]interface{}
	Memory        map[string][]string
	Task          *repo.ScheduledTasks
	HeartBeatFunc func() error
	Deps          *TaskProcessorDeps
}

type ScheduledTaskProcessor interface {
	Run(ScheduledTaskProcessorInput) error
}

// BaseProcessor provides common functionality for all processors
type BaseProcessor struct {
	Deps *TaskProcessorDeps
}

func (b *BaseProcessor) handleError(task *repo.ScheduledTasks, errMsg string, existingErrors []string) error {
	if task.Errors.Json() != nil {
		existingErrors = *task.Errors.Json()
	}
	existingErrors = append(existingErrors, errMsg)
	task.Errors = *database.NewDbJsonFromValue(existingErrors)
	return fmt.Errorf("%s", errMsg)
}

func (b *BaseProcessor) updateTaskStats(input *ScheduledTaskProcessorInput, successCount, failedCount int, failedEmails []string) error {
	input.Task.SuccessCount = uint(successCount)
	input.Task.FailedCount = uint(failedCount)

	var existingErrors []string
	if input.Task.Errors.Json() != nil {
		existingErrors = *input.Task.Errors.Json()
	}
	existingErrors = append(existingErrors, failedEmails...)

	if failedCount > 0 {
		if successCount > 0 {
			existingErrors = append(existingErrors, fmt.Sprintf("Warning: %d out of %d email IDs failed to sync", failedCount, failedCount+successCount))
		} else {
			existingErrors = append(existingErrors, fmt.Sprintf("Error: All %d email IDs failed to sync", failedCount))
		}
	}

	input.Task.Errors = *database.NewDbJsonFromValue(existingErrors)

	if failedCount > 0 && successCount == 0 {
		return fmt.Errorf("failed to process %d emails", failedCount)
	}
	return nil
}

// ScheduledTaskManager manages scheduled task processing
type ScheduledTaskManager struct {
	Deps      *TaskProcessorDeps
	processor map[string]ScheduledTaskProcessor
}

func NewScheduledTaskManager(store *db.PostgresDb) *ScheduledTaskManager {
	deps := &TaskProcessorDeps{
		Store: store,
		Repo:  repo.NewScheduledTasksRepository(store.DB),
	}
	return &ScheduledTaskManager{
		Deps: deps,
		processor: map[string]ScheduledTaskProcessor{
			"gmail":   NewScheduledGmailProcessor(deps),
			"outlook": NewScheduledOutlookProcessor(deps),
		},
	}
}

func (s *ScheduledTaskManager) Start() {
	c := cron.New()
	c.AddFunc("@every 30s", func() {
		ctx := s.createScheduledTaskContext("process_scheduled_tasks")
		logger.Info(ctx, "Processing scheduled tasks")
		if err := s.ProcessScheduledTasks(ctx); err != nil {
			logger.Error(ctx, "Failed to process scheduled tasks", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully processed scheduled tasks")
		}
	})
	c.Start()
	logger.Info(context.Background(), "Scheduled task processor started successfully")
}

func (s *ScheduledTaskManager) createScheduledTaskContext(operation string) context.Context {
	traceID := uuid.New().String()
	ctx := logger.WithTraceID(context.Background(), traceID)
	logger.Info(ctx, "Scheduled task processing started", logger.String("operation", operation))
	return ctx
}

func (s *ScheduledTaskManager) ProcessScheduledTasks(ctx context.Context) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	processedCount, errorCount := 0, 0

	for {
		task, err := s.Deps.Repo.GetNextScheduledTask()
		if err != nil {
			if strings.Contains(err.Error(), "record not found") {
				logger.Info(ctx, "No scheduled tasks to process")
				break
			}
			return fmt.Errorf("failed to get next scheduled task: %w", err)
		}

		logger.Info(ctx, "Processing scheduled task",
			logger.Int("task_id", int(task.ID)),
			logger.String("method", task.Method),
		)

		// Update task status to running
		task.Status = "running"
		now := time.Now()
		task.StartTime = &now
		if err := s.Deps.Store.DB.Save(task).Error; err != nil {
			logger.Error(ctx, "Failed to update task status to running",
				logger.Int("task_id", int(task.ID)),
				logger.ErrorField(err),
			)
			continue
		}

		// Send notification for scheduled task started running
		priority := "normal"
		data := map[string]interface{}{
			"event":    "scheduled_task_started_running",
			"level":    2,
			"task_id":  task.ID,
			"method":   task.Method,
			"login_id": task.LoginId,
		}
		satellite.SendNotificationAsync(ctx, task.UserID, "Scheduled Task Started", fmt.Sprintf("Scheduled task for %s has started running", task.LoginId), &priority, data, nil)

		processErr := s.processScheduledTask(ctx, task)
		if updateErr := s.UpdateScheduledTaskStatus(ctx, task, processErr); updateErr != nil {
			logger.Error(ctx, "Failed to update scheduled task status",
				logger.Int("task_id", int(task.ID)),
				logger.ErrorField(updateErr),
			)
			continue
		}

		processedCount++
		if processErr != nil {
			errorCount++
		}
	}

	logger.Info(ctx, "Scheduled task processing completed",
		logger.Int("processed", processedCount),
		logger.Int("errors", errorCount),
	)
	return nil
}

func (s *ScheduledTaskManager) processScheduledTask(ctx context.Context, task *repo.ScheduledTasks) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	processor, ok := s.processor[task.Method]
	if !ok {
		return fmt.Errorf("processor for method '%s' not found", task.Method)
	}

	logger.Info(ctx, "Executing processor for scheduled task",
		logger.Int("task_id", int(task.ID)),
		logger.String("method", task.Method),
	)

	inputData, memory := make(map[string]interface{}), make(map[string][]string)
	if task.InputData != nil {
		inputData = *task.InputData.Json()
	}
	if task.Memory != nil {
		memory = *task.Memory.Json()
	}

	err = processor.Run(ScheduledTaskProcessorInput{
		Ctx:       ctx,
		InputData: inputData,
		Memory:    memory,
		Task:      task,
		Deps:      s.Deps,
		HeartBeatFunc: func() error {
			currentTask, err := s.Deps.Repo.GetScheduledTaskByID(task.ID)
			if err != nil {
				return fmt.Errorf("failed to get task status: %w", err)
			}
			if currentTask.Status != "running" {
				return fmt.Errorf("task status changed to '%s', stopping execution", currentTask.Status)
			}
			if err := s.Deps.Repo.UpdateHeartBeatForScheduledTask(task.ID); err != nil {
				return fmt.Errorf("failed to update heartbeat: %w", err)
			}
			return nil
		},
	})

	if memory != nil {
		task.Memory = database.NewDbJsonFromValue(memory)
	}
	return err
}

func (s *ScheduledTaskManager) UpdateScheduledTaskStatus(ctx context.Context, task *repo.ScheduledTasks, processErr error) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	if task.StartTime != nil {
		task.Execution = uint64(time.Since(*task.StartTime).Seconds())
	}

	var hasError, hasSuccess bool
	var errorCount, successCount int

	if task.Memory != nil {
		memory := *task.Memory.Json()
		for status, emailIDs := range memory {
			if strings.HasPrefix(status, "error:") {
				hasError = true
				errorCount += len(emailIDs)
			} else if status == "synced" {
				hasSuccess = true
				successCount += len(emailIDs)
			}
		}
	}

	// Determine task status
	switch {
	case processErr != nil:
		task.Status = "failed"
	case hasError && hasSuccess:
		task.Status = "partially_completed"
	case hasError && !hasSuccess:
		task.Status = "failed"
	case hasSuccess && !hasError:
		task.Status = "completed"
	default:
		task.Status = "completed"
	}

	// Update errors if any
	if processErr != nil || hasError {
		var existingErrors []string
		if task.Errors.Json() != nil {
			existingErrors = *task.Errors.Json()
		}
		if processErr != nil {
			existingErrors = append(existingErrors, processErr.Error())
		}
		if hasError {
			existingErrors = append(existingErrors, fmt.Sprintf("%d IDs failed to sync", errorCount))
		}
		task.Errors = *database.NewDbJsonFromValue(existingErrors)
	} else if task.Errors.Json() == nil {
		task.Errors = *database.NewDbJsonFromValue([]string{})
	}

	if err := s.Deps.Store.DB.Save(task).Error; err != nil {
		logger.Error(ctx, "Failed to save scheduled task status",
			logger.Int("task_id", int(task.ID)),
			logger.ErrorField(err),
		)
		return fmt.Errorf("failed to save scheduled task: %w", err)
	}

	// Send notifications based on task status
	var priority string
	var title, body string
	var level int
	var event string

	switch task.Status {
	case "completed":
		priority = "normal"
		level = 2
		event = "scheduled_task_successfully_completed"
		title = "Scheduled Task Completed"
		body = fmt.Sprintf("Scheduled task for %s completed successfully. Processed %d email(s) successfully in %d seconds", task.LoginId, task.SuccessCount, task.Execution)
	case "partially_completed":
		priority = "normal"
		level = 3
		event = "scheduled_task_partially_completed"
		title = "Scheduled Task Partially Completed"
		body = fmt.Sprintf("Scheduled task for %s partially completed. %d succeeded, %d failed in %d seconds", task.LoginId, task.SuccessCount, task.FailedCount, task.Execution)
	case "failed":
		priority = "high"
		level = 4
		event = "scheduled_task_failed"
		title = "Scheduled Task Failed"
		errorMsg := "Unknown error"
		if task.Errors.Json() != nil && len(*task.Errors.Json()) > 0 {
			errors := *task.Errors.Json()
			errorMsg = errors[len(errors)-1]
		}
		body = fmt.Sprintf("Scheduled task for %s failed: %s", task.LoginId, errorMsg)
	default:
		// No notification for other statuses
		logger.Info(ctx, "Scheduled task status updated",
			logger.Int("task_id", int(task.ID)),
			logger.String("status", task.Status),
			logger.Int("success_count", int(task.SuccessCount)),
			logger.Int("failed_count", int(task.FailedCount)),
		)
		return nil
	}

	data := map[string]interface{}{
		"event":         event,
		"level":         level,
		"task_id":       task.ID,
		"method":        task.Method,
		"login_id":      task.LoginId,
		"success_count": task.SuccessCount,
		"failed_count":  task.FailedCount,
		"execution":     task.Execution,
	}
	if task.Errors.Json() != nil {
		data["errors"] = *task.Errors.Json()
	}
	satellite.SendNotificationAsync(ctx, task.UserID, title, body, &priority, data, nil)

	logger.Info(ctx, "Scheduled task status updated",
		logger.Int("task_id", int(task.ID)),
		logger.String("status", task.Status),
		logger.Int("success_count", int(task.SuccessCount)),
		logger.Int("failed_count", int(task.FailedCount)),
	)
	return nil
}
