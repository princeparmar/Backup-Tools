package storage

import (
	"log"
	"os"
	"time"

	"golang.org/x/oauth2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type PosgresStore struct {
	DB *gorm.DB
}

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

// NewPostgresStore creates a new PostgreSQL store instance
func NewPostgresStore(dsn string, queryLogging bool) (*PosgresStore, error) {

	logLevel := logger.Silent
	if queryLogging {
		logLevel = logger.Info
	}

	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: newLogger})
	if err != nil {
		return nil, err
	}

	return &PosgresStore{DB: db}, nil
}

// Migrate runs database migrations
func (s *PosgresStore) Migrate() error {
	err := s.DB.AutoMigrate(
		&GoogleAuthStorage{},
		&ShopifyAuthStorage{},
		&QuickbooksAuthStorage{},
		&CronJobListingDB{},
		&TaskListingDB{},
	)
	if err != nil {
		return err
	}

	return nil
}

func (s *PosgresStore) writeToken(model interface{}) error {
	return s.DB.Create(model).Error
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

func (s *PosgresStore) readToken(cookie string, model interface{}) error {
	return s.DB.Where("cookie = ?", cookie).First(model).Error
}

// Google Auth operations
func (s *PosgresStore) WriteGoogleAuthToken(JWTtoken, accessToken string) error {
	data := &GoogleAuthStorage{
		JWTtoken: JWTtoken,
		Token: oauth2.Token{
			AccessToken: accessToken,
		},
	}
	return s.DB.Create(data).Error
}

func (s *PosgresStore) ReadGoogleAuthToken(JWTtoken string) (string, error) {
	var res GoogleAuthStorage
	if err := s.DB.Where("jw_ttoken= ?", JWTtoken).First(&res).Error; err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

// Shopify Auth operations
func (s *PosgresStore) WriteShopifyAuthToken(cookie, token string) error {
	return s.writeToken(&ShopifyAuthStorage{Cookie: cookie, Token: token})
}

func (s *PosgresStore) ReadShopifyAuthToken(cookie string) (string, error) {
	var res ShopifyAuthStorage
	if err := s.readToken(cookie, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}

// Quickbooks Auth operations
func (s *PosgresStore) WriteQuickbooksAuthToken(cookie, token string) error {
	return s.writeToken(&QuickbooksAuthStorage{Cookie: cookie, Token: token})
}

func (s *PosgresStore) ReadQuickbooksAuthToken(cookie string) (string, error) {
	var res QuickbooksAuthStorage
	if err := s.readToken(cookie, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}
