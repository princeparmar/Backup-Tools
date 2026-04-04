package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	gormio "gorm.io/gorm"
)

// Job message status constants
const (
	JobMessageStatusInfo    = "info"
	JobMessageStatusWarning = "warning"
	JobMessageStatusError   = "error"

	JobMessagePushToQueue = "push to queue"
)

// Task status constants
const (
	JobStatusCreated    = "created"
	JobStatusInQueue    = "in_queue"
	JobStatusInProgress = "in_progress"
	JobStatusSuccess    = "success"
	JobStatusFailed     = "failed"

	TaskStatusPushed  = "pushed"
	TaskStatusRunning = "running"
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed"
)

// Other constants
const (
	MaxRetryCount = 3
)

// CronJobListingDB represents a cron job in the database
type CronJobListingDB struct {
	gorm.GormModel

	UserID         string  `json:"user_id" gorm:"column:user_id;uniqueIndex:idx_name_sync_type_user"`
	ParentID       *string `json:"parent_id,omitempty" gorm:"column:parent_id;index:idx_parent_id"`
	StorjProjectID string  `json:"storj_project_id,omitempty" gorm:"column:storj_project_id;index:idx_storj_project_id"` // Storj project ID extracted from access grant

	// Name + SyncType + UserID should be unique
	Name     string     `json:"name" gorm:"uniqueIndex:idx_name_sync_type_user"`
	Method   string     `json:"method"`
	Interval string     `json:"interval"`
	On       string     `json:"on"`
	LastRun  *time.Time `json:"last_run"`

	// Change the type from map[string]interface{} to *database.DbJson[map[string]interface{}]
	InputData *database.DbJson[map[string]interface{}] `json:"input_data" gorm:"type:jsonb"`

	StorxToken string `json:"storx_token,omitempty"`

	Message string `json:"message"`

	// MessageStatus will be one of the following: "info", "warning", "error"
	MessageStatus string `json:"message_status"`
	Active        bool   `json:"active"`

	// Memory will be used to store the state of the task. this will be json field
	TaskMemory TaskMemory `json:"task_memory" gorm:"type:jsonb"`

	// Tasks associated with the cron job
	Tasks []TaskListingDB `gorm:"foreignKey:CronJobID"`

	SyncType string `json:"sync_type" gorm:"uniqueIndex:idx_name_sync_type_user"`

	Status string `json:"status" gorm:"default:created"`

	// Hidden column for scheduled tasks - when true, hides the cron job from the live view
	// Default is false, and it gets reset to false when a new task is created
	Hidden bool `json:"hidden" gorm:"default:false"`
	// Placeholder: true = OAuth/StorX token holder for Workspace children only (not listed, no scheduled backup).
	// Set to false when the admin mailbox is included as a normal backup.
	Placeholder bool `json:"placeholder" gorm:"column:placeholder;default:false"`
	// When user fixes the error and reactivates, this should be set to false
	AutoDeactivated bool `json:"autodeactivated" gorm:"column:auto_deactivated;default:false"`
}

// TaskMemory represents the memory state of a task
type TaskMemory struct {
	GmailNextToken *string `json:"gmail_next_token"`
	GmailSyncCount uint    `json:"gmail_sync_count"`

	OutlookSyncCount uint `json:"outlook_sync_count"`
	OutlookSkipCount uint `json:"outlook_skip_count"`

	// Sync completion flags for one-time syncs
	GmailSyncComplete    bool `json:"gmail_sync_complete"`
	OutlookSyncComplete  bool `json:"outlook_sync_complete"`
	DatabaseSyncComplete bool `json:"database_sync_complete"`
}

// Scan implements the sql.Scanner interface
func (t *TaskMemory) Scan(value interface{}) error {
	if value == nil {
		return nil
	}

	switch v := value.(type) {
	case string:
		return json.Unmarshal([]byte(v), t)
	case []uint8:
		return json.Unmarshal(v, t)
	default:
		return fmt.Errorf("unsupported type: %T", v)
	}
}

