package repo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

// AutosyncBackupPolicyDB stores shared schedule policy rows. Jobs reference policies by cron_job_listing_dbs.policy_id.
type AutosyncBackupPolicyDB struct {
	gorm.GormModel

	UserID        string     `json:"user_id" gorm:"column:user_id;index"`
	CredentialID  uint       `json:"credential_id" gorm:"column:credential_id;index"`
	Interval      string     `json:"interval" gorm:"column:interval"`
	On            string     `json:"on" gorm:"column:on"`
	RetentionType string     `json:"retention_type" gorm:"column:retention_type;default:never"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" gorm:"column:expires_at"`
	IsExpired     bool       `json:"is_expired" gorm:"column:is_expired;default:false"`
}

const (
	RetentionNever  = "never"
	Retention30Days = "30_days"
	Retention1Year  = "1_year"
	Retention7Years = "7_years"
)

// AutosyncBackupPolicyRepository handles autosync_backup_policy_dbs.
type AutosyncBackupPolicyRepository struct {
	db *gorm.DB
}

// NewAutosyncBackupPolicyRepository creates a policy repository.
func NewAutosyncBackupPolicyRepository(db *gorm.DB) *AutosyncBackupPolicyRepository {
	return &AutosyncBackupPolicyRepository{db: db}
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

// ListByCredentialID returns all policies for a credential.
func (r *AutosyncBackupPolicyRepository) ListByCredentialID(credentialID uint) ([]AutosyncBackupPolicyDB, error) {
	if credentialID == 0 {
		return nil, nil
	}
	var rows []AutosyncBackupPolicyDB
	err := r.db.Where("credential_id = ?", credentialID).Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list policies by credential: %w", err)
	}
	return rows, nil
}

// ListByUserID returns all policies for a user (shared policies across credentials).
func (r *AutosyncBackupPolicyRepository) ListByUserID(userID string) ([]AutosyncBackupPolicyDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var rows []AutosyncBackupPolicyDB
	err := r.db.Where("user_id = ?", userID).Order("credential_id ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list policies by user: %w", err)
	}
	return rows, nil
}

// ListByUserAndCredentialID returns policies owned by a user for one credential.
func (r *AutosyncBackupPolicyRepository) ListByUserAndCredentialID(userID string, credentialID uint) ([]AutosyncBackupPolicyDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" || credentialID == 0 {
		return nil, nil
	}
	var rows []AutosyncBackupPolicyDB
	err := r.db.Where("user_id = ? AND credential_id = ?", userID, credentialID).Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list policies by user and credential: %w", err)
	}
	return rows, nil
}

func policyScheduleFingerprint(userID string, credentialID uint, interval, on, retentionType string) string {
	return fmt.Sprintf("%s|%d|%s|%s|%s",
		strings.TrimSpace(userID), credentialID,
		strings.TrimSpace(interval), strings.TrimSpace(on), strings.TrimSpace(retentionType))
}

// PolicyMergeGroupResult describes one merged duplicate fingerprint group.
type PolicyMergeGroupResult struct {
	Fingerprint       string `json:"fingerprint"`
	CanonicalPolicyID uint   `json:"canonical_policy_id"`
	RemovedPolicyIDs  []uint `json:"removed_policy_ids"`
	JobsRebound       int    `json:"jobs_rebound"`
}

// PolicyMergeResult is returned by MergeDuplicatePolicies.
type PolicyMergeResult struct {
	MergedGroups    int                      `json:"merged_groups"`
	JobsRebound     int                      `json:"jobs_rebound"`
	PoliciesRemoved int                      `json:"policies_removed"`
	Groups          []PolicyMergeGroupResult `json:"groups"`
}

func pickCanonicalPolicy(policies []AutosyncBackupPolicyDB, jobCounts map[uint]int, now time.Time) AutosyncBackupPolicyDB {
	best := policies[0]
	bestScore := -1
	for i := range policies {
		p := policies[i]
		score := jobCounts[p.ID] * 100
		if !IsPolicyExpired(&p, now) {
			score += 10000
		}
		if score > bestScore || (score == bestScore && p.ID < best.ID) {
			best = p
			bestScore = score
		}
	}
	return best
}

