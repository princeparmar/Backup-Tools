package storage

import (
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type PosgresStore struct {
	DB *gorm.DB
}

type GoogleAuthStorage struct {
	JWTtoken    string
	GoogleToken string
}

// type GoogleAuthStorage struct {
// 	Cookie string
// 	oauth2.Token
// }

type ShopifyAuthStorage struct {
	Cookie string
	Token  string
}

type QuickbooksAuthStorage struct {
	Cookie string
	Token  string
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
	err = storage.DB.AutoMigrate(&ShopifyAuthStorage{})
	if err != nil {
		return err
	}
	err = storage.DB.AutoMigrate(&QuickbooksAuthStorage{})
	if err != nil {
		return err
	}
	return nil
}

func (storage *PosgresStore) WriteGoogleAuthToken(JWTtoken, googleToken string) error {
	data := GoogleAuthStorage{
		JWTtoken:    JWTtoken,
		GoogleToken: googleToken,
	}

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

func (storage *PosgresStore) ReadGoogleAuthToken(cookie string) (string, error) {
	var res GoogleAuthStorage
	storage.DB.Where("cookie = ?", cookie).First(&res)
	return res.GoogleToken, nil
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
