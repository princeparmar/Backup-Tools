package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/google/uuid"
	gormio "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// System access-check job identity / method.
const (
	MethodMSAccessVerify   = "ms_access_verify"
	ResourceTypeSystem     = "system"
	SyncTypeSystem         = "system"
	SystemAccessCheckName  = "System: Access check"
	accessCheckInputKey    = "access_check"
	AccessCheckPhaseProbing   = "probing"
	AccessCheckPhaseCompleted = "completed"
	AccessCheckMaterializeFinal = "final"
	AccessCheckMaterializeDraft = "draft"
)

// AccessCheckSelectionItem is one user×service pair to probe.
type AccessCheckSelectionItem struct {
	Email    string `json:"email"`
	UserID   string `json:"user_id"`
	Service  string `json:"service"`
	Method   string `json:"method,omitempty"`
}

// AccessCheckResultItem is one probe outcome (preview-compatible shape).
type AccessCheckResultItem struct {
	Email      string `json:"email"`
	Service    string `json:"service"`
	Method     string `json:"method,omitempty"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code,omitempty"`
	Message    string `json:"message,omitempty"`
	JobID      uint   `json:"job_id,omitempty"`
}

// AccessCheckState is stored under cron job input_data.access_check.
type AccessCheckState struct {
	TenantID             string                   `json:"tenant_id"`
	CredentialID         uint                     `json:"credential_id"`
	SelectionFingerprint string                   `json:"selection_fingerprint"`
	Selection            []AccessCheckSelectionItem `json:"selection"`
	Results              []AccessCheckResultItem  `json:"results"`
	LinkedJobIDs         []uint                   `json:"linked_job_ids,omitempty"`
	LinkedByKey          map[string]uint          `json:"linked_by_key,omitempty"`
	JobsMaterialized     bool                     `json:"jobs_materialized"`
	MaterializeMode      string                   `json:"materialize_mode,omitempty"`
	Phase                string                   `json:"phase"`
	StartedAt            string                   `json:"started_at"`
	Summary              map[string]int           `json:"summary,omitempty"`
}

// AccessCheckItemKey builds the stable email|service key for linking drafts.
func AccessCheckItemKey(email, service string) string {
	return strings.ToLower(strings.TrimSpace(email)) + "|" + strings.ToLower(strings.TrimSpace(service))
}

// AccessCheckSelectionFingerprint builds a stable hash for idempotent start.
func AccessCheckSelectionFingerprint(tenantID string, credentialID uint, items []AccessCheckSelectionItem) string {
	type row struct{ Email, UserID, Service, Method string }
	rows := make([]row, 0, len(items))
	for _, it := range items {
		rows = append(rows, row{
			Email:   strings.ToLower(strings.TrimSpace(it.Email)),
			UserID:  strings.TrimSpace(it.UserID),
			Service: strings.ToLower(strings.TrimSpace(it.Service)),
			Method:  strings.TrimSpace(it.Method),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Email != b.Email {
			return a.Email < b.Email
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Method < b.Method
	})
	payload, _ := json.Marshal(struct {
		TenantID     string `json:"t"`
		CredentialID uint   `json:"c"`
		Items        []row  `json:"i"`
	}{TenantID: strings.ToLower(strings.TrimSpace(tenantID)), CredentialID: credentialID, Items: rows})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// JobAccessCheck returns the access_check state from a system job's input_data.
func JobAccessCheck(job *CronJobListingDB) *AccessCheckState {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return nil
	}
	raw, ok := (*job.InputData.Json())[accessCheckInputKey]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var st AccessCheckState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil
	}
	return &st
}

// SetJobAccessCheck writes access_check into job input_data (in memory).
func SetJobAccessCheck(job *CronJobListingDB, st *AccessCheckState) map[string]interface{} {
	input := copyJobInput(job)
	if st == nil {
		delete(input, accessCheckInputKey)
	} else {
		input[accessCheckInputKey] = st
	}
	if job != nil {
		job.InputData = database.NewDbJsonFromValue(input)
	}
	return map[string]interface{}{
		"input_data": database.NewDbJsonFromValue(input),
	}
}

// AccessCheckSummaryFromResults recomputes summary counts.
func AccessCheckSummaryFromResults(results []AccessCheckResultItem) map[string]int {
	summary := map[string]int{"accessible": 0, "no_access": 0, "probe_error": 0, "total": len(results)}
	for _, r := range results {
		switch strings.ToLower(strings.TrimSpace(r.Status)) {
		case "accessible":
			summary["accessible"]++
		case "no_access":
			summary["no_access"]++
		default:
			summary["probe_error"]++
		}
	}
	return summary
}

// FindOpenAccessCheckJob returns an in-flight system access-check job for the same fingerprint
// that has not yet materialized backup jobs (refresh-safe start).
func (r *CronJobRepository) FindOpenAccessCheckJob(userID, tenantID string, credentialID uint, fingerprint string) (*CronJobListingDB, error) {
	userID = strings.TrimSpace(userID)
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	fingerprint = strings.TrimSpace(fingerprint)
	if userID == "" || tenantID == "" || credentialID == 0 || fingerprint == "" {
		return nil, nil
	}
	var rows []CronJobListingDB
	err := r.db.Where(
		"user_id = ? AND provider = ? AND tenant_id = ? AND job_type = ? AND method = ? AND deleted_at IS NULL",
		userID, CredentialProviderMicrosoft, tenantID, JobTypeSystem, MethodMSAccessVerify,
	).Order("id DESC").Limit(20).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		st := JobAccessCheck(&rows[i])
		if st == nil || st.JobsMaterialized {
			continue
		}
		if st.CredentialID != credentialID {
			continue
		}
		if strings.TrimSpace(st.SelectionFingerprint) != fingerprint {
			continue
		}
		return &rows[i], nil
	}
	return nil, nil
}

