package repo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	gormio "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Access-verify state values stored under cron job input_data.access_verify.state.
const (
	AccessVerifyStatePending     = "pending"
	AccessVerifyStateUnavailable = "unavailable"
	AccessVerifyStateOK          = "ok"
)

// Access-verify batch statuses.
const (
	AccessVerifyBatchQueued    = "queued"
	AccessVerifyBatchRunning   = "running"
	AccessVerifyBatchCompleted = "completed"
	AccessVerifyBatchFailed    = "failed"
)

const accessVerifyInputKey = "access_verify"

// AccessVerifyMarker is stored in cron job input_data under "access_verify".
type AccessVerifyMarker struct {
	State            string `json:"state"`
	CheckedAt        string `json:"checked_at,omitempty"`
	UnavailableSince string `json:"unavailable_since,omitempty"`
	ReasonCode       string `json:"reason_code,omitempty"`
	Message          string `json:"message,omitempty"`
	BatchID          uint   `json:"batch_id,omitempty"`
}

// MicrosoftAccessVerifyBatchDB is one durable verification batch for a tenant/credential.
type MicrosoftAccessVerifyBatchDB struct {
	ID             uint `json:"id" gorm:"primaryKey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gormio.DeletedAt `gorm:"index"`

	UserID         string `json:"user_id" gorm:"column:user_id;not null;index:idx_ms_access_verify_claim"`
	CredentialID   uint   `json:"credential_id" gorm:"column:credential_id;not null;index"`
	TenantID       string `json:"tenant_id" gorm:"column:tenant_id;not null;index:idx_ms_access_verify_claim"`
	StorjProjectID string `json:"storj_project_id" gorm:"column:storj_project_id;not null;default:''"`

	Status   string                                     `json:"status" gorm:"column:status;not null;default:queued;index:idx_ms_access_verify_claim"`
	JobIDs   *database.DbJson[[]uint]                   `json:"job_ids" gorm:"column:job_ids;type:jsonb"`
	Attempts uint                                       `json:"attempts" gorm:"column:attempts;not null;default:0"`
	LastError string                                    `json:"last_error,omitempty" gorm:"column:last_error;type:varchar(512)"`
	NotifiedAt *time.Time                               `json:"notified_at,omitempty" gorm:"column:notified_at"`
	ClaimedAt  *time.Time                               `json:"claimed_at,omitempty" gorm:"column:claimed_at"`
	Summary    *database.DbJson[map[string]interface{}] `json:"summary,omitempty" gorm:"column:summary;type:jsonb"`
}

func (MicrosoftAccessVerifyBatchDB) TableName() string { return "microsoft_access_verify_batches" }

// MicrosoftAccessVerifyBatchRepository persists access-verify batches.
type MicrosoftAccessVerifyBatchRepository struct {
	db *gorm.DB
}

func NewMicrosoftAccessVerifyBatchRepository(db *gorm.DB) *MicrosoftAccessVerifyBatchRepository {
	return &MicrosoftAccessVerifyBatchRepository{db: db}
}

// EnqueueBatch creates a queued batch for the given job ids. Empty jobIDs is a no-op.
// Idempotent when an open (queued/running) batch already covers the same user+tenant+credential
// with the same job set: returns the existing batch.
func (r *MicrosoftAccessVerifyBatchRepository) EnqueueBatch(userID string, credentialID uint, tenantID, projectID string, jobIDs []uint) (*MicrosoftAccessVerifyBatchDB, error) {
	userID = strings.TrimSpace(userID)
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	projectID = strings.TrimSpace(projectID)
	jobIDs = uniqueUintIDs(jobIDs)
	if userID == "" || credentialID == 0 || tenantID == "" || len(jobIDs) == 0 {
		return nil, fmt.Errorf("user_id, credential_id, tenant_id and job_ids are required")
	}

	var open []MicrosoftAccessVerifyBatchDB
	if err := r.db.Where(
		"user_id = ? AND tenant_id = ? AND credential_id = ? AND status IN ?",
		userID, tenantID, credentialID, []string{AccessVerifyBatchQueued, AccessVerifyBatchRunning},
	).Order("id desc").Find(&open).Error; err != nil {
		return nil, err
	}
	for i := range open {
		existing := open[i].JobIDList()
		if uintSliceEqual(existing, jobIDs) {
			return &open[i], nil
		}
		// Merge into the newest queued batch for this scope.
		if open[i].Status == AccessVerifyBatchQueued {
			merged := uniqueUintIDs(append(existing, jobIDs...))
			open[i].JobIDs = database.NewDbJsonFromValue(merged)
			if err := r.db.Model(&open[i]).Update("job_ids", open[i].JobIDs).Error; err != nil {
				return nil, err
			}
			return &open[i], nil
		}
	}

	row := &MicrosoftAccessVerifyBatchDB{
		UserID:         userID,
		CredentialID:   credentialID,
		TenantID:       tenantID,
		StorjProjectID: projectID,
		Status:         AccessVerifyBatchQueued,
		JobIDs:         database.NewDbJsonFromValue(jobIDs),
	}
	if err := r.db.Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// ClaimNextQueued claims one queued batch for processing (multi-instance safe).
func (r *MicrosoftAccessVerifyBatchRepository) ClaimNextQueued() (*MicrosoftAccessVerifyBatchDB, error) {
	tx := r.db.Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	var row MicrosoftAccessVerifyBatchDB
	err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ?", AccessVerifyBatchQueued).
		Order("id asc").
		First(&row).Error
	if err != nil {
		tx.Rollback()
		if err == gormio.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	now := time.Now().UTC()
	row.Status = AccessVerifyBatchRunning
	row.Attempts++
	row.ClaimedAt = &now
	if err := tx.Model(&row).Updates(map[string]interface{}{
		"status":     row.Status,
		"attempts":   row.Attempts,
		"claimed_at": row.ClaimedAt,
		"updated_at": now,
	}).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// MarkCompleted finishes a batch.
func (r *MicrosoftAccessVerifyBatchRepository) MarkCompleted(id uint, summary map[string]interface{}, notified bool) error {
	now := time.Now().UTC()
	patch := map[string]interface{}{
		"status":     AccessVerifyBatchCompleted,
		"last_error": "",
		"updated_at": now,
	}
	if summary != nil {
		patch["summary"] = database.NewDbJsonFromValue(summary)
	}
	if notified {
		patch["notified_at"] = now
	}
	return r.db.Model(&MicrosoftAccessVerifyBatchDB{}).Where("id = ?", id).Updates(patch).Error
}

// MarkFailed records a batch failure and leaves it failed (not auto-requeued).
func (r *MicrosoftAccessVerifyBatchRepository) MarkFailed(id uint, errMsg string) error {
	return r.db.Model(&MicrosoftAccessVerifyBatchDB{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":     AccessVerifyBatchFailed,
		"last_error": truncateErr(errMsg, 512),
		"updated_at": time.Now().UTC(),
	}).Error
}

// RequeueRunningStale returns running batches stuck longer than age back to queued.
func (r *MicrosoftAccessVerifyBatchRepository) RequeueRunningStale(age time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-age)
	res := r.db.Model(&MicrosoftAccessVerifyBatchDB{}).
		Where("status = ? AND claimed_at IS NOT NULL AND claimed_at < ?", AccessVerifyBatchRunning, cutoff).
		Updates(map[string]interface{}{
			"status":     AccessVerifyBatchQueued,
			"claimed_at": nil,
			"updated_at": time.Now().UTC(),
		})
	return res.RowsAffected, res.Error
}

func (b *MicrosoftAccessVerifyBatchDB) JobIDList() []uint {
	if b == nil || b.JobIDs == nil || b.JobIDs.Json() == nil {
		return nil
	}
	return append([]uint(nil), (*b.JobIDs.Json())...)
}

// JobAccessVerify returns the access_verify marker from a cron job's input_data.
func JobAccessVerify(job *CronJobListingDB) *AccessVerifyMarker {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return nil
	}
	raw, ok := (*job.InputData.Json())[accessVerifyInputKey]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var m AccessVerifyMarker
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	if strings.TrimSpace(m.State) == "" {
		return nil
	}
	return &m
}

// IsAccessVerifyOwned reports whether the job is owned by the access-verify workflow
// (pending draft or confirmed unavailable).
func IsAccessVerifyOwned(job *CronJobListingDB) bool {
	m := JobAccessVerify(job)
	if m == nil {
		return false
	}
	switch m.State {
	case AccessVerifyStatePending, AccessVerifyStateUnavailable:
		return true
	default:
		return false
	}
}

// SetJobAccessVerifyPending writes pending draft marker and returns patch fields for UpdateCronJobByID.
func SetJobAccessVerifyPending(job *CronJobListingDB, batchID uint, message string) map[string]interface{} {
	marker := AccessVerifyMarker{
		State:      AccessVerifyStatePending,
		ReasonCode: "ACCESS_VERIFY_PENDING",
		Message:    message,
		BatchID:    batchID,
	}
	return accessVerifyJobPatch(job, marker, false, message)
}

// SetJobAccessVerifyUnavailable writes unavailable marker and returns patch fields.
func SetJobAccessVerifyUnavailable(job *CronJobListingDB, reasonCode, message string, batchID uint) map[string]interface{} {
	now := time.Now().UTC().Format(time.RFC3339)
	prev := JobAccessVerify(job)
	unavailableSince := now
	if prev != nil && strings.TrimSpace(prev.UnavailableSince) != "" {
		unavailableSince = prev.UnavailableSince
	}
	marker := AccessVerifyMarker{
		State:            AccessVerifyStateUnavailable,
		CheckedAt:        now,
		UnavailableSince: unavailableSince,
		ReasonCode:       reasonCode,
		Message:          message,
		BatchID:          batchID,
	}
	return accessVerifyJobPatch(job, marker, false, message)
}

// ClearJobAccessVerifyForPromote clears verification ownership and activates the job.
// Message is only set when the job was verification-owned (draft/unavailable). Already-active
// production jobs must not have their existing logs overwritten — callers should skip those.
func ClearJobAccessVerifyForPromote(job *CronJobListingDB) map[string]interface{} {
	input := copyJobInput(job)
	delete(input, accessVerifyInputKey)
	patch := map[string]interface{}{
		"active":           true,
		"auto_deactivated": false,
		"input_data":       database.NewDbJsonFromValue(input),
	}
	if IsAccessVerifyOwned(job) {
		patch["message"] = "Access verified"
		patch["message_status"] = JobMessageStatusInfo
	}
	return patch
}

func accessVerifyJobPatch(job *CronJobListingDB, marker AccessVerifyMarker, active bool, message string) map[string]interface{} {
	input := copyJobInput(job)
	input[accessVerifyInputKey] = marker
	msgStatus := JobMessageStatusWarning
	if marker.State == AccessVerifyStatePending {
		msgStatus = JobMessageStatusInfo
	}
	if strings.TrimSpace(message) == "" {
		message = marker.Message
	}
	return map[string]interface{}{
		"active":         active,
		"message":        message,
		"message_status": msgStatus,
		"input_data":     database.NewDbJsonFromValue(input),
	}
}

func copyJobInput(job *CronJobListingDB) map[string]interface{} {
	out := map[string]interface{}{}
	if job != nil && job.InputData != nil && job.InputData.Json() != nil {
		for k, v := range *job.InputData.Json() {
			out[k] = v
		}
	}
	return out
}

func uniqueUintIDs(ids []uint) []uint {
	seen := map[uint]struct{}{}
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
	return out
}

func uintSliceEqual(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[uint]int{}
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

func truncateErr(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}