// LiveCronJobListingDB represents a live cron job for display purposes
type LiveCronJobListingDB struct {
	ID            uint                `json:"id"`
	Name          string              `json:"name"`
	Method        string              `json:"method"`
	Message       string              `json:"message"`
	MessageStatus string              `json:"message_status"`
	Tasks         []LiveTaskListingDB `json:"tasks"`
}

// LiveTaskListingDB represents a live task for display purposes
type LiveTaskListingDB struct {
	StartTime *time.Time `json:"start_time"`
	Status    string     `json:"status"`
}

// CronJobRepository handles all database operations for cron jobs
type CronJobRepository struct {
	db *gorm.DB
}

// NewCronJobRepository creates a new cron job repository
func NewCronJobRepository(db *gorm.DB) *CronJobRepository {
	return &CronJobRepository{db: db}
}

// CronJobFilter represents filter parameters for cron job queries
type CronJobFilter struct {
	Name          string `json:"name,omitempty"`          // Filter by name (partial match)
	Method        string `json:"method,omitempty"`        // Filter by method
	SyncType      string `json:"syncType,omitempty"`      // Filter by sync type
	Active        *bool  `json:"active,omitempty"`        // Filter by active status
	Status        string `json:"status,omitempty"`        // Filter by status (created, in_queue, in_progress, success, failed)
	MessageStatus string `json:"messageStatus,omitempty"` // Filter by message status (info, warning, error)
	Interval      string `json:"interval,omitempty"`      // Filter by interval (daily, weekly, monthly)
}

// GetAllCronJobsForUser retrieves all cron jobs for a specific user with optional filtering
func (r *CronJobRepository) GetAllCronJobsForUser(userID string, filter *CronJobFilter) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	query := r.db.Where("user_id = ?", userID).Where("COALESCE(placeholder, false) = ?", false)

	// Apply filters if provided
	if filter != nil {
		if filter.Name != "" {
			query = query.Where("name ILIKE ?", "%"+filter.Name+"%")
		}
		if filter.Method != "" {
			query = query.Where("method = ?", filter.Method)
		}
		if filter.SyncType != "" {
			query = query.Where("sync_type = ?", filter.SyncType)
		}
		if filter.Active != nil {
			query = query.Where("active = ?", *filter.Active)
		}
		if filter.Status != "" {
			query = query.Where("status = ?", filter.Status)
		}
		if filter.MessageStatus != "" {
			query = query.Where("message_status = ?", filter.MessageStatus)
		}
		if filter.Interval != "" {
			query = query.Where("interval = ?", filter.Interval)
		}
	}

	db := query.Order("created_at DESC").Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}

	return res, nil
}

// GetAllCronJobsForUserUnfiltered returns all cron jobs for a user including placeholder rows (duplicate/conflict checks only).
func (r *CronJobRepository) GetAllCronJobsForUserUnfiltered(userID string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}
	return res, nil
}

