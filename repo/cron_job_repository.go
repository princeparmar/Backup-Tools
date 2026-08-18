package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
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
	JobStatusCancelled  = "cancelled"

	TaskStatusPushed  = "pushed"
	TaskStatusRunning = "running"
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed"

	TaskTriggerScheduled = "scheduled"
	TaskTriggerOnDemand  = "on_demand"
)

// Other constants
const (
	MaxRetryCount = 3
	// MaxFailurePeriods is how many schedule periods (day/week/month) may end in exhausted task retries before the job is deactivated.
	MaxFailurePeriods = 3
)

// CronJobListingDB represents a cron job in the database
type CronJobListingDB struct {
	gorm.GormModel

	UserID string `json:"user_id" gorm:"column:user_id;uniqueIndex:idx_name_sync_type_user"`
	// StorjProjectID is API-only; persisted on google_backup_credentials (see EnrichCronJobFromCredential).
	StorjProjectID string `json:"storj_project_id,omitempty" gorm:"-"`

	// Name + Method + SyncType + UserID must be unique (one cron job per service per mailbox).
	Name     string `json:"name" gorm:"uniqueIndex:idx_name_sync_type_user"`
	Method   string `json:"method" gorm:"uniqueIndex:idx_name_sync_type_user"`
	Interval string `json:"interval"`
	On       string `json:"on" gorm:"-"` // API-only; canonical value from autosync_backup_policy_dbs
	// PolicyID links to autosync_backup_policy_dbs.id.
	PolicyID uint       `json:"policy_id,omitempty" gorm:"column:policy_id;index"`
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
	// Placeholder: legacy token-holder Gmail rows; new jobs use google_backup_credential_dbs instead.
	Placeholder bool `json:"placeholder" gorm:"column:placeholder;default:false"`
	// When user fixes the error and reactivates, this should be set to false
	AutoDeactivated bool `json:"autodeactivated" gorm:"column:auto_deactivated;default:false"`
	// FailurePeriods counts schedule periods that ended with all per-task retries exhausted (retry_count reached MaxRetryCount). Reset on success or user reactivation.
	FailurePeriods uint `json:"failure_periods" gorm:"column:failure_periods;default:0"`
	// StorxRefreshFailures counts consecutive Satellite storx refresh failures; reset on successful refresh.
	StorxRefreshFailures uint `json:"storx_refresh_failures" gorm:"column:storx_refresh_failures;default:0"`
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

	// Drive incremental sync state (ID-based autosync architecture)
	DrivePageToken    *string `json:"drive_page_token,omitempty"`
	DriveBaselineDone bool    `json:"drive_baseline_done,omitempty"`

	// Photos incremental sync state (ID-based autosync architecture)
	PhotosBaselineDone bool `json:"photos_baseline_done,omitempty"`

	// Contacts incremental sync state (ID-based autosync architecture)
	ContactsBaselineDone bool `json:"contacts_baseline_done,omitempty"`

	// Calendar incremental sync state (per-calendar syncToken; see CalendarCalendarState)
	CalendarCalendars map[string]CalendarCalendarState `json:"calendar_calendars,omitempty"`
}

// CalendarCalendarState holds baseline + sync token for one Google calendar.
type CalendarCalendarState struct {
	BaselineDone bool   `json:"baseline_done,omitempty"`
	SyncToken    string `json:"sync_token,omitempty"`
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
	db       *gorm.DB
	credRepo *GoogleBackupCredentialRepository
}

// NewCronJobRepository creates a new cron job repository
func NewCronJobRepository(db *gorm.DB) *CronJobRepository {
	return &CronJobRepository{
		db:       db,
		credRepo: NewGoogleBackupCredentialRepository(db),
	}
}

// CronJobFilter represents filter parameters for cron job queries
type CronJobFilter struct {
	Name          string `json:"name,omitempty"`          // Filter by name (partial match)
	Method        string `json:"method,omitempty"`        // Filter by method
	SyncType      string `json:"syncType,omitempty"`      // Filter by sync type
	Active        *bool  `json:"active,omitempty"`        // Filter by active status
	Status        string `json:"status,omitempty"`        // Filter by status (created, in_queue, in_progress, success, failed, cancelled)
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

// ServiceJobCountRow is per-method job counts for GET /auto-sync/job/services.
type ServiceJobCountRow struct {
	Method       string
	TotalJobs    int
	ActiveJobs   int
	DeactiveJobs int
}

// ListServiceJobCountsForUser returns job counts grouped by method for non-placeholder rows.
func (r *CronJobRepository) ListServiceJobCountsForUser(userID string) ([]ServiceJobCountRow, error) {
	type countRow struct {
		Method       string
		TotalJobs    int
		ActiveJobs   int
		DeactiveJobs int
	}
	var rows []countRow
	err := r.db.Model(&CronJobListingDB{}).
		Select(`method,
			COUNT(*) AS total_jobs,
			COUNT(*) FILTER (WHERE active = true) AS active_jobs,
			COUNT(*) FILTER (WHERE active = false) AS deactive_jobs`).
		Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
		Group("method").
		Order("method ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list service job counts: %w", err)
	}
	out := make([]ServiceJobCountRow, 0, len(rows))
	for i := range rows {
		out = append(out, ServiceJobCountRow{
			Method:       rows[i].Method,
			TotalJobs:    rows[i].TotalJobs,
			ActiveJobs:   rows[i].ActiveJobs,
			DeactiveJobs: rows[i].DeactiveJobs,
		})
	}
	return out, nil
}

