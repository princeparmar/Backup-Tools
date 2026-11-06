package repo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Tenant link categories: how the sign-in relates to the tenant.
const (
	MicrosoftTenantCategoryHome           = "home"
	MicrosoftTenantCategoryGuest          = "guest"
	MicrosoftTenantCategoryExternalMember = "external_member"
	MicrosoftTenantCategoryPersonal       = "personal"
)

// Role / token statuses. Unknown stays unknown: a failure never means "admin" or "no role".
const (
	MicrosoftStatusUnknown         = "unknown"
	MicrosoftStatusKnown           = "known"
	MicrosoftStatusWorking         = "working"
	MicrosoftStatusSigninRequired  = "signin_required"
	MicrosoftStatusConsentRequired = "consent_required"
	MicrosoftStatusError           = "error"
)

// Connection states of a tenant link.
const (
	MicrosoftConnectionDiscovered   = "discovered"
	MicrosoftConnectionConnected    = "connected"
	MicrosoftConnectionDisconnected = "disconnected"
)

// Backup modes chosen by the user. Access state never changes this automatically.
const (
	MicrosoftBackupModePersonal     = "personal"
	MicrosoftBackupModeOrganization = "organization"
)

// Auth modes: how StorX authenticates to the tenant.
const (
	MicrosoftAuthModeDelegated   = "delegated"
	MicrosoftAuthModeApplication = "application"
)

// Discovery statuses, stored on the home link only.
const (
	MicrosoftDiscoveryComplete          = "complete"
	MicrosoftDiscoveryUnavailable       = "unavailable"
	MicrosoftDiscoveryPermissionMissing = "permission_missing"
)

// Tenant availability values (microsoft_tenants.availability).
const (
	MicrosoftTenantAvailable   = "available"
	MicrosoftTenantUnavailable = "unavailable"
	MicrosoftTenantDeleted     = "deleted"
)

// MicrosoftRoleAssignment is one directory role of the sign-in in a tenant. Scope is "/" for
// tenant-wide roles or "/administrativeUnits/{id}" for scoped ones.
type MicrosoftRoleAssignment struct {
	TemplateID string `json:"template_id"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
}

// MicrosoftAccountTenantDB is a tenant reachable by a Microsoft sign-in ("accessible tenant").
// Identity is (credential_id, tenant_id); object_id is the person's object in that tenant.
type MicrosoftAccountTenantDB struct {
	CredentialID uint   `json:"credential_id" gorm:"column:credential_id;primaryKey;autoIncrement:false"`
	TenantID     string `json:"tenant_id" gorm:"column:tenant_id;primaryKey"`

	TenantName    string  `json:"tenant_name" gorm:"column:tenant_name"`
	DefaultDomain string  `json:"default_domain" gorm:"column:default_domain"`
	ObjectID      *string `json:"object_id,omitempty" gorm:"column:object_id"`
	Category      string  `json:"category" gorm:"column:category"`
	UserType      string  `json:"user_type" gorm:"column:user_type"`
	HomeTenantID  string  `json:"home_tenant_id" gorm:"column:home_tenant_id"`

	Roles      *database.DbJson[[]MicrosoftRoleAssignment] `json:"roles" gorm:"column:roles;type:jsonb"`
	IsAdmin    bool                                        `json:"is_admin" gorm:"column:is_admin;not null;default:false"`
	RoleStatus string                                      `json:"role_status" gorm:"column:role_status;not null;default:unknown"`

	TokenStatus string `json:"token_status" gorm:"column:token_status;not null;default:unknown"`
	TokenError  string `json:"token_error" gorm:"column:token_error"`

	AuthMode        string     `json:"auth_mode" gorm:"column:auth_mode;not null;default:delegated"`
	BackupMode      string     `json:"backup_mode" gorm:"column:backup_mode;not null;default:personal"`
	ConnectionState string     `json:"connection_state" gorm:"column:connection_state;not null;default:discovered;index"`
	ConnectedAt     *time.Time `json:"connected_at" gorm:"column:connected_at"`
	DisconnectedAt  *time.Time `json:"disconnected_at" gorm:"column:disconnected_at"`

	DiscoveryStatus string     `json:"discovery_status,omitempty" gorm:"column:discovery_status"`
	CheckedAt       *time.Time `json:"checked_at" gorm:"column:checked_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MicrosoftAccountTenantDB) TableName() string { return "microsoft_account_tenants" }

// RoleList returns stored roles (never nil).
func (l *MicrosoftAccountTenantDB) RoleList() []MicrosoftRoleAssignment {
	if l == nil || l.Roles == nil || l.Roles.Json() == nil {
		return []MicrosoftRoleAssignment{}
	}
	return append([]MicrosoftRoleAssignment{}, (*l.Roles.Json())...)
}

// Connected reports whether the link is connected.
func (l *MicrosoftAccountTenantDB) Connected() bool {
	return l != nil && l.ConnectionState == MicrosoftConnectionConnected
}

// Application reports whether StorX uses the tenant app-only token for this link.
func (l *MicrosoftAccountTenantDB) Application() bool {
	return l != nil && l.AuthMode == MicrosoftAuthModeApplication
}

// ObjectIDValue returns object_id or "".
func (l *MicrosoftAccountTenantDB) ObjectIDValue() string {
	if l == nil || l.ObjectID == nil {
		return ""
	}
	return *l.ObjectID
}

// MicrosoftAccountTenantRepository handles microsoft_account_tenants.
type MicrosoftAccountTenantRepository struct {
	db *gorm.DB
}

// NewMicrosoftAccountTenantRepository creates a tenant link repository.
func NewMicrosoftAccountTenantRepository(db *gorm.DB) *MicrosoftAccountTenantRepository {
	return &MicrosoftAccountTenantRepository{db: db}
}

// Get returns the link, or (nil, nil) when it does not exist.
func (r *MicrosoftAccountTenantRepository) Get(credentialID uint, tenantID string) (*MicrosoftAccountTenantDB, error) {
	tenantID = normalizeTenantID(tenantID)
	if credentialID == 0 || tenantID == "" {
		return nil, fmt.Errorf("credential_id and tenant_id are required")
	}
	var row MicrosoftAccountTenantDB
	err := r.db.Where("credential_id = ? AND tenant_id = ?", credentialID, tenantID).First(&row).Error
	if errors.Is(err, gormio.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get microsoft tenant link: %w", err)
	}
	return &row, nil
}

// ListByCredential returns every tenant link of a sign-in (home first, then by name).
func (r *MicrosoftAccountTenantRepository) ListByCredential(credentialID uint) ([]MicrosoftAccountTenantDB, error) {
	var rows []MicrosoftAccountTenantDB
	err := r.db.Where("credential_id = ?", credentialID).
		Order("CASE WHEN category = 'home' THEN 0 ELSE 1 END, tenant_name ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list microsoft tenant links: %w", err)
	}
	return rows, nil
}

