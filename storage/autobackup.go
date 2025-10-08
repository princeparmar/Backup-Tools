package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// Add this type to handle the InputData field
type JSONMap map[string]interface{}

// Add Scanner implementation for JSONMap
func (m *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*m = nil
		return nil
	}

	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("unsupported type for JSONMap: %T", value)
	}

	return json.Unmarshal(data, m)
}

// Models for automated storage
type CronJobListingDB struct {
	gorm.Model

	UserID string `json:"user_id" gorm:"uniqueIndex:idx_name_method_user"`

	// In this table Name + Method + UserID should be unique
	Name     string    `json:"name" gorm:"uniqueIndex:idx_name_method_user"`
	Method   string    `json:"method" gorm:"uniqueIndex:idx_name_method_user"`
	Interval string    `json:"interval"`
	On       string    `json:"on"`
	LastRun  time.Time `json:"last_run"`

	// Change the type from map[string]interface{} to JSONMap
	InputData JSONMap `json:"input_data" gorm:"type:jsonb"`

	StorxToken string `json:"storx_token"`

	// Message will be the message to be displayed to the user
	// push to queue
	// task is running
	// task is completed for %d email|photos|files for (today|this week|this month)
	// task failed with error %s
	Message string `json:"message"`

	// MessageStatus will be one of the following: "info", "warning", "error"
	MessageStatus string `json:"message_status"`
	Active        bool   `json:"active"`

	// Memory will be used to store the state of the task. this will be json field
	TaskMemory TaskMemory `json:"task_memory" gorm:"type:jsonb"`

	// Tasks associated with the cron job
	Tasks []TaskListingDB `gorm:"foreignKey:CronJobID"`
}

// Add a Scanner interface implementation for InputData if needed
func (c *CronJobListingDB) ScanJSON(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("failed to unmarshal JSONB value")
	}

	var temp map[string]interface{}
	err := json.Unmarshal(bytes, &temp)
	if err != nil {
		return err
	}

	c.InputData = temp
	return nil
}

func MastTokenForCronJobListingDB(cronJobs []CronJobListingDB) []CronJobListingDB {
	for i := range cronJobs {
		MastTokenForCronJobDB(&cronJobs[i])
	}

	return cronJobs
}

func MastTokenForCronJobDB(cronJob *CronJobListingDB) {
	cronJob.StorxToken = utils.MaskString(cronJob.StorxToken)
	if cronJob.InputData != nil && cronJob.InputData["refresh_token"] != nil {
		cronJob.InputData["refresh_token"] = utils.MaskString(cronJob.InputData["refresh_token"].(string))
	}
}

