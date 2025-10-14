package repo

import (
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"gorm.io/gorm"
)

type ScheduledTasks struct {
	gorm.Model
	UserID       string                                   `json:"user_id"`
	LoginId      string                                   `json:"login_id"`
	Method       string                                   `json:"method"`
	StorxToken   string                                   `json:"storx_token"`
	Memory       *database.DbJson[map[string]string]      `json:"memory" gorm:"type:jsonb"`
	Status       string                                   `json:"status" default:"created"`
	InputData    *database.DbJson[map[string]interface{}] `json:"input_data" gorm:"type:jsonb"`
	StartTime    *time.Time                               `json:"start_time"`
	Execution    uint64                                   `json:"execution"`
	SuccessCount uint                                     `json:"success_count"`
	FailedCount  uint                                     `json:"failed_count"`
	Errors       database.DbJson[[]string]                `json:"errors" gorm:"type:jsonb"`
	HeartBeat    *time.Time                               `json:"heart_beat"`
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
