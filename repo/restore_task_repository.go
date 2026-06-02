package repo

import (
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"gorm.io/gorm/clause"
)

// Restore task status values.
const (
	RestoreTaskStatusRunning   = "running"
	RestoreTaskStatusSuccess   = "success"
	RestoreTaskStatusFailed    = "failed"
	RestoreTaskStatusRetrying  = "retrying"
	RestoreTaskStatusCancelled = "cancelled"
)

// RestoreTaskListingDB is a batch audit row + retry metadata (not the execution driver).
type RestoreTaskListingDB struct {
	gorm.GormModel

	RestoreJobID uint `json:"restore_job_id" gorm:"not null;index"`

	Status     string `json:"status" gorm:"not null;index:idx_restore_tasks_status_retry,priority:1"`
	BatchIndex uint   `json:"batch_index" gorm:"default:0"`

	CursorStartID uint `json:"cursor_start_id" gorm:"default:0"`
	CursorEndID   uint `json:"cursor_end_id" gorm:"default:0"`

	ItemCount      uint `json:"item_count" gorm:"default:0"`
	ProcessedCount uint `json:"processed_count" gorm:"default:0"`
	FailedCount    uint `json:"failed_count" gorm:"default:0"`

	RetryCount    uint       `json:"retry_count" gorm:"default:0"`
	RetryAfter    *time.Time `json:"retry_after,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty" gorm:"index:idx_restore_tasks_status_retry,priority:2"`
	LastErrorCode string     `json:"last_error_code,omitempty"`

	Message       string     `json:"message"`
	StartTime     *time.Time `json:"start_time,omitempty"`
	Execution     uint64     `json:"execution" gorm:"default:0"`
	LastHeartBeat *time.Time `json:"last_heart_beat,omitempty"`
}

// RestoreTaskRepository handles batch audit / retry rows for restore jobs.
type RestoreTaskRepository struct {
	db *gorm.DB
}

func NewRestoreTaskRepository(db *gorm.DB) *RestoreTaskRepository {
	return &RestoreTaskRepository{db: db}
}

func (r *RestoreTaskRepository) Create(task *RestoreTaskListingDB) error {
	if err := r.db.Create(task).Error; err != nil {
		return fmt.Errorf("create restore task: %w", err)
	}
	return nil
}

func (r *RestoreTaskRepository) Update(id uint, updates map[string]interface{}) error {
	return r.db.Model(&RestoreTaskListingDB{}).Where("id = ?", id).Updates(updates).Error
}

// ClaimNextRetryTask claims a retrying task that is due.
func (r *RestoreTaskRepository) ClaimNextRetryTask() (*RestoreTaskListingDB, error) {
	var task RestoreTaskListingDB
	now := time.Now()
	tx := r.db.Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ? AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?", RestoreTaskStatusRetrying, now).
		Order("next_attempt_at ASC").
		First(&task).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	start := time.Now()
	if err := tx.Model(&task).Updates(map[string]interface{}{
		"status":          RestoreTaskStatusRunning,
		"start_time":      start,
		"last_heart_beat": start,
	}).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *RestoreTaskRepository) CancelPendingForJob(jobID uint) error {
	return r.db.Model(&RestoreTaskListingDB{}).
		Where("restore_job_id = ? AND status IN ?", jobID, []string{RestoreTaskStatusRunning, RestoreTaskStatusRetrying}).
		Updates(map[string]interface{}{
			"status":  RestoreTaskStatusCancelled,
			"message": "cancelled with job",
		}).Error
}

func (r *RestoreTaskRepository) MissedHeartbeatForTasks(staleBefore time.Time) error {
	var tasks []RestoreTaskListingDB
	if err := r.db.Where("status = ? AND (last_heart_beat IS NULL OR last_heart_beat < ?)",
		RestoreTaskStatusRunning, staleBefore).Find(&tasks).Error; err != nil {
		return err
	}
	for _, t := range tasks {
		_ = r.db.Model(&RestoreTaskListingDB{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
			"status":  RestoreTaskStatusFailed,
			"message": "restore task missed heartbeat",
		})
	}
	return nil
}
