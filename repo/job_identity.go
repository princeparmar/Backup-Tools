package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	gormio "gorm.io/gorm"
)

// ProviderForMethod returns the provider owning a job method (outlook* = microsoft).
func ProviderForMethod(method string) string {
	if IsMicrosoftMethod(method) {
		return CredentialProviderMicrosoft
	}
	return CredentialProviderGoogle
}

// IsMicrosoftMethod reports whether a job method is a Microsoft service.
func IsMicrosoftMethod(method string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "outlook")
}

// MicrosoftResourceTypeForMethod returns the resource type a Microsoft job method backs up.
func MicrosoftResourceTypeForMethod(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "outlook_sharepoint":
		return ResourceTypeSite
	case "outlook_teams":
		return ResourceTypeTeam
	case "outlook_groups":
		return ResourceTypeGroup
	default:
		return ResourceTypeUser
	}
}

// BeforeCreate fills the job identity for jobs created without one. Google jobs keep their
// name-based uniqueness through resource_id = name.
func (j *CronJobListingDB) BeforeCreate(*gormio.DB) error {
	if strings.TrimSpace(j.Provider) == "" {
		j.Provider = ProviderForMethod(j.Method)
	}
	if strings.TrimSpace(j.ResourceType) == "" {
		j.ResourceType = ResourceTypeUser
	}
	if strings.TrimSpace(j.ResourceID) == "" {
		j.ResourceID = j.Name
	}
	j.TenantID = normalizeTenantID(j.TenantID)
	return nil
}

// FindMicrosoftResourceJob finds a Microsoft job by its identity (never by display name).
func (r *CronJobRepository) FindMicrosoftResourceJob(userID, tenantID, resourceType, resourceID, method, syncType string) (*CronJobListingDB, error) {
	var res CronJobListingDB
	err := r.db.Where("user_id = ? AND provider = ? AND tenant_id = ? AND resource_type = ? AND resource_id = ? AND method = ? AND sync_type = ?",
		userID, CredentialProviderMicrosoft, normalizeTenantID(tenantID), resourceType, strings.TrimSpace(resourceID), method, syncType).
		First(&res).Error
	if errors.Is(err, gormio.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find microsoft resource job: %w", err)
	}
	return &res, nil
}

// MicrosoftJobIdentity is the identity of a Microsoft job.
type MicrosoftJobIdentity struct {
	TenantID     string
	ResourceType string
	ResourceID   string
}

// CreateMicrosoftResourceJob creates a Microsoft job keyed by tenant + resource; name is a label.
func (r *CronJobRepository) CreateMicrosoftResourceJob(userID, name, method, syncType string, credentialID uint, id MicrosoftJobIdentity, inputData map[string]interface{}) (*CronJobListingDB, error) {
	if credentialID == 0 {
		return nil, fmt.Errorf("credential_id is required")
	}
	id.TenantID = normalizeTenantID(id.TenantID)
	id.ResourceID = strings.TrimSpace(id.ResourceID)
	if id.TenantID == "" || id.ResourceType == "" || id.ResourceID == "" {
		return nil, fmt.Errorf("tenant_id, resource_type and resource_id are required")
	}
	if inputData == nil {
		inputData = map[string]interface{}{}
	}
	if _, ok := inputData["email"]; !ok {
		inputData["email"] = strings.TrimSpace(name)
	}
	inputData["credential_id"] = credentialID
	data := CronJobListingDB{
		UserID:       userID,
		Name:         strings.TrimSpace(name),
		Method:       method,
		SyncType:     syncType,
		Provider:     CredentialProviderMicrosoft,
		TenantID:     id.TenantID,
		ResourceType: id.ResourceType,
		ResourceID:   id.ResourceID,
		InputData:    database.NewDbJsonFromValue(inputData),
		Status:       JobStatusCreated,
	}
	if syncType == "one_time" {
		data.Interval = "one_time"
		data.Active = true
	}
	if err := r.db.Create(&data).Error; err != nil {
		return nil, fmt.Errorf("error creating cron job: %v", err)
	}
	return &data, nil
}

