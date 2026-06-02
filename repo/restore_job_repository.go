package repo

import (
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormdb "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Restore job status values.
const (
	RestoreJobStatusQueued           = "queued"
	RestoreJobStatusRunning          = "running"
	RestoreJobStatusCompleted        = "completed"
	RestoreJobStatusPartialCompleted = "partial_completed"
	RestoreJobStatusFailed           = "failed"
	RestoreJobStatusCancelled        = "cancelled"
)

const RestoreMaxRetryCount = 3

// RestoreJobListingDB tracks a full restore-all operation for one service + account.
type RestoreJobListingDB struct {
	gorm.GormModel

	UserID  string `json:"user_id" gorm:"not null;index:idx_restore_jobs_user_service_login,priority:1"`
	LoginID string `json:"login_id" gorm:"not null;index:idx_restore_jobs_user_service_login,priority:3"`
	Method  string `json:"method" gorm:"not null;index:idx_restore_jobs_user_service_login,priority:2"`
	Service string `json:"service" gorm:"not null"`

	Status string `json:"status" gorm:"not null;default:queued;index"`

	CursorID       uint `json:"cursor_id" gorm:"default:0"`
	TotalCount     uint `json:"total_count" gorm:"default:0"`
	ProcessedCount uint `json:"processed_count" gorm:"default:0"`
	FailedCount    uint `json:"failed_count" gorm:"default:0"`

	TasksTotal     uint `json:"tasks_total" gorm:"default:0"`
	TasksCompleted uint `json:"tasks_completed" gorm:"default:0"`
	TasksFailed    uint `json:"tasks_failed" gorm:"default:0"`

	StorxToken string `json:"storx_token" gorm:"type:text"`

	InputData *database.DbJson[map[string]interface{}] `json:"input_data" gorm:"type:jsonb"`

	Message       string `json:"message"`
	MessageStatus string `json:"message_status"`

	CancelledAt   *time.Time `json:"cancelled_at,omitempty"`
	LastHeartBeat *time.Time `json:"last_heart_beat,omitempty"`
}

// LiveRestoreJobListingDB is the live-view payload for in-progress restore jobs (UI polling).
type LiveRestoreJobListingDB struct {
	ID              uint                       `json:"id"`
	Service         string                     `json:"service"`
	Method          string                     `json:"method"`
	LoginID         string                     `json:"login_id"`
	Status          string                     `json:"status"`
	Message         string                     `json:"message"`
	MessageStatus   string                     `json:"message_status"`
	TotalCount      uint                       `json:"total"`
	ProcessedCount  uint                       `json:"processed"`
	FailedCount     uint                       `json:"failed"`
	CursorID        uint                       `json:"cursor_id"`
	TasksTotal      uint                       `json:"tasks_total"`
	TasksCompleted  uint                       `json:"tasks_completed"`
	TasksFailed     uint                       `json:"tasks_failed"`
	ProgressPercent float64                    `json:"progress_percent"`
	Tasks           []LiveRestoreTaskListingDB `json:"tasks"`
}

// LiveRestoreTaskListingDB is a running or retrying batch task on a restore job.
type LiveRestoreTaskListingDB struct {
	StartTime  *time.Time `json:"start_time"`
	Status     string     `json:"status"`
	BatchIndex uint       `json:"batch_index"`
}

// RestoreProgressPercent returns 0–100 from job counters (matches live endpoint).
func RestoreProgressPercent(total, processed, failed uint) float64 {
	if total == 0 {
		return 0
	}
	done := processed + failed
	if done > total {
		done = total
	}
	return float64(done) / float64(total) * 100
}

// RestoreDeadItemDB stores permanently failed object keys (DLQ).
type RestoreDeadItemDB struct {
	gorm.GormModel

	RestoreJobID  uint   `json:"restore_job_id" gorm:"index"`
	RestoreTaskID uint   `json:"restore_task_id" gorm:"index"`
	ObjectKey     string `json:"object_key" gorm:"not null;type:varchar(1000)"`
	Service       string `json:"service" gorm:"not null"`
	Reason        string `json:"reason" gorm:"type:text"`
	LastErrorCode string `json:"last_error_code"`
	RetryCount    uint   `json:"retry_count" gorm:"default:0"`
}

// RestoreJobRepository handles restore jobs and dead-letter (failed key) rows.
type RestoreJobRepository struct {
	db *gorm.DB
}

func NewRestoreJobRepository(db *gorm.DB) *RestoreJobRepository {
	return &RestoreJobRepository{db: db}
}

func (r *RestoreJobRepository) Create(job *RestoreJobListingDB) error {
	if err := r.db.Create(job).Error; err != nil {
		return fmt.Errorf("create restore job: %w", err)
	}
	return nil
}

func (r *RestoreJobRepository) GetByID(id uint) (*RestoreJobListingDB, error) {
	var job RestoreJobListingDB
	if err := r.db.First(&job, id).Error; err != nil {
		return nil, fmt.Errorf("get restore job: %w", err)
	}
	return &job, nil
}

func (r *RestoreJobRepository) GetByIDForUser(userID string, id uint) (*RestoreJobListingDB, error) {
	var job RestoreJobListingDB
	if err := r.db.Where("user_id = ? AND id = ?", userID, id).First(&job).Error; err != nil {
		return nil, fmt.Errorf("get restore job for user: %w", err)
	}
	return &job, nil
}

func activeRestoreJobStatuses() []string {
	return []string{RestoreJobStatusQueued, RestoreJobStatusRunning}
}

func (r *RestoreJobRepository) HasActiveJob(userID, method, loginID string) (bool, error) {
	var count int64
	err := r.db.Model(&RestoreJobListingDB{}).
		Where("user_id = ? AND method = ? AND login_id = ? AND status IN ?",
			userID, method, loginID, activeRestoreJobStatuses()).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check active restore job: %w", err)
	}
	return count > 0, nil
}

