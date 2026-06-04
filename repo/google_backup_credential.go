package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

// GoogleBackupCredentialDB stores shared Google OAuth + StorX tokens for autosync jobs.
// Uniqueness: storj_project_id (primary); email may repeat across projects.
type GoogleBackupCredentialDB struct {
	gorm.GormModel

	Email          string `json:"email" gorm:"column:email;index:idx_google_backup_cred_email"`
	StorjProjectID string `json:"storj_project_id,omitempty" gorm:"column:storj_project_id;uniqueIndex:idx_google_backup_cred_project_id"`
	AccountType    string `json:"account_type" gorm:"column:account_type;not null;default:personal"`
	RefreshToken   string `json:"refresh_token,omitempty" gorm:"column:refresh_token"`
	StorxToken     string `json:"storx_token,omitempty" gorm:"column:storx_token"`
}

// GoogleBackupCredentialRepository handles google_backup_credential_dbs.
type GoogleBackupCredentialRepository struct {
	db *gorm.DB
}

// NewGoogleBackupCredentialRepository creates a credential repository.
func NewGoogleBackupCredentialRepository(db *gorm.DB) *GoogleBackupCredentialRepository {
	return &GoogleBackupCredentialRepository{db: db}
}

// GetByID loads a credential by primary key.
func (r *GoogleBackupCredentialRepository) GetByID(id uint) (*GoogleBackupCredentialDB, error) {
	if id == 0 {
		return nil, gormio.ErrRecordNotFound
	}
	var row GoogleBackupCredentialDB
	if err := r.db.First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetByStorjProjectID loads a credential by Satellite/Storj project id (unique per row).
func (r *GoogleBackupCredentialRepository) GetByStorjProjectID(projectID string) (*GoogleBackupCredentialDB, bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, false, nil
	}
	var row GoogleBackupCredentialDB
	err := r.db.Where("storj_project_id = ?", projectID).First(&row).Error
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get credential by storj_project_id: %w", err)
	}
	return &row, true, nil
}

// FindIDForUserAndProjectID returns credential id when any cron job for userID links to a credential with storj_project_id.
func (r *GoogleBackupCredentialRepository) FindIDForUserAndProjectID(userID, projectID string) (uint, bool, error) {
	projectID = strings.TrimSpace(projectID)
	userID = strings.TrimSpace(userID)
	if projectID == "" || userID == "" {
		return 0, false, nil
	}
	var cred GoogleBackupCredentialDB
	err := r.db.Table("google_backup_credential_dbs AS c").
		Select("c.id").
		Joins(`INNER JOIN cron_job_listing_dbs j ON (j.input_data->>'credential_id')::bigint = c.id AND j.deleted_at IS NULL`).
		Where("j.user_id = ? AND c.storj_project_id = ?", userID, projectID).
		First(&cred).Error
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("find credential for user and project_id: %w", err)
	}
	return cred.ID, true, nil
}

// FindIDForUserAndEmail returns credential id when any cron job for userID links to a credential with email (legacy fallback).
func (r *GoogleBackupCredentialRepository) FindIDForUserAndEmail(userID, email string) (uint, bool, error) {
	email = strings.TrimSpace(email)
	userID = strings.TrimSpace(userID)
	if email == "" || userID == "" {
		return 0, false, nil
	}
	var cred GoogleBackupCredentialDB
	err := r.db.Table("google_backup_credential_dbs AS c").
		Select("c.id").
		Joins(`INNER JOIN cron_job_listing_dbs j ON (j.input_data->>'credential_id')::bigint = c.id AND j.deleted_at IS NULL`).
		Where("j.user_id = ? AND LOWER(TRIM(c.email)) = LOWER(?)", userID, email).
		First(&cred).Error
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("find credential for user and email: %w", err)
	}
	return cred.ID, true, nil
}

func normalizeCredentialAccountType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "employee_workspace":
		return "employee_workspace"
	case "admin_workspace":
		return "admin_workspace"
	case "personal":
		return "personal"
	default:
		return ""
	}
}

// Create inserts a new credential row.
func (r *GoogleBackupCredentialRepository) Create(email, projectID, accountType, refreshToken, storxToken string) (*GoogleBackupCredentialDB, error) {
	acct := normalizeCredentialAccountType(accountType)
	if acct == "" {
		acct = "personal"
	}
	row := GoogleBackupCredentialDB{
		Email:          strings.TrimSpace(email),
		StorjProjectID: strings.TrimSpace(projectID),
		AccountType:    acct,
		RefreshToken:   strings.TrimSpace(refreshToken),
		StorxToken:     strings.TrimSpace(storxToken),
	}
	if row.StorjProjectID == "" {
		return nil, fmt.Errorf("storj_project_id is required")
	}
	if row.Email == "" {
		return nil, fmt.Errorf("credential email is required")
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create credential: %w", err)
	}
	return &row, nil
}