type TaskMemory struct {
	GmailNextToken *string `json:"gmail_next_token"`
	GmailSyncCount uint    `json:"gmail_sync_count"`

	OutlookSyncCount uint `json:"outlook_sync_count"`
	OutlookSkipCount uint `json:"outlook_skip_count"`
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

type TaskListingDB struct {
	gorm.Model

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

func (storage *PosgresStore) GetAllCronJobs() ([]CronJobListingDB, error) {
	start := time.Now()

	var res []CronJobListingDB
	db := storage.DB.Find(&res)
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_get_all_cron_jobs_failed", "storage")
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_all_cron_jobs_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_all_cron_jobs_total", 1, "database", "postgres", "status", "success")
	prometheus.RecordCounter("postgres_cron_jobs_retrieved_total", int64(len(res)), "database", "postgres")

	return res, nil
}

func (storage *PosgresStore) GetAllCronJobsForUser(userID string) ([]CronJobListingDB, error) {
	start := time.Now()

	var res []CronJobListingDB
	db := storage.DB.Where("user_id = ?", userID).Find(&res)
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_get_cron_jobs_for_user_failed", "storage")
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_cron_jobs_for_user_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_cron_jobs_for_user_total", 1, "database", "postgres", "status", "success")
	prometheus.RecordCounter("postgres_user_cron_jobs_retrieved_total", int64(len(res)), "database", "postgres")

	return res, nil
}

type LiveCronJobListingDB struct {
	ID               uint                `json:"id"`
	Name             string              `json:"name"`
	Method           string              `json:"method"`
	Message          string              `json:"message"`
	MessageStatus    string              `json:"message_status"`
	Active           bool                `json:"active"`
	GmailSyncCount   uint                `json:"gmail_sync_count"`
	OutlookSyncCount uint                `json:"outlook_sync_count"`
	Tasks            []LiveTaskListingDB `json:"tasks"`
}

type LiveTaskListingDB struct {
	StartTime  *time.Time `json:"start_time"`
	Status     string     `json:"status"`
	RetryCount uint       `json:"retry_count"`
	Execution  uint64     `json:"execution"`
}

func (storage *PosgresStore) GetAllActiveCronJobsForUser(userID string) ([]LiveCronJobListingDB, error) {
	start := time.Now()
	// Query to get active cron jobs with their failed/running tasks in one go
	query := `
		SELECT 
			cj.id,cj.name,cj.method,cj.message,cj.message_status,cj.active,
			COALESCE((cj.task_memory->>'gmail_sync_count')::int, 0) as gmail_sync_count,
			COALESCE((cj.task_memory->>'outlook_sync_count')::int, 0) as outlook_sync_count,
			t.start_time,t.status,t.retry_count,t.execution
		FROM cron_job_listing_dbs cj
		LEFT JOIN task_listing_dbs t ON cj.id = t.cron_job_id AND t.status IN ('failed', 'running')
		WHERE cj.user_id = $1 
		AND cj.deleted_at IS NULL
		ORDER BY cj.id, t.start_time DESC`

	rows, err := storage.DB.Raw(query, userID).Rows()
	if err != nil {
		prometheus.RecordError("postgres_get_active_cron_jobs_for_user_failed", "storage")
		return nil, fmt.Errorf("error executing query: %v", err)
	}
	defer rows.Close()

	// Map to group tasks by cron job
	cronJobsMap := make(map[uint]*LiveCronJobListingDB)
	var results []LiveCronJobListingDB

	for rows.Next() {
		var (
			cronJobID        uint
			name             string
			method           string
			message          string
			messageStatus    string
			active           bool
			gmailSyncCount   uint
			outlookSyncCount uint
			startTime        *time.Time
			status           *string
			retryCount       *uint
			execution        *uint64
		)

		err := rows.Scan(
			&cronJobID, &name, &method, &message, &messageStatus, &active,
			&gmailSyncCount, &outlookSyncCount, &startTime, &status, &retryCount, &execution,
		)
		if err != nil {
			prometheus.RecordError("postgres_scan_active_cron_jobs_failed", "storage")
			return nil, fmt.Errorf("error scanning row: %v", err)
		}

		// Create or get cron job entry
		if _, exists := cronJobsMap[cronJobID]; !exists {
			cronJobsMap[cronJobID] = &LiveCronJobListingDB{
				ID:               cronJobID,
				Name:             name,
				Method:           method,
				Message:          message,
				MessageStatus:    messageStatus,
				Active:           active,
				GmailSyncCount:   gmailSyncCount,
				OutlookSyncCount: outlookSyncCount,
				Tasks:            []LiveTaskListingDB{},
			}
		}

		// Add task if it exists (status will be non-null for failed/running tasks)
		if status != nil && startTime != nil {
			task := LiveTaskListingDB{
				StartTime:  startTime,
				Status:     *status,
				RetryCount: *retryCount,
				Execution:  *execution,
			}
			cronJobsMap[cronJobID].Tasks = append(cronJobsMap[cronJobID].Tasks, task)
		}
	}

	if err := rows.Err(); err != nil {
		prometheus.RecordError("postgres_rows_iteration_failed", "storage")
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	// Convert map to slice and filter out cron jobs without tasks
	for _, cronJob := range cronJobsMap {
		if len(cronJob.Tasks) > 0 {
			results = append(results, *cronJob)
		}
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_active_cron_jobs_for_user_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_active_cron_jobs_for_user_total", 1, "database", "postgres", "status", "success")
	prometheus.RecordCounter("postgres_user_active_cron_jobs_retrieved_total", int64(len(results)), "database", "postgres")

	return results, nil
}

func (storage *PosgresStore) UpdateHeartBeatForTask(ID uint) error {
	start := time.Now()

	db := storage.DB.Model(&TaskListingDB{}).Where("id = ?", ID).Update("last_heart_beat", time.Now())
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_update_heartbeat_failed", "storage")
		return fmt.Errorf("error updating heartbeat for task: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_update_heartbeat_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_update_heartbeat_total", 1, "database", "postgres", "status", "success")

	return nil
}

// MissedHeartbeatForTask updates the heartbeat for the task if it has not been updated for more than 10 minutes
func (storage *PosgresStore) MissedHeartbeatForTask() error {
	start := time.Now()

	// start a transaction, select all tasks with lock where last_heart_beat is more than 1 minute ago
	// update status to failed and message to "missed heartbeat"
	// and for job set message to process got stuck because of some reason
	// and message status to error
	tx := storage.DB.Begin()
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

		db = tx.Save(&job)
		if db != nil && db.Error != nil {
			tx.Rollback()
			return fmt.Errorf("error updating job: %v", db.Error)
		}
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		prometheus.RecordError("postgres_missed_heartbeat_commit_failed", "storage")
		return fmt.Errorf("error committing transaction: %v", err.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_missed_heartbeat_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_missed_heartbeat_total", 1, "database", "postgres", "status", "success")
	prometheus.RecordCounter("postgres_tasks_marked_failed_total", int64(len(tasks)), "database", "postgres", "reason", "missed_heartbeat")

	return nil
}

// GetJobsToProcess gives the jobs that are to be processed next
func (storage *PosgresStore) GetJobsToProcess() ([]CronJobListingDB, error) {
	start := time.Now()

	var res []CronJobListingDB
	tx := storage.DB.Begin()

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
	db := tx.Raw(sqlQuery, JobMessagePushToQueue, time.Now().Format("2006-01-02"),
		time.Now().Weekday().String(),
		fmt.Sprint(time.Now().Day()),
		TaskStatusRunning, TaskStatusPushed).Scan(&res)
	if db.Error != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_get_jobs_to_process_failed", "storage")
		return nil, fmt.Errorf("error getting jobs to process: %v", db.Error)
	}

	// update message to "push to queue" and message status to "info"
	for i := range res {
		res[i].Message = JobMessagePushToQueue
		res[i].MessageStatus = JobMessageStatusInfo

		db = tx.Save(&res[i])
		if db != nil && db.Error != nil {
			tx.Rollback()
			prometheus.RecordError("postgres_update_cron_job_in_get_jobs_failed", "storage")
			return nil, fmt.Errorf("error updating cron job: %v", db.Error)
		}
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		prometheus.RecordError("postgres_get_jobs_to_process_commit_failed", "storage")
		return nil, fmt.Errorf("error committing transaction: %v", err.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_jobs_to_process_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_jobs_to_process_total", 1, "database", "postgres", "status", "success")
	prometheus.RecordCounter("postgres_jobs_ready_for_processing_total", int64(len(res)), "database", "postgres")

	return res, nil
}

func (storage *PosgresStore) GetJobByIDForUser(userID string, jobID uint) (*CronJobListingDB, error) {
	start := time.Now()

	var res CronJobListingDB
	db := storage.DB.Where("user_id = ? AND id = ?", userID, jobID).First(&res)
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_get_job_by_id_for_user_failed", "storage")
		return nil, fmt.Errorf("error getting cron job for user: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_job_by_id_for_user_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_job_by_id_for_user_total", 1, "database", "postgres", "status", "success")

	return &res, nil
}

// GetPushedTask gives pushed task and update the status to running and set start time with table locking.
func (storage *PosgresStore) GetPushedTask() (*TaskListingDB, error) {
	start := time.Now()

	var res TaskListingDB
	tx := storage.DB.Begin()
	// lock table tasks for update and select and return the first row with status pushed
	// or status 'failed' and retry count less than 3
	db := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Joins("JOIN cron_job_listing_dbs ON task_listing_dbs.cron_job_id = cron_job_listing_dbs.id").
		Where("cron_job_listing_dbs.active = ? AND (task_listing_dbs.status = ? OR (task_listing_dbs.status = ? AND task_listing_dbs.retry_count < ?))",
			true, TaskStatusPushed, TaskStatusFailed, MaxRetryCount).
		First(&res)
	if db.Error != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_get_pushed_task_failed", "storage")
		return nil, fmt.Errorf("error getting pushed task: %v", db.Error)
	}

	// Update status to running and set start time
	res.Status = TaskStatusRunning
	startTime := time.Now()
	res.StartTime = &startTime
	res.LastHeartBeat = &startTime
	res.Message = "Automatic backup started"

	db = tx.Save(&res)
	if db != nil && db.Error != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_update_pushed_task_status_failed", "storage")
		return nil, fmt.Errorf("error updating pushed task status: %v", db.Error)
	}

	var job CronJobListingDB
	db = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", res.CronJobID).First(&job)
	if db.Error != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_get_job_for_pushed_task_failed", "storage")
		return nil, fmt.Errorf("error getting job: %v", db.Error)
	}

	job.Message = "Automatic backup started"
	job.MessageStatus = JobMessageStatusInfo

	db = tx.Save(&job)
	if db != nil && db.Error != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_update_job_for_pushed_task_failed", "storage")
		return nil, fmt.Errorf("error updating pushed task status: %v", db.Error)
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		prometheus.RecordError("postgres_get_pushed_task_commit_failed", "storage")
		return nil, fmt.Errorf("error committing transaction: %v", err.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_pushed_task_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_pushed_task_total", 1, "database", "postgres", "status", "success")

	return &res, nil
}

func (storage *PosgresStore) GetTaskByID(ID uint) (*TaskListingDB, error) {
	start := time.Now()

	var res TaskListingDB
	db := storage.DB.First(&res, ID)
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_get_task_by_id_failed", "storage")
		return nil, fmt.Errorf("error getting task by ID: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_task_by_id_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_task_by_id_total", 1, "database", "postgres", "status", "success")

	return &res, nil
}

func (storage *PosgresStore) CreateTaskForCronJob(cronJobID uint) (*TaskListingDB, error) {
	start := time.Now()

	data := TaskListingDB{
		CronJobID: cronJobID,
		Status:    TaskStatusPushed,
	}

	// create new entry in database and return newly created task
	res := storage.DB.Create(&data)
	if res != nil && res.Error != nil {
		prometheus.RecordError("postgres_create_task_failed", "storage")
		return nil, fmt.Errorf("error creating task for cron job: %v", res.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_create_task_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_create_task_total", 1, "database", "postgres", "status", "success")

	return &data, nil
}

func (storage *PosgresStore) UpdateTaskByID(ID uint, m map[string]interface{}) error {
	start := time.Now()

	// update task by ID
	// create a transaction to update the task and check if m["status"] == "failed"
	// then update "retry_count" and "status"
	tx := storage.DB.Begin()

	db := tx.Model(&TaskListingDB{}).Where("id = ?", ID).Updates(m)
	if db != nil && db.Error != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_update_task_by_id_failed", "storage")
		return fmt.Errorf("error updating task by ID: %v", db.Error)
	}

	if m["status"] == TaskStatusFailed {
		db = tx.Model(&TaskListingDB{}).Where("id = ?", ID).Update("retry_count", gorm.Expr("retry_count + 1"))
		if db != nil && db.Error != nil {
			tx.Rollback()
			prometheus.RecordError("postgres_update_task_retry_count_failed", "storage")
			return fmt.Errorf("error updating task by ID: %v", db.Error)
		}
		prometheus.RecordCounter("postgres_task_retry_total", 1, "database", "postgres", "task_id", fmt.Sprintf("%d", ID))
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		prometheus.RecordError("postgres_update_task_by_id_commit_failed", "storage")
		return fmt.Errorf("error committing transaction: %v", err.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_update_task_by_id_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_update_task_by_id_total", 1, "database", "postgres", "status", "success")

	return nil

}

func (storage *PosgresStore) GetCronJobByID(ID uint) (*CronJobListingDB, error) {
	start := time.Now()

	var res CronJobListingDB
	db := storage.DB.First(&res, ID)
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_get_cron_job_by_id_failed", "storage")
		return nil, fmt.Errorf("error getting cron job by ID: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_get_cron_job_by_id_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_get_cron_job_by_id_total", 1, "database", "postgres", "status", "success")

	return &res, nil
}

func (storage *PosgresStore) CreateCronJobForUser(userID, name, method string, inputData map[string]interface{}) (*CronJobListingDB, error) {
	start := time.Now()

	data := CronJobListingDB{
		UserID:    userID,
		Name:      name,
		Method:    method,
		InputData: inputData,
	}
	// create new entry in database and return newly created cron job
	res := storage.DB.Create(&data)
	if res != nil && res.Error != nil {
		prometheus.RecordError("postgres_create_cron_job_failed", "storage")
		return nil, fmt.Errorf("error creating cron job: %v", res.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_create_cron_job_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_create_cron_job_total", 1, "database", "postgres", "status", "success", "method", method)

	return &data, nil
}

func (storage *PosgresStore) DeleteCronJobByID(ID uint) error {
	start := time.Now()

	res := storage.DB.Delete(&CronJobListingDB{}, ID)
	if res != nil && res.Error != nil {
		prometheus.RecordError("postgres_delete_cron_job_failed", "storage")
		return fmt.Errorf("error deleting cron job: %v", res.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_delete_cron_job_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_delete_cron_job_total", 1, "database", "postgres", "status", "success")

	return nil
}

func (storage *PosgresStore) UpdateCronJobByID(ID uint, m map[string]interface{}) error {
	start := time.Now()

	tx := storage.DB.Begin()
	if tx.Error != nil {
		prometheus.RecordError("postgres_update_cron_job_transaction_start_failed", "storage")
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
		prometheus.RecordError("postgres_update_cron_job_failed", "storage")
		return fmt.Errorf("error updating cron job: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		prometheus.RecordError("postgres_update_cron_job_not_found", "storage")
		return fmt.Errorf("no cron job found with id %d", ID)
	}

	// Get the updated cron job
	var updatedJob CronJobListingDB
	if err := tx.First(&updatedJob, ID).Error; err != nil {
		prometheus.RecordError("postgres_get_updated_cron_job_failed", "storage")
		return fmt.Errorf("error getting updated cron job: %w", err)
	}

	// Validate activation if the job is being activated
	if active, exists := m["active"]; exists && active == true {
		if err := storage.validateJobForActivation(&updatedJob); err != nil {
			prometheus.RecordError("postgres_cron_job_activation_validation_failed", "storage")
			return err
		}
		prometheus.RecordCounter("postgres_cron_job_activated_total", 1, "database", "postgres", "method", updatedJob.Method)
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		prometheus.RecordError("postgres_update_cron_job_commit_failed", "storage")
		return fmt.Errorf("error committing transaction: %w", err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_update_cron_job_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_update_cron_job_total", 1, "database", "postgres", "status", "success")

	committed = true
	return nil
}

func (storage *PosgresStore) ListAllTasksByJobID(ID, limit, offset uint) ([]TaskListingDB, error) {
	start := time.Now()

	var res []TaskListingDB
	db := storage.DB.Where("cron_job_id = ?", ID).Limit(int(limit)).Offset(int(offset)).Order("created_at DESC").Find(&res)
	if db != nil && db.Error != nil {
		prometheus.RecordError("postgres_list_tasks_by_job_id_failed", "storage")
		return nil, fmt.Errorf("error getting tasks for job: %v", db.Error)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_list_tasks_by_job_id_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_list_tasks_by_job_id_total", 1, "database", "postgres", "status", "success")
	prometheus.RecordCounter("postgres_tasks_listed_total", int64(len(res)), "database", "postgres", "job_id", fmt.Sprintf("%d", ID))

	return res, nil
}

// DeleteAllJobsAndTasksByEmail deletes all jobs and related tasks for a user by email
// Returns the list of deleted job IDs and task IDs
func (storage *PosgresStore) DeleteAllJobsAndTasksByEmail(email string) ([]uint, []uint, error) {
	start := time.Now()

	// Start a transaction to ensure atomicity
	tx := storage.DB.Begin()
	if tx.Error != nil {
		prometheus.RecordError("postgres_delete_jobs_by_email_transaction_start_failed", "storage")
		return nil, nil, fmt.Errorf("error starting transaction: %v", tx.Error)
	}

	// First, get all job IDs for this email before deleting
	var jobs []CronJobListingDB
	if err := tx.Where("name = ?", email).Find(&jobs).Error; err != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_get_jobs_for_email_failed", "storage")
		return nil, nil, fmt.Errorf("error getting jobs for email: %v", err)
	}

	if len(jobs) == 0 {
		tx.Rollback()
		prometheus.RecordError("postgres_no_jobs_found_for_email", "storage")
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
			prometheus.RecordError("postgres_get_tasks_for_job_failed", "storage")
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
			prometheus.RecordError("postgres_delete_tasks_failed", "storage")
			return nil, nil, fmt.Errorf("error deleting tasks: %v", err)
		}
		prometheus.RecordCounter("postgres_tasks_deleted_total", int64(len(taskIDs)), "database", "postgres", "reason", "email_cleanup")
	}

	// Delete all jobs for the email (hard delete)
	if err := tx.Exec("DELETE FROM cron_job_listing_dbs WHERE name = ?", email).Error; err != nil {
		tx.Rollback()
		prometheus.RecordError("postgres_delete_jobs_for_email_failed", "storage")
		return nil, nil, fmt.Errorf("error deleting jobs for email: %v", err)
	}
	prometheus.RecordCounter("postgres_jobs_deleted_total", int64(len(deletedJobIDs)), "database", "postgres", "reason", "email_cleanup")

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		prometheus.RecordError("postgres_delete_jobs_by_email_commit_failed", "storage")
		return nil, nil, fmt.Errorf("error committing transaction: %v", err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("postgres_delete_jobs_by_email_duration", duration, "database", "postgres")
	prometheus.RecordCounter("postgres_delete_jobs_by_email_total", 1, "database", "postgres", "status", "success")

	return deletedJobIDs, taskIDs, nil
}

// ValidateJobForActivation checks if the job has all required fields and authentication tokens for activation
func (storage *PosgresStore) validateJobForActivation(job *CronJobListingDB) error {
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
	inputData := job.InputData

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
