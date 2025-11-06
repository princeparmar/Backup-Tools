package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	gormdb "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TaskListingDB represents a task in the database
type TaskListingDB struct {
	gorm.GormModel

	CronJobID uint `gorm:"constraint:OnDelete:CASCADE;" json:"cron_job_id"` // Add delete cascade here

	// Status will be one of the following: "pushed", "running", "success", "failed"
	Status string `json:"status"`

	// Message will be the message to be displayed to the user
	Message string `json:"message"`

	// StartTime will be the time when the task was started.
	StartTime *time.Time `json:"start_time"`

	// Execution time in milliseconds
	Execution uint64 `json:"execution"`

	// RetryCount will be the number of times the task has been retried
	// Maximum 3 retries are allowed
	RetryCount uint `json:"retry_count"`

	// LastHeartBeat will be the time when the task was last heartbeat
	LastHeartBeat *time.Time `json:"last_heart_beat"`
}

// TaskRepository handles all database operations for tasks
type TaskRepository struct {
	db *gorm.DB
}

// NewTaskRepository creates a new task repository
func NewTaskRepository(db *gorm.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

// UpdateHeartBeatForTask updates the heartbeat for a specific task
func (r *TaskRepository) UpdateHeartBeatForTask(ID uint) error {
	db := r.db.Model(&TaskListingDB{}).Where("id = ?", ID).Update("last_heart_beat", time.Now())
	if db != nil && db.Error != nil {
		return fmt.Errorf("error updating heartbeat for task: %v", db.Error)
	}

	return nil
}

// MissedHeartbeatForTask updates the heartbeat for tasks that have missed their heartbeat
func (r *TaskRepository) MissedHeartbeatForTask() error {
	// start a transaction, select all tasks with lock where last_heart_beat is more than 1 minute ago
	// update status to failed and message to "missed heartbeat"
	// and for job set message to process got stuck because of some reason
	// and message status to error
	tx := r.db.Begin()
	logger.Info(context.Background(), "Starting transaction for missed heartbeat check")

	var tasks []TaskListingDB
	db := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("status = ? AND (last_heart_beat < ? OR last_heart_beat is null)", "running", time.Now().Add(-10*time.Minute).Format("2006-01-02 15:04:05-0700")).Find(&tasks)
	if db.Error != nil {
		tx.Rollback()
		return fmt.Errorf("error getting tasks with missed heartbeat: %v", db.Error)
	}

	for _, task := range tasks {
		logger.Info(context.Background(), "Updating task", logger.Int("task_id", int(task.ID)), logger.String("with missed heartbeat", "with missed heartbeat"))

		task.Status = TaskStatusFailed
		task.Message = "Process got stuck because of some reason. Marked as failed"

		task.Execution = uint64(time.Since(*task.StartTime).Seconds())
		task.RetryCount++

		db = tx.Save(&task)
		if db != nil && db.Error != nil {
			tx.Rollback()
			return fmt.Errorf("error updating task: %v", db.Error)
		}

		var job CronJobListingDB
		db = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", task.CronJobID).First(&job)
		if db.Error != nil {
			tx.Rollback()
			return fmt.Errorf("error getting job: %v", db.Error)
		}

		job.Message = "Process got stuck because of some reason. Marked as failed"
		job.MessageStatus = JobMessageStatusError
		job.Status = JobStatusFailed

		db = tx.Save(&job)
		if db != nil && db.Error != nil {
			tx.Rollback()
			return fmt.Errorf("error updating job: %v", db.Error)
		}
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return nil
}

// GetPushedTask retrieves a pushed task and updates its status to running
func (r *TaskRepository) GetPushedTask() (*TaskListingDB, error) {
	var res TaskListingDB
	tx := r.db.Begin()
	// lock table tasks for update and select and return the first row with status pushed
	// or status 'failed' and retry count less than 3
	db := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Joins("JOIN cron_job_listing_dbs ON task_listing_dbs.cron_job_id = cron_job_listing_dbs.id").
		Where("cron_job_listing_dbs.active = ? AND (task_listing_dbs.status = ? OR (task_listing_dbs.status = ? AND task_listing_dbs.retry_count < ?))",
			true, TaskStatusPushed, TaskStatusFailed, MaxRetryCount).
		First(&res)
	if db.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting pushed task: %v", db.Error)
	}

	// Update status to running and set start time
	res.Status = TaskStatusRunning
	startTime := time.Now()
	res.StartTime = &startTime
	res.LastHeartBeat = &startTime
	res.Message = "Automatic backup started"

	if err := tx.Save(&res).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error updating pushed task status: %v", err)
	}

	var job CronJobListingDB
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", res.CronJobID).First(&job).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting job: %v", err)
	}

	job.Message = "Automatic backup started"
	job.MessageStatus = JobMessageStatusInfo
	job.Status = JobStatusInProgress

	if err := tx.Save(&job).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error updating pushed task status: %v", err)
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return nil, fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return &res, nil
}

// GetTaskByID retrieves a task by its ID
func (r *TaskRepository) GetTaskByID(ID uint) (*TaskListingDB, error) {
	var res TaskListingDB
	db := r.db.First(&res, ID)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting task by ID: %v", db.Error)
	}

	return &res, nil
}

// CreateTaskForCronJob creates a new task for a cron job
func (r *TaskRepository) CreateTaskForCronJob(cronJobID uint) (*TaskListingDB, error) {
	tx := r.db.Begin()

	data := TaskListingDB{
		CronJobID: cronJobID,
		Status:    TaskStatusPushed,
	}

	// create new entry in database and return newly created task
	res := tx.Create(&data)
	if res != nil && res.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error creating task for cron job: %v", res.Error)
	}

	// Update cron job status to In_Queue when task is created with pushed status
	if err := tx.Model(&CronJobListingDB{}).Where("id = ?", cronJobID).Update("status", JobStatusInQueue).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error updating cron job status: %v", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("error committing transaction: %v", err)
	}

	return &data, nil
}

// UpdateTaskByID updates a task by its ID
func (r *TaskRepository) UpdateTaskByID(ID uint, m map[string]interface{}) error {
	// update task by ID
	// create a transaction to update the task and check if m["status"] == "failed"
	// then update "retry_count" and "status"
	tx := r.db.Begin()
	db := tx.Model(&TaskListingDB{}).Where("id = ?", ID).Updates(m)
	if db != nil && db.Error != nil {
		tx.Rollback()
		return fmt.Errorf("error updating task by ID: %v", db.Error)
	}

	if m["status"] == TaskStatusFailed {
		if err := tx.Model(&TaskListingDB{}).Where("id = ?", ID).Update("retry_count", gormdb.Expr("retry_count + 1")).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("error updating task by ID: %v", err)
		}
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return nil
}

// ListAllTasksByJobID retrieves all tasks for a specific job with pagination
func (r *TaskRepository) ListAllTasksByJobID(ID, limit, offset uint) ([]TaskListingDB, error) {
	var res []TaskListingDB
	db := r.db.Where("cron_job_id = ?", ID).Limit(int(limit)).Offset(int(offset)).Order("created_at DESC").Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting tasks for job: %v", db.Error)
	}

	return res, nil
}