// GetAllActiveCronJobsForUser retrieves active cron jobs with their failed/running tasks
func (r *CronJobRepository) GetAllActiveCronJobsForUser(userID string) ([]LiveCronJobListingDB, error) {
	// Query to get active cron jobs with their failed/running tasks in one go
	query := `
		SELECT 
			cj.id,cj.name,cj.method,cj.message,cj.message_status,
			t.start_time,t.status
		FROM cron_job_listing_dbs cj
		LEFT JOIN task_listing_dbs t ON cj.id = t.cron_job_id 
			AND t.status IN ('failed','running')
		WHERE cj.user_id = $1 
		AND cj.deleted_at IS NULL
		AND (cj.hidden = false OR cj.hidden IS NULL)
		AND COALESCE(cj.placeholder, false) = false
		ORDER BY cj.id, t.start_time DESC`

	rows, err := r.db.Raw(query, userID).Rows()
	if err != nil {
		return nil, fmt.Errorf("error executing query: %v", err)
	}
	defer rows.Close()

	// Map to group tasks by cron job
	cronJobsMap := make(map[uint]*LiveCronJobListingDB)
	var results []LiveCronJobListingDB

	for rows.Next() {
		var (
			cronJobID     uint
			name          string
			method        string
			message       string
			messageStatus string
			startTime     *time.Time
			status        *string
		)

		err := rows.Scan(
			&cronJobID, &name, &method, &message, &messageStatus,
			&startTime, &status,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning row: %v", err)
		}

		// Create or get cron job entry
		if _, exists := cronJobsMap[cronJobID]; !exists {
			cronJobsMap[cronJobID] = &LiveCronJobListingDB{
				ID:            cronJobID,
				Name:          name,
				Method:        method,
				Message:       message,
				MessageStatus: messageStatus,
				Tasks:         []LiveTaskListingDB{},
			}
		}

		// Add task if it exists (status will be non-null for failed/running tasks)
		if status != nil && startTime != nil {
			task := LiveTaskListingDB{
				StartTime: startTime,
				Status:    *status,
			}
			cronJobsMap[cronJobID].Tasks = append(cronJobsMap[cronJobID].Tasks, task)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	// Convert map to slice and filter out cron jobs without tasks
	for _, cronJob := range cronJobsMap {
		if len(cronJob.Tasks) > 0 {
			results = append(results, *cronJob)
		}
	}

	return results, nil
}

// GetJobsToProcess retrieves jobs that are ready to be processed
func (r *CronJobRepository) GetJobsToProcess() ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	tx := r.db.Begin()

	// Pre-aggregate failed task counts in a subquery; outer query has no GROUP BY so FOR UPDATE is allowed (PostgreSQL forbids FOR UPDATE with GROUP BY).
	// Parameter order: fail_status, startOfDay, endOfDay, message, status_not_in_1, status_not_in_2,
	// startOfDay, failed_status, startOfDay, endOfDay, maxRetry, weekday, day, task_running, task_pushed
	sqlQuery := `
		SELECT c.*
		FROM cron_job_listing_dbs c
		WHERE c.id IN (
			SELECT c2.id
			FROM cron_job_listing_dbs c2
			LEFT JOIN (
				SELECT cron_job_id, COUNT(*) AS fail_count
				FROM task_listing_dbs
				WHERE status = ?
				AND created_at >= ? AND created_at < ?
				GROUP BY cron_job_id
			) t_fail ON t_fail.cron_job_id = c2.id
			WHERE c2.active = true
			AND (c2.message IS NULL OR c2.message != ?)
			AND c2.status NOT IN (?, ?)
			AND (
				c2.last_run IS NULL
				OR c2.last_run < ?
				OR (
					c2.status = ?
					AND c2.last_run >= ? AND c2.last_run < ?
					AND COALESCE(t_fail.fail_count, 0) < ?
				)
			)
			AND (
				c2.interval = 'daily'
				OR (c2.interval = 'weekly' AND c2."on" = ?)
				OR (c2.interval = 'monthly' AND c2."on" = ?)
			)
			AND NOT EXISTS (
				SELECT 1
				FROM task_listing_dbs t
				WHERE t.cron_job_id = c2.id
				AND t.status IN (?, ?)
			)
			AND c2.deleted_at IS NULL
			AND COALESCE(c2.placeholder, false) = false
			LIMIT 10
		)
		FOR UPDATE OF c
	`

	// Calculate time range for today (midnight to midnight) for index-friendly queries
	now := time.Now()
	location := now.Location()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	endOfDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, location)

	rawQuery := tx.Raw(sqlQuery,
		TaskStatusFailed, startOfDay, endOfDay,
		JobMessagePushToQueue,
		JobStatusInQueue, JobStatusInProgress,
		startOfDay, JobStatusFailed, startOfDay, endOfDay, MaxRetryCount,
		now.Weekday().String(), fmt.Sprint(now.Day()),
		TaskStatusRunning, TaskStatusPushed)

	scanResult := rawQuery.Scan(&res)
	if scanResult.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting jobs to process: %v", scanResult.Error)
	}

	// Claim job: message, message_status, and status so another worker won't pick it if this one crashes before creating the task
	for i := range res {
		res[i].Message = JobMessagePushToQueue
		res[i].MessageStatus = JobMessageStatusInfo
		res[i].Status = JobStatusInQueue

		if err := tx.Save(&res[i]).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("error updating cron job: %w", err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("error committing transaction: %w", err)
	}

	return res, nil
}