// FindOrCreateForUser finds or creates a credential scoped by storj_project_id (primary) then legacy email via jobs.
func (r *GoogleBackupCredentialRepository) FindOrCreateForUser(userID, email, projectID, accountType, refreshToken, storxToken string) (*GoogleBackupCredentialDB, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if id, ok, err := r.FindIDForUserAndProjectID(userID, projectID); err != nil {
			return nil, err
		} else if ok {
			return r.mergeAndReload(id, email, projectID, accountType, refreshToken, storxToken)
		}
		if cred, ok, err := r.GetByStorjProjectID(projectID); err != nil {
			return nil, err
		} else if ok {
			return r.mergeAndReload(cred.ID, email, projectID, accountType, refreshToken, storxToken)
		}
	}
	if id, ok, err := r.FindIDForUserAndEmail(userID, email); err != nil {
		return nil, err
	} else if ok {
		return r.mergeAndReload(id, email, projectID, accountType, refreshToken, storxToken)
	}
	return r.Create(email, projectID, accountType, refreshToken, storxToken)
}

func (r *GoogleBackupCredentialRepository) mergeAndReload(id uint, email, projectID, accountType, refreshToken, storxToken string) (*GoogleBackupCredentialDB, error) {
	if err := r.mergeFieldsIfProvided(id, email, projectID, accountType, refreshToken, storxToken); err != nil {
		return nil, err
	}
	return r.GetByID(id)
}

func (r *GoogleBackupCredentialRepository) mergeFieldsIfProvided(id uint, email, projectID, accountType, refreshToken, storxToken string) error {
	patch := map[string]interface{}{}
	if t := strings.TrimSpace(email); t != "" {
		patch["email"] = t
	}
	if t := strings.TrimSpace(projectID); t != "" {
		patch["storj_project_id"] = t
	}
	if t := normalizeCredentialAccountType(accountType); t != "" {
		patch["account_type"] = t
	}
	if t := strings.TrimSpace(refreshToken); t != "" {
		patch["refresh_token"] = t
	}
	if t := strings.TrimSpace(storxToken); t != "" {
		patch["storx_token"] = t
	}
	if len(patch) == 0 {
		return nil
	}
	return r.db.Model(&GoogleBackupCredentialDB{}).Where("id = ?", id).Updates(patch).Error
}

// UpdateTokens updates refresh_token and/or storx_token on the credential row.
func (r *GoogleBackupCredentialRepository) UpdateTokens(id uint, refreshToken, storxToken *string) error {
	if id == 0 {
		return fmt.Errorf("credential id is required")
	}
	patch := map[string]interface{}{}
	if refreshToken != nil {
		patch["refresh_token"] = strings.TrimSpace(*refreshToken)
	}
	if storxToken != nil {
		patch["storx_token"] = strings.TrimSpace(*storxToken)
	}
	if len(patch) == 0 {
		return nil
	}
	return r.db.Model(&GoogleBackupCredentialDB{}).Where("id = ?", id).Updates(patch).Error
}

// UpdateStorjProjectID sets storj_project_id on the credential (unique key for linking jobs).
func (r *GoogleBackupCredentialRepository) UpdateStorjProjectID(id uint, projectID string) error {
	if id == 0 {
		return fmt.Errorf("credential id is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	return r.db.Model(&GoogleBackupCredentialDB{}).Where("id = ?", id).Update("storj_project_id", projectID).Error
}

// ClearTokens clears OAuth and StorX tokens on the credential.
func (r *GoogleBackupCredentialRepository) ClearTokens(id uint) error {
	if id == 0 {
		return nil
	}
	return r.db.Model(&GoogleBackupCredentialDB{}).Where("id = ?", id).Updates(map[string]interface{}{
		"refresh_token": "",
		"storx_token":   "",
	}).Error
}

// OAuthHolderEmail returns the credential email when it differs from the mailbox (corporate delegation).
func OAuthHolderEmail(cred *GoogleBackupCredentialDB, mailbox string) string {
	if cred == nil {
		return ""
	}
	holder := strings.TrimSpace(cred.Email)
	mailbox = strings.TrimSpace(mailbox)
	if holder == "" || mailbox == "" || strings.EqualFold(holder, mailbox) {
		return holder
	}
	return holder
}
