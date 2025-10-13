package repo

import (
	"time"

	"gorm.io/gorm"
)

type ScheduledTasks struct {
	gorm.Model
	UserID       string                 `json:"user_id"`
	LoginId      string                 `json:"login_id"`
	Method       string                 `json:"method"`
	StorxToken   string                 `json:"storx_token"`
	Memory       map[string]interface{} `json:"memory" gorm:"type:jsonb"`
	Status       string                 `json:"status" default:"created"`
	InputData    map[string]interface{} `json:"input_data" gorm:"type:jsonb"`
	StartTime    *time.Time             `json:"start_time"`
	Execution    uint64                 `json:"execution"`
	SuccessCount uint                   `json:"success_count"`
	FailedCount  uint                   `json:"failed_count"`
	Errors       []string               `json:"errors" gorm:"type:jsonb"`
	HeartBeat    *time.Time             `json:"heart_beat"`
	DeletedAt    *time.Time             `json:"deleted_at"`
}

// Create creates a new scheduled task in the database
func (st *ScheduledTasks) Create(db *gorm.DB) error {
	return db.Model(st).Create(st).Error
}

// DeleteByID deletes a scheduled task by its ID
func (st *ScheduledTasks) DeleteByID(db *gorm.DB, id uint) error {
	return db.Delete(&ScheduledTasks{}, id).Error
}

// DeleteByUserID deletes all scheduled tasks for a specific user ID
func (st *ScheduledTasks) DeleteByUserID(db *gorm.DB, userID string) error {
	return db.Where("user_id = ?", userID).Delete(&ScheduledTasks{}).Error
}

// GetByLoginID retrieves all scheduled tasks for a specific login ID
func (st *ScheduledTasks) GetByLoginID(db *gorm.DB, loginID string) ([]ScheduledTasks, error) {
	var tasks []ScheduledTasks
	err := db.Where("login_id = ?", loginID).Find(&tasks).Error
	return tasks, err
}

// GetByUserID retrieves all scheduled tasks for a specific user ID
func (st *ScheduledTasks) GetByUserID(db *gorm.DB, userID string) ([]ScheduledTasks, error) {
	var tasks []ScheduledTasks
	err := db.Where("user_id = ?", userID).Find(&tasks).Error
	return tasks, err
}