// GetJobByIDForUser retrieves a specific cron job by ID for a user (excludes placeholder token-holder rows).
func (r *CronJobRepository) GetJobByIDForUser(userID string, jobID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := r.db.Where("user_id = ? AND id = ?", userID, jobID).Where("COALESCE(placeholder, false) = ?", false).First(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron job for user: %v", db.Error)
	}

	return &res, nil
}

// GetJobsByUserAndParentIDAndMethod retrieves all jobs for a user grouped under a parent_id for a specific method.
func (r *CronJobRepository) GetJobsByUserAndParentIDAndMethod(userID, parentID, method string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := r.db.Where("user_id = ? AND parent_id = ? AND method = ?", userID, parentID, method).Order("created_at DESC").Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs for user/parent/method: %v", db.Error)
	}
	return res, nil
}

// FindGmailJobByUserNameSyncType returns the gmail job if it exists; (nil, false, nil) when not found.
func (r *CronJobRepository) FindGmailJobByUserNameSyncType(userID, name, syncType string) (*CronJobListingDB, bool, error) {
	var res CronJobListingDB
	q := r.db.Where("user_id = ? AND name = ? AND method = ? AND sync_type = ?", userID, name, "gmail", syncType).First(&res)
	if q.Error != nil {
		if errors.Is(q.Error, gormio.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("error getting gmail cron job: %w", q.Error)
	}
	return &res, true, nil
}

// GmailConnectedAccountEmail is the admin mailbox (OAuth token row) for a corporate child job.
// Source of truth is column parent_id only — not input_data.connected_email.
func GmailConnectedAccountEmail(job *CronJobListingDB) string {
	if job == nil || job.ParentID == nil {
		return ""
	}
	p := strings.TrimSpace(*job.ParentID)
	if p == "" || strings.EqualFold(p, strings.TrimSpace(job.Name)) {
		return ""
	}
	return p
}

// GmailResolvedRefreshToken returns refresh_token from this job, or from the admin row for a Gmail corporate child (via GmailParentRowForCorporateChild).
func (r *CronJobRepository) GmailResolvedRefreshToken(job *CronJobListingDB) string {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return ""
	}
	j := job.InputData.Json()
	if s, ok := (*j)["refresh_token"].(string); ok {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	parent, err := r.GmailParentRowForCorporateChild(job)
	if err != nil || parent == nil || parent.InputData == nil || parent.InputData.Json() == nil {
		return ""
	}
	pj := parent.InputData.Json()
	if s, ok := (*pj)["refresh_token"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// GmailParentRowForCorporateChild loads the admin cron row when it exists (parent_id = admin, name != admin).
// If there is no admin backup job row — e.g. only delegated mailboxes were selected — returns (nil, nil), not an error.
func (r *CronJobRepository) GmailParentRowForCorporateChild(job *CronJobListingDB) (*CronJobListingDB, error) {
	if job == nil || job.Method != "gmail" {
		return nil, nil
	}
	admin := GmailConnectedAccountEmail(job)
	if admin == "" {
		return nil, nil
	}
	parent, ok, err := r.FindGmailJobByUserNameSyncType(job.UserID, admin, job.SyncType)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return parent, nil
}

// gmailResolvedStringFromLocalOrParent returns trimmed local if non-empty; otherwise the same field from the admin parent row for a Gmail corporate child.
func (r *CronJobRepository) gmailResolvedStringFromLocalOrParent(job *CronJobListingDB, local string, fromParent func(*CronJobListingDB) string) string {
	if job == nil {
		return ""
	}
	if t := strings.TrimSpace(local); t != "" {
		return t
	}
	parent, err := r.GmailParentRowForCorporateChild(job)
	if err != nil || parent == nil {
		return ""
	}
	return strings.TrimSpace(fromParent(parent))
}

// GmailResolvedStorxToken returns storx_token from this row, or from the parent admin row when this Gmail corporate child has no local token.
func (r *CronJobRepository) GmailResolvedStorxToken(job *CronJobListingDB) string {
	if job == nil {
		return ""
	}
	return r.gmailResolvedStringFromLocalOrParent(job, job.StorxToken, func(p *CronJobListingDB) string { return p.StorxToken })
}

// GmailAdminJobForSharedStorx returns the cron row that holds the shared StorX grant for Workspace-style Gmail:
// the parent admin job for corporate children, or the job itself when it is not under another Gmail backup parent.
func (r *CronJobRepository) GmailAdminJobForSharedStorx(job *CronJobListingDB) (*CronJobListingDB, error) {
	if job == nil || job.Method != "gmail" {
		return nil, nil
	}
	parent, err := r.GmailParentRowForCorporateChild(job)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		return parent, nil
	}
	return job, nil
}

// DeactivateGmailWorkspaceTreeForStorxFailure clears storx_token / storj_project_id and deactivates the admin job
// and every Gmail child whose parent_id is that admin mailbox (same user_id and sync_type).
// Used when StorX is missing or Satellite uplink rejects the access grant so connected accounts do not keep running with a bad token.
func (r *CronJobRepository) DeactivateGmailWorkspaceTreeForStorxFailure(job *CronJobListingDB, message string) error {
	if job == nil || job.Method != "gmail" {
		return nil
	}
	admin, err := r.GmailAdminJobForSharedStorx(job)
	if err != nil {
		return err
	}
	if admin == nil {
		return nil
	}
	adminName := strings.TrimSpace(admin.Name)
	if adminName == "" {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"
	}
	updates := map[string]interface{}{
		"active":           false,
		"auto_deactivated": true,
		"storx_token":      "",
		"storj_project_id": "",
		"message":          message,
		"message_status":   JobMessageStatusError,
	}
	q := r.db.Model(&CronJobListingDB{}).
		Where("user_id = ? AND method = ? AND sync_type = ?", job.UserID, "gmail", job.SyncType).
		Where("(id = ? OR parent_id = ?)", admin.ID, adminName)
	return q.Updates(updates).Error
}

// StripGmailRefreshTokenFromCronJobInputData clears refresh_token in input_data when the key exists (in-memory or before persisting).
func StripGmailRefreshTokenFromCronJobInputData(job *CronJobListingDB) {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return
	}
	if _, ok := (*job.InputData.Json())["refresh_token"]; ok {
		(*job.InputData.Json())["refresh_token"] = ""
	}
}

// DeactivateGmailWorkspaceTreeForGoogleAuthFailure clears refresh_token in input_data on the admin job and every
// connected Gmail child (same tree as StorX failure), deactivates all of them, and sets a shared error message.
func (r *CronJobRepository) DeactivateGmailWorkspaceTreeForGoogleAuthFailure(job *CronJobListingDB, message string) error {
	if job == nil || job.Method != "gmail" {
		return nil
	}
	admin, err := r.GmailAdminJobForSharedStorx(job)
	if err != nil {
		return err
	}
	if admin == nil {
		return nil
	}
	adminName := strings.TrimSpace(admin.Name)
	if adminName == "" {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"
	}

	tx := r.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("error starting transaction: %w", tx.Error)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	var rows []CronJobListingDB
	if err := tx.Where("user_id = ? AND method = ? AND sync_type = ?", job.UserID, "gmail", job.SyncType).
		Where("(id = ? OR parent_id = ?)", admin.ID, adminName).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("error loading gmail workspace jobs: %w", err)
	}

	for i := range rows {
		StripGmailRefreshTokenFromCronJobInputData(&rows[i])
		updateMap := map[string]interface{}{
			"active":           false,
			"auto_deactivated": true,
			"message":          message,
			"message_status":   JobMessageStatusError,
		}
		if rows[i].InputData != nil {
			updateMap["input_data"] = rows[i].InputData
		}
		if err := tx.Model(&CronJobListingDB{}).Where("id = ?", rows[i].ID).Updates(updateMap).Error; err != nil {
			return fmt.Errorf("error updating cron job %d: %w", rows[i].ID, err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("error committing gmail auth failure updates: %w", err)
	}
	committed = true
	return nil
}

// GetCronJobByID retrieves a cron job by ID
func (r *CronJobRepository) GetCronJobByID(ID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := r.db.First(&res, ID)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron job by ID: %v", db.Error)
	}

	return &res, nil
}

// GetAccessGrantByProjectID retrieves the access grant (storx_token) for a given project_id and method
func (r *CronJobRepository) GetAccessGrantByProjectID(projectID, method string) (string, error) {
	var cronJob CronJobListingDB
	result := r.db.Where("storj_project_id = ? AND method = ? AND active = true AND storx_token != ''",
		projectID, method).First(&cronJob)

	if result.Error != nil {
		return "", fmt.Errorf("access grant not found for project_id %s and method %s: %w", projectID, method, result.Error)
	}

	if cronJob.StorxToken == "" {
		return "", fmt.Errorf("access grant is empty for project_id %s and method %s", projectID, method)
	}

	return cronJob.StorxToken, nil
}

// CreateCronJobForUser creates a new cron job for a user
func (r *CronJobRepository) CreateCronJobForUser(userID, name, method string, syncType string, inputData map[string]interface{}, parentID *string) (*CronJobListingDB, error) {
	return r.CreateCronJobForUserWithPlaceholder(userID, name, method, syncType, inputData, parentID, false)
}

// CreateCronJobForUserWithPlaceholder creates a cron job; when placeholder is true the row is kept inactive and hidden from listings.
func (r *CronJobRepository) CreateCronJobForUserWithPlaceholder(userID, name, method string, syncType string, inputData map[string]interface{}, parentID *string, placeholder bool) (*CronJobListingDB, error) {
	data := CronJobListingDB{
		UserID:      userID,
		ParentID:    parentID,
		Name:        name,
		Method:      method,
		SyncType:    syncType,
		InputData:   database.NewDbJsonFromValue(inputData),
		Status:      JobStatusCreated,
		LastRun:     nil,
		Placeholder: placeholder,
	}

	// Set interval and activation for one-time backups
	if syncType == "one_time" {
		data.Interval = "one_time"
		data.On = ""
		if !placeholder {
			data.Active = true
		}
	}

	// create new entry in database and return newly created cron job
	res := r.db.Create(&data)
	if res != nil && res.Error != nil {
		return nil, fmt.Errorf("error creating cron job: %v", res.Error)
	}

	return &data, nil
}

// DeleteCronJobByID deletes a cron job by ID
func (r *CronJobRepository) DeleteCronJobByID(ID uint) error {
	res := r.db.Delete(&CronJobListingDB{}, ID)
	if res != nil && res.Error != nil {
		return fmt.Errorf("error deleting cron job: %v", res.Error)
	}
	return nil
}

// UpdateCronJobByID updates a cron job by ID
func (r *CronJobRepository) UpdateCronJobByID(ID uint, m map[string]interface{}) error {
	tx := r.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("error starting transaction: %w", tx.Error)
	}

	// Use a flag to track if we should rollback
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	// Update the cron job
	res := tx.Model(&CronJobListingDB{}).Where("id = ?", ID).Updates(m)
	if res.Error != nil {
		return fmt.Errorf("error updating cron job: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("no cron job found with id %d", ID)
	}

	// Get the updated cron job
	var updatedJob CronJobListingDB
	if err := tx.First(&updatedJob, ID).Error; err != nil {
		return fmt.Errorf("error getting updated cron job: %w", err)
	}

	// Validate activation if the job is being activated
	if active, exists := m["active"]; exists && active == true {
		if err := r.validateJobForActivation(&updatedJob); err != nil {
			return err
		}
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("error committing transaction: %w", err)
	}

	committed = true
	return nil
}

// UpdateCronJobFieldsForCron updates a cron job by ID for cron processing.
// For one-time sync jobs, only specific fields are allowed (status, message, message_status, last_run).
func (r *CronJobRepository) UpdateCronJobFieldsForCron(ID uint, fields map[string]interface{}) error {
	tx := r.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("error starting transaction: %w", tx.Error)
	}

	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	// Load existing job to enforce one-time sync constraints
	var existingJob CronJobListingDB
	if err := tx.First(&existingJob, ID).Error; err != nil {
		return fmt.Errorf("error getting existing cron job: %w", err)
	}

	updateMap := fields

	// For one-time sync jobs, only allow specific fields to be updated
	if existingJob.SyncType == "one_time" {
		allowedFields := map[string]bool{
			"status":         true,
			"message":        true,
			"message_status": true,
			"last_run":       true,
			"input_data":     true, // e.g. cleared oauth refresh_token after auth failure
		}

		filteredMap := make(map[string]interface{})
		for key, value := range fields {
			if allowedFields[key] {
				filteredMap[key] = value
			}
		}

		if len(filteredMap) == 0 {
			return fmt.Errorf("cannot update one_time sync job: only status, message, message_status, last_run, and input_data fields are allowed")
		}

		updateMap = filteredMap
	}

	// Perform the update
	res := tx.Model(&CronJobListingDB{}).Where("id = ?", ID).Updates(updateMap)
	if res.Error != nil {
		return fmt.Errorf("error updating cron job for cron: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("no cron job found with id %d", ID)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("error committing cron update transaction: %w", err)
	}

	committed = true
	return nil
}

// DeleteAllJobsAndTasksByEmail deletes all jobs and related tasks for a user by email
// Returns the list of deleted job IDs and task IDs
func (r *CronJobRepository) DeleteAllJobsAndTasksByEmail(email string) ([]uint, []uint, error) {
	// Start a transaction to ensure atomicity
	tx := r.db.Begin()
	if tx.Error != nil {
		return nil, nil, fmt.Errorf("error starting transaction: %v", tx.Error)
	}

	// First, get all job IDs for this email before deleting
	var jobs []CronJobListingDB
	if err := tx.Where("name = ?", email).Find(&jobs).Error; err != nil {
		tx.Rollback()
		return nil, nil, fmt.Errorf("error getting jobs for email: %v", err)
	}

	if len(jobs) == 0 {
		tx.Rollback()
		return nil, nil, fmt.Errorf("no jobs found for email: %s", email)
	}

	// Extract job IDs
	var deletedJobIDs []uint
	for _, job := range jobs {
		deletedJobIDs = append(deletedJobIDs, job.ID)
	}

	// Get all task IDs for these jobs before deleting
	var taskIDs []uint
	for _, jobID := range deletedJobIDs {
		var tasks []TaskListingDB
		if err := tx.Where("cron_job_id = ?", jobID).Find(&tasks).Error; err != nil {
			tx.Rollback()
			return nil, nil, fmt.Errorf("error getting tasks for job %d: %v", jobID, err)
		}
		for _, task := range tasks {
			taskIDs = append(taskIDs, task.ID)
		}
	}

	// Delete all tasks for these jobs first (hard delete)
	if len(taskIDs) > 0 {
		if err := tx.Exec("DELETE FROM task_listing_dbs WHERE cron_job_id IN ?", deletedJobIDs).Error; err != nil {
			tx.Rollback()
			return nil, nil, fmt.Errorf("error deleting tasks: %v", err)
		}
	}

	// Delete all jobs for the email (hard delete)
	if err := tx.Exec("DELETE FROM cron_job_listing_dbs WHERE name = ?", email).Error; err != nil {
		tx.Rollback()
		return nil, nil, fmt.Errorf("error deleting jobs for email: %v", err)
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		return nil, nil, fmt.Errorf("error committing transaction: %v", err)
	}

	return deletedJobIDs, taskIDs, nil
}

const workspaceServiceAccountFile = "workspace-service-account.json"

// gmailDelegationOnlyMayApply mirrors apps/google GmailJobUsesDelegationWithoutOAuth without importing apps/google
// (avoids import cycle: google → db → repo). Used only for activation checks.
func gmailDelegationOnlyMayApply(job *CronJobListingDB) bool {
	if job == nil {
		return false
	}
	if _, err := os.Stat(workspaceServiceAccountFile); err != nil {
		return false
	}
	mailbox := strings.TrimSpace(job.Name)
	if job.InputData != nil && job.InputData.Json() != nil {
		inputData := job.InputData.Json()
		if v, ok := (*inputData)["email"].(string); ok && strings.TrimSpace(v) != "" {
			mailbox = strings.TrimSpace(v)
		}
	}
	connected := GmailConnectedAccountEmail(job)
	var subject string
	switch {
	case mailbox != "" && !strings.EqualFold(mailbox, "me"):
		subject = mailbox
	case connected != "":
		subject = connected
	default:
		return false
	}
	e := strings.ToLower(strings.TrimSpace(subject))
	if e == "" || !strings.Contains(e, "@") {
		return false
	}
	if strings.HasSuffix(e, "@gmail.com") || strings.HasSuffix(e, "@googlemail.com") {
		return strings.EqualFold(strings.TrimSpace(utils.GetEnvWithKey("GMAIL_DELEGATION_ALLOW_GMAIL_COM")), "true")
	}
	return true
}

// ValidateJobForActivation checks if the job has all required fields and authentication tokens for activation
func (r *CronJobRepository) validateJobForActivation(job *CronJobListingDB) error {
	// Validate required fields for activation
	storx := job.StorxToken
	if job.Method == "gmail" {
		storx = r.GmailResolvedStorxToken(job)
	}
	if strings.TrimSpace(storx) == "" {
		return fmt.Errorf("storx_token is required when activating backup")
	}
	if job.Interval == "" {
		return fmt.Errorf("interval is required when activating backup")
	}
	if job.On == "" {
		return fmt.Errorf("on is required when activating backup")
	}

	// Parse existing input_data to check for authentication tokens
	var inputData map[string]interface{}
	if job.InputData != nil && job.InputData.Json() != nil {
		inputData = *job.InputData.Json()
	}

	switch job.Method {
	case "gmail":
		rt := r.GmailResolvedRefreshToken(job)
		if rt == "" && !gmailDelegationOnlyMayApply(job) {
			return fmt.Errorf("refresh_token is required in input_data for gmail method (unless domain-wide delegation is configured for this mailbox)")
		}
		emailVal := strings.TrimSpace(job.Name)
		if v, ok := inputData["email"].(string); ok && strings.TrimSpace(v) != "" {
			emailVal = strings.TrimSpace(v)
		}
		if emailVal == "" {
			return fmt.Errorf("email is required in input_data or job name for gmail method")
		}
	case "outlook":
		// Check if refresh_token exists in input_data
		if refreshToken, exists := inputData["refresh_token"]; !exists || refreshToken == "" {
			return fmt.Errorf("refresh_token is required in input_data for outlook method")
		}
	case "database", "psql_database", "mysql_database":
		// Check if database connection details exist in input_data
		requiredFields := []string{"host", "port", "username", "password", "database_name"}
		for _, field := range requiredFields {
			if value, exists := inputData[field]; !exists || value == "" {
				return fmt.Errorf("%s is required in input_data for database method", field)
			}
		}
	}
	return nil
}

// MaskTokenForCronJobListingDB masks sensitive tokens in cron job data
func MaskTokenForCronJobListingDB(cronJobs []CronJobListingDB) []CronJobListingDB {
	for i := range cronJobs {
		MaskTokenForCronJobDB(&cronJobs[i])
	}

	return cronJobs
}

// MaskTokenForCronJobDB masks sensitive tokens in a single cron job
func MaskTokenForCronJobDB(cronJob *CronJobListingDB) {
	cronJob.StorxToken = utils.MaskString(cronJob.StorxToken)
	if cronJob.InputData != nil && cronJob.InputData.Json() != nil {
		if refreshToken, exists := (*cronJob.InputData.Json())["refresh_token"]; exists {
			(*cronJob.InputData.Json())["refresh_token"] = utils.MaskString(refreshToken.(string))
		}
	}
}
