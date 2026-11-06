package repo

import (
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"gorm.io/gorm/clause"
)

// Scope selection modes.
const (
	MicrosoftScopeSelected = "selected"
	MicrosoftScopeAll      = "all"
)

// MicrosoftBackupScopeDB says what should be backed up for a tenant, per resource type. With
// selection_mode=all, reconcile creates jobs for new resources; with selected, the existing jobs are
// the selection and reconcile never adds jobs.
type MicrosoftBackupScopeDB struct {
	ID uint `json:"id" gorm:"primaryKey"`

	UserID         string `json:"user_id" gorm:"column:user_id;not null;uniqueIndex:idx_ms_backup_scope,priority:1"`
	StorjProjectID string `json:"storj_project_id" gorm:"column:storj_project_id;not null;default:'';uniqueIndex:idx_ms_backup_scope,priority:2"`
	CredentialID   uint   `json:"credential_id" gorm:"column:credential_id;not null;index"`
	TenantID       string `json:"tenant_id" gorm:"column:tenant_id;not null;uniqueIndex:idx_ms_backup_scope,priority:3"`
	ResourceType   string `json:"resource_type" gorm:"column:resource_type;not null;uniqueIndex:idx_ms_backup_scope,priority:4"`
	SelectionMode  string `json:"selection_mode" gorm:"column:selection_mode;not null;default:selected"`
	SyncType       string `json:"sync_type" gorm:"column:sync_type;not null;uniqueIndex:idx_ms_backup_scope,priority:5"`
	PolicyID       uint   `json:"policy_id" gorm:"column:policy_id"`

	Active           bool       `json:"active" gorm:"column:active;not null;default:true"`
	LastReconciledAt *time.Time `json:"last_reconciled_at" gorm:"column:last_reconciled_at"`
	ReconcileError   string     `json:"reconcile_error" gorm:"column:reconcile_error"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MicrosoftBackupScopeDB) TableName() string { return "microsoft_backup_scopes" }

// MicrosoftBackupScopeRepository handles microsoft_backup_scopes.
type MicrosoftBackupScopeRepository struct {
	db *gorm.DB
}

// NewMicrosoftBackupScopeRepository creates a scope repository.
func NewMicrosoftBackupScopeRepository(db *gorm.DB) *MicrosoftBackupScopeRepository {
	return &MicrosoftBackupScopeRepository{db: db}
}

// Upsert creates or updates the scope for (user, project, tenant, resource_type, sync_type).
// Switching all -> selected keeps existing jobs; new resources simply stop being added.
func (r *MicrosoftBackupScopeRepository) Upsert(s MicrosoftBackupScopeDB) (*MicrosoftBackupScopeDB, error) {
	s.UserID = strings.TrimSpace(s.UserID)
	s.TenantID = normalizeTenantID(s.TenantID)
	s.ResourceType = strings.ToLower(strings.TrimSpace(s.ResourceType))
	if s.UserID == "" || s.TenantID == "" || s.ResourceType == "" || s.SyncType == "" || s.CredentialID == 0 {
		return nil, fmt.Errorf("user_id, credential_id, tenant_id, resource_type and sync_type are required")
	}
	if s.SelectionMode != MicrosoftScopeAll {
		s.SelectionMode = MicrosoftScopeSelected
	}
	s.Active = true
	updates := []string{"credential_id", "selection_mode", "active", "updated_at"}
	if s.PolicyID > 0 {
		updates = append(updates, "policy_id")
	}
	if err := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"}, {Name: "storj_project_id"}, {Name: "tenant_id"}, {Name: "resource_type"}, {Name: "sync_type"},
		},
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(&s).Error; err != nil {
		return nil, fmt.Errorf("upsert microsoft backup scope: %w", err)
	}
	var out MicrosoftBackupScopeDB
	err := r.db.Where("user_id = ? AND storj_project_id = ? AND tenant_id = ? AND resource_type = ? AND sync_type = ?",
		s.UserID, s.StorjProjectID, s.TenantID, s.ResourceType, s.SyncType).First(&out).Error
	if err != nil {
		return nil, fmt.Errorf("reload microsoft backup scope: %w", err)
	}
	return &out, nil
}

// EnsureSelected records a selected scope unless one already exists (an existing "all" scope is kept).
func (r *MicrosoftBackupScopeRepository) EnsureSelected(s MicrosoftBackupScopeDB) error {
	s.UserID = strings.TrimSpace(s.UserID)
	s.TenantID = normalizeTenantID(s.TenantID)
	s.ResourceType = strings.ToLower(strings.TrimSpace(s.ResourceType))
	if s.UserID == "" || s.TenantID == "" || s.ResourceType == "" || s.SyncType == "" || s.CredentialID == 0 {
		return fmt.Errorf("user_id, credential_id, tenant_id, resource_type and sync_type are required")
	}
	s.SelectionMode = MicrosoftScopeSelected
	s.Active = true
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&s).Error
}

// ListActive returns every active scope (reconcile input).
func (r *MicrosoftBackupScopeRepository) ListActive() ([]MicrosoftBackupScopeDB, error) {
	var rows []MicrosoftBackupScopeDB
	if err := r.db.Where("active = ?", true).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list microsoft backup scopes: %w", err)
	}
	return rows, nil
}

// SetReconcileResult records the last reconcile time and error ("" on success).
func (r *MicrosoftBackupScopeRepository) SetReconcileResult(id uint, reconcileErr string) error {
	now := time.Now().UTC()
	return r.db.Model(&MicrosoftBackupScopeDB{}).Where("id = ?", id).Updates(map[string]interface{}{
		"last_reconciled_at": &now,
		"reconcile_error":    strings.TrimSpace(reconcileErr),
		"updated_at":         now,
	}).Error
}
