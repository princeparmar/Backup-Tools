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
		&TaskListingDB{},
	)
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
