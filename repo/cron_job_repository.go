package repo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
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

	UserID string `json:"user_id"`

	// Name + SyncType should be unique
	Name     string    `json:"name" gorm:"uniqueIndex:idx_name_sync_type"`
	Method   string    `json:"method"`
	Interval string    `json:"interval"`
	On       string    `json:"on"`
	LastRun  time.Time `json:"last_run"`

	// Change the type from map[string]interface{} to *database.DbJson[map[string]interface{}]
	InputData *database.DbJson[map[string]interface{}] `json:"input_data" gorm:"type:jsonb"`

	StorxToken string `json:"storx_token"`

	Message string `json:"message"`

	// MessageStatus will be one of the following: "info", "warning", "error"
	MessageStatus string `json:"message_status"`
	Active        bool   `json:"active"`

	// Memory will be used to store the state of the task. this will be json field
	TaskMemory TaskMemory `json:"task_memory" gorm:"type:jsonb"`

	// Tasks associated with the cron job
	Tasks []TaskListingDB `gorm:"foreignKey:CronJobID"`

	SyncType string `json:"sync_type" gorm:"uniqueIndex:idx_name_sync_type"`
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

// GetAllCronJobs retrieves all cron jobs
func (r *CronJobRepository) GetAllCronJobs() ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := r.db.Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs: %v", db.Error)
	}

	return res, nil
}

// GetAllCronJobsForUser retrieves all cron jobs for a specific user
func (r *CronJobRepository) GetAllCronJobsForUser(userID string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := r.db.Where("user_id = ?", userID).Find(&res)
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
		LEFT JOIN task_listing_dbs t ON cj.id = t.cron_job_id AND t.status IN ('failed', 'running')
		WHERE cj.user_id = $1 
		AND cj.deleted_at IS NULL
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

	// The raw SQL query
	sqlQuery := `
		SELECT *
		FROM cron_job_listing_dbs
		WHERE active = true
		AND (message is null or message != ?)
		AND DATE(last_run) != ?
		AND (interval = 'daily'
				OR (interval = 'weekly' AND "on" = ?)
			OR (interval = 'monthly' AND "on" = ?))
		AND id not in (
			SELECT DISTINCT cron_job_id FROM task_listing_dbs
			WHERE status IN (?, ?)
		)
		AND deleted_at is null
		LIMIT 10
		FOR UPDATE
	`

	// Execute the raw SQL query and store the result in the cronJobs slice
	rawQuery := tx.Raw(sqlQuery, JobMessagePushToQueue, time.Now().Format("2006-01-02"),
		time.Now().Weekday().String(),
		fmt.Sprint(time.Now().Day()),
		TaskStatusRunning, TaskStatusPushed)

	scanResult := rawQuery.Scan(&res)
	if scanResult.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting jobs to process: %v", scanResult.Error)
	}

	// update message to "push to queue" and message status to "info"
	for i := range res {
		res[i].Message = JobMessagePushToQueue
		res[i].MessageStatus = JobMessageStatusInfo

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

// GetJobByIDForUser retrieves a specific cron job by ID for a user
func (r *CronJobRepository) GetJobByIDForUser(userID string, jobID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := r.db.Where("user_id = ? AND id = ?", userID, jobID).First(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron job for user: %v", db.Error)
	}

	return &res, nil
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

// CreateCronJobForUser creates a new cron job for a user
func (r *CronJobRepository) CreateCronJobForUser(userID, name, method string, syncType string, inputData map[string]interface{}) (*CronJobListingDB, error) {
	data := CronJobListingDB{
		UserID:    userID,
		Name:      name,
		Method:    method,
		SyncType:  syncType,
		InputData: database.NewDbJsonFromValue(inputData),
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

// ValidateJobForActivation checks if the job has all required fields and authentication tokens for activation
func (r *CronJobRepository) validateJobForActivation(job *CronJobListingDB) error {
	// Validate required fields for activation
	if job.StorxToken == "" {
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
		// Check if refresh_token exists in input_data
		if refreshToken, exists := inputData["refresh_token"]; !exists || refreshToken == "" {
			return fmt.Errorf("refresh_token is required in input_data for gmail method")
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
