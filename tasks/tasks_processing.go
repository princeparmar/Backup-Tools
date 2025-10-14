package crons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type ScheduledTaskProcessorInput struct {
	InputData     map[string]interface{}
	Memory        map[string]string
	Task          *repo.ScheduledTasks
	HeartBeatFunc func() error
}

type ScheduledTaskProcessor interface {
	Run(ScheduledTaskProcessorInput) error
}

var scheduledTaskProcessorMap = map[string]ScheduledTaskProcessor{
	"gmail":   NewScheduledGmailProcessor(),
	"outlook": NewScheduledOutlookProcessor(),
}

type ScheduledTaskManager struct {
	store *storage.PosgresStore
}

func NewScheduledTaskManager(store *storage.PosgresStore) *ScheduledTaskManager {
	return &ScheduledTaskManager{store: store}
}

// createScheduledTaskContext creates a context with trace ID for scheduled task processing
func createScheduledTaskContext(operation string) context.Context {
	traceID := uuid.New().String()
	ctx := logger.WithTraceID(context.Background(), traceID)
	logger.Info(ctx, "Scheduled task processing started", logger.String("operation", operation))
	return ctx
}

func (s *ScheduledTaskManager) Start() {
	c := cron.New()

	// Process scheduled tasks
	c.AddFunc("@every 30s", func() {
		ctx := createScheduledTaskContext("process_scheduled_tasks")
		logger.Info(ctx, "Processing scheduled tasks")
		err := s.ProcessScheduledTasks(ctx)
		if err != nil {
			logger.Error(ctx, "Failed to process scheduled tasks", logger.ErrorField(err))
		} else {
			logger.Info(ctx, "Successfully processed scheduled tasks")
		}
	})

	c.Start()
	logger.Info(context.Background(), "Scheduled task processor started successfully")
}

func (s *ScheduledTaskManager) ProcessScheduledTasks(ctx context.Context) error {
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	processedCount := 0
	errorCount := 0

	for {
		task, err := s.store.GetNextScheduledTask()
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
		if err := s.store.DB.Save(task).Error; err != nil {
			logger.Error(ctx, "Failed to update task status to running",
				logger.Int("task_id", int(task.ID)),
				logger.ErrorField(err),
			)
			continue
		}

		logger.Info(ctx, "Task status updated to running",
			logger.Int("task_id", int(task.ID)),
			logger.String("status", task.Status))

		// Process the scheduled task
		processErr := s.processScheduledTask(ctx, task)

		// Update task status
		if updateErr := s.UpdateScheduledTaskStatus(task, processErr); updateErr != nil {
			logger.Error(ctx, "Failed to update scheduled task status",
				logger.Int("task_id", int(task.ID)),
				logger.ErrorField(updateErr),
			)
			// Continue with next task even if status update fails
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

	processor, ok := scheduledTaskProcessorMap[task.Method]
	if !ok {
		return fmt.Errorf("processor for method '%s' not found", task.Method)
	}

	logger.Info(ctx, "Executing processor for scheduled task",
		logger.Int("task_id", int(task.ID)),
		logger.String("method", task.Method),
	)

	// Get input data and memory from the task
	var inputData map[string]interface{}
	var memory map[string]string

	if task.InputData != nil {
		inputData = *task.InputData.Json()
	}
	if task.Memory != nil {
		memory = *task.Memory.Json()
	}

	err = processor.Run(ScheduledTaskProcessorInput{
		InputData: inputData,
		Memory:    memory,
		Task:      task,
		HeartBeatFunc: func() error {
			// Check if task is still running
			currentTask, err := s.store.GetScheduledTaskByID(task.ID)
			if err != nil {
				return fmt.Errorf("failed to get task status: %w", err)
			}

			if currentTask.Status != "running" {
				return fmt.Errorf("task status changed to '%s', stopping execution", currentTask.Status)
			}

			// Update heartbeat
			if err := s.store.UpdateHeartBeatForScheduledTask(task.ID); err != nil {
				return fmt.Errorf("failed to update heartbeat: %w", err)
			}

			return nil
		},
	})

	// Update memory in the task after processing
	if memory != nil {
		task.Memory = database.NewDbJsonFromValue(memory)
	}

	return err
}

func (s *ScheduledTaskManager) UpdateScheduledTaskStatus(task *repo.ScheduledTasks, processErr error) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	// Set execution time
	if task.StartTime != nil {
		task.Execution = uint64(time.Since(*task.StartTime).Seconds())
	}

	// Check memory status for different completion states
	var hasError, hasSuccess bool
	var errorCount, successCount int

	if task.Memory != nil {
		memory := *task.Memory.Json()
		for _, status := range memory {
			if strings.HasPrefix(status, "error:") {
				hasError = true
				errorCount++
			} else if status == "synced" {
				hasSuccess = true
				successCount++
			}
		}
	}

	// Determine task status based on results
	if processErr != nil {
		// If there's a processor error, mark as failed
		task.Status = "failed"
	} else if hasError && hasSuccess {
		// Some succeeded, some failed - partially completed
		task.Status = "partially_completed"
	} else if hasError && !hasSuccess {
		// All failed - failed
		task.Status = "failed"
	} else if hasSuccess && !hasError {
		// All succeeded - completed
		task.Status = "completed"
	} else {
		// No processing occurred - keep as created/running
		task.Status = "completed"
	}

	// Update errors if there are any
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
	} else {
		// Ensure Errors field is properly initialized even when no errors
		if task.Errors.Json() == nil {
			task.Errors = *database.NewDbJsonFromValue([]string{})
		}
	}

	// Save task to database
	if err := s.store.DB.Save(task).Error; err != nil {
		logger.Error(ctx, "Failed to save scheduled task status",
			logger.Int("task_id", int(task.ID)),
			logger.ErrorField(err),
		)
		return fmt.Errorf("failed to save scheduled task: %w", err)
	}

	logger.Info(ctx, "Scheduled task status updated",
		logger.Int("task_id", int(task.ID)),
		logger.String("status", task.Status),
		logger.Int("success_count", int(task.SuccessCount)),
		logger.Int("failed_count", int(task.FailedCount)),
	)

	return nil
}
