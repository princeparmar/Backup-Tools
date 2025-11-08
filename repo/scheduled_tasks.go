package repo

import (
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

type ScheduledTasks struct {
	gorm.GormModel
	UserID       string                                   `json:"user_id"`
	LoginId      string                                   `json:"login_id"`
	Method       string                                   `json:"method"`
	StorxToken   string                                   `json:"storx_token"`
	Memory       *database.DbJson[map[string][]string]    `json:"memory" gorm:"type:jsonb"`
	Status       string                                   `json:"status" default:"created"`
	InputData    *database.DbJson[map[string]interface{}] `json:"input_data" gorm:"type:jsonb"`
	StartTime    *time.Time                               `json:"start_time"`
	Execution    uint64                                   `json:"execution"`
	SuccessCount uint                                     `json:"success_count"`
	FailedCount  uint                                     `json:"failed_count"`
	Errors       database.DbJson[[]string]                `json:"errors" gorm:"type:jsonb"`
	HeartBeat    *time.Time                               `json:"heart_beat"`
}

type LiveScheduledTasks struct {
	Memory       *database.DbJson[map[string][]string] `json:"memory" gorm:"type:jsonb"`
	Status       string                                `json:"status" default:"created"`
	StartTime    *time.Time                            `json:"start_time"`
	Execution    uint64                                `json:"execution"`
	SuccessCount uint                                  `json:"success_count"`
	FailedCount  uint                                  `json:"failed_count"`
	Errors       database.DbJson[[]string]             `json:"errors" gorm:"type:jsonb"`
	HeartBeat    *time.Time                            `json:"heart_beat"`
}
type ScheduledTasksFilter struct {
	UserID    string     `json:"user_id"`
	LoginID   string     `json:"login_id"`
	Method    string     `json:"method"`
	Status    string     `json:"status"`
	StartTime *time.Time `json:"start_time"`
	Order     string     `json:"order" default:"desc"`
}

// Create creates a new scheduled task in the database
func (st *ScheduledTasks) Create(db *gorm.DB) error {
	return db.Model(st).Create(st).Error
}

// GetTasksForCurrentUser retrieves all scheduled tasks for a specific user ID with filtering
func (st *ScheduledTasks) GetTasksForCurrentUser(db *gorm.DB, filter ScheduledTasksFilter) ([]ScheduledTasks, error) {
	var tasks []ScheduledTasks

	// Start with base query for user_id
	query := db.Where("user_id = ?", filter.UserID)

	// Add additional filters if provided
	if filter.LoginID != "" {
		query = query.Where("login_id = ?", filter.LoginID)
	}

	if filter.Method != "" {
		query = query.Where("method = ?", filter.Method)
	}

	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}

	if filter.StartTime != nil {
		query = query.Where("start_time >= ?", filter.StartTime)
	}

	// Apply ordering
	orderClause := "created_at " + filter.Order
	if filter.Order == "" {
		orderClause = "created_at desc" // default order
	}

	err := query.Order(orderClause).Find(&tasks).Error
	return tasks, err
}

// ScheduledTasksRepository handles all database operations for scheduled tasks
type ScheduledTasksRepository struct {
	db *gorm.DB
}

// NewScheduledTasksRepository creates a new scheduled tasks repository
func NewScheduledTasksRepository(db *gorm.DB) *ScheduledTasksRepository {
	return &ScheduledTasksRepository{db: db}
}

// GetNextScheduledTask gets the next scheduled task to process
func (r *ScheduledTasksRepository) GetNextScheduledTask() (*ScheduledTasks, error) {
	var task ScheduledTasks
	err := r.db.Where("status = ?", "created").First(&task).Error
	return &task, err
}

// GetScheduledTaskByID gets a scheduled task by ID
func (r *ScheduledTasksRepository) GetScheduledTaskByID(id uint) (*ScheduledTasks, error) {
	var task ScheduledTasks
	err := r.db.First(&task, id).Error
	return &task, err
}

// UpdateHeartBeatForScheduledTask updates the heartbeat for a scheduled task
func (r *ScheduledTasksRepository) UpdateHeartBeatForScheduledTask(id uint) error {
	now := time.Now()
	err := r.db.Model(&ScheduledTasks{}).Where("id = ?", id).Update("heart_beat", &now).Error
	return err
}

// GetAllRunningScheduledTasksForUser retrieves all running scheduled tasks for a specific user
func (r *ScheduledTasksRepository) GetAllRunningScheduledTasksForUser(userID string) ([]LiveScheduledTasks, error) {
	var tasks []LiveScheduledTasks
	query := `
		SELECT memory,status,start_time,execution,success_count,failed_count,errors,heart_beat
		FROM scheduled_tasks
		WHERE user_id = ? AND status = 'running'
		ORDER BY start_time DESC`
	err := r.db.Raw(query, userID).Scan(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("error getting running scheduled tasks for user: %v", err)
	}
	return tasks, nil
}

// MissedHeartbeatForScheduledTask checks for scheduled tasks with missed heartbeats
func (r *ScheduledTasksRepository) MissedHeartbeatForScheduledTask() error {
	// Find scheduled tasks that are running but haven't updated heartbeat in 10 minutes
	var tasks []ScheduledTasks
	err := r.db.Where("status = ? AND (heart_beat < ? OR heart_beat IS NULL)",
		"running", time.Now().Add(-10*time.Minute)).Find(&tasks).Error
	if err != nil {
		return fmt.Errorf("error getting scheduled tasks with missed heartbeat: %v", err)
	}

	for _, task := range tasks {
		// Update task status to failed
		err := r.db.Model(&ScheduledTasks{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"status": "failed",
			"errors": `["Process got stuck because of server restart or crash. Marked as failed"]`,
		}).Error
		if err != nil {
			return fmt.Errorf("error updating scheduled task %d: %v", task.ID, err)
		}
	}

	return nil
}