// UsersGroupsJobFilter scopes jobs for GET /users-groups.
type UsersGroupsJobFilter struct {
	EmailSearch  string
	MailboxEmail string // exact match on job name or input_data.email
	Method       string
	Domain       string
	OrgUnitPath  string
}

// ListJobsForUsersGroups returns non-placeholder cron jobs for users-groups listing.
func (r *CronJobRepository) ListJobsForUsersGroups(userID string, f *UsersGroupsJobFilter) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	query := r.db.Model(&CronJobListingDB{}).
		Where("cron_job_listing_dbs.user_id = ?", userID).
		Where("COALESCE(cron_job_listing_dbs.placeholder, false) = ?", false)

	if f != nil {
		if domain := strings.ToLower(strings.TrimSpace(f.Domain)); domain != "" {
			query = query.Joins(`INNER JOIN google_backup_credential_dbs c ON (cron_job_listing_dbs.input_data->>'credential_id')::bigint = c.id AND c.deleted_at IS NULL`).
				Where("LOWER(SPLIT_PART(TRIM(c.email), '@', 2)) = ?", domain)
		}
		if mailbox := strings.TrimSpace(f.MailboxEmail); mailbox != "" {
			query = query.Where(
				"(LOWER(TRIM(cron_job_listing_dbs.name)) = LOWER(?) OR LOWER(TRIM(COALESCE(cron_job_listing_dbs.input_data->>'email', ''))) = LOWER(?))",
				mailbox, mailbox,
			)
		}
		if search := strings.TrimSpace(f.EmailSearch); search != "" {
			like := "%" + search + "%"
			query = query.Where("(cron_job_listing_dbs.name ILIKE ? OR cron_job_listing_dbs.input_data->>'email' ILIKE ? OR cron_job_listing_dbs.input_data->>'org_unit_path' ILIKE ?)", like, like, like)
		}
		if orgUnit := strings.TrimSpace(f.OrgUnitPath); orgUnit != "" {
			query = query.Where("LOWER(TRIM(COALESCE(cron_job_listing_dbs.input_data->>'org_unit_path', ''))) = LOWER(?)", orgUnit)
		}
		if method := strings.TrimSpace(f.Method); method != "" {
			query = query.Where("cron_job_listing_dbs.method = ?", method)
		}
	}

	db := query.Order("cron_job_listing_dbs.name ASC, cron_job_listing_dbs.method ASC").Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("list jobs for users-groups: %w", db.Error)
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

// PeriodStartForInterval returns the start of the current scheduling period in loc for daily / weekly / monthly jobs.
// Weekly periods begin Monday 00:00 in loc (aligned with interval day checks in GetJobsToProcess).
func PeriodStartForInterval(interval string, now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	n := now.In(loc)
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "3h":
		blockHour := (n.Hour() / 3) * 3
		return time.Date(n.Year(), n.Month(), n.Day(), blockHour, 0, 0, 0, loc)
	case "12h":
		blockHour := (n.Hour() / 12) * 12
		return time.Date(n.Year(), n.Month(), n.Day(), blockHour, 0, 0, 0, loc)
	case "daily":
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	case "weekly":
		wd := int(n.Weekday()) // Sunday = 0
		daysFromMonday := (wd + 6) % 7
		monday := n.AddDate(0, 0, -daysFromMonday)
		return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, loc)
	case "monthly":
		return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	}
}