// MergeDuplicatePolicies merges rows that share the same schedule fingerprint (includes credential_id in the key).
// Canonical row prefers a non-expired policy with the most linked jobs.
func (r *AutosyncBackupPolicyRepository) MergeDuplicatePolicies(userID string, dryRun bool) (*PolicyMergeResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	policies, err := r.ListByUserID(userID)
	if err != nil {
		return nil, err
	}
	if len(policies) < 2 {
		return &PolicyMergeResult{}, nil
	}

	type jobCountRow struct {
		PolicyID uint
		Count    int
	}
	var counts []jobCountRow
	if err := r.db.Model(&CronJobListingDB{}).
		Select("policy_id, COUNT(*) AS count").
		Where("user_id = ? AND policy_id > 0", userID).
		Group("policy_id").
		Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("count jobs per policy: %w", err)
	}
	jobCounts := make(map[uint]int, len(counts))
	for _, c := range counts {
		jobCounts[c.PolicyID] = c.Count
	}

	now := time.Now().UTC()
	groups := make(map[string][]AutosyncBackupPolicyDB)
	for i := range policies {
		fp := policyScheduleFingerprint(policies[i].UserID, policies[i].CredentialID, policies[i].Interval, policies[i].On, policies[i].RetentionType)
		groups[fp] = append(groups[fp], policies[i])
	}

	out := &PolicyMergeResult{Groups: make([]PolicyMergeGroupResult, 0)}
	for fp, rows := range groups {
		if len(rows) < 2 {
			continue
		}
		canonical := pickCanonicalPolicy(rows, jobCounts, now)
		removed := make([]uint, 0, len(rows)-1)
		groupJobs := 0
		for i := range rows {
			if rows[i].ID == canonical.ID {
				continue
			}
			removed = append(removed, rows[i].ID)
			var rebound int64
			q := r.db.Model(&CronJobListingDB{}).Where("user_id = ? AND policy_id = ?", userID, rows[i].ID)
			if dryRun {
				if err := q.Count(&rebound).Error; err != nil {
					return nil, fmt.Errorf("count jobs for policy %d: %w", rows[i].ID, err)
				}
			} else {
				res := q.Updates(map[string]interface{}{
					"policy_id": canonical.ID,
					"interval":  canonical.Interval,
				})
				if res.Error != nil {
					return nil, fmt.Errorf("rebind jobs from policy %d: %w", rows[i].ID, res.Error)
				}
				rebound = res.RowsAffected
			}
			groupJobs += int(rebound)
			if !dryRun {
				if err := r.db.Delete(&AutosyncBackupPolicyDB{}, rows[i].ID).Error; err != nil {
					return nil, fmt.Errorf("delete duplicate policy %d: %w", rows[i].ID, err)
				}
			}
		}
		out.Groups = append(out.Groups, PolicyMergeGroupResult{
			Fingerprint:       fp,
			CanonicalPolicyID: canonical.ID,
			RemovedPolicyIDs:  removed,
			JobsRebound:       groupJobs,
		})
		out.MergedGroups++
		out.JobsRebound += groupJobs
		out.PoliciesRemoved += len(removed)
	}
	return out, nil
}

func normalizeRetentionType(retentionType string) (string, error) {
	rt := strings.TrimSpace(strings.ToLower(retentionType))
	if rt == "" {
		rt = RetentionNever
	}
	switch rt {
	case RetentionNever, Retention30Days, Retention1Year, Retention7Years:
		return rt, nil
	default:
		return "", fmt.Errorf("invalid retention_type: %s", retentionType)
	}
}

func retentionExpiryFrom(base time.Time, retentionType string) (*time.Time, error) {
	switch retentionType {
	case RetentionNever:
		return nil, nil
	case Retention30Days:
		t := base.AddDate(0, 0, 30)
		return &t, nil
	case Retention1Year:
		t := base.AddDate(1, 0, 0)
		return &t, nil
	case Retention7Years:
		t := base.AddDate(7, 0, 0)
		return &t, nil
	default:
		return nil, fmt.Errorf("invalid retention_type: %s", retentionType)
	}
}

func policyExpiredAt(expiresAt *time.Time, now time.Time) bool {
	if expiresAt == nil {
		return false
	}
	return !expiresAt.After(now)
}

// IsPolicyExpired reports whether a policy row is past expires_at.
func IsPolicyExpired(policy *AutosyncBackupPolicyDB, now time.Time) bool {
	if policy == nil {
		return false
	}
	if policy.IsExpired {
		return true
	}
	return policyExpiredAt(policy.ExpiresAt, now)
}

// syncExpiredFlags aligns is_expired with expires_at so the partial unique index matches app logic.
func (r *AutosyncBackupPolicyRepository) syncExpiredFlags() error {
	return r.db.Exec(`
UPDATE autosync_backup_policy_dbs
SET is_expired = true
WHERE deleted_at IS NULL
  AND is_expired = false
  AND expires_at IS NOT NULL
  AND expires_at <= NOW()
`).Error
}