// ListMicrosoftJobsByTenant returns the user's Microsoft jobs for a credential in a tenant.
func (r *CronJobRepository) ListMicrosoftJobsByTenant(credentialID uint, tenantID string) ([]CronJobListingDB, error) {
	var rows []CronJobListingDB
	err := r.db.Where("provider = ? AND tenant_id = ?", CredentialProviderMicrosoft, normalizeTenantID(tenantID)).
		Where(whereInputDataCredentialID, credentialID).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list microsoft jobs by tenant: %w", err)
	}
	return rows, nil
}

// FindMicrosoftJobsForRestore returns the user's Microsoft jobs for a method whose name, email or
// resource ID equals loginID, optionally limited to one tenant (newest active first).
func (r *CronJobRepository) FindMicrosoftJobsForRestore(userID, method, tenantID, loginID string) ([]CronJobListingDB, error) {
	loginID = strings.TrimSpace(loginID)
	q := r.db.Where("user_id = ? AND provider = ? AND method = ?", strings.TrimSpace(userID), CredentialProviderMicrosoft, strings.TrimSpace(method))
	if tid := normalizeTenantID(tenantID); tid != "" {
		q = q.Where("tenant_id = ?", tid)
	}
	var rows []CronJobListingDB
	err := q.Where("(LOWER(name) = LOWER(?) OR resource_id = ?)", loginID, loginID).
		Order("active DESC, id DESC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("find microsoft jobs for restore: %w", err)
	}
	return rows, nil
}

// ListMicrosoftScopeJobs returns the user's Microsoft jobs of one resource type and sync type in a
// tenant (all credentials; callers filter by credential).
func (r *CronJobRepository) ListMicrosoftScopeJobs(userID, tenantID, resourceType, syncType string) ([]CronJobListingDB, error) {
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ? AND provider = ? AND tenant_id = ? AND resource_type = ? AND sync_type = ?",
		userID, CredentialProviderMicrosoft, normalizeTenantID(tenantID), resourceType, syncType).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list microsoft scope jobs: %w", err)
	}
	return rows, nil
}

// ListMicrosoftResourceJobs returns the user's Microsoft jobs of one resource (every method).
func (r *CronJobRepository) ListMicrosoftResourceJobs(userID, tenantID, resourceType, resourceID string) ([]CronJobListingDB, error) {
	var rows []CronJobListingDB
	err := r.db.Where("user_id = ? AND provider = ? AND tenant_id = ? AND resource_type = ? AND resource_id = ?",
		userID, CredentialProviderMicrosoft, normalizeTenantID(tenantID), resourceType, strings.TrimSpace(resourceID)).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list microsoft resource jobs: %w", err)
	}
	return rows, nil
}

// ListActiveMicrosoftJobsByTenant returns every active Microsoft job of a tenant (all users).
func (r *CronJobRepository) ListActiveMicrosoftJobsByTenant(tenantID string) ([]CronJobListingDB, error) {
	var rows []CronJobListingDB
	err := r.db.Where("provider = ? AND tenant_id = ? AND active = ?", CredentialProviderMicrosoft, normalizeTenantID(tenantID), true).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list active microsoft jobs by tenant: %w", err)
	}
	return rows, nil
}

// PauseJobs deactivates jobs with a warning message. Backups are kept.
func (r *CronJobRepository) PauseJobs(ids []uint, message string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Model(&CronJobListingDB{}).Where("id IN ?", ids).Updates(map[string]interface{}{
		"active":         false,
		"message":        message,
		"message_status": "warning",
	}).Error
}

const (
	legacyCredentialIndex = "idx_google_backup_cred_user_project_email"
	legacyCronJobIndex    = "idx_name_sync_type_user"
	credentialIdentityIdx = "idx_backup_cred_identity"
	cronJobIdentityIdx    = "idx_cron_job_identity"
)