// GetJobsToProcess retrieves jobs that are ready to be processed
func (r *CronJobRepository) GetJobsToProcess() ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	if err := r.db.Model(&CronJobListingDB{}).
		Where("policy_id IN (SELECT id FROM autosync_backup_policy_dbs WHERE expires_at IS NOT NULL AND expires_at <= NOW() AND deleted_at IS NULL)").
		Updates(map[string]interface{}{
			"active":           false,
			"auto_deactivated": true,
			"message":          "Backup policy is expired. Update retention policy before re-activating.",
			"message_status":   JobMessageStatusError,
		}).Error; err != nil {
		return nil, fmt.Errorf("deactivate expired policy jobs: %w", err)
	}
	tx := r.db.Begin()

	// One task creation per schedule period: last_run must be before the period start for this job's interval,
	// or null. No pushed/running task for the job. Retries on the same row are not new creations.
	// Parameter order: message, status_not_in_1, status_not_in_2,
	// daily, weekly, monthly, 3h, 12h period starts, daily (ELSE),
	// weekday, day, task_running, task_pushed
	sqlQuery := `
		SELECT c.*
		FROM cron_job_listing_dbs c
		WHERE c.id IN (
			SELECT c2.id
			FROM cron_job_listing_dbs c2
			LEFT JOIN autosync_backup_policy_dbs p ON p.id = c2.policy_id AND p.deleted_at IS NULL
			WHERE c2.active = true
			AND (c2.message IS NULL OR c2.message != ?)
			AND c2.status NOT IN (?, ?)
			AND c2.policy_id IS NOT NULL
			AND (p.expires_at IS NULL OR p.expires_at > NOW())
			AND (
				c2.last_run IS NULL
				OR c2.last_run < CASE p.interval
					WHEN 'daily' THEN ?::timestamptz
					WHEN 'weekly' THEN ?::timestamptz
					WHEN 'monthly' THEN ?::timestamptz
					WHEN '3h' THEN ?::timestamptz
					WHEN '12h' THEN ?::timestamptz
					ELSE ?::timestamptz
				END
			)
			AND (
				p.interval = 'daily'
				OR p.interval = '3h'
				OR p.interval = '12h'
				OR (p.interval = 'weekly' AND p."on" = ?)
				OR (p.interval = 'monthly' AND p."on" = ?)
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

	now := time.Now()
	location := now.Location()
	dailyStart := PeriodStartForInterval("daily", now, location)
	weeklyStart := PeriodStartForInterval("weekly", now, location)
	monthlyStart := PeriodStartForInterval("monthly", now, location)
	threeHourStart := PeriodStartForInterval("3h", now, location)
	twelveHourStart := PeriodStartForInterval("12h", now, location)

	rawQuery := tx.Raw(sqlQuery,
		JobMessagePushToQueue,
		JobStatusInQueue, JobStatusInProgress,
		dailyStart, weeklyStart, monthlyStart, threeHourStart, twelveHourStart, dailyStart,
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

// GetJobsByIDsForUser loads multiple cron jobs for a user in one query.
func (r *CronJobRepository) GetJobsByIDsForUser(userID string, jobIDs []uint) ([]CronJobListingDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" || len(jobIDs) == 0 {
		return nil, nil
	}
	uniq := make([]uint, 0, len(jobIDs))
	seen := make(map[uint]struct{}, len(jobIDs))
	for _, id := range jobIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ? AND id IN ?", userID, uniq).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get jobs by ids for user: %w", err)
	}
	return rows, nil
}

// GetJobByIDForUser retrieves a specific cron job for a user (excludes placeholder token-holder rows).
func (r *CronJobRepository) GetJobByIDForUser(userID string, jobID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := r.db.Where("user_id = ? AND id = ?", userID, jobID).Where("COALESCE(placeholder, false) = ?", false).First(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron job for user: %v", db.Error)
	}

	return &res, nil
}

// =============================================================================
// ACTIVE: google_backup_credentials + input_data.credential_id
// =============================================================================
// One credential row per storj_project_id (unique). Many cron jobs (gmail, drive, …)
// per mailbox share the same credential_id in input_data.
//
// On cron run / API update the repo resolves:
//   - ResolvedStorxToken / GmailResolvedStorxToken → cred.storx_token, else job.storx_token
//   - GmailResolvedRefreshToken / ResolvedRefreshToken → cred.refresh_token, else job input_data
//   - ResolvedOAuthHolderEmail → cred.email when delegated mailbox ≠ holder
//
// On failure: DeactivateAllJobsForCredential (all linked jobs + optional clear cred tokens).
//
// LEGACY(parent_id) helpers below are commented out — do not delete (reference only).
// =============================================================================

// JobCredentialID returns google_backup_credentials.id from input_data.credential_id.
func JobCredentialID(job *CronJobListingDB) uint {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return 0
	}
	return credentialIDFromInputData(*job.InputData.Json())
}

// whereInputDataCredentialID matches cron jobs linked via input_data.credential_id (PostgreSQL jsonb).
const whereInputDataCredentialID = `(input_data->>'credential_id')::bigint = ?`

func credentialIDFromInputData(m map[string]interface{}) uint {
	switch v := m["credential_id"].(type) {
	case float64:
		if v > 0 {
			return uint(v)
		}
	case int:
		if v > 0 {
			return uint(v)
		}
	case uint:
		if v > 0 {
			return v
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return uint(n)
		}
	}
	return 0
}

// IsGoogleMediaOrGmailMethod is true for Google autosync methods that use shared credentials.
func IsGoogleMediaOrGmailMethod(method string) bool {
	switch method {
	case "gmail", "google_drive", "google_photos", "google_calendar", "google_contacts":
		return true
	default:
		return false
	}
}

// ResolvedOAuthHolderEmail returns the Google account that holds OAuth (credential email or mailbox).
func (r *CronJobRepository) ResolvedOAuthHolderEmail(job *CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if cid := JobCredentialID(job); cid > 0 && r.credRepo != nil {
		if cred, err := r.credRepo.GetByID(cid); err == nil && cred != nil {
			mailbox := strings.TrimSpace(job.Name)
			if job.InputData != nil && job.InputData.Json() != nil {
				if e, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(e) != "" {
					mailbox = strings.TrimSpace(e)
				}
			}
			return OAuthHolderEmail(cred, mailbox)
		}
	}
	return strings.TrimSpace(job.Name)
}

// GmailResolvedRefreshToken returns refresh_token from credential, else job input_data (legacy rows only).
func (r *CronJobRepository) GmailResolvedRefreshToken(job *CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if cid := JobCredentialID(job); cid > 0 && r.credRepo != nil {
		if cred, err := r.credRepo.GetByID(cid); err == nil && cred != nil {
			if t := strings.TrimSpace(cred.RefreshToken); t != "" {
				return t
			}
		}
	}
	if job.InputData != nil && job.InputData.Json() != nil {
		if s, ok := (*job.InputData.Json())["refresh_token"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// ResolvedRefreshToken resolves refresh_token for any Google autosync method.
func (r *CronJobRepository) ResolvedRefreshToken(job *CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if IsGoogleMediaOrGmailMethod(job.Method) {
		return r.GmailResolvedRefreshToken(job)
	}
	if job.InputData == nil || job.InputData.Json() == nil {
		return ""
	}
	if s, ok := (*job.InputData.Json())["refresh_token"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// GmailResolvedStorxToken returns storx_token from credential, else this job row (legacy rows without credential).
func (r *CronJobRepository) GmailResolvedStorxToken(job *CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if cid := JobCredentialID(job); cid > 0 && r.credRepo != nil {
		if cred, err := r.credRepo.GetByID(cid); err == nil && cred != nil {
			if t := strings.TrimSpace(cred.StorxToken); t != "" {
				return t
			}
		}
	}
	return strings.TrimSpace(job.StorxToken)
}

// ResolvedStorjProjectID returns storj_project_id from the linked credential.
func (r *CronJobRepository) ResolvedStorjProjectID(job *CronJobListingDB) string {
	if job == nil || r.credRepo == nil {
		return ""
	}
	cid := JobCredentialID(job)
	if cid == 0 {
		return ""
	}
	cred, err := r.credRepo.GetByID(cid)
	if err != nil || cred == nil {
		return ""
	}
	return strings.TrimSpace(cred.StorjProjectID)
}

// EnrichCronJobFromCredential fills API-only fields from google_backup_credentials.
// Tokens are not returned on credential-linked Google job rows (Satellite connected-account section).
func (r *CronJobRepository) EnrichCronJobFromCredential(job *CronJobListingDB) {
	if job == nil {
		return
	}
	if pid := r.ResolvedStorjProjectID(job); pid != "" {
		job.StorjProjectID = pid
	}
	if JobCredentialID(job) > 0 && IsGoogleMediaOrGmailMethod(job.Method) {
		job.StorxToken = ""
	}
}

// ResolvedStorxToken resolves storx_token for any Google autosync method.
func (r *CronJobRepository) ResolvedStorxToken(job *CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if IsGoogleMediaOrGmailMethod(job.Method) {
		return r.GmailResolvedStorxToken(job)
	}
	return strings.TrimSpace(job.StorxToken)
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

// FindJobForRestore returns the autosync cron job for a user, method, and mailbox login_id.
func (r *CronJobRepository) FindJobForRestore(userID, method, loginID string) (*CronJobListingDB, bool, error) {
	userID = strings.TrimSpace(userID)
	method = strings.TrimSpace(method)
	loginID = strings.TrimSpace(loginID)
	if userID == "" || method == "" || loginID == "" {
		return nil, false, nil
	}
	var job CronJobListingDB
	err := r.db.Where("user_id = ? AND method = ? AND deleted_at IS NULL", userID, method).
		Where("(name = ? OR (input_data->>'email') = ?)", loginID, loginID).
		Order("active DESC, id DESC").
		First(&job).Error
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("find job for restore: %w", err)
	}
	return &job, true, nil
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

// GetAccessGrantByProjectID returns storx_token from google_backup_credentials for project_id.
func (r *CronJobRepository) GetAccessGrantByProjectID(projectID, method string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project_id is required")
	}
	if r.credRepo == nil {
		return "", fmt.Errorf("credential repository not configured")
	}
	cred, ok, err := r.credRepo.GetByStorjProjectID(projectID)
	if err != nil {
		return "", fmt.Errorf("access grant not found for project_id %s: %w", projectID, err)
	}
	if !ok || cred == nil {
		return "", fmt.Errorf("access grant not found for project_id %s", projectID)
	}
	token := strings.TrimSpace(cred.StorxToken)
	if token == "" {
		return "", fmt.Errorf("access grant is empty for project_id %s", projectID)
	}
	if method != "" {
		var count int64
		if err := r.db.Model(&CronJobListingDB{}).
			Where(whereInputDataCredentialID, cred.ID).
			Where("method = ? AND active = ?", method, true).
			Count(&count).Error; err != nil {
			return "", fmt.Errorf("access grant lookup for project_id %s: %w", projectID, err)
		}
		if count == 0 {
			return "", fmt.Errorf("no active %s job for project_id %s", method, projectID)
		}
	}
	return token, nil
}

// ListJobsByCredentialID returns all cron jobs for a user sharing a credential.
func (r *CronJobRepository) ListJobsByCredentialID(userID string, credentialID uint) ([]CronJobListingDB, error) {
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ? AND "+whereInputDataCredentialID, userID, credentialID).
		Where("COALESCE(placeholder, false) = ?", false).
		Order("created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list jobs by credential_id: %w", err)
	}
	return rows, nil
}

// HasLinkedJobsForCredential reports whether a credential has any linked jobs for a user.
func (r *CronJobRepository) HasLinkedJobsForCredential(userID string, credentialID uint) (bool, error) {
	if credentialID == 0 {
		return false, nil
	}
	var exists bool
	err := r.db.Raw(`