// CreateSystemAccessCheckJob inserts a new inactive system job for MS access verify.
func (r *CronJobRepository) CreateSystemAccessCheckJob(
	userID, tenantID string,
	credentialID uint,
	selection []AccessCheckSelectionItem,
	fingerprint string,
) (*CronJobListingDB, error) {
	userID = strings.TrimSpace(userID)
	tenantID = strings.ToLower(strings.TrimSpace(tenantID))
	if userID == "" || tenantID == "" || credentialID == 0 {
		return nil, fmt.Errorf("user_id, tenant_id and credential_id are required")
	}
	if fingerprint == "" {
		fingerprint = AccessCheckSelectionFingerprint(tenantID, credentialID, selection)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	st := &AccessCheckState{
		TenantID:             tenantID,
		CredentialID:         credentialID,
		SelectionFingerprint: fingerprint,
		Selection:            selection,
		Results:              []AccessCheckResultItem{},
		JobsMaterialized:     false,
		Phase:                AccessCheckPhaseProbing,
		StartedAt:            now,
		Summary:              AccessCheckSummaryFromResults(nil),
	}
	runID := uuid.NewString()
	input := map[string]interface{}{
		"credential_id":   credentialID,
		accessCheckInputKey: st,
	}
	data := CronJobListingDB{
		UserID:       userID,
		Name:         SystemAccessCheckName,
		Method:       MethodMSAccessVerify,
		SyncType:     SyncTypeSystem,
		Provider:     CredentialProviderMicrosoft,
		TenantID:     tenantID,
		ResourceType: ResourceTypeSystem,
		ResourceID:   runID,
		JobType:      JobTypeSystem,
		Active:       false,
		Hidden:       true,
		InputData:    database.NewDbJsonFromValue(input),
		Status:       JobStatusCreated,
		Message:      SystemAccessCheckTaskMessage,
		MessageStatus: JobMessageStatusInfo,
	}
	if err := r.db.Create(&data).Error; err != nil {
		return nil, fmt.Errorf("create system access-check job: %w", err)
	}
	return &data, nil
}

// LockSystemAccessCheckJob loads a system access-check job FOR UPDATE inside tx.
func (r *CronJobRepository) LockSystemAccessCheckJob(tx *gormio.DB, jobID uint, userID string) (*CronJobListingDB, error) {
	db := tx
	if db == nil {
		db = r.db.GetGormDB()
	}
	var job CronJobListingDB
	q := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_type = ?", jobID, JobTypeSystem)
	if strings.TrimSpace(userID) != "" {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// PersistAccessCheckState updates input_data.access_check (and optional message/status).
func (r *CronJobRepository) PersistAccessCheckState(jobID uint, st *AccessCheckState, message, messageStatus, status string) error {
	var job CronJobListingDB
	if err := r.db.Where("id = ?", jobID).First(&job).Error; err != nil {
		return err
	}
	patch := SetJobAccessCheck(&job, st)
	if message != "" {
		patch["message"] = message
	}
	if messageStatus != "" {
		patch["message_status"] = messageStatus
	}
	if status != "" {
		patch["status"] = status
	}
	return r.db.Model(&CronJobListingDB{}).Where("id = ?", jobID).Updates(patch).Error
}

// BeginTx starts a repository transaction for materialize locking.
func (r *CronJobRepository) BeginTx() *gormio.DB {
	return r.db.Begin()
}