// UserHasLink reports whether any of the user's Microsoft credentials is linked to the tenant.
func (r *MicrosoftAccountTenantRepository) UserHasLink(userID, tenantID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	tenantID = normalizeTenantID(tenantID)
	if userID == "" || tenantID == "" {
		return false, nil
	}
	var n int64
	err := r.db.Model(&MicrosoftAccountTenantDB{}).
		Joins("JOIN google_backup_credential_dbs c ON c.id = microsoft_account_tenants.credential_id").
		Where("c.user_id = ? AND microsoft_account_tenants.tenant_id = ?", userID, tenantID).Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("check tenant link: %w", err)
	}
	return n > 0, nil
}

// MicrosoftTenantDiscovery is what discovery learned about one tenant for a sign-in.
type MicrosoftTenantDiscovery struct {
	TenantID        string
	TenantName      string
	DefaultDomain   string
	ObjectID        string
	Category        string
	UserType        string
	HomeTenantID    string
	DiscoveryStatus string
}

// UpsertDiscovered creates the link as discovered, or refreshes its discovery fields. Connection,
// auth mode and backup mode are never touched here.
func (r *MicrosoftAccountTenantRepository) UpsertDiscovered(credentialID uint, d MicrosoftTenantDiscovery) (*MicrosoftAccountTenantDB, error) {
	tenantID := normalizeTenantID(d.TenantID)
	if credentialID == 0 || tenantID == "" {
		return nil, fmt.Errorf("credential_id and tenant_id are required")
	}
	now := time.Now().UTC()
	row := MicrosoftAccountTenantDB{
		CredentialID:    credentialID,
		TenantID:        tenantID,
		TenantName:      strings.TrimSpace(d.TenantName),
		DefaultDomain:   strings.TrimSpace(d.DefaultDomain),
		Category:        strings.TrimSpace(d.Category),
		UserType:        strings.TrimSpace(d.UserType),
		HomeTenantID:    normalizeTenantID(d.HomeTenantID),
		Roles:           database.NewDbJsonFromValue([]MicrosoftRoleAssignment{}),
		RoleStatus:      MicrosoftStatusUnknown,
		TokenStatus:     MicrosoftStatusUnknown,
		AuthMode:        MicrosoftAuthModeDelegated,
		BackupMode:      MicrosoftBackupModePersonal,
		ConnectionState: MicrosoftConnectionDiscovered,
		DiscoveryStatus: strings.TrimSpace(d.DiscoveryStatus),
		CheckedAt:       &now,
	}
	updates := []string{"category", "user_type", "home_tenant_id", "checked_at", "updated_at"}
	if row.TenantName != "" {
		updates = append(updates, "tenant_name")
	}
	if row.DefaultDomain != "" {
		updates = append(updates, "default_domain")
	}
	if row.DiscoveryStatus != "" {
		updates = append(updates, "discovery_status")
	}
	if oid := strings.TrimSpace(d.ObjectID); oid != "" {
		row.ObjectID = &oid
		updates = append(updates, "object_id")
	}
	err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "credential_id"}, {Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(&row).Error
	if err != nil {
		return nil, fmt.Errorf("upsert microsoft tenant link: %w", err)
	}
	return r.Get(credentialID, tenantID)
}

