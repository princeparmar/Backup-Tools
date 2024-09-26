package storage

import (
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/utils"
)

// Models for automated storage
type CronJobListingDB struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Method   string `json:"method"`
	Interval string `json:"interval"`
	On       string `json:"on"`
	LastRun  string `json:"last_run"`

	AuthToken    string `json:"auth_token"`
	RefreshToken string `json:"refresh_token"`

	StorxToken string `json:"storx_token"`

	Message       string `json:"message"`
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
	cronJob.AuthToken = utils.MaskString(cronJob.AuthToken)
	cronJob.RefreshToken = utils.MaskString(cronJob.RefreshToken)
}

type TaskListingDB struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	CronJobID uint `gorm:"foreignKey:CronJobListingDB.ID" json:"cron_job_id"`

	// Status will be one of the following: "pushed", "running", "completed", "failed"
	Status              string    `json:"status"`
	CompletedPercentage float64   `json:"completed_percentage"`
	Message             string    `json:"message"`
	StartTime           time.Time `json:"start_time"`

	// Execution time in milliseconds
	Execution uint64 `json:"execution"`
}

func (storage *PosgresStore) GetAllCronJobsForUser(userID string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := storage.DB.Where("user_id = ?", userID).Find(&res)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}
	return res, nil
}

// GetJobsToProcess returns all cron jobs that are active and have not been run in the last 24 hours with table locking.
func (storage *PosgresStore) GetJobsToProcess() ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	tx := storage.DB.Begin()

	// select from jobs inner join tasks where active is true
	// and if interval is "daily" then last_run date should not be today
	// and if interval is "weekly" then last_run date should not be this week and day should be same as today. like monday, tuesday etc
	// and if interval is "monthly" then last_run date should not be this month and date should be same as today
	// and there should not be any task in task table with "pushed" or "running" status

	db := tx.Table("cron_job_listing_dbs").
		Select("cron_job_listing_dbs.*").
		Joins("left join task_listing_dbs on cron_job_listing_dbs.id = task_listing_dbs.cron_job_id").
		Where("cron_job_listing_dbs.active = true").
		Where("task_listing_dbs.id is null or task_listing_dbs.status in ('completed', 'failed')").
		Where("cron_job_listing_dbs.interval = 'daily' and cron_job_listing_dbs.last_run != ?", time.Now().Format("2006-01-02")).
		Or("cron_job_listing_dbs.interval = 'weekly' and cron_job_listing_dbs.last_run != ? and cron_job_listing_dbs.on = ?", time.Now().Format("2006-01-02"), time.Now().Weekday().String()).
		Or("cron_job_listing_dbs.interval = 'monthly' and cron_job_listing_dbs.last_run != ? and cron_job_listing_dbs.on = ?", time.Now().Format("2006-01-02"), time.Now().Day()).
		Find(&res)

	// update message to "pushing to queue" and message status to "info"
	for i := range res {
		res[i].Message = "pushing to queue"
		res[i].MessageStatus = "info"

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

func (storage *PosgresStore) IsCronAvailableForUser(userID string, jobID uint) bool {
	var res CronJobListingDB
	db := storage.DB.Where("user_id = ? AND id = ?", userID, jobID).First(&res)
	return db == nil || db.Error == nil
}

// GetPushedTask gives pushed task and update the status to running and set start time with table locking.
func (storage *PosgresStore) GetPushedTask() (*TaskListingDB, error) {
	var res TaskListingDB
	tx := storage.DB.Begin()
	db := tx.Where("status = ?", "pushed").First(&res)
	if db != nil && db.Error != nil {
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
	res := storage.DB.Model(&TaskListingDB{}).Where("id = ?", ID).Updates(m)
	if res != nil && res.Error != nil {
		return fmt.Errorf("error updating task: %v", res.Error)
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
