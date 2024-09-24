package storage

import (
	"fmt"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/utils"
	"golang.org/x/oauth2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type PosgresStore struct {
	DB *gorm.DB
}

// type GoogleAuthStorage struct {
// 	JWTtoken    string
// 	GoogleToken string
// }

type GoogleAuthStorage struct {
	JWTtoken string
	oauth2.Token
}

type ShopifyAuthStorage struct {
	Cookie string
	Token  string
}

type QuickbooksAuthStorage struct {
	Cookie string
	Token  string
}

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
	TokenStatus  string `json:"token_status"`
	Active       bool   `json:"active"`
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

type StorxTokenDB struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UserID  string `gorm:"unique" json:"user_id"`
	Token   string `gorm:"unique" json:"token"`
	Enabled bool   `json:"enabled"`
}

type TaskListingDB struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CronJobID uint      `gorm:"foreignKey:CronJobListingDB.ID" json:"cron_job_id"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	StartTime time.Time `json:"start_time"`
	Execution uint64    `json:"execution"`
}

func NewPostgresStore() (*PosgresStore, error) {
	dsn := os.Getenv("POSTGRES_DSN")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &PosgresStore{DB: db}, nil
}

func (storage *PosgresStore) Migrate() error {
	return storage.DB.AutoMigrate(
		&GoogleAuthStorage{}, &ShopifyAuthStorage{},
		&QuickbooksAuthStorage{}, &CronJobListingDB{},
		&StorxTokenDB{}, &TaskListingDB{},
	)
}

func (storage *PosgresStore) GetAllCronJobsForUser(userID string) ([]CronJobListingDB, error) {
	var res []CronJobListingDB
	db := storage.DB.Where("user_id = ?", userID).Find(&res)
	if db != nil {
		return nil, fmt.Errorf("error getting cron jobs for user: %v", db.Error)
	}
	return res, nil
}

func (storage *PosgresStore) IsCronAvailableForUser(userID string, jobID uint) bool {
	var res CronJobListingDB
	db := storage.DB.Where("user_id = ? AND id = ?", userID, jobID).First(&res)
	return db == nil || db.Error == nil
}

func (storage *PosgresStore) GetCronJobByID(ID uint) (*CronJobListingDB, error) {
	var res CronJobListingDB
	db := storage.DB.First(&res, ID)
	if db != nil {
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
	if res != nil {
		return nil, fmt.Errorf("error creating cron job: %v", res.Error)
	}

	return &data, nil
}

func (storage *PosgresStore) DeleteCronJobByID(ID uint) error {
	res := storage.DB.Delete(&CronJobListingDB{}, ID)
	if res != nil {
		return fmt.Errorf("error deleting cron job: %v", res.Error)
	}
	return nil
}

func (storage *PosgresStore) UpdateCronJobByID(ID uint, m map[string]interface{}) error {
	res := storage.DB.Model(&CronJobListingDB{}).Where("id = ?", ID).Updates(m)
	if res != nil {
		return fmt.Errorf("error updating cron job interval: %v", res.Error)
	}
	return nil
}

func (storage *PosgresStore) ListAllTasksByJobID(ID uint) ([]TaskListingDB, error) {
	var res []TaskListingDB
	db := storage.DB.Where("cron_job_id = ?", ID).Find(&res)
	if db != nil {
		return nil, fmt.Errorf("error getting tasks for job: %v", db.Error)
	}
	return res, nil
}

func (storage *PosgresStore) WriteGoogleAuthToken(JWToken, authToken string) error {
	data := GoogleAuthStorage{}
	data.JWTtoken = JWToken
	data.AccessToken = authToken
	// data.RefreshToken = refreshToken

	res := storage.DB.Create(data)
	if res.Error != nil {
		return res.Error
	}

	return nil
}

// func (storage *PosgresStore) WriteGoogleAuthToken(cookie string, token *oauth2.Token) error {
// 	data := GoogleAuthStorage{
// 		Cookie: cookie,
// 		Token:  *token,
// 	}

// 	res := storage.DB.Create(data)
// 	if res.Error != nil {
// 		return res.Error
// 	}

// 	return nil
// }

// func (storage *PosgresStore) ReadGoogleAuthToken(cookie string) (*oauth2.Token, error) {
// 	var res GoogleAuthStorage
// 	storage.DB.Where("cookie = ?", cookie).First(&res)
// 	return &res.Token, nil
// }

func (storage *PosgresStore) ReadGoogleAuthToken(JWTtoken string) (oauth2.Token, error) {
	var res GoogleAuthStorage
	storage.DB.Where("jw_ttoken = ?", JWTtoken).First(&res)
	return oauth2.Token{
		AccessToken: res.AccessToken,
		// RefreshToken: res.RefreshToken,
	}, nil
}

func (storage *PosgresStore) WriteShopifyAuthToken(cookie string, token string) error {
	data := ShopifyAuthStorage{
		Cookie: cookie,
		Token:  token,
	}

	res := storage.DB.Create(data)
	if res.Error != nil {
		return res.Error
	}

	return nil
}

func (storage *PosgresStore) ReadShopifyAuthToken(cookie string) (string, error) {
	var res ShopifyAuthStorage
	storage.DB.Where("cookie = ?", cookie).First(&res)
	return res.Token, nil
}

func (storage *PosgresStore) WriteQuickbooksAuthToken(cookie string, token string) error {
	data := QuickbooksAuthStorage{
		Cookie: cookie,
		Token:  token,
	}

	res := storage.DB.Create(data)
	if res.Error != nil {
		return res.Error
	}

	return nil
}

func (storage *PosgresStore) ReadQuickbooksAuthToken(cookie string) (string, error) {
	var res QuickbooksAuthStorage
	storage.DB.Where("cookie = ?", cookie).First(&res)
	return res.Token, nil
}
