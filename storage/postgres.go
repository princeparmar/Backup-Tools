package storage

import (
	"os"

	"golang.org/x/oauth2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type PosgresStore struct {
	DB *gorm.DB
}

type GoogleAuthStorage struct {
	Cookie string
	oauth2.Token
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
	err := storage.DB.AutoMigrate(&GoogleAuthStorage{})
	if err != nil {
		return err
	}
	return nil
}

func (storage *PosgresStore) WriteGoogleAuthToken(cookie string, token *oauth2.Token) error {
	data := GoogleAuthStorage{
		Cookie: cookie,
		Token:  *token,
	}

	res := storage.DB.Create(data)
	if res.Error != nil {
		return res.Error
	}

	return nil
}

func (storage *PosgresStore) ReadGoogleAuthToken(cookie string) (*oauth2.Token, error) {
	var res GoogleAuthStorage
	storage.DB.Where("cookie = ?", cookie).First(&res)
	return &res.Token, nil
}
