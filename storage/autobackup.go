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
