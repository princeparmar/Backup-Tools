package repo

import (
	"errors"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

// OAuthCredentialDB represents an OAuth credential stored for a job (e.g. Gmail).
// Created at job creation from UI token; cron reads refresh_token from here.
type OAuthCredentialDB struct {
	gorm.GormModel

	UserID       string `json:"user_id" gorm:"column:user_id;not null;index"`
	Email        string `json:"email" gorm:"column:email;not null"`
	Source       string `json:"source" gorm:"column:source;not null"` // e.g. "gmail"
	AccessToken  string `json:"-" gorm:"column:access_token"`
	RefreshToken string `json:"-" gorm:"column:refresh_token;not null"`
}

// TableName returns the table name for GORM.
func (OAuthCredentialDB) TableName() string {
	return "oauth_credentials"
}

// OAuthCredentialRepository handles all database operations for oauth_credentials.
type OAuthCredentialRepository struct {
	db *gorm.DB
}

// NewOAuthCredentialRepository creates a new OAuth credential repository.
func NewOAuthCredentialRepository(db *gorm.DB) *OAuthCredentialRepository {
	return &OAuthCredentialRepository{db: db}
}

// Create inserts a new credential and populates the record ID. Returns error on failure.
func (r *OAuthCredentialRepository) Create(c *OAuthCredentialDB) error {
	res := r.db.Create(c)
	if res != nil && res.Error != nil {
		return fmt.Errorf("error creating oauth credential: %v", res.Error)
	}
	return nil
}

// GetByID retrieves the credential by ID.
func (r *OAuthCredentialRepository) GetByID(id uint) (*OAuthCredentialDB, error) {
	var cred OAuthCredentialDB
	db := r.db.First(&cred, id)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting oauth credential: %v", db.Error)
	}
	return &cred, nil
}

// GetRefreshTokenByID returns only the refresh_token for the given credential ID (e.g. for cron).
func (r *OAuthCredentialRepository) GetRefreshTokenByID(id uint) (string, error) {
	var cred OAuthCredentialDB
	db := r.db.Select("refresh_token").First(&cred, id)
	if db != nil && db.Error != nil {
		return "", fmt.Errorf("error getting oauth credential: %v", db.Error)
	}
	return cred.RefreshToken, nil
}

// GetByIDAndUser retrieves the credential by ID and user ID (for authorization checks if needed).
func (r *OAuthCredentialRepository) GetByIDAndUser(id uint, userID string) (*OAuthCredentialDB, error) {
	var cred OAuthCredentialDB
	db := r.db.Where("id = ? AND user_id = ?", id, userID).First(&cred)
	if db != nil && db.Error != nil {
		return nil, fmt.Errorf("error getting oauth credential: %v", db.Error)
	}
	return &cred, nil
}

// GetByUserIDAndSourceAndEmail returns the credential for this user/source/email (connected account). Returns (nil, nil) when not found so caller can create one.
func (r *OAuthCredentialRepository) GetByUserIDAndSourceAndEmail(userID, source, email string) (*OAuthCredentialDB, error) {
	var cred OAuthCredentialDB
	db := r.db.Where("user_id = ? AND source = ? AND email = ?", userID, source, email).First(&cred)
	if db != nil && db.Error != nil {
		if errors.Is(db.Error, gormio.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting oauth credential: %v", db.Error)
	}
	return &cred, nil
}

// ClearRefreshTokenByID sets refresh_token to empty for the given credential ID (e.g. when cron detects invalid Google credentials).
func (r *OAuthCredentialRepository) ClearRefreshTokenByID(id uint) error {
	res := r.db.Model(&OAuthCredentialDB{}).Where("id = ?", id).Update("refresh_token", "")
	if res != nil && res.Error != nil {
		return fmt.Errorf("error clearing oauth credential refresh token: %v", res.Error)
	}
	return nil
}
