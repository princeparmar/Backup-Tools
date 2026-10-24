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

// Microsoft tenant consent statuses (shared contract consent.status).
const (
	MicrosoftConsentNotRequested = "not_requested"
	MicrosoftConsentGranted      = "granted"
	MicrosoftConsentInsufficient = "insufficient"
	MicrosoftConsentRevoked      = "revoked"
	MicrosoftConsentAuthError    = "auth_error"
)

// MicrosoftCapabilityError explains why a capability is false (shared contract capability_errors).
type MicrosoftCapabilityError struct {
	Code    string `json:"code"`
	Role    string `json:"role,omitempty"`
	Message string `json:"message,omitempty"`
}

// MicrosoftTenantDB is the Backup-Tools authority for a tenant's admin consent, application
// permissions and capabilities. Tenant users are listed live from Graph, never stored. Only the consent endpoint and the
// capability engine write consent/capability fields.
type MicrosoftTenantDB struct {
	TenantID   string `json:"tenant_id" gorm:"column:tenant_id;primaryKey"`
	TenantName string `json:"tenant_name" gorm:"column:tenant_name"`

	ConsentStatus string     `json:"consent_status" gorm:"column:consent_status;not null;default:not_requested;index"`
	ConsentedBy   string     `json:"consented_by" gorm:"column:consented_by"`
	ConsentedAt   *time.Time `json:"consented_at" gorm:"column:consented_at"`
	LastError     string     `json:"last_error" gorm:"column:last_error"`

	GrantedRoles     *database.DbJson[[]string]                            `json:"granted_roles" gorm:"column:granted_roles;type:jsonb"`
	Capabilities     *database.DbJson[map[string]bool]                     `json:"capabilities" gorm:"column:capabilities;type:jsonb"`
	CapabilityErrors *database.DbJson[map[string]MicrosoftCapabilityError] `json:"capability_errors" gorm:"column:capability_errors;type:jsonb"`
	CapabilityStatus string                                                `json:"capability_status" gorm:"column:capability_status"`
	ProbeVersion     int                                                   `json:"probe_version" gorm:"column:probe_version;not null;default:0"`
	LastProbeAt      *time.Time                                            `json:"last_probe_at" gorm:"column:last_probe_at"`

	// Cloud selects login/Graph/ARM hosts (global only today).
	Cloud string `json:"cloud" gorm:"column:cloud;not null;default:global"`
	// ServicePrincipalID is the platform app's enterprise application object in this tenant.
	ServicePrincipalID    string     `json:"service_principal_id" gorm:"column:service_principal_id"`
	Availability          string     `json:"availability" gorm:"column:availability;not null;default:available"`
	AvailabilityCheckedAt *time.Time `json:"availability_checked_at" gorm:"column:availability_checked_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MicrosoftTenantDB) TableName() string { return "microsoft_tenants" }

// GrantedRoleList returns granted application roles (never nil).
func (t *MicrosoftTenantDB) GrantedRoleList() []string {
	if t == nil || t.GrantedRoles == nil || t.GrantedRoles.Json() == nil {
		return []string{}
	}
	return append([]string{}, (*t.GrantedRoles.Json())...)
}

// CapabilityMap returns capabilities (never nil).
func (t *MicrosoftTenantDB) CapabilityMap() map[string]bool {
	out := map[string]bool{}
	if t == nil || t.Capabilities == nil || t.Capabilities.Json() == nil {
		return out
	}
	for k, v := range *t.Capabilities.Json() {
		out[k] = v
	}
	return out
}

// CapabilityErrorMap returns capability errors (never nil).
func (t *MicrosoftTenantDB) CapabilityErrorMap() map[string]MicrosoftCapabilityError {
	out := map[string]MicrosoftCapabilityError{}
	if t == nil || t.CapabilityErrors == nil || t.CapabilityErrors.Json() == nil {
		return out
	}
	for k, v := range *t.CapabilityErrors.Json() {
		out[k] = v
	}
	return out
}

// Capability reports one capability flag.
func (t *MicrosoftTenantDB) Capability(name string) bool {
	return t.CapabilityMap()[name]
}

// MicrosoftTenantRepository handles microsoft_tenants.
type MicrosoftTenantRepository struct {
	db *gorm.DB
}

// NewMicrosoftTenantRepository creates a tenant repository.
func NewMicrosoftTenantRepository(db *gorm.DB) *MicrosoftTenantRepository {
	return &MicrosoftTenantRepository{db: db}
}

func normalizeTenantID(tenantID string) string {
	return strings.ToLower(strings.TrimSpace(tenantID))
}

// Get returns the tenant row, or (nil, nil) when it does not exist yet.
func (r *MicrosoftTenantRepository) Get(tenantID string) (*MicrosoftTenantDB, error) {
	tenantID = normalizeTenantID(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	var row MicrosoftTenantDB
	err := r.db.Where("tenant_id = ?", tenantID).First(&row).Error
	if errors.Is(err, gormio.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get microsoft tenant: %w", err)
	}
	return &row, nil
}

// GetOrCreate returns the tenant row, creating a not_requested row when missing.
func (r *MicrosoftTenantRepository) GetOrCreate(tenantID, tenantName string) (*MicrosoftTenantDB, error) {
	tenantID = normalizeTenantID(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	row := MicrosoftTenantDB{
		TenantID:         tenantID,
		TenantName:       strings.TrimSpace(tenantName),
		ConsentStatus:    MicrosoftConsentNotRequested,
		GrantedRoles:     database.NewDbJsonFromValue([]string{}),
		Capabilities:     database.NewDbJsonFromValue(map[string]bool{}),
		CapabilityErrors: database.NewDbJsonFromValue(map[string]MicrosoftCapabilityError{}),
	}
	if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create microsoft tenant: %w", err)
	}
	got, err := r.Get(tenantID)
	if err != nil {
		return nil, err
	}
	if got != nil && got.TenantName == "" && strings.TrimSpace(tenantName) != "" {
		_ = r.db.Model(&MicrosoftTenantDB{}).Where("tenant_id = ?", tenantID).
			Update("tenant_name", strings.TrimSpace(tenantName)).Error
		got.TenantName = strings.TrimSpace(tenantName)
	}
	return got, nil
}

// MicrosoftConsentUpdate is the result of a consent check.
type MicrosoftConsentUpdate struct {
	Status       string
	ConsentedBy  string
	GrantedRoles []string
	LastError    string
	// MarkConsented sets consented_by/consented_at (fresh admin consent callback).
	MarkConsented bool
}

// SaveConsent persists a consent outcome.
func (r *MicrosoftTenantRepository) SaveConsent(tenantID string, u MicrosoftConsentUpdate) error {
	tenantID = normalizeTenantID(tenantID)
	roles := u.GrantedRoles
	if roles == nil {
		roles = []string{}
	}
	patch := map[string]interface{}{
		"consent_status": u.Status,
		"granted_roles":  database.NewDbJsonFromValue(roles),
		"last_error":     u.LastError,
		"updated_at":     time.Now().UTC(),
	}
	if u.MarkConsented {
		now := time.Now().UTC()
		patch["consented_by"] = strings.TrimSpace(u.ConsentedBy)
		patch["consented_at"] = &now
	}
	return r.db.Model(&MicrosoftTenantDB{}).Where("tenant_id = ?", tenantID).Updates(patch).Error
}

// SaveServicePrincipal records the platform app's service principal in the tenant ("" = missing).
func (r *MicrosoftTenantRepository) SaveServicePrincipal(tenantID, servicePrincipalID string) error {
	return r.db.Model(&MicrosoftTenantDB{}).Where("tenant_id = ?", normalizeTenantID(tenantID)).Updates(map[string]interface{}{
		"service_principal_id": strings.TrimSpace(servicePrincipalID),
		"updated_at":           time.Now().UTC(),
	}).Error
}

// SetAvailability records whether the tenant still exists for Microsoft (available/unavailable/deleted).
func (r *MicrosoftTenantRepository) SetAvailability(tenantID, availability string) error {
	now := time.Now().UTC()
	return r.db.Model(&MicrosoftTenantDB{}).Where("tenant_id = ?", normalizeTenantID(tenantID)).Updates(map[string]interface{}{
		"availability":            availability,
		"availability_checked_at": &now,
		"updated_at":              now,
	}).Error
}

// MicrosoftCapabilityUpdate is a capability engine result.
type MicrosoftCapabilityUpdate struct {
	Capabilities map[string]bool
	Errors       map[string]MicrosoftCapabilityError
	Status       string
	ProbeVersion int
}

// SaveCapabilities persists a capability engine result.
func (r *MicrosoftTenantRepository) SaveCapabilities(tenantID string, u MicrosoftCapabilityUpdate) error {
	tenantID = normalizeTenantID(tenantID)
	caps := u.Capabilities
	if caps == nil {
		caps = map[string]bool{}
	}
	errs := u.Errors
	if errs == nil {
		errs = map[string]MicrosoftCapabilityError{}
	}
	now := time.Now().UTC()
	return r.db.Model(&MicrosoftTenantDB{}).Where("tenant_id = ?", tenantID).Updates(map[string]interface{}{
		"capabilities":      database.NewDbJsonFromValue(caps),
		"capability_errors": database.NewDbJsonFromValue(errs),
		"capability_status": u.Status,
		"probe_version":     u.ProbeVersion,
		"last_probe_at":     &now,
		"updated_at":        now,
	}).Error
}