// MigrateBackupIdentity moves existing credentials and jobs to the provider/account/resource
// identity before AutoMigrate runs, in one transaction: add the columns with defaults, backfill
// existing rows, drop the legacy unique index and create the new one. For Google the new keys
// allow exactly the same rows as the old ones. Fresh databases (no tables) are left to AutoMigrate.
func MigrateBackupIdentity(db *gormio.DB) error {
	return db.Transaction(func(tx *gormio.DB) error {
		m := tx.Migrator()
		if m.HasTable(&GoogleBackupCredentialDB{}) {
			for _, field := range []string{"Provider", "ExternalAccountID"} {
				if !m.HasColumn(&GoogleBackupCredentialDB{}, field) {
					if err := m.AddColumn(&GoogleBackupCredentialDB{}, field); err != nil {
						return fmt.Errorf("add credential column %s: %w", field, err)
					}
				}
			}
			if err := tx.Exec(`UPDATE google_backup_credential_dbs SET
				provider = CASE WHEN TRIM(COALESCE(tenant_id, '')) <> '' THEN 'microsoft' ELSE 'google' END,
				external_account_id = LOWER(TRIM(COALESCE(email, '')))
				WHERE external_account_id = ''`).Error; err != nil {
				return fmt.Errorf("backfill credential identity: %w", err)
			}
			if m.HasIndex(&GoogleBackupCredentialDB{}, legacyCredentialIndex) {
				if err := m.DropIndex(&GoogleBackupCredentialDB{}, legacyCredentialIndex); err != nil {
					return fmt.Errorf("drop %s: %w", legacyCredentialIndex, err)
				}
			}
			if !m.HasIndex(&GoogleBackupCredentialDB{}, credentialIdentityIdx) {
				if err := m.CreateIndex(&GoogleBackupCredentialDB{}, credentialIdentityIdx); err != nil {
					return fmt.Errorf("create %s: %w", credentialIdentityIdx, err)
				}
			}
		}
		if m.HasTable(&CronJobListingDB{}) {
			for _, field := range []string{"Provider", "TenantID", "ResourceType", "ResourceID"} {
				if !m.HasColumn(&CronJobListingDB{}, field) {
					if err := m.AddColumn(&CronJobListingDB{}, field); err != nil {
						return fmt.Errorf("add cron job column %s: %w", field, err)
					}
				}
			}
			if err := tx.Exec(`UPDATE cron_job_listing_dbs SET
				provider = CASE WHEN method LIKE 'outlook%' THEN 'microsoft' ELSE 'google' END,
				resource_type = CASE method
					WHEN 'outlook_sharepoint' THEN 'site'
					WHEN 'outlook_teams' THEN 'team'
					WHEN 'outlook_groups' THEN 'group'
					ELSE 'user' END,
				resource_id = name
				WHERE resource_id = ''`).Error; err != nil {
				return fmt.Errorf("backfill cron job identity: %w", err)
			}
			if err := tx.Exec(`UPDATE cron_job_listing_dbs SET tenant_id = COALESCE((
				SELECT LOWER(TRIM(COALESCE(c.tenant_id, ''))) FROM google_backup_credential_dbs c
				WHERE c.id = CAST(cron_job_listing_dbs.input_data->>'credential_id' AS INTEGER)), '')
				WHERE provider = 'microsoft' AND tenant_id = ''`).Error; err != nil {
				return fmt.Errorf("backfill cron job tenant: %w", err)
			}
			if m.HasIndex(&CronJobListingDB{}, legacyCronJobIndex) {
				if err := m.DropIndex(&CronJobListingDB{}, legacyCronJobIndex); err != nil {
					return fmt.Errorf("drop %s: %w", legacyCronJobIndex, err)
				}
			}
			if !m.HasIndex(&CronJobListingDB{}, cronJobIdentityIdx) {
				if err := m.CreateIndex(&CronJobListingDB{}, cronJobIdentityIdx); err != nil {
					return fmt.Errorf("create %s: %w", cronJobIdentityIdx, err)
				}
			}
		}
		return nil
	})
}
