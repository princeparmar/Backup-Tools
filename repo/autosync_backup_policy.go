package repo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
)

// AutosyncBackupPolicyDB stores shared schedule policy rows. Jobs reference policies by cron_job_listing_dbs.policy_id.
type AutosyncBackupPolicyDB struct {
	gorm.GormModel

	UserID        string     `json:"user_id" gorm:"column:user_id;index"`
	Name          string     `json:"name" gorm:"column:name"`
	Interval      string     `json:"interval" gorm:"column:interval"`
	On            string     `json:"on" gorm:"column:on"`
	RetentionType string     `json:"retention_type" gorm:"column:retention_type;default:never"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" gorm:"column:expires_at"`
	IsExpired     bool       `json:"is_expired" gorm:"column:is_expired;default:false"`
}

// PolicyOption is a slim row for move/policy pickers.
type PolicyOption struct {
	PolicyID uint   `json:"policy_id"`
	Name     string `json:"name"`
}

var (
	ErrPolicyNameExists    = errors.New("policy name already exists for user")
	ErrPolicyHasLinkedJobs = errors.New("policy has linked jobs")
)

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

// GetByIDs loads policies by primary keys in one query. Missing ids are omitted.
func (r *AutosyncBackupPolicyRepository) GetByIDs(ids []uint) (map[uint]*AutosyncBackupPolicyDB, error) {
	out := make(map[uint]*AutosyncBackupPolicyDB)
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
	var rows []AutosyncBackupPolicyDB
	if err := r.db.Where("id IN ?", uniq).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("get policies by ids: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// ListByUserID returns all policies for a user.
func (r *AutosyncBackupPolicyRepository) ListByUserID(userID string) ([]AutosyncBackupPolicyDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var rows []AutosyncBackupPolicyDB
	err := r.db.Where("user_id = ?", userID).Order("name ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list policies by user: %w", err)
	}
	return rows, nil
}

// ListByUserIDWithLinkedJobs returns policies that have at least one linked job.
func (r *AutosyncBackupPolicyRepository) ListByUserIDWithLinkedJobs(userID string) ([]AutosyncBackupPolicyDB, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var rows []AutosyncBackupPolicyDB
	err := r.db.
		Table("autosync_backup_policy_dbs AS p").
		Select("p.*").
		Joins(`INNER JOIN cron_job_listing_dbs j ON j.policy_id = p.id AND j.deleted_at IS NULL`).
		Where("p.user_id = ? AND p.deleted_at IS NULL", userID).
		Group("p.id").
		Order("p.name ASC, p.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list policies with linked jobs: %w", err)
	}
	return rows, nil
}

// HasPoliciesForUser reports whether the user has any named backup policies.
func (r *AutosyncBackupPolicyRepository) HasPoliciesForUser(userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	var count int64
	err := r.db.Model(&AutosyncBackupPolicyDB{}).Where("user_id = ?", userID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("has policies for user: %w", err)
	}
	return count > 0, nil
}

// ListPolicyOptions returns policy_id and name for all user policies (including empty).
func (r *AutosyncBackupPolicyRepository) ListPolicyOptions(userID string) ([]PolicyOption, error) {
	policies, err := r.ListByUserID(userID)
	if err != nil {
		return nil, err
	}
	out := make([]PolicyOption, 0, len(policies))
	for i := range policies {
		out = append(out, PolicyOption{PolicyID: policies[i].ID, Name: policies[i].Name})
	}
	return out, nil
}

