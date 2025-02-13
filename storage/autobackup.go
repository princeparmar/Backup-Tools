package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/utils"
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
	var res []CronJobListingDB
	db := storage.DB.Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}
	return res, nil
}

func (storage *PosgresStore) GetAllCronJobsForUser(userID string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := storage.DB.Where("user_id = ?", userID).Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}
	return res, nil
}

func (storage *PosgresStore) UpdateHeartBeatForTask(ID uint) error {
	db := storage.DB.Model(&TaskListingDB{}).Where("id = ?", ID).Update("last_heart_beat", time.Now())
	if db != nil && db.Error != nil {
		return fmt.Errorf("error updating heartbeat for task: %v", db.Error)
	}
	return nil
}

// MissedHeartbeatForTask updates the heartbeat for the task if it has not been updated for more than 10 minutes
func (storage *PosgresStore) MissedHeartbeatForTask() error {
	// start a transaction, select all tasks with lock where last_heart_beat is more than 1 minute ago
	// update status to failed and message to "missed heartbeat"
	// and for job set message to process got stuck because of some reason
	// and message status to error
	tx := storage.DB.Begin().Debug()

	var tasks []TaskListingDB
	db := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("status = ? AND (last_heart_beat < ? OR last_heart_beat is null)", "running", time.Now().Add(-10*time.Minute).Format("2006-01-02 15:04:05-0700")).Find(&tasks)
	if db.Error != nil {
		tx.Rollback()
		return fmt.Errorf("error getting tasks with missed heartbeat: %v", db.Error)
	}

	for _, task := range tasks {
		fmt.Println("Updating task", task.ID, "with missed heartbeat")

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
		return fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return nil
}

// GetJobsToProcess gives the jobs that are to be processed next
func (storage *PosgresStore) GetJobsToProcess() ([]CronJobListingDB, error) {
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
		return nil, fmt.Errorf("error getting jobs to process: %v", db.Error)
	}

	// update message to "push to queue" and message status to "info"
	for i := range res {
		res[i].Message = JobMessagePushToQueue
		res[i].MessageStatus = JobMessageStatusInfo

		db = tx.Save(&res[i])
		if db != nil && db.Error != nil {
			tx.Rollback()
			return nil, fmt.Errorf("error updating cron job: %v", db.Error)
		}
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return nil, fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return res, nil
}

func (storage *PosgresStore) GetJobByIDForUser(userID string, jobID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := storage.DB.Where("user_id = ? AND id = ?", userID, jobID).First(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron job for user: %v", db.Error)
	}

	return &res, nil
}

// GetPushedTask gives pushed task and update the status to running and set start time with table locking.
func (storage *PosgresStore) GetPushedTask() (*TaskListingDB, error) {
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
		return nil, fmt.Errorf("error updating pushed task status: %v", db.Error)
	}

	var job CronJobListingDB
	db = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", res.CronJobID).First(&job)
	if db.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting job: %v", db.Error)
	}

	job.Message = "Automatic backup started"
	job.MessageStatus = JobMessageStatusInfo

	db = tx.Save(&job)
	if db != nil && db.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error updating pushed task status: %v", db.Error)
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return nil, fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return &res, nil
}

func (storage *PosgresStore) GetTaskByID(ID uint) (*TaskListingDB, error) {
	var res TaskListingDB
	db := storage.DB.First(&res, ID)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting task by ID: %v", db.Error)
	}
	return &res, nil
}

func (storage *PosgresStore) CreateTaskForCronJob(cronJobID uint) (*TaskListingDB, error) {
	data := TaskListingDB{
		CronJobID: cronJobID,
		Status:    TaskStatusPushed,
	}

	// create new entry in database and return newly created task
	res := storage.DB.Create(&data)
	if res != nil && res.Error != nil {
		return nil, fmt.Errorf("error creating task for cron job: %v", res.Error)
	}
	return &data, nil
}

func (storage *PosgresStore) UpdateTaskByID(ID uint, m map[string]interface{}) error {
	// update task by ID
	// create a transaction to update the task and check if m["status"] == "failed"
	// then update "retry_count" and "status"
	tx := storage.DB.Begin()

	db := tx.Model(&TaskListingDB{}).Where("id = ?", ID).Updates(m)
	if db != nil && db.Error != nil {
		tx.Rollback()
		return fmt.Errorf("error updating task by ID: %v", db.Error)
	}

	if m["status"] == TaskStatusFailed {
		db = tx.Model(&TaskListingDB{}).Where("id = ?", ID).Update("retry_count", gorm.Expr("retry_count + 1"))
		if db != nil && db.Error != nil {
			tx.Rollback()
			return fmt.Errorf("error updating task by ID: %v", db.Error)
		}
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return nil

}

func (storage *PosgresStore) GetCronJobByID(ID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := storage.DB.First(&res, ID)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron job by ID: %v", db.Error)
	}
	return &res, nil
}

func (storage *PosgresStore) CreateCronJobForUser(userID, name, method string, inputData map[string]interface{}) (*CronJobListingDB, error) {
	data := CronJobListingDB{
		UserID:    userID,
		Name:      name,
		Method:    method,
		InputData: inputData,
	}
	// create new entry in database and return newly created cron job
	res := storage.DB.Create(&data)
	if res != nil && res.Error != nil {
		return nil, fmt.Errorf("error creating cron job: %v", res.Error)
	}

	return &data, nil
}

func (storage *PosgresStore) DeleteCronJobByID(ID uint) error {
	res := storage.DB.Delete(&CronJobListingDB{}, ID)
	if res != nil && res.Error != nil {
		return fmt.Errorf("error deleting cron job: %v", res.Error)
	}
	return nil
}

func (storage *PosgresStore) UpdateCronJobByID(ID uint, m map[string]interface{}) error {
	res := storage.DB.Model(&CronJobListingDB{}).Where("id = ?", ID).Updates(m)
	if res != nil && res.Error != nil {
		return fmt.Errorf("error updating cron job interval: %v", res.Error)
	}

	return nil
}

func (storage *PosgresStore) ListAllTasksByJobID(ID, limit, offset uint) ([]TaskListingDB, error) {
	var res []TaskListingDB
	db := storage.DB.Where("cron_job_id = ?", ID).Limit(int(limit)).Offset(int(offset)).Order("created_at DESC").Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting tasks for job: %v", db.Error)
	}
	return res, nil
}