SELECT EXISTS (
  SELECT 1 FROM cron_job_listing_dbs
  WHERE user_id = ?
    AND (input_data->>'credential_id')::bigint = ?
    AND deleted_at IS NULL
)`, userID, credentialID).Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("has linked jobs for credential: %w", err)
	}
	return exists, nil
}

// ListBackupMailboxEmailsForUser returns distinct mailbox emails with backup jobs for a user (all projects).
func (r *CronJobRepository) ListBackupMailboxEmailsForUser(userID, excludeEmail string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	var emails []string
	err := r.db.Table("cron_job_listing_dbs AS j").
		Select(`DISTINCT LOWER(TRIM(COALESCE(NULLIF(j.input_data->>'email', ''), j.name))) AS email`).
		Joins(`INNER JOIN google_backup_credential_dbs c ON (j.input_data->>'credential_id')::bigint = c.id AND c.deleted_at IS NULL`).
		Where("j.user_id = ? AND j.deleted_at IS NULL", userID).
		Where("COALESCE(j.placeholder, false) = ?", false).
		Order("email ASC").
		Pluck("email", &emails).Error
	if err != nil {
		return nil, fmt.Errorf("list backup mailbox emails for user: %w", err)
	}
	if exclude := strings.TrimSpace(strings.ToLower(excludeEmail)); exclude != "" {
		filtered := make([]string, 0, len(emails))
		for _, e := range emails {
			if strings.ToLower(strings.TrimSpace(e)) == exclude {
				continue
			}
			filtered = append(filtered, strings.TrimSpace(e))
		}
		emails = filtered
	}
	return emails, nil
}

// ListBackupMailboxEmailsForUserProject returns distinct mailbox emails with backup jobs for a project.
func (r *CronJobRepository) ListBackupMailboxEmailsForUserProject(userID, projectID, excludeEmail string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return nil, fmt.Errorf("user_id and project_id are required")
	}
	var emails []string
	err := r.db.Table("cron_job_listing_dbs AS j").
		Select(`DISTINCT LOWER(TRIM(COALESCE(NULLIF(j.input_data->>'email', ''), j.name))) AS email`).
		Joins(`INNER JOIN google_backup_credential_dbs c ON (j.input_data->>'credential_id')::bigint = c.id AND c.deleted_at IS NULL`).
		Where("j.user_id = ? AND j.deleted_at IS NULL AND c.storj_project_id = ?", userID, projectID).
		Where("COALESCE(j.placeholder, false) = ?", false).
		Order("email ASC").
		Pluck("email", &emails).Error
	if err != nil {
		return nil, fmt.Errorf("list backup mailbox emails: %w", err)
	}
	if exclude := strings.TrimSpace(strings.ToLower(excludeEmail)); exclude != "" {
		filtered := make([]string, 0, len(emails))
		for _, e := range emails {
			if strings.ToLower(strings.TrimSpace(e)) == exclude {
				continue
			}
			filtered = append(filtered, strings.TrimSpace(e))
		}
		emails = filtered
	}
	return emails, nil
}

// ListAllAutosyncJobsForUser returns every autosync job for a user.
func (r *CronJobRepository) ListAllAutosyncJobsForUser(userID string) ([]CronJobListingDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ?", userID).
		Order("name ASC, method ASC, id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list all autosync jobs for user: %w", err)
	}
	return rows, nil
}

// ListJobsGroupedByPolicyIDs bulk-loads jobs for many policies in one query.
func (r *CronJobRepository) ListJobsGroupedByPolicyIDs(userID string, policyIDs []uint) (map[uint][]CronJobListingDB, error) {
	out := make(map[uint][]CronJobListingDB)
	if len(policyIDs) == 0 {
		return out, nil
	}
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ? AND policy_id IN ?", userID, policyIDs).
		Order("policy_id ASC, created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list jobs grouped by policy ids: %w", err)
	}
	for i := range rows {
		pid := rows[i].PolicyID
		out[pid] = append(out[pid], rows[i])
	}
	return out, nil
}

// ListJobsByPolicyID returns all cron jobs for a user linked to a policy.
func (r *CronJobRepository) ListJobsByPolicyID(userID string, policyID uint) ([]CronJobListingDB, error) {
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ? AND policy_id = ?", userID, policyID).
		Order("created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list jobs by policy_id: %w", err)
	}
	return rows, nil
}

// GetJobByUserAndPolicyID returns one cron job for a user linked to a policy.
func (r *CronJobRepository) GetJobByUserAndPolicyID(userID string, policyID uint) (*CronJobListingDB, error) {
	var row CronJobListingDB
	err := r.db.Where("user_id = ? AND policy_id = ?", userID, policyID).
		Order("id ASC").
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get job by policy_id: %w", err)
	}
	return &row, nil
}

// UpdateActiveForCredential sets active state on all jobs linked to a credential for a user.
func (r *CronJobRepository) UpdateActiveForCredential(userID string, credentialID uint, active bool, patch map[string]interface{}) error {
	if credentialID == 0 {
		return nil
	}
	if patch == nil {
		patch = map[string]interface{}{"active": active}
	}
	return r.db.Model(&CronJobListingDB{}).
		Where("user_id = ? AND "+whereInputDataCredentialID, userID, credentialID).
		Where("COALESCE(placeholder, false) = ?", false).
		Updates(patch).Error
}

// DeactivateJobsForCredentialOrLegacyStorx deactivates all jobs for credential_id, else this job only.
func (r *CronJobRepository) DeactivateJobsForCredentialOrLegacyStorx(job *CronJobListingDB, message string) error {
	if job == nil {
		return nil
	}
	if cid := JobCredentialID(job); cid > 0 {
		return r.DeactivateAllJobsForCredential(cid, message, true)
	}
	job.Active = false
	job.AutoDeactivated = true
	job.StorxToken = ""
	job.Message = message
	job.MessageStatus = JobMessageStatusError
	patch := map[string]interface{}{
		"active":           false,
		"auto_deactivated": true,
		"storx_token":      "",
		"message":          message,
		"message_status":   JobMessageStatusError,
	}
	if job.Status != JobStatusSuccess {
		job.Status = JobStatusCancelled
		patch["status"] = JobStatusCancelled
	}
	return r.UpdateCronJobByID(job.ID, patch)
}

// DeactivateJobsForCredentialOrLegacyGoogleAuth clears credential tokens and deactivates linked jobs, else this job only.
func (r *CronJobRepository) DeactivateJobsForCredentialOrLegacyGoogleAuth(job *CronJobListingDB, message string) error {
	if job == nil {
		return nil
	}
	var credID uint
	if cid := JobCredentialID(job); cid > 0 {
		credID = cid
		if err := r.credRepo.ClearTokens(cid); err != nil {
			return err
		}
		if err := r.DeactivateAllJobsForCredential(cid, message, false); err != nil {
			return err
		}
	} else {
		StripGmailRefreshTokenFromCronJobInputData(job)
		job.Active = false
		job.AutoDeactivated = true
		job.Message = message
		job.MessageStatus = JobMessageStatusError
		patch := map[string]interface{}{
			"active":           false,
			"auto_deactivated": true,
			"message":          message,
			"message_status":   JobMessageStatusError,
		}
		if job.Status != JobStatusSuccess {
			job.Status = JobStatusCancelled
			patch["status"] = JobStatusCancelled
		}
		if job.InputData != nil {
			patch["input_data"] = job.InputData
		}
		if err := r.UpdateCronJobByID(job.ID, patch); err != nil {
			return err
		}
	}

	userID := strings.TrimSpace(job.UserID)
	email := strings.TrimSpace(job.Name)
	if credID > 0 && r.credRepo != nil {
		if cred, err := r.credRepo.GetByID(credID); err == nil && cred != nil {
			if e := strings.TrimSpace(cred.Email); e != "" {
				email = e
			}
		}
	}
	if userID != "" && email != "" {
		_ = satellite.ClearGoogleRefreshToken(context.Background(), userID, email)
	}
	return nil
}

// DeactivateAllActiveJobsForUser deactivates every active non-placeholder cron job for a user.
func (r *CronJobRepository) DeactivateAllActiveJobsForUser(userID, message string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Automatic backup deactivated because your CyberLS storage limit was exceeded. Please free up space and reactivate."
	}
	updates := map[string]interface{}{
		"active":           false,
		"auto_deactivated": true,
		"message":          message,
		"message_status":   JobMessageStatusError,
	}
	if err := r.db.Model(&CronJobListingDB{}).
		Where("user_id = ? AND active = ? AND COALESCE(placeholder, false) = ?", userID, true, false).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("deactivate active jobs for user: %w", err)
	}
	return r.db.Model(&CronJobListingDB{}).
		Where("user_id = ? AND COALESCE(placeholder, false) = ?", userID, false).
		Where("status <> ?", JobStatusSuccess).
		Updates(map[string]interface{}{"status": JobStatusCancelled}).Error
}

// DeactivateAllJobsForCredential deactivates every job with credential_id and optionally clears credential tokens.
func (r *CronJobRepository) DeactivateAllJobsForCredential(credentialID uint, message string, clearCredentialTokens bool) error {
	if credentialID == 0 {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Automatic backup deactivated due to credential error. Please update credentials and reactivate."
	}
	updates := map[string]interface{}{
		"active":           false,
		"auto_deactivated": true,
		"message":          message,
		"message_status":   JobMessageStatusError,
	}
	if err := r.db.Model(&CronJobListingDB{}).Where(whereInputDataCredentialID, credentialID).Updates(updates).Error; err != nil {
		return err
	}
	// StorX/credential deactivation: mark non-success jobs cancelled; successful jobs keep status=success.
	if err := r.db.Model(&CronJobListingDB{}).
		Where(whereInputDataCredentialID, credentialID).
		Where("status <> ?", JobStatusSuccess).
		Updates(map[string]interface{}{"status": JobStatusCancelled}).Error; err != nil {
		return err
	}
	if clearCredentialTokens && r.credRepo != nil {
		return r.credRepo.ClearTokens(credentialID)
	}
	return nil
}

// CreateCronJobForUserWithCredential creates a Google autosync job linked to a shared credential.
func (r *CronJobRepository) CreateCronJobForUserWithCredential(userID, name, method, syncType string, credentialID uint, inputData map[string]interface{}) (*CronJobListingDB, error) {
	if credentialID == 0 {
		return nil, fmt.Errorf("credential_id is required")
	}
	if inputData == nil {
		inputData = map[string]interface{}{}
	}
	inputData["email"] = strings.TrimSpace(name)
	inputData["credential_id"] = credentialID
	data := CronJobListingDB{
		UserID:      userID,
		Name:        name,
		Method:      method,
		SyncType:    syncType,
		InputData:   database.NewDbJsonFromValue(inputData),
		Status:      JobStatusCreated,
		LastRun:     nil,
		Placeholder: false,
	}
	if syncType == "one_time" {
		data.Interval = "one_time"
		data.Active = true
	}
	res := r.db.Create(&data)
	if res != nil && res.Error != nil {
		return nil, fmt.Errorf("error creating cron job: %v", res.Error)
	}
	return &data, nil
}

// CreateCronJobForUser creates a new cron job for a user (Google autosync: use CreateCronJobForUserWithCredential).
func (r *CronJobRepository) CreateCronJobForUser(userID, name, method string, syncType string, inputData map[string]interface{}) (*CronJobListingDB, error) {
	return r.CreateCronJobForUserWithPlaceholder(userID, name, method, syncType, inputData, false)
}

// CreateCronJobForUserWithPlaceholder creates a cron job; when placeholder is true the row is kept inactive and hidden from listings.
func (r *CronJobRepository) CreateCronJobForUserWithPlaceholder(userID, name, method string, syncType string, inputData map[string]interface{}, placeholder bool) (*CronJobListingDB, error) {
	data := CronJobListingDB{
		UserID:      userID,
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

// stripDeprecatedCronJobColumnUpdates removes fields no longer stored on cron_job_listing_dbs.
func stripDeprecatedCronJobColumnUpdates(m map[string]interface{}) {
	delete(m, "parent_id")
	delete(m, "storj_project_id")
}

// UpdateCronJobByID updates a cron job by ID
func (r *CronJobRepository) UpdateCronJobByID(ID uint, m map[string]interface{}) error {
	stripDeprecatedCronJobColumnUpdates(m)
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
	stripDeprecatedCronJobColumnUpdates(fields)
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
			"status":           true,
			"message":          true,
			"message_status":   true,
			"last_run":         true,
			"input_data":       true, // e.g. cleared oauth refresh_token after auth failure
			"failure_periods":  true,
			"active":           true,
			"auto_deactivated": true,
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
	if mailbox == "" || strings.EqualFold(mailbox, "me") {
		return false
	}
	subject := mailbox
	// DISABLED(parent_id): connected := GmailConnectedAccountEmail(job)
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
func (r *CronJobRepository) scheduleForActivation(job *CronJobListingDB) (interval, on string) {
	if job == nil {
		return "", ""
	}
	interval = strings.TrimSpace(job.Interval)
	var row struct {
		Interval string
		On       string
	}
	err := r.db.Table("autosync_backup_policy_dbs").
		Select(`interval, "on"`).
		Where("id = ? AND deleted_at IS NULL", job.PolicyID).
		First(&row).Error
	if err == nil {
		if strings.TrimSpace(row.Interval) != "" {
			interval = strings.TrimSpace(row.Interval)
		}
		return interval, strings.TrimSpace(row.On)
	}
	return interval, ""
}

func (r *CronJobRepository) validateJobForActivation(job *CronJobListingDB) error {
	if job == nil || job.PolicyID == 0 {
		return fmt.Errorf("policy_id is required when activating backup")
	}
	var policy AutosyncBackupPolicyDB
	if err := r.db.Where("id = ? AND deleted_at IS NULL", job.PolicyID).First(&policy).Error; err != nil {
		return fmt.Errorf("policy not found for activation: %w", err)
	}
	if IsPolicyExpired(&policy, time.Now().UTC()) {
		return fmt.Errorf("policy is expired; update retention policy before activating this job")
	}
	// Validate required fields for activation
	storx := r.ResolvedStorxToken(job)
	if strings.TrimSpace(storx) == "" {
		return fmt.Errorf("storx_token is required when activating backup")
	}
	interval, on := r.scheduleForActivation(job)
	if interval == "" {
		return fmt.Errorf("interval is required when activating backup")
	}
	// Hourly schedules use empty on; daily/weekly/monthly require on.
	if on == "" && interval != "3h" && interval != "12h" {
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
	case "outlook", "google_drive", "google_photos", "google_calendar", "google_contacts":
		rt := r.ResolvedRefreshToken(job)
		if strings.TrimSpace(rt) == "" {
			return fmt.Errorf("refresh_token is required in input_data for %s method", job.Method)
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

// AutoSyncJobStats is mailbox counts + last snapshot time for GET /autosync/stats.
type AutoSyncJobStats struct {
	ConnectedAccounts           int
	ConnectedAccountsGrowthWeek int
	LastSyncAt                  *time.Time
}

var autosyncStatsWorkspaceMethods = []string{
	"gmail", "google_drive", "google_photos", "google_contacts", "google_calendar",
}

// GetAutoSyncJobStats loads mailbox counts in one SQL round trip (no full job rows).
func (r *CronJobRepository) GetAutoSyncJobStats(userID string) (*AutoSyncJobStats, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}

	const q = `
