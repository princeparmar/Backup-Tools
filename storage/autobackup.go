package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Models for automated storage
type CronJobListingDB struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	Method   string    `json:"method"`
	Interval string    `json:"interval"`
	On       string    `json:"on"`
	LastRun  time.Time `json:"last_run"`

	RefreshToken string `json:"refresh_token"`

	StorxToken string `json:"storx_token"`

	// Message will be the message to be displayed to the user
	// pushing to queue
	// task is running
	// task is completed for %d email|photos|files for (today|this week|this month)
	// task failed with error %s
	Message string `json:"message"`

	// MessageStatus will be one of the following: "info", "warning", "error"
	MessageStatus string `json:"message_status"`
	Active        bool   `json:"active"`
}

func MastTokenForCronJobListingDB(cronJobs []CronJobListingDB) []CronJobListingDB {
	for i := range cronJobs {
		MastTokenForCronJobDB(&cronJobs[i])
	}

	return cronJobs
}

func MastTokenForCronJobDB(cronJob *CronJobListingDB) {
	cronJob.StorxToken = utils.MaskString(cronJob.StorxToken)
	cronJob.RefreshToken = utils.MaskString(cronJob.RefreshToken)
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
	ID        uint `gorm:"primaryKey" json:"id"`
	CronJobID uint `gorm:"foreignKey:CronJobListingDB.ID" json:"cron_job_id"`

	// Status will be one of the following: "pushed", "running", "success", "failed"
	Status string `json:"status"`

	// Message will be the message to be displayed to the user
	Message string `json:"message"`

	// StartTime will be the time when the task was started
	StartTime time.Time `json:"start_time"`

	// Execution time in milliseconds
	Execution uint64 `json:"execution"`

	// RetryCount will be the number of times the task has been retried
	// Maximum 3 retries are allowed
	RetryCount uint `json:"retry_count"`

	// Memory will be used to store the state of the task. this will be json field
	TaskMemory TaskMemory `json:"task_memory" gorm:"type:jsonb"`
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

// GetJobsToProcess returns all cron jobs that are active and have not been run in
// given interval with table locking with limit 10.
func (storage *PosgresStore) GetJobsToProcess() ([]uint, error) {
	var res []CronJobListingDB
	tx := storage.DB.Begin()

	/*
		SELECT cron_job_listing_dbs.*
		FROM   "cron_job_listing_dbs"
		       LEFT JOIN task_listing_dbs
		              ON cron_job_listing_dbs.id = task_listing_dbs.cron_job_id
		WHERE  cron_job_listing_dbs.active = true
			   AND cron_job_listing_dbs.message != 'pushing to queue'
		       AND cron_job_listing_dbs.last_run != '2024-09-26'
		       AND ( task_listing_dbs.id IS NULL
		              OR task_listing_dbs.status IN ( 'completed', 'failed' ) )
		       AND ( cron_job_listing_dbs.interval = 'daily'
		              OR ( cron_job_listing_dbs.interval = 'weekly'
		                   AND cron_job_listing_dbs.on = 'Thursday' )
		              OR ( cron_job_listing_dbs.interval = 'monthly'
		                   AND cron_job_listing_dbs.on = 26 ) )
	*/
	// The raw SQL query
	sqlQuery := `
	WITH locked_jobs AS (
		SELECT cron_job_listing_dbs.*
		FROM cron_job_listing_dbs
		WHERE cron_job_listing_dbs.active = true
		AND (cron_job_listing_dbs.message is null or cron_job_listing_dbs.message != 'pushing to queue')
		AND DATE(cron_job_listing_dbs.last_run) != ?
		AND (cron_job_listing_dbs.interval = 'daily'
			OR (cron_job_listing_dbs.interval = 'weekly' AND cron_job_listing_dbs.on = ?)
			OR (cron_job_listing_dbs.interval = 'monthly' AND cron_job_listing_dbs.on = ?))
		LIMIT 10
		FOR UPDATE
	)
	SELECT locked_jobs.*
	FROM locked_jobs
	LEFT JOIN task_listing_dbs
	ON locked_jobs.id = task_listing_dbs.cron_job_id
	WHERE task_listing_dbs.id IS NULL OR task_listing_dbs.status NOT IN ('running', 'pushed')
	`

	// Execute the raw SQL query and store the result in the cronJobs slice
	db := tx.Raw(sqlQuery, time.Now().Format("2006-01-02"), time.Now().Weekday().String(), fmt.Sprint(time.Now().Day())).Scan(&res)
	if db.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting jobs to process: %v", db.Error)
	}

	out := make([]uint, len(res))
	// update message to "pushing to queue" and message status to "info"
	for i := range res {
		res[i].Message = "pushing to queue"
		res[i].MessageStatus = "info"

		db = tx.Save(&res[i])
		if db != nil && db.Error != nil {
			tx.Rollback()
			return nil, fmt.Errorf("error updating cron job: %v", db.Error)
		}

		out[i] = res[i].ID
	}

	err := tx.Commit()
	if err != nil && err.Error != nil {
		return nil, fmt.Errorf("error committing transaction: %v", err.Error)
	}

	return out, nil
}

func (storage *PosgresStore) IsCronAvailableForUser(userID string, jobID uint) bool {
	var res CronJobListingDB
	db := storage.DB.Where("user_id = ? AND id = ?", userID, jobID).First(&res)
	return db == nil || db.Error == nil
}

// GetPushedTask gives pushed task and update the status to running and set start time with table locking.
func (storage *PosgresStore) GetPushedTask() (*TaskListingDB, error) {
	var res TaskListingDB
	tx := storage.DB.Begin()
	// lock table tasks for update and select and return the first row with status pushed
	// or status 'failed' and retry count less than 3
	db := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status = ? OR (status = ? AND retry_count < 3)", "pushed", "failed").First(&res)
	if db.Error != nil {
		tx.Rollback()
		return nil, fmt.Errorf("error getting pushed task: %v", db.Error)
	}

	// Update status to running and set start time
	res.Status = "running"
	res.StartTime = time.Now()

	db = tx.Save(&res)
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
		Status:    "pushed",
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

	if m["status"] == "failed" {
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

func (storage *PosgresStore) CreateCronJobForUser(userID, name, method, interval, on string) (*CronJobListingDB, error) {
	data := CronJobListingDB{
		UserID:   userID,
		Name:     name,
		Interval: interval,
		Method:   method,
		On:       on,
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

func (storage *PosgresStore) ListAllTasksByJobID(ID uint) ([]TaskListingDB, error) {
	var res []TaskListingDB
	db := storage.DB.Where("cron_job_id = ?", ID).Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting tasks for job: %v", db.Error)
	}
	return res, nil
}
