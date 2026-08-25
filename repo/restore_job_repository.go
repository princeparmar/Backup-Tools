package repo

import (
	"fmt"
	"strings"
	"time"

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

// IsRestoreJobTerminal reports whether a restore job has finished and must not be updated.
func IsRestoreJobTerminal(status string) bool {
	switch status {
	case RestoreJobStatusCompleted, RestoreJobStatusPartialCompleted, RestoreJobStatusFailed, RestoreJobStatusCancelled:
		return true
	default:
		return false
	}
}

const RestoreMaxRetryCount = 3

// RestoreJobListingDB tracks a full restore-all operation for one service + account.
type RestoreJobListingDB struct {
	gorm.GormModel

	UserID         string `json:"user_id" gorm:"not null;index:idx_restore_jobs_user_service_login,priority:1"`
	StorjProjectID string `json:"storj_project_id" gorm:"column:storj_project_id;index:idx_restore_jobs_project"`
	LoginID        string `json:"login_id" gorm:"not null;index:idx_restore_jobs_user_service_login,priority:3"`
	TargetEmail    string `json:"target_email,omitempty" gorm:"column:target_email;type:varchar(255)"`
	Method         string `json:"method" gorm:"not null;index:idx_restore_jobs_user_service_login,priority:2"`
	AccountType    string `json:"account_type" gorm:"column:account_type;not null;default:personal"`
	CredentialID   uint   `json:"credential_id" gorm:"column:credential_id;index"`
	CronJobID      uint   `json:"cron_job_id" gorm:"column:cron_job_id;index"`

	Status string `json:"status" gorm:"not null;default:queued;index"`

	CursorID       uint `json:"cursor_id" gorm:"default:0"`
	TotalCount     uint `json:"total_count" gorm:"default:0"`
	ProcessedCount uint `json:"processed_count" gorm:"default:0"`
	FailedCount    uint `json:"failed_count" gorm:"default:0"`

	Message       string `json:"message" gorm:"type:varchar(512)"`
	MessageStatus string `json:"message_status" gorm:"column:message_status;not null;default:info"`

	CancelledAt   *time.Time `json:"cancelled_at,omitempty"`
	LastHeartBeat *time.Time `json:"last_heart_beat,omitempty"`
}

// APIServiceFromMethod maps internal method to UI service name.
func APIServiceFromMethod(method string) string {
	switch method {
	case "google_drive":
		return "drive"
	case "google_photos":
		return "photos"
	case "google_calendar":
		return "calendar"
	case "google_contacts":
		return "contacts"
	case "outlook":
		return "outlook"
	case "outlook_calendar":
		return "outlook_calendar"
	case "outlook_contacts":
		return "outlook_contacts"
	case "outlook_onedrive":
		return "outlook_onedrive"
	case "outlook_sharepoint":
		return "outlook_sharepoint"
	case "outlook_teams":
		return "outlook_teams"
	case "outlook_groups":
		return "outlook_groups"
	default:
		return method
	}
}

// LiveRestoreJobListingDB is the live-view payload for in-progress restore jobs (UI polling).
type LiveRestoreJobListingDB struct {
	ID              uint                       `json:"id"`
	Method          string                     `json:"method"`
	LoginID         string                     `json:"login_id"`
	Status          string                     `json:"status"`
	Message         string                     `json:"message"`
	MessageStatus   string                     `json:"message_status"`
	TotalCount      uint                       `json:"total"`
	ProcessedCount  uint                       `json:"processed"`
	FailedCount     uint                       `json:"failed"`
	CursorID        uint                       `json:"cursor_id"`
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

	RestoreJobID uint   `json:"restore_job_id" gorm:"index"`
	ObjectKey    string `json:"object_key" gorm:"not null;type:varchar(1000)"`
	ErrorCode    string `json:"error_code" gorm:"column:error_code;type:varchar(32)"`
	Reason       string `json:"reason" gorm:"type:varchar(500)"`
}

// RestoreJobRepository handles restore jobs and dead-letter (failed key) rows.
type RestoreJobRepository struct {
	db *gorm.DB
}

func NewRestoreJobRepository(db *gorm.DB) *RestoreJobRepository {
	return &RestoreJobRepository{db: db}
}

// IsRestoreJobMigration reports cross-account restore (target_email set and differs from login_id).
func IsRestoreJobMigration(job *RestoreJobListingDB) bool {
	if job == nil {
		return false
	}
	target := strings.TrimSpace(job.TargetEmail)
	if target == "" {
		return false
	}
	return !strings.EqualFold(target, strings.TrimSpace(job.LoginID))
}

// EffectiveRestoreMessageStatus returns stored message_status or derives it from job status.
func EffectiveRestoreMessageStatus(job *RestoreJobListingDB) string {
	if job != nil && strings.TrimSpace(job.MessageStatus) != "" {
		return strings.TrimSpace(job.MessageStatus)
	}
	if job != nil {
		return RestoreMessageStatusFromJobStatus(job.Status)
	}
	return JobMessageStatusInfo
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
		"message":         "restore in progress",
		"message_status":  JobMessageStatusInfo,
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

const restoreRunningBatchGap = 2 * time.Minute

// ClaimNextRunningJob claims a running job ready for the next batch (heartbeat not updated recently).
func (r *RestoreJobRepository) ClaimNextRunningJob() (*RestoreJobListingDB, error) {
	readyBefore := time.Now().Add(-restoreRunningBatchGap)
	var job RestoreJobListingDB
	tx := r.db.Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ? AND (last_heart_beat IS NULL OR last_heart_beat < ?)", RestoreJobStatusRunning, readyBefore).
		Order("last_heart_beat ASC NULLS FIRST").
		First(&job).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	now := time.Now()
	if err := tx.Model(&job).Update("last_heart_beat", now).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &job, nil
}

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

func (r *RestoreJobRepository) CountPendingRetryTasks(jobID uint) (int64, error) {
	var count int64
	now := time.Now()
	err := r.db.Model(&RestoreTaskListingDB{}).
		Where("restore_job_id = ? AND (status = ? OR (status = ? AND next_attempt_at > ?))",
			jobID, RestoreTaskStatusRunning, RestoreTaskStatusRetrying, now).
		Count(&count).Error
	return count, err
}

func (r *RestoreJobRepository) GetAllActiveRestoreJobsForUser(userID string) ([]LiveRestoreJobListingDB, error) {
	query := `
		SELECT
			rj.id, rj.method, rj.login_id, rj.status, rj.message, rj.message_status,
			rj.total_count, rj.processed_count, rj.failed_count, rj.cursor_id,
			rt.started_at, rt.status, rt.batch_index
		FROM restore_job_listing_dbs rj
		LEFT JOIN restore_task_listing_dbs rt ON rj.id = rt.restore_job_id
			AND rt.deleted_at IS NULL
			AND rt.status IN ('running','retrying')
		WHERE rj.user_id = $1
		AND rj.deleted_at IS NULL
		AND rj.status = 'running'
		ORDER BY rj.id DESC, rt.started_at DESC`

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
			method         string
			loginID        string
			status         string
			message        string
			messageStatus  string
			totalCount     uint
			processedCount uint
			failedCount    uint
			cursorID       uint
			startTime      *time.Time
			taskStatus     *string
			batchIndex     *uint
		)
		if err := rows.Scan(
			&jobID, &method, &loginID, &status, &message, &messageStatus,
			&totalCount, &processedCount, &failedCount, &cursorID,
			&startTime, &taskStatus, &batchIndex,
		); err != nil {
			return nil, fmt.Errorf("scan active restore job: %w", err)
		}

		if _, exists := jobsMap[jobID]; !exists {
			jobsMap[jobID] = &LiveRestoreJobListingDB{
				ID:              jobID,
				Method:          method,
				LoginID:         loginID,
				Status:          status,
				Message:         message,
				MessageStatus:   EffectiveRestoreMessageStatus(&RestoreJobListingDB{MessageStatus: messageStatus, Status: status}),
				TotalCount:      totalCount,
				ProcessedCount:  processedCount,
				FailedCount:     failedCount,
				CursorID:        cursorID,
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

// RestoreJobListFilter query options for GET /restore/jobs.
type RestoreJobListFilter struct {
	Method   string
	Status   string
	Search   string
	FromTime *time.Time
	ToTime   *time.Time
	Limit    int
	Offset   int
}

const restoreJobListDefaultLimit = 20
const restoreJobListMaxLimit = 100

// RestoreMethodFromAPIService maps UI service or internal method to restore method column value.
func RestoreMethodFromAPIService(service string) string {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "gmail":
		return "gmail"
	case "drive":
		return "google_drive"
	case "photos":
		return "google_photos"
	case "calendar":
		return "google_calendar"
	case "contacts":
		return "google_contacts"
	case "google_drive", "google_photos", "google_calendar", "google_contacts":
		return strings.ToLower(strings.TrimSpace(service))
	default:
		return ""
	}
}

// ValidRestoreJobStatus reports whether status is a known restore job status.
func ValidRestoreJobStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case RestoreJobStatusQueued, RestoreJobStatusRunning, RestoreJobStatusCompleted,
		RestoreJobStatusPartialCompleted, RestoreJobStatusFailed, RestoreJobStatusCancelled:
		return true
	default:
		return false
	}
}

func (r *RestoreJobRepository) ListByUser(userID string, filter *RestoreJobListFilter) ([]RestoreJobListingDB, error) {
	limit := restoreJobListDefaultLimit
	offset := 0
	query := r.db.Where("user_id = ?", userID)
	if filter != nil {
		if filter.Method != "" {
			query = query.Where("method = ?", filter.Method)
		}
		if filter.Status != "" {
			query = query.Where("status = ?", filter.Status)
		}
		if search := strings.TrimSpace(filter.Search); search != "" {
			query = query.Where("login_id ILIKE ?", "%"+search+"%")
		}
		if filter.FromTime != nil {
			query = query.Where("created_at >= ?", *filter.FromTime)
		}
		if filter.ToTime != nil {
			query = query.Where("created_at <= ?", *filter.ToTime)
		}
		if filter.Limit > 0 {
			limit = filter.Limit
		}
		if filter.Offset > 0 {
			offset = filter.Offset
		}
	}
	if limit > restoreJobListMaxLimit {
		limit = restoreJobListMaxLimit
	}
	var jobs []RestoreJobListingDB
	err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&jobs).Error
	return jobs, err
}

func (r *RestoreJobRepository) MissedHeartbeatForJobs(staleBefore time.Time) error {
	return r.db.Model(&RestoreJobListingDB{}).
		Where("status = ? AND (last_heart_beat IS NULL OR last_heart_beat < ?)", RestoreJobStatusRunning, staleBefore).
		Updates(map[string]interface{}{
			"status":         RestoreJobStatusFailed,
			"message":        "restore job missed heartbeat",
			"message_status": JobMessageStatusError,
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

// CountBatchesForJob returns how many batch rows exist (for batch_index assignment).
func (r *RestoreJobRepository) CountBatchesForJob(jobID uint) (uint, error) {
	var count int64
	err := r.db.Model(&RestoreTaskListingDB{}).Where("restore_job_id = ?", jobID).Count(&count).Error
	return uint(count), err
}