WITH mailbox_first AS (
  SELECT
    LOWER(TRIM(COALESCE(NULLIF(TRIM(input_data->>'email'), ''), name))) AS mailbox,
    MIN(created_at) AS first_created
  FROM cron_job_listing_dbs
  WHERE user_id = ?
    AND deleted_at IS NULL
    AND COALESCE(placeholder, false) = false
    AND method IN ?
    AND TRIM(COALESCE(NULLIF(TRIM(input_data->>'email'), ''), name)) <> ''
  GROUP BY 1
)
SELECT
  COALESCE((SELECT COUNT(*)::bigint FROM mailbox_first), 0) AS connected_accounts,
  COALESCE((SELECT COUNT(*)::bigint FROM mailbox_first WHERE first_created > NOW() - INTERVAL '7 days'), 0) AS growth_week,
  (SELECT MAX(last_run) FROM cron_job_listing_dbs
    WHERE user_id = ? AND deleted_at IS NULL AND COALESCE(placeholder, false) = false) AS last_sync_at`

	var row struct {
		ConnectedAccounts int64
		GrowthWeek        int64
		LastSyncAt        sql.NullTime
	}
	if err := r.db.Raw(q, userID, autosyncStatsWorkspaceMethods, userID).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("autosync job stats: %w", err)
	}

	out := &AutoSyncJobStats{
		ConnectedAccounts:           int(row.ConnectedAccounts),
		ConnectedAccountsGrowthWeek: int(row.GrowthWeek),
	}
	if row.LastSyncAt.Valid {
		t := row.LastSyncAt.Time
		out.LastSyncAt = &t
	}
	return out, nil
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

// =============================================================================
// LEGACY(parent_id) — commented out; replaced by google_backup_credentials.
// =============================================================================
/*
func (r *CronJobRepository) GetJobsByUserAndParentIDAndMethod(userID, parentID, method string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := r.db.Where("user_id = ? AND parent_id = ? AND method = ?", userID, parentID, method).Order("created_at DESC").Find(&res)
	...
}

func (r *CronJobRepository) FindGmailJobByUserNameSyncType(userID, name, syncType string) (*CronJobListingDB, bool, error) { ... }

func (r *CronJobRepository) FindGmailJobsByUserSyncTypeAndNames(userID, syncType string, names []string) (map[string]*CronJobListingDB, error) { ... }

func GmailConnectedAccountEmail(job *CronJobListingDB) string { ... }

func (r *CronJobRepository) GmailParentRowForCorporateChild(job *CronJobListingDB) (*CronJobListingDB, error) { ... }

func (r *CronJobRepository) gmailResolvedStringFromLocalOrParent(job *CronJobListingDB, local string, fromParent func(*CronJobListingDB) string) string { ... }

func (r *CronJobRepository) GmailAdminJobForSharedStorx(job *CronJobListingDB) (*CronJobListingDB, error) { ... }

func (r *CronJobRepository) DeactivateGmailWorkspaceTreeForStorxFailure(job *CronJobListingDB, message string) error { ... }

func (r *CronJobRepository) DeactivateGmailWorkspaceTreeForGoogleAuthFailure(job *CronJobListingDB, message string) error { ... }
*/