// PolicyNameExists reports whether name is taken for user (case-insensitive).
func (r *AutosyncBackupPolicyRepository) PolicyNameExists(userID, name string, excludePolicyID uint) (bool, error) {
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	if userID == "" || name == "" {
		return false, nil
	}
	q := r.db.Model(&AutosyncBackupPolicyDB{}).
		Where("user_id = ? AND LOWER(name) = LOWER(?)", userID, name)
	if excludePolicyID > 0 {
		q = q.Where("id <> ?", excludePolicyID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, fmt.Errorf("check policy name exists: %w", err)
	}
	return count > 0, nil
}

// EnsureUniquePolicyName returns baseName or baseName + " 2", " 3", … when taken.
func (r *AutosyncBackupPolicyRepository) EnsureUniquePolicyName(userID, baseName string) (string, error) {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		return "", fmt.Errorf("policy name is required")
	}
	candidate := baseName
	for i := 2; i < 1000; i++ {
		exists, err := r.PolicyNameExists(userID, candidate, 0)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s %d", baseName, i)
	}
	return "", fmt.Errorf("could not allocate unique policy name")
}

// GetByNameForUser loads a policy by user and exact name (case-insensitive).
func (r *AutosyncBackupPolicyRepository) GetByNameForUser(userID, name string) (*AutosyncBackupPolicyDB, error) {
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	if userID == "" || name == "" {
		return nil, gormio.ErrRecordNotFound
	}
	var row AutosyncBackupPolicyDB
	err := r.db.Where("user_id = ? AND LOWER(name) = LOWER(?)", userID, name).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func policyScheduleFingerprint(userID, interval, on, retentionType string) string {
	return fmt.Sprintf("%s|%s|%s|%s",
		strings.TrimSpace(userID),
		strings.TrimSpace(interval), strings.TrimSpace(on), strings.TrimSpace(retentionType))
}

// MergeablePolicyGroupData is internal repo data for one duplicate schedule group.
type MergeablePolicyGroupData struct {
	Policies  []AutosyncBackupPolicyDB
	PolicyIDs []uint
	TotalJobs int
}

// MergeExecuteResult is returned by MergeSelectedPolicyGroup after a successful merge.
type MergeExecuteResult struct {
	NewPolicyID         uint
	RemovedPolicyIDs    []uint
	SourcePolicyIDs     []uint
	JobsMoved           int
	TotalJobsAfterMerge int
}

// MergeIncompleteGroupError is returned when submitted policy_ids omit members of the duplicate set.
type MergeIncompleteGroupError struct {
	MissingPolicyIDs []uint
}

func (e *MergeIncompleteGroupError) Error() string {
	return "incomplete merge group"
}

var (
	ErrMergePolicyIDsRequired = errors.New("at least two policy_ids are required")
	ErrMergePolicyNotFound    = errors.New("policy not found for user")
	ErrMergeMixedGroups       = errors.New("all policy_ids must belong to the same schedule group")
)

func buildMergeableGroupData(rows []AutosyncBackupPolicyDB, jobCounts map[uint]int) MergeablePolicyGroupData {
	policyIDs := make([]uint, 0, len(rows))
	totalJobs := 0
	for i := range rows {
		policyIDs = append(policyIDs, rows[i].ID)
		totalJobs += jobCounts[rows[i].ID]
	}
	sort.Slice(policyIDs, func(i, j int) bool { return policyIDs[i] < policyIDs[j] })
	return MergeablePolicyGroupData{
		Policies:  rows,
		PolicyIDs: policyIDs,
		TotalJobs: totalJobs,
	}
}

func (r *AutosyncBackupPolicyRepository) loadPolicyJobCounts(userID string) (map[uint]int, error) {
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
	return jobCounts, nil
}

func groupPoliciesByFingerprint(policies []AutosyncBackupPolicyDB) map[string][]AutosyncBackupPolicyDB {
	groups := make(map[string][]AutosyncBackupPolicyDB)
	for i := range policies {
		fp := policyScheduleFingerprint(policies[i].UserID, policies[i].Interval, policies[i].On, policies[i].RetentionType)
		groups[fp] = append(groups[fp], policies[i])
	}
	return groups
}

// ListMergeablePolicyGroups returns duplicate schedule groups (2+ policies) for a user.
func (r *AutosyncBackupPolicyRepository) ListMergeablePolicyGroups(userID string) ([]MergeablePolicyGroupData, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	policies, err := r.ListByUserID(userID)
	if err != nil {
		return nil, err
	}
	if len(policies) < 2 {
		return nil, nil
	}
	jobCounts, err := r.loadPolicyJobCounts(userID)
	if err != nil {
		return nil, err
	}
	groups := groupPoliciesByFingerprint(policies)
	out := make([]MergeablePolicyGroupData, 0)
	for _, rows := range groups {
		if len(rows) < 2 {
			continue
		}
		out = append(out, buildMergeableGroupData(rows, jobCounts))
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].PolicyIDs) == 0 || len(out[j].PolicyIDs) == 0 {
			return i < j
		}
		return out[i].PolicyIDs[0] < out[j].PolicyIDs[0]
	})
	return out, nil
}