func (r *AutosyncBackupPolicyRepository) findActivePolicyByFingerprint(userID string, credentialID uint, interval, on, retentionType string, now time.Time) (*AutosyncBackupPolicyDB, error) {
	var row AutosyncBackupPolicyDB
	err := r.db.
		Where("user_id = ? AND credential_id = ? AND interval = ? AND \"on\" = ? AND retention_type = ?",
			userID, credentialID, interval, on, retentionType).
		Where("is_expired = ?", false).
		Where("expires_at IS NULL OR expires_at > ?", now).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// findIndexedActivePolicy matches idx_autosync_policy_fingerprint_active (is_expired only, no time predicate).
func (r *AutosyncBackupPolicyRepository) findIndexedActivePolicy(userID string, credentialID uint, interval, on, retentionType string) (*AutosyncBackupPolicyDB, error) {
	var row AutosyncBackupPolicyDB
	err := r.db.
		Where("user_id = ? AND credential_id = ? AND interval = ? AND \"on\" = ? AND retention_type = ?",
			userID, credentialID, interval, on, retentionType).
		Where("is_expired = ?", false).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *AutosyncBackupPolicyRepository) insertPolicyRow(userID string, credentialID uint, interval, on, retentionType string) (*AutosyncBackupPolicyDB, error) {
	rt, err := normalizeRetentionType(retentionType)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if credentialID == 0 {
		return nil, fmt.Errorf("credential_id is required")
	}
	if interval == "" {
		return nil, fmt.Errorf("interval is required")
	}
	now := time.Now().UTC()
	expiresAt, err := retentionExpiryFrom(now, rt)
	if err != nil {
		return nil, err
	}
	row := AutosyncBackupPolicyDB{
		UserID:        userID,
		CredentialID:  credentialID,
		Interval:      strings.TrimSpace(interval),
		On:            strings.TrimSpace(on),
		RetentionType: rt,
		ExpiresAt:     expiresAt,
		IsExpired:     policyExpiredAt(expiresAt, now),
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}
	return &row, nil
}

// FindOrCreatePolicy returns an existing non-expired shared policy by fingerprint, or creates one.
func (r *AutosyncBackupPolicyRepository) FindOrCreatePolicy(userID string, credentialID uint, interval, on, retentionType string) (*AutosyncBackupPolicyDB, error) {
	userID = strings.TrimSpace(userID)
	interval = strings.TrimSpace(interval)
	on = strings.TrimSpace(on)
	rt, err := normalizeRetentionType(retentionType)
	if err != nil {
		return nil, err
	}
	if err := r.syncExpiredFlags(); err != nil {
		return nil, fmt.Errorf("sync policy expiry flags: %w", err)
	}
	now := time.Now().UTC()
	if row, ferr := r.findActivePolicyByFingerprint(userID, credentialID, interval, on, rt, now); ferr == nil {
		return row, nil
	} else if !errors.Is(ferr, gormio.ErrRecordNotFound) {
		return nil, fmt.Errorf("find active policy by fingerprint: %w", ferr)
	}
	row, err := r.insertPolicyRow(userID, credentialID, interval, on, rt)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
		_ = r.syncExpiredFlags()
		if existing, ferr := r.findActivePolicyByFingerprint(userID, credentialID, interval, on, rt, now); ferr == nil {
			return existing, nil
		}
		if existing, ferr := r.findIndexedActivePolicy(userID, credentialID, interval, on, rt); ferr == nil {
			if policyExpiredAt(existing.ExpiresAt, now) {
				if uerr := r.UpdatePolicy(existing.ID, interval, on, rt); uerr != nil {
					return nil, uerr
				}
				return r.GetByID(existing.ID)
			}
			return existing, nil
		}
	}
	return row, err
}

// CreateNewPolicy always inserts a new policy row (bypasses fingerprint reuse). Prefer FindOrCreatePolicy.
func (r *AutosyncBackupPolicyRepository) CreateNewPolicy(userID string, credentialID uint, interval, on, retentionType string) (*AutosyncBackupPolicyDB, error) {
	return r.insertPolicyRow(userID, credentialID, interval, on, retentionType)
}

// AssignPolicyToJob attaches a policy to one job and syncs the interval cache.
func (r *AutosyncBackupPolicyRepository) AssignPolicyToJob(jobID, policyID uint) error {
	if jobID == 0 || policyID == 0 {
		return fmt.Errorf("job_id and policy_id are required")
	}
	policy, err := r.GetByID(policyID)
	if err != nil {
		return err
	}
	patch := map[string]interface{}{"policy_id": policyID}
	if strings.TrimSpace(policy.Interval) != "" {
		patch["interval"] = policy.Interval
	}
	if err := r.db.Model(&CronJobListingDB{}).Where("id = ?", jobID).Updates(patch).Error; err != nil {
		return fmt.Errorf("assign policy to job: %w", err)
	}
	return nil
}

