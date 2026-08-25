package repo

import (
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"golang.org/x/oauth2"
)

// GoogleAuthStorage represents Google authentication storage
type GoogleAuthStorage struct {
	gorm.GormModel
	JWTtoken string
	oauth2.Token
}

// ShopifyAuthStorage represents Shopify authentication storage
type ShopifyAuthStorage struct {
	gorm.GormModel
	Cookie string
	Token  string
}

// QuickbooksAuthStorage represents Quickbooks authentication storage
type QuickbooksAuthStorage struct {
	gorm.GormModel
	Cookie string
	Token  string
}

// AuthRepository handles all database operations for authentication storage
type AuthRepository struct {
	db *gorm.DB
}

// NewAuthRepository creates a new auth repository
func NewAuthRepository(db *gorm.DB) *AuthRepository {
	return &AuthRepository{db: db}
}

// writeToken is a generic helper function to write tokens
func (r *AuthRepository) writeToken(model interface{}) error {
	return r.db.Create(model).Error
}

// readToken is a generic helper function to read tokens
func (r *AuthRepository) readToken(cookie string, model interface{}) error {
	return r.db.Where("cookie = ?", cookie).First(model).Error
}

// Google Auth operations
func (r *AuthRepository) WriteGoogleAuthToken(JWTtoken, accessToken string) error {
	data := &GoogleAuthStorage{
		JWTtoken: JWTtoken,
		Token: oauth2.Token{
			AccessToken: accessToken,
		},
	}
	return r.db.Create(data).Error
}

func (r *AuthRepository) ReadGoogleAuthToken(JWTtoken string) (string, error) {
	var res GoogleAuthStorage
	if err := r.db.Where("jw_ttoken = ?", JWTtoken).First(&res).Error; err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

// MicrosoftAuthStorage stores Graph access tokens for restore (POST /microsoft-auth), mirrors GoogleAuthStorage.
// Covers all Microsoft Graph restore services, not Outlook mail only.
type MicrosoftAuthStorage struct {
	gorm.GormModel
	JWTtoken string
	oauth2.Token
}

// WriteMicrosoftAuthToken stores a Graph access token keyed by a random lookup id (embedded in microsoft-auth JWT).
func (r *AuthRepository) WriteMicrosoftAuthToken(JWTtoken, accessToken string) error {
	data := &MicrosoftAuthStorage{
		JWTtoken: JWTtoken,
		Token: oauth2.Token{
			AccessToken: accessToken,
		},
	}
	return r.db.Create(data).Error
}

// ReadMicrosoftAuthToken returns the Graph access token for a microsoft-auth JWT lookup key.
func (r *AuthRepository) ReadMicrosoftAuthToken(JWTtoken string) (string, error) {
	var res MicrosoftAuthStorage
	if err := r.db.Where("jw_ttoken = ?", JWTtoken).First(&res).Error; err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

// Shopify Auth operations
func (r *AuthRepository) WriteShopifyAuthToken(cookie, token string) error {
	return r.writeToken(&ShopifyAuthStorage{Cookie: cookie, Token: token})
}

func (r *AuthRepository) ReadShopifyAuthToken(cookie string) (string, error) {
	var res ShopifyAuthStorage
	if err := r.readToken(cookie, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}

// Quickbooks Auth operations
func (r *AuthRepository) WriteQuickbooksAuthToken(cookie, token string) error {
	return r.writeToken(&QuickbooksAuthStorage{Cookie: cookie, Token: token})
}

func (r *AuthRepository) ReadQuickbooksAuthToken(cookie string) (string, error) {
	var res QuickbooksAuthStorage
	if err := r.readToken(cookie, &res); err != nil {
		return "", err
	}
	return res.Token, nil
}