func sortedUintSet(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func missingPolicyIDs(submitted, full []uint) []uint {
	submittedSet := make(map[uint]struct{}, len(submitted))
	for _, id := range submitted {
		submittedSet[id] = struct{}{}
	}
	missing := make([]uint, 0)
	for _, id := range full {
		if _, ok := submittedSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// MergeSelectedPolicyGroup merges one duplicate group into a newly created named policy.
func (r *AutosyncBackupPolicyRepository) MergeSelectedPolicyGroup(userID string, policyIDs []uint, name string) (*MergeExecuteResult, error) {
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	policyIDs = sortedUintSet(policyIDs)
	if len(policyIDs) < 2 {
		return nil, ErrMergePolicyIDsRequired
	}

	loaded := make([]AutosyncBackupPolicyDB, 0, len(policyIDs))
	fingerprint := ""
	for _, id := range policyIDs {
		policy, err := r.GetByID(id)
		if err != nil {
			if errors.Is(err, gormio.ErrRecordNotFound) {
				return nil, ErrMergePolicyNotFound
			}
			return nil, err
		}
		if strings.TrimSpace(policy.UserID) != userID {
			return nil, ErrMergePolicyNotFound
		}
		fp := policyScheduleFingerprint(policy.UserID, policy.Interval, policy.On, policy.RetentionType)
		if fingerprint == "" {
			fingerprint = fp
		} else if fingerprint != fp {
			return nil, ErrMergeMixedGroups
		}
		loaded = append(loaded, *policy)
	}

	allPolicies, err := r.ListByUserID(userID)
	if err != nil {
		return nil, err
	}
	groups := groupPoliciesByFingerprint(allPolicies)
	fullGroup, ok := groups[fingerprint]
	if !ok || len(fullGroup) < 2 {
		return nil, ErrMergeMixedGroups
	}
	fullIDs := make([]uint, 0, len(fullGroup))
	for i := range fullGroup {
		fullIDs = append(fullIDs, fullGroup[i].ID)
	}
	sort.Slice(fullIDs, func(i, j int) bool { return fullIDs[i] < fullIDs[j] })
	if missing := missingPolicyIDs(policyIDs, fullIDs); len(missing) > 0 {
		return nil, &MergeIncompleteGroupError{MissingPolicyIDs: missing}
	}

	template := fullGroup[0]
	newPolicy, err := r.CreatePolicy(userID, name, template.Interval, template.On, template.RetentionType)
	if err != nil {
		return nil, err
	}

	removed := make([]uint, 0, len(fullGroup))
	jobsMoved := 0
	for i := range fullGroup {
		removed = append(removed, fullGroup[i].ID)
		res := r.db.Model(&CronJobListingDB{}).
			Where("user_id = ? AND policy_id = ?", userID, fullGroup[i].ID).
			Updates(map[string]interface{}{
				"policy_id": newPolicy.ID,
				"interval":  newPolicy.Interval,
			})
		if res.Error != nil {
			return nil, fmt.Errorf("rebind jobs from policy %d: %w", fullGroup[i].ID, res.Error)
		}
		jobsMoved += int(res.RowsAffected)
		if err := r.db.Delete(&AutosyncBackupPolicyDB{}, fullGroup[i].ID).Error; err != nil {
			return nil, fmt.Errorf("delete merged policy %d: %w", fullGroup[i].ID, err)
		}
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return &MergeExecuteResult{
		NewPolicyID:         newPolicy.ID,
		RemovedPolicyIDs:    removed,
		SourcePolicyIDs:     fullIDs,
		JobsMoved:           jobsMoved,
		TotalJobsAfterMerge: jobsMoved,
	}, nil
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

// DeletePolicyForUser soft-deletes a policy when it has no linked jobs.
// Returns linked job count when ErrPolicyHasLinkedJobs.
func (r *AutosyncBackupPolicyRepository) DeletePolicyForUser(userID string, policyID uint) (int, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, fmt.Errorf("user_id is required")
	}
	if policyID == 0 {
		return 0, gormio.ErrRecordNotFound
	}
	policy, err := r.GetByID(policyID)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(policy.UserID) != userID {
		return 0, gormio.ErrRecordNotFound
	}
	var linkedCount int64
	if err := r.db.Model(&CronJobListingDB{}).
		Where("user_id = ? AND policy_id = ?", userID, policyID).
		Count(&linkedCount).Error; err != nil {
		return 0, fmt.Errorf("count linked jobs for policy: %w", err)
	}
	if linkedCount > 0 {
		return int(linkedCount), ErrPolicyHasLinkedJobs
	}
	if err := r.db.Delete(&AutosyncBackupPolicyDB{}, policyID).Error; err != nil {
		return 0, fmt.Errorf("delete policy: %w", err)
	}
	return 0, nil
}

// CreatePolicy inserts a named policy row for a user.
func (r *AutosyncBackupPolicyRepository) CreatePolicy(userID, name, interval, on, retentionType string) (*AutosyncBackupPolicyDB, error) {
	rt, err := normalizeRetentionType(retentionType)
	if err != nil {
		return nil, err
	}
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	interval = strings.TrimSpace(interval)
	on = strings.TrimSpace(on)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	if interval == "" {
		return nil, fmt.Errorf("interval is required")
	}
	exists, err := r.PolicyNameExists(userID, name, 0)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrPolicyNameExists
	}
	now := time.Now().UTC()
	expiresAt, err := retentionExpiryFrom(now, rt)
	if err != nil {
		return nil, err
	}
	row := AutosyncBackupPolicyDB{
		UserID:        userID,
		Name:          name,
		Interval:      interval,
		On:            on,
		RetentionType: rt,
		ExpiresAt:     expiresAt,
		IsExpired:     policyExpiredAt(expiresAt, now),
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}
	return &row, nil
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

// BackfillFromJobs inserts named policy rows from existing cron jobs missing policy_id.
func (r *AutosyncBackupPolicyRepository) BackfillFromJobs() error {
	type backfillRow struct {
		JobID         uint
		UserID        string
		CredentialID  uint
		GoogleEmail   string
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
  COALESCE(g.email, '') AS google_email,
  COALESCE(p.interval, c.interval) AS interval,
  COALESCE(p."on", '') AS "on",
  COALESCE(p.retention_type, 'never') AS retention_type
FROM cron_job_listing_dbs c
LEFT JOIN autosync_backup_policy_dbs p ON p.id = c.policy_id AND p.deleted_at IS NULL
LEFT JOIN google_backup_credentials g ON g.id = (c.input_data->>'credential_id')::bigint AND g.deleted_at IS NULL
WHERE c.deleted_at IS NULL
  AND (c.policy_id IS NULL OR c.policy_id = 0)
  AND c.interval NOT IN ('', 'one_time')
`).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load policy backfill rows: %w", err)
	}
	for i := range rows {
		row := rows[i]
		defaultName := strings.TrimSpace(row.GoogleEmail)
		if defaultName == "" {
			defaultName = fmt.Sprintf("Policy %d", row.JobID)
		}
		name, nerr := r.EnsureUniquePolicyName(row.UserID, defaultName)
		if nerr != nil {
			return nerr
		}
		policy, cerr := r.CreatePolicy(row.UserID, name, row.Interval, row.On, row.RetentionType)
		if cerr != nil {
			return cerr
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