// UpdatePolicy updates one shared policy row and syncs interval cache on linked jobs.
func (r *AutosyncBackupPolicyRepository) UpdatePolicy(policyID uint, interval, on, retentionType string) error {
	rt, err := normalizeRetentionType(retentionType)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expiresAt, err := retentionExpiryFrom(now, rt)
	if err != nil {
		return err
	}
	patch := map[string]interface{}{
		"interval":       strings.TrimSpace(interval),
		"on":             strings.TrimSpace(on),
		"retention_type": rt,
		"expires_at":     expiresAt,
		"is_expired":     policyExpiredAt(expiresAt, now),
	}
	if err := r.db.Model(&AutosyncBackupPolicyDB{}).Where("id = ?", policyID).Updates(patch).Error; err != nil {
		return fmt.Errorf("update policy schedule: %w", err)
	}
	return r.syncJobIntervalCacheByPolicyID(policyID, patch["interval"].(string))
}

// RebindJobToPolicy updates one job to use a new policy_id and syncs interval cache.
func (r *AutosyncBackupPolicyRepository) RebindJobToPolicy(jobID, policyID uint) error {
	return r.AssignPolicyToJob(jobID, policyID)
}

// RebindJobsToPolicy updates selected jobs to a new policy_id and syncs interval cache.
func (r *AutosyncBackupPolicyRepository) RebindJobsToPolicy(jobIDs []uint, policyID uint) error {
	if len(jobIDs) == 0 {
		return nil
	}
	policy, err := r.GetByID(policyID)
	if err != nil {
		return err
	}
	return r.db.Model(&CronJobListingDB{}).
		Where("id IN ?", jobIDs).
		Updates(map[string]interface{}{
			"policy_id": policyID,
			"interval":  policy.Interval,
		}).Error
}

func (r *AutosyncBackupPolicyRepository) syncJobIntervalCacheByPolicyID(policyID uint, interval string) error {
	return r.db.Model(&CronJobListingDB{}).Where("policy_id = ?", policyID).Update("interval", interval).Error
}

// EnrichJobFromPolicy sets PolicyID, Interval, and On on a job from its policy row when present.
func (r *AutosyncBackupPolicyRepository) EnrichJobFromPolicy(job *CronJobListingDB) {
	if job == nil || job.ID == 0 {
		return
	}
	if job.PolicyID == 0 {
		return
	}
	policy, err := r.GetByID(job.PolicyID)
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
// It sets cron_job_listing_dbs.policy_id for existing rows, and dedupes same user+credential+schedule fingerprint.
func (r *AutosyncBackupPolicyRepository) BackfillFromJobs() error {
	type backfillRow struct {
		JobID         uint
		UserID        string
		CredentialID  uint
		Interval      string
		On            string
		RetentionType string
	}
	var rows []backfillRow
	err := r.db.Raw(`
SELECT
  c.id AS job_id,
  c.user_id,
  (c.input_data->>'credential_id')::bigint AS credential_id,
  COALESCE(p.interval, c.interval) AS interval,
  COALESCE(p."on", '') AS "on",
  COALESCE(p.retention_type, 'never') AS retention_type
FROM cron_job_listing_dbs c
LEFT JOIN autosync_backup_policy_dbs p ON p.id = c.policy_id AND p.deleted_at IS NULL
WHERE c.deleted_at IS NULL
  AND c.interval NOT IN ('', 'one_time')
  AND (c.input_data->>'credential_id') IS NOT NULL
  AND (c.input_data->>'credential_id')::bigint > 0
`).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load policy backfill rows: %w", err)
	}
	for i := range rows {
		row := rows[i]
		policy, ferr := r.FindOrCreatePolicy(row.UserID, row.CredentialID, row.Interval, row.On, row.RetentionType)
		if ferr != nil {
			return ferr
		}
		if err := r.AssignPolicyToJob(row.JobID, policy.ID); err != nil {
			return err
		}
	}
	return nil
}

// EnforceExpiredPolicies marks expired policy rows and deactivates linked jobs.
func (r *AutosyncBackupPolicyRepository) EnforceExpiredPolicies() error {
	if err := r.db.Exec(`
UPDATE autosync_backup_policy_dbs
SET is_expired = true
WHERE deleted_at IS NULL
  AND is_expired = false
  AND expires_at IS NOT NULL
  AND expires_at <= NOW()
`).Error; err != nil {
		return fmt.Errorf("mark expired policies: %w", err)
	}
	patch := map[string]interface{}{
		"active":           false,
		"auto_deactivated": true,
		"message":          "Backup policy is expired. Update retention policy before re-activating.",
		"message_status":   JobMessageStatusError,
	}
	return r.db.Model(&CronJobListingDB{}).
		Where("policy_id IN (SELECT id FROM autosync_backup_policy_dbs WHERE is_expired = true AND deleted_at IS NULL)").
		Updates(patch).Error
}