// SaveRoles stores the roles read for the sign-in in the tenant. isAdmin must only be true when
// status is known and a tenant-wide admin role is present.
func (r *MicrosoftAccountTenantRepository) SaveRoles(credentialID uint, tenantID string, roles []MicrosoftRoleAssignment, status string, isAdmin bool) error {
	if roles == nil {
		roles = []MicrosoftRoleAssignment{}
	}
	if status != MicrosoftStatusKnown {
		isAdmin = false
	}
	now := time.Now().UTC()
	return r.db.Model(&MicrosoftAccountTenantDB{}).
		Where("credential_id = ? AND tenant_id = ?", credentialID, normalizeTenantID(tenantID)).
		Updates(map[string]interface{}{
			"roles":       database.NewDbJsonFromValue(roles),
			"role_status": status,
			"is_admin":    isAdmin,
			"checked_at":  &now,
			"updated_at":  now,
		}).Error
}

// SaveTokenStatus stores whether a tenant-scoped token can be minted for the link.
func (r *MicrosoftAccountTenantRepository) SaveTokenStatus(credentialID uint, tenantID, status, tokenError string) error {
	return r.db.Model(&MicrosoftAccountTenantDB{}).
		Where("credential_id = ? AND tenant_id = ?", credentialID, normalizeTenantID(tenantID)).
		Updates(map[string]interface{}{
			"token_status": status,
			"token_error":  strings.TrimSpace(tokenError),
			"updated_at":   time.Now().UTC(),
		}).Error
}

// SaveObjectID records the person's object ID in the tenant once known.
func (r *MicrosoftAccountTenantRepository) SaveObjectID(credentialID uint, tenantID, objectID string) error {
	objectID = strings.TrimSpace(objectID)
	if objectID == "" {
		return nil
	}
	return r.db.Model(&MicrosoftAccountTenantDB{}).
		Where("credential_id = ? AND tenant_id = ?", credentialID, normalizeTenantID(tenantID)).
		Updates(map[string]interface{}{"object_id": objectID, "updated_at": time.Now().UTC()}).Error
}

// Connect marks the link connected with the user's chosen backup mode and the matching auth mode.
// Reconnecting reuses the same row.
func (r *MicrosoftAccountTenantRepository) Connect(credentialID uint, tenantID, backupMode, authMode string) error {
	now := time.Now().UTC()
	res := r.db.Model(&MicrosoftAccountTenantDB{}).
		Where("credential_id = ? AND tenant_id = ?", credentialID, normalizeTenantID(tenantID)).
		Updates(map[string]interface{}{
			"connection_state": MicrosoftConnectionConnected,
			"backup_mode":      backupMode,
			"auth_mode":        authMode,
			"connected_at":     &now,
			"updated_at":       now,
		})
	if res.Error != nil {
		return fmt.Errorf("connect microsoft tenant link: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gormio.ErrRecordNotFound
	}
	return nil
}

// Disconnect marks the link disconnected. Backups and jobs are kept.
func (r *MicrosoftAccountTenantRepository) Disconnect(credentialID uint, tenantID string) error {
	now := time.Now().UTC()
	res := r.db.Model(&MicrosoftAccountTenantDB{}).
		Where("credential_id = ? AND tenant_id = ?", credentialID, normalizeTenantID(tenantID)).
		Updates(map[string]interface{}{
			"connection_state": MicrosoftConnectionDisconnected,
			"disconnected_at":  &now,
			"updated_at":       now,
		})
	if res.Error != nil {
		return fmt.Errorf("disconnect microsoft tenant link: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gormio.ErrRecordNotFound
	}
	return nil
}

// MarkApplicationTokenError sets token_status=error on every application-mode link (expired app secret).
func (r *MicrosoftAccountTenantRepository) MarkApplicationTokenError(message string) error {
	return r.db.Model(&MicrosoftAccountTenantDB{}).
		Where("auth_mode = ?", MicrosoftAuthModeApplication).
		Updates(map[string]interface{}{
			"token_status": MicrosoftStatusError,
			"token_error":  strings.TrimSpace(message),
			"updated_at":   time.Now().UTC(),
		}).Error
}
