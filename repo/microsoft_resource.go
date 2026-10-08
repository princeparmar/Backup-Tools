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

// Job / resource types. Google jobs always use ResourceTypeUser.
const (
	ResourceTypeUser    = "user"
	ResourceTypeMailbox = "mailbox"
	ResourceTypeDrive   = "drive"
	ResourceTypeSite    = "site"
	ResourceTypeTeam    = "team"
	ResourceTypeGroup   = "group"
)

// Microsoft resource lifecycle states.
const (
	MicrosoftResourceActive   = "active"
	MicrosoftResourceDisabled = "disabled"
	MicrosoftResourceDeleted  = "deleted"
)

// MicrosoftResourceDB is a Microsoft object selected for backup or seen by a job. It is owned by
// tenant_id; membership from another tenant never adds a resource here.
type MicrosoftResourceDB struct {
	TenantID     string `json:"tenant_id" gorm:"column:tenant_id;primaryKey"`
	ResourceType string `json:"resource_type" gorm:"column:resource_type;primaryKey"`
	ExternalID   string `json:"external_id" gorm:"column:external_id;primaryKey"`

	// OwnerExternalID is the owning user's object ID for user-owned services (mailbox, drive).
	OwnerExternalID string `json:"owner_external_id,omitempty" gorm:"column:owner_external_id"`
	DisplayName     string `json:"display_name" gorm:"column:display_name"`
	Mail            string `json:"mail,omitempty" gorm:"column:mail"`
	UPN             string `json:"upn,omitempty" gorm:"column:upn"`
	UserType        string `json:"user_type,omitempty" gorm:"column:user_type"`
	HomeTenantID    string `json:"home_tenant_id,omitempty" gorm:"column:home_tenant_id"`

	State      string     `json:"state" gorm:"column:state;not null;default:active"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty" gorm:"column:deleted_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty" gorm:"column:last_seen_at"`

	Metadata *database.DbJson[map[string]interface{}] `json:"metadata" gorm:"column:metadata;type:jsonb"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MicrosoftResourceDB) TableName() string { return "microsoft_resources" }

// MetadataMap returns metadata (never nil).
func (r *MicrosoftResourceDB) MetadataMap() map[string]interface{} {
	if r == nil || r.Metadata == nil || r.Metadata.Json() == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(*r.Metadata.Json()))
	for k, v := range *r.Metadata.Json() {
		out[k] = v
	}
	return out
}

// UnavailableServices returns metadata.unavailable_services (services the resource has no data for).
func (r *MicrosoftResourceDB) UnavailableServices() []string {
	raw, ok := r.MetadataMap()["unavailable_services"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// MicrosoftResourceRepository handles microsoft_resources.
type MicrosoftResourceRepository struct {
	db *gorm.DB
}

// NewMicrosoftResourceRepository creates a resource repository.
func NewMicrosoftResourceRepository(db *gorm.DB) *MicrosoftResourceRepository {
	return &MicrosoftResourceRepository{db: db}
}

func normalizeResourceKey(tenantID, resourceType, externalID string) (string, string, string, error) {
	tenantID = normalizeTenantID(tenantID)
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	externalID = strings.TrimSpace(externalID)
	if tenantID == "" || resourceType == "" || externalID == "" {
		return "", "", "", fmt.Errorf("tenant_id, resource_type and external_id are required")
	}
	return tenantID, resourceType, externalID, nil
}

// Upsert creates or refreshes a resource as seen now. A deleted or disabled resource seen again
// becomes active (same object ID restored).
func (r *MicrosoftResourceRepository) Upsert(res MicrosoftResourceDB) (*MicrosoftResourceDB, error) {
	tid, typ, id, err := normalizeResourceKey(res.TenantID, res.ResourceType, res.ExternalID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res.TenantID, res.ResourceType, res.ExternalID = tid, typ, id
	res.State = MicrosoftResourceActive
	res.DeletedAt = nil
	res.LastSeenAt = &now
	if res.Metadata == nil {
		res.Metadata = database.NewDbJsonFromValue(map[string]interface{}{})
	}
	updates := []string{"state", "deleted_at", "last_seen_at", "updated_at"}
	for col, v := range map[string]string{
		"owner_external_id": res.OwnerExternalID, "display_name": res.DisplayName, "mail": res.Mail,
		"upn": res.UPN, "user_type": res.UserType, "home_tenant_id": res.HomeTenantID,
	} {
		if strings.TrimSpace(v) != "" {
			updates = append(updates, col)
		}
	}
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "resource_type"}, {Name: "external_id"}},
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(&res).Error; err != nil {
		return nil, fmt.Errorf("upsert microsoft resource: %w", err)
	}
	return r.Get(tid, typ, id)
}

// Get returns the resource, or (nil, nil) when unknown.
func (r *MicrosoftResourceRepository) Get(tenantID, resourceType, externalID string) (*MicrosoftResourceDB, error) {
	tid, typ, id, err := normalizeResourceKey(tenantID, resourceType, externalID)
	if err != nil {
		return nil, err
	}
	var row MicrosoftResourceDB
	err = r.db.Where("tenant_id = ? AND resource_type = ? AND external_id = ?", tid, typ, id).First(&row).Error
	if errors.Is(err, gormio.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get microsoft resource: %w", err)
	}
	return &row, nil
}

// ListByTenantAndType returns the tenant's known resources of one type.
func (r *MicrosoftResourceRepository) ListByTenantAndType(tenantID, resourceType string) ([]MicrosoftResourceDB, error) {
	var rows []MicrosoftResourceDB
	err := r.db.Where("tenant_id = ? AND resource_type = ?", normalizeTenantID(tenantID), strings.ToLower(strings.TrimSpace(resourceType))).
		Order("display_name ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list microsoft resources: %w", err)
	}
	return rows, nil
}

// MarkState sets the lifecycle state (active/disabled/deleted). Backups are never touched.
func (r *MicrosoftResourceRepository) MarkState(tenantID, resourceType, externalID, state string) error {
	tid, typ, id, err := normalizeResourceKey(tenantID, resourceType, externalID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	patch := map[string]interface{}{"state": state, "updated_at": now}
	if state == MicrosoftResourceDeleted {
		patch["deleted_at"] = &now
	} else {
		patch["deleted_at"] = nil
	}
	return r.db.Model(&MicrosoftResourceDB{}).
		Where("tenant_id = ? AND resource_type = ? AND external_id = ?", tid, typ, id).Updates(patch).Error
}

// AddUnavailableService records that the resource has no data for a service (no mailbox, no OneDrive).
func (r *MicrosoftResourceRepository) AddUnavailableService(tenantID, resourceType, externalID, service string) error {
	row, err := r.Get(tenantID, resourceType, externalID)
	if err != nil || row == nil {
		return err
	}
	meta := row.MetadataMap()
	services := row.UnavailableServices()
	for _, s := range services {
		if s == service {
			return nil
		}
	}
	meta["unavailable_services"] = append(services, service)
	return r.db.Model(&MicrosoftResourceDB{}).
		Where("tenant_id = ? AND resource_type = ? AND external_id = ?", row.TenantID, row.ResourceType, row.ExternalID).
		Updates(map[string]interface{}{"metadata": database.NewDbJsonFromValue(meta), "updated_at": time.Now().UTC()}).Error
}