// ClaimNextQueuedJob claims one queued job (SKIP LOCKED) and sets it to running.
func (r *RestoreJobRepository) ClaimNextQueuedJob() (*RestoreJobListingDB, error) {
	var job RestoreJobListingDB
	tx := r.db.Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ?", RestoreJobStatusQueued).
		Order("id ASC").
		First(&job).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	now := time.Now()
	if err := tx.Model(&job).Updates(map[string]interface{}{
		"status":          RestoreJobStatusRunning,
		"last_heart_beat": now,
	}).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// AdvanceRestoreJobCursor atomically advances cursor and batch counters (CAS).
func (r *RestoreJobRepository) AdvanceRestoreJobCursor(jobID, expectedCursor, newCursor uint, batchOK, batchFail uint) (bool, error) {
	res := r.db.Model(&RestoreJobListingDB{}).
		Where("id = ? AND cursor_id = ?", jobID, expectedCursor).
		Updates(map[string]interface{}{
			"cursor_id":       newCursor,
			"processed_count": gormdb.Expr("processed_count + ?", batchOK),
			"failed_count":    gormdb.Expr("failed_count + ?", batchFail),
		})
	if res.Error != nil {
		return false, fmt.Errorf("advance restore cursor: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *RestoreJobRepository) UpdateJob(id uint, updates map[string]interface{}) error {
	if err := r.db.Model(&RestoreJobListingDB{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("update restore job: %w", err)
	}
	return nil
}

func (r *RestoreJobRepository) UpdateHeartBeat(id uint) error {
	now := time.Now()
	return r.db.Model(&RestoreJobListingDB{}).Where("id = ?", id).Update("last_heart_beat", now).Error
}

func (r *RestoreJobRepository) IncrementTasksTotal(jobID uint, delta uint) error {
	return r.db.Model(&RestoreJobListingDB{}).Where("id = ?", jobID).
		Update("tasks_total", gormdb.Expr("tasks_total + ?", delta)).Error
}

func (r *RestoreJobRepository) IncrementTasksCompleted(jobID uint) error {
	return r.db.Model(&RestoreJobListingDB{}).Where("id = ?", jobID).
		Update("tasks_completed", gormdb.Expr("tasks_completed + 1")).Error
}

func (r *RestoreJobRepository) IncrementTasksFailed(jobID uint) error {
	return r.db.Model(&RestoreJobListingDB{}).Where("id = ?", jobID).
		Update("tasks_failed", gormdb.Expr("tasks_failed + 1")).Error
}

func (r *RestoreJobRepository) CountPendingRetryTasks(jobID uint) (int64, error) {
	var count int64
	now := time.Now()
	err := r.db.Model(&RestoreTaskListingDB{}).
		Where("restore_job_id = ? AND (status = ? OR (status = ? AND next_attempt_at > ?))",
			jobID, RestoreTaskStatusRunning, RestoreTaskStatusRetrying, now).
		Count(&count).Error
	return count, err
}

// GetAllActiveRestoreJobsForUser returns running restore jobs (live UI — same idea as autosync /live).
// Queued jobs are omitted until the worker claims them as running.
func (r *RestoreJobRepository) GetAllActiveRestoreJobsForUser(userID string) ([]LiveRestoreJobListingDB, error) {
	query := `
		SELECT
			rj.id, rj.service, rj.method, rj.login_id, rj.status, rj.message, rj.message_status,
			rj.total_count, rj.processed_count, rj.failed_count, rj.cursor_id,
			rj.tasks_total, rj.tasks_completed, rj.tasks_failed,
			rt.start_time, rt.status, rt.batch_index
		FROM restore_job_listing_dbs rj
		LEFT JOIN restore_task_listing_dbs rt ON rj.id = rt.restore_job_id
			AND rt.deleted_at IS NULL
			AND rt.status IN ('running','retrying')
		WHERE rj.user_id = $1
		AND rj.deleted_at IS NULL
		AND rj.status = 'running'
		ORDER BY rj.id DESC, rt.start_time DESC`

	rows, err := r.db.Raw(query, userID).Rows()
	if err != nil {
		return nil, fmt.Errorf("active restore jobs query: %w", err)
	}
	defer rows.Close()

	jobsMap := make(map[uint]*LiveRestoreJobListingDB)
	var order []uint

	for rows.Next() {
		var (
			jobID          uint
			service        string
			method         string
			loginID        string
			status         string
			message        string
			messageStatus  string
			totalCount     uint
			processedCount uint
			failedCount    uint
			cursorID       uint
			tasksTotal     uint
			tasksCompleted uint
			tasksFailed    uint
			startTime      *time.Time
			taskStatus     *string
			batchIndex     *uint
		)
		if err := rows.Scan(
			&jobID, &service, &method, &loginID, &status, &message, &messageStatus,
			&totalCount, &processedCount, &failedCount, &cursorID,
			&tasksTotal, &tasksCompleted, &tasksFailed,
			&startTime, &taskStatus, &batchIndex,
		); err != nil {
			return nil, fmt.Errorf("scan active restore job: %w", err)
		}

		if _, exists := jobsMap[jobID]; !exists {
			jobsMap[jobID] = &LiveRestoreJobListingDB{
				ID:              jobID,
				Service:         service,
				Method:          method,
				LoginID:         loginID,
				Status:          status,
				Message:         message,
				MessageStatus:   messageStatus,
				TotalCount:      totalCount,
				ProcessedCount:  processedCount,
				FailedCount:     failedCount,
				CursorID:        cursorID,
				TasksTotal:      tasksTotal,
				TasksCompleted:  tasksCompleted,
				TasksFailed:     tasksFailed,
				ProgressPercent: RestoreProgressPercent(totalCount, processedCount, failedCount),
				Tasks:           []LiveRestoreTaskListingDB{},
			}
			order = append(order, jobID)
		}

		if taskStatus != nil && startTime != nil {
			idx := uint(0)
			if batchIndex != nil {
				idx = *batchIndex
			}
			jobsMap[jobID].Tasks = append(jobsMap[jobID].Tasks, LiveRestoreTaskListingDB{
				StartTime:  startTime,
				Status:     *taskStatus,
				BatchIndex: idx,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active restore jobs: %w", err)
	}

	results := make([]LiveRestoreJobListingDB, 0, len(order))
	for _, id := range order {
		results = append(results, *jobsMap[id])
	}
	return results, nil
}

func (r *RestoreJobRepository) ListByUser(userID string, limit int) ([]RestoreJobListingDB, error) {
	if limit <= 0 {
		limit = 20
	}
	var jobs []RestoreJobListingDB
	err := r.db.Where("user_id = ?", userID).Order("id DESC").Limit(limit).Find(&jobs).Error
	return jobs, err
}

func (r *RestoreJobRepository) MissedHeartbeatForJobs(staleBefore time.Time) error {
	return r.db.Model(&RestoreJobListingDB{}).
		Where("status = ? AND (last_heart_beat IS NULL OR last_heart_beat < ?)", RestoreJobStatusRunning, staleBefore).
		Updates(map[string]interface{}{
			"status":  RestoreJobStatusFailed,
			"message": "restore job missed heartbeat",
		}).Error
}

func (r *RestoreJobRepository) CreateDeadItem(item *RestoreDeadItemDB) error {
	if err := r.db.Create(item).Error; err != nil {
		return fmt.Errorf("create restore dead item: %w", err)
	}
	return nil
}

func (r *RestoreJobRepository) ListDeadItemsByJobID(jobID uint, limit int) ([]RestoreDeadItemDB, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []RestoreDeadItemDB
	err := r.db.Where("restore_job_id = ?", jobID).Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
