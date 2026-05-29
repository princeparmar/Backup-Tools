package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

// AutosyncBackupPolicyDB stores per-job schedule (interval + on). Canonical source for cron scheduling.
type AutosyncBackupPolicyDB struct {
	gorm.GormModel

	CredentialID uint   `json:"credential_id" gorm:"column:credential_id;index"`
	JobID        uint   `json:"job_id" gorm:"column:job_id;uniqueIndex"`
	Interval     string `json:"interval" gorm:"column:interval"`
	On           string `json:"on" gorm:"column:on"`
}

// AutosyncBackupPolicyRepository handles autosync_backup_policy_dbs.
type AutosyncBackupPolicyRepository struct {
	db *gorm.DB
}

// NewAutosyncBackupPolicyRepository creates a policy repository.
func NewAutosyncBackupPolicyRepository(db *gorm.DB) *AutosyncBackupPolicyRepository {
	return &AutosyncBackupPolicyRepository{db: db}
}

// CreatePolicy inserts a policy row and syncs job.interval cache.
func (r *AutosyncBackupPolicyRepository) CreatePolicy(credentialID, jobID uint, interval, on string) (*AutosyncBackupPolicyDB, error) {
	if jobID == 0 {
		return nil, fmt.Errorf("job_id is required")
	}
	row := AutosyncBackupPolicyDB{
		CredentialID: credentialID,
		JobID:        jobID,
		Interval:     strings.TrimSpace(interval),
		On:           strings.TrimSpace(on),
	}
	if row.Interval == "" {
		return nil, fmt.Errorf("interval is required")
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}
	if err := r.syncJobIntervalCache(jobID, row.Interval); err != nil {
		return nil, err
	}
	return &row, nil
}

// GetByID loads a policy by primary key.
func (r *AutosyncBackupPolicyRepository) GetByID(policyID uint) (*AutosyncBackupPolicyDB, error) {
	if policyID == 0 {
		return nil, gormio.ErrRecordNotFound
	}
	var row AutosyncBackupPolicyDB
	if err := r.db.First(&row, policyID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetByJobID loads the policy for a cron job.
func (r *AutosyncBackupPolicyRepository) GetByJobID(jobID uint) (*AutosyncBackupPolicyDB, error) {
	if jobID == 0 {
		return nil, gormio.ErrRecordNotFound
	}
	var row AutosyncBackupPolicyDB
	err := r.db.Where("job_id = ?", jobID).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListByCredentialID returns all policies for a credential ordered by job_id.
func (r *AutosyncBackupPolicyRepository) ListByCredentialID(credentialID uint) ([]AutosyncBackupPolicyDB, error) {
	if credentialID == 0 {
		return nil, nil
	}
	var rows []AutosyncBackupPolicyDB
	err := r.db.Where("credential_id = ?", credentialID).Order("job_id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list policies by credential: %w", err)
	}
	return rows, nil
}

// UpdateSchedule updates interval and on for one policy and syncs job.interval cache.
func (r *AutosyncBackupPolicyRepository) UpdateSchedule(policyID uint, interval, on string) error {
	policy, err := r.GetByID(policyID)
	if err != nil {
		return err
	}
	patch := map[string]interface{}{
		"interval": strings.TrimSpace(interval),
		"on":       strings.TrimSpace(on),
	}
	if err := r.db.Model(&AutosyncBackupPolicyDB{}).Where("id = ?", policyID).Updates(patch).Error; err != nil {
		return fmt.Errorf("update policy schedule: %w", err)
	}
	return r.syncJobIntervalCache(policy.JobID, patch["interval"].(string))
}

// UpdateScheduleForCredential bulk-updates interval+on for all policies under a credential.
func (r *AutosyncBackupPolicyRepository) UpdateScheduleForCredential(credentialID uint, interval, on string) error {
	if credentialID == 0 {
		return nil
	}
	interval = strings.TrimSpace(interval)
	on = strings.TrimSpace(on)
	policies, err := r.ListByCredentialID(credentialID)
	if err != nil {
		return err
	}
	if len(policies) == 0 {
		return nil
	}
	patch := map[string]interface{}{"interval": interval, "on": on}
	if err := r.db.Model(&AutosyncBackupPolicyDB{}).Where("credential_id = ?", credentialID).Updates(patch).Error; err != nil {
		return fmt.Errorf("bulk update policies: %w", err)
	}
	for _, p := range policies {
		if err := r.syncJobIntervalCache(p.JobID, interval); err != nil {
			return err
		}
	}
	return nil
}

func (r *AutosyncBackupPolicyRepository) syncJobIntervalCache(jobID uint, interval string) error {
	return r.db.Model(&CronJobListingDB{}).Where("id = ?", jobID).Update("interval", interval).Error
}

// EnrichJobFromPolicy sets PolicyID, Interval, and On on a job from its policy row when present.
func (r *AutosyncBackupPolicyRepository) EnrichJobFromPolicy(job *CronJobListingDB) {
	if job == nil || job.ID == 0 {
		return
	}
	policy, err := r.GetByJobID(job.ID)
	if err != nil {
		if errors.Is(err, gormio.ErrRecordNotFound) {
			return
		}
		return
	}
	job.PolicyID = policy.ID
	job.Interval = policy.Interval
	job.On = policy.On
}

// EnrichJobsFromPolicy batch-enriches jobs from policies for a credential.
func (r *AutosyncBackupPolicyRepository) EnrichJobsFromPolicy(jobs []CronJobListingDB) {
	for i := range jobs {
		r.EnrichJobFromPolicy(&jobs[i])
	}
}

// BackfillFromJobs inserts policy rows from existing credential-linked cron jobs.
// Idempotent: if cron_job_listing_dbs no longer has legacy column "on", backfill uses empty on.
func (r *AutosyncBackupPolicyRepository) BackfillFromJobs() error {
	onExpr := "''"
	if r.cronJobsTableHasOnColumn() {
		onExpr = `COALESCE("on", '')`
	}
	sql := fmt.Sprintf(`
INSERT INTO autosync_backup_policy_dbs (credential_id, job_id, interval, "on", created_at, updated_at)
SELECT (input_data->>'credential_id')::bigint, id, interval, %s, NOW(), NOW()
FROM cron_job_listing_dbs
WHERE (input_data->>'credential_id') IS NOT NULL
  AND interval NOT IN ('', 'one_time')
  AND deleted_at IS NULL
ON CONFLICT (job_id) DO NOTHING
`, onExpr)
	return r.db.Exec(sql).Error
}

func (r *AutosyncBackupPolicyRepository) cronJobsTableHasOnColumn() bool {
	var exists bool
	err := r.db.Raw(`
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = current_schema()
    AND table_name = 'cron_job_listing_dbs'
    AND column_name = 'on'
)`).Scan(&exists).Error
	return err == nil && exists
}
