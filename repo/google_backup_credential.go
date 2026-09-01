package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

// GoogleBackupCredentialDB stores shared Google OAuth + StorX tokens for autosync jobs.
// Uniqueness: (user_id, storj_project_id, email) — same email may exist per Satellite user.
type GoogleBackupCredentialDB struct {
	gorm.GormModel

	UserID         string `json:"user_id,omitempty" gorm:"column:user_id;not null;default:'';index;uniqueIndex:idx_google_backup_cred_user_project_email,priority:1"`
	Email          string `json:"email" gorm:"column:email;uniqueIndex:idx_google_backup_cred_user_project_email,priority:3"`
	StorjProjectID string `json:"storj_project_id,omitempty" gorm:"column:storj_project_id;uniqueIndex:idx_google_backup_cred_user_project_email,priority:2;index:idx_google_backup_cred_project_id"`
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

// GetByIDs loads credentials by primary keys in one query. Missing ids are omitted.
func (r *GoogleBackupCredentialRepository) GetByIDs(ids []uint) (map[uint]*GoogleBackupCredentialDB, error) {
	out := make(map[uint]*GoogleBackupCredentialDB)
	if len(ids) == 0 {
		return out, nil
	}
	uniq := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}
	var rows []GoogleBackupCredentialDB
	if err := r.db.Where("id IN ?", uniq).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("get credentials by ids: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// GetByStorjProjectID loads one credential row for a Storj project (first match when several Google accounts share a project).
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

// FindByUserProjectAndEmail loads a credential row scoped by user_id + project + email (no cron job required).
func (r *GoogleBackupCredentialRepository) FindByUserProjectAndEmail(userID, projectID, email string) (*GoogleBackupCredentialDB, bool, error) {
	projectID = strings.TrimSpace(projectID)
	email = strings.TrimSpace(email)
	userID = strings.TrimSpace(userID)
	if projectID == "" || email == "" || userID == "" {
		return nil, false, nil
	}
	var row GoogleBackupCredentialDB
	err := r.db.Where(
		"user_id = ? AND storj_project_id = ? AND LOWER(TRIM(email)) = LOWER(?)",
		userID, projectID, email,
	).First(&row).Error
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("find credential by user, project, email: %w", err)
	}
	return &row, true, nil
}

// FindIDForUserProjectAndEmail returns credential id for a user's linked jobs matching project_id and OAuth holder email.
func (r *GoogleBackupCredentialRepository) FindIDForUserProjectAndEmail(userID, projectID, email string) (uint, bool, error) {
	projectID = strings.TrimSpace(projectID)
	email = strings.TrimSpace(email)
	userID = strings.TrimSpace(userID)
	if projectID == "" || email == "" || userID == "" {
		return 0, false, nil
	}
	var cred GoogleBackupCredentialDB
	err := r.db.Table("google_backup_credential_dbs AS c").
		Select("c.id").
		Joins(`INNER JOIN cron_job_listing_dbs j ON (j.input_data->>'credential_id')::bigint = c.id AND j.deleted_at IS NULL`).
		Where("j.user_id = ? AND c.storj_project_id = ? AND LOWER(TRIM(c.email)) = LOWER(?)", userID, projectID, email).
		First(&cred).Error
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("find credential for user, project_id, and email: %w", err)
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

// Create inserts a new credential row (legacy — prefer CreateForUser).
func (r *GoogleBackupCredentialRepository) Create(email, projectID, accountType, refreshToken, storxToken string) (*GoogleBackupCredentialDB, error) {
	return r.CreateForUser("", email, projectID, accountType, refreshToken, storxToken)
}

// CreateForUser inserts a new user-scoped credential row.
func (r *GoogleBackupCredentialRepository) CreateForUser(userID, email, projectID, accountType, refreshToken, storxToken string) (*GoogleBackupCredentialDB, error) {
	acct := normalizeCredentialAccountType(accountType)
	if acct == "" {
		acct = "personal"
	}
	row := GoogleBackupCredentialDB{
		UserID:         strings.TrimSpace(userID),
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

// FindOrCreateForUser finds or creates a credential per Google account (email), not per storj_project_id alone.
// Reconnecting the same google_email updates tokens on the existing row; a different email always creates a new row
// even when project_id matches another connected account.
func (r *GoogleBackupCredentialRepository) FindOrCreateForUser(userID, email, projectID, accountType, refreshToken, storxToken string) (*GoogleBackupCredentialDB, error) {
	email = strings.TrimSpace(email)
	projectID = strings.TrimSpace(projectID)
	userID = strings.TrimSpace(userID)
	if userID != "" && projectID != "" {
		if row, ok, err := r.FindByUserProjectAndEmail(userID, projectID, email); err != nil {
			return nil, err
		} else if ok {
			return r.mergeAndReload(row.ID, email, projectID, accountType, refreshToken, storxToken, userID)
		}
	}
	if id, ok, err := r.FindIDForUserAndEmail(userID, email); err != nil {
		return nil, err
	} else if ok {
		return r.mergeAndReload(id, email, projectID, accountType, refreshToken, storxToken, userID)
	}
	if projectID != "" {
		if id, ok, err := r.FindIDForUserProjectAndEmail(userID, projectID, email); err != nil {
			return nil, err
		} else if ok {
			return r.mergeAndReload(id, email, projectID, accountType, refreshToken, storxToken, userID)
		}
	}
	return r.CreateForUser(userID, email, projectID, accountType, refreshToken, storxToken)
}

// ListByUserID returns all credentials for a user, optionally excluding a mailbox email.
func (r *GoogleBackupCredentialRepository) ListByUserID(userID, excludeEmail string) ([]GoogleBackupCredentialDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	q := r.db.Where("user_id = ?", userID)
	if exclude := strings.TrimSpace(excludeEmail); exclude != "" {
		q = q.Where("LOWER(TRIM(email)) <> LOWER(?)", exclude)
	}
	var rows []GoogleBackupCredentialDB
	if err := q.Order("email ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list credentials by user: %w", err)
	}
	return rows, nil
}

// ListByUserAndProject returns credentials for a user and project, optionally excluding a mailbox email.
func (r *GoogleBackupCredentialRepository) ListByUserAndProject(userID, projectID, excludeEmail string) ([]GoogleBackupCredentialDB, error) {
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return nil, fmt.Errorf("user_id and project_id are required")
	}
	q := r.db.Where("user_id = ? AND storj_project_id = ?", userID, projectID)
	if exclude := strings.TrimSpace(excludeEmail); exclude != "" {
		q = q.Where("LOWER(TRIM(email)) <> LOWER(?)", exclude)
	}
	var rows []GoogleBackupCredentialDB
	if err := q.Order("email ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list credentials by user and project: %w", err)
	}
	return rows, nil
}

func (r *GoogleBackupCredentialRepository) mergeAndReload(id uint, email, projectID, accountType, refreshToken, storxToken, userID string) (*GoogleBackupCredentialDB, error) {
	if err := r.mergeFieldsIfProvided(id, email, projectID, accountType, refreshToken, storxToken, userID); err != nil {
		return nil, err
	}
	return r.GetByID(id)
}

func (r *GoogleBackupCredentialRepository) mergeFieldsIfProvided(id uint, email, projectID, accountType, refreshToken, storxToken, userID string) error {
	patch := map[string]interface{}{}
	if t := strings.TrimSpace(userID); t != "" {
		patch["user_id"] = t
	}
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

// ClearStorxToken removes storx_token from the credential row (Google refresh_token is kept).
func (r *GoogleBackupCredentialRepository) ClearStorxToken(id uint) error {
	if id == 0 {
		return nil
	}
	return r.db.Model(&GoogleBackupCredentialDB{}).Where("id = ?", id).Update("storx_token", "").Error
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

// ListUniqueDomainsForUser returns distinct domains from credential emails linked to the user's jobs.
func (r *GoogleBackupCredentialRepository) ListUniqueDomainsForUser(userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var domains []string
	err := r.db.Table("google_backup_credential_dbs AS c").
		Select("DISTINCT LOWER(SPLIT_PART(TRIM(c.email), '@', 2)) AS domain").
		Joins(`INNER JOIN cron_job_listing_dbs j ON (j.input_data->>'credential_id')::bigint = c.id AND j.deleted_at IS NULL`).
		Where("j.user_id = ? AND COALESCE(j.placeholder, false) = ?", userID, false).
		Where("(j.input_data->>'credential_id') IS NOT NULL AND (j.input_data->>'credential_id')::bigint > 0").
		Where("SPLIT_PART(TRIM(c.email), '@', 2) <> ''").
		Order("domain ASC").
		Pluck("domain", &domains).Error
	if err != nil {
		return nil, fmt.Errorf("list unique domains for user: %w", err)
	}
	return domains, nil
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
