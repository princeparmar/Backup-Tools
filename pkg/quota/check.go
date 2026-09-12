package quota

import (
	"context"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storx"
)

// UsageSnapshot is storage + bandwidth from satellite usage-limits.
type UsageSnapshot struct {
	StorageUsed    int64
	StorageLimit   int64
	BandwidthUsed  int64
	BandwidthLimit int64
}

// RemainingStorage returns max(0, limit-used). Unlimited (limit<=0) returns a large headroom.
func (u UsageSnapshot) RemainingStorage() int64 {
	if u.StorageLimit <= 0 {
		return 1 << 62
	}
	r := u.StorageLimit - u.StorageUsed
	if r < 0 {
		return 0
	}
	return r
}

// RemainingBandwidth returns max(0, limit-used). Unlimited (limit<=0) returns large headroom.
func (u UsageSnapshot) RemainingBandwidth() int64 {
	if u.BandwidthLimit <= 0 {
		return 1 << 62
	}
	r := u.BandwidthLimit - u.BandwidthUsed
	if r < 0 {
		return 0
	}
	return r
}

// FetchUsageLimits loads Redis-backed usage from Satellite for the job's user/project.
func FetchUsageLimits(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB) (UsageSnapshot, error) {
	if store == nil || job == nil {
		return UsageSnapshot{}, fmt.Errorf("quota: missing store or job")
	}
	userID, projectID, email, err := resolveJobIdentity(store, job)
	if err != nil {
		return UsageSnapshot{}, err
	}
	_ = email
	limits, err := satellite.GetProjectUsageLimits(ctx, userID, projectID)
	if err != nil {
		return UsageSnapshot{}, err
	}
	return UsageSnapshot{
		StorageUsed:    limits.StorageUsed,
		StorageLimit:   limits.StorageLimit,
		BandwidthUsed:  limits.BandwidthUsed,
		BandwidthLimit: limits.BandwidthLimit,
	}, nil
}

// FetchUsageLimitsForRestore loads usage for a restore job's user/project.
func FetchUsageLimitsForRestore(ctx context.Context, userID, projectID string) (UsageSnapshot, error) {
	limits, err := satellite.GetProjectUsageLimits(ctx, userID, projectID)
	if err != nil {
		return UsageSnapshot{}, err
	}
	return UsageSnapshot{
		StorageUsed:    limits.StorageUsed,
		StorageLimit:   limits.StorageLimit,
		BandwidthUsed:  limits.BandwidthUsed,
		BandwidthLimit: limits.BandwidthLimit,
	}, nil
}

func resolveJobIdentity(store *db.PostgresDb, job *repo.CronJobListingDB) (userID, projectID, email string, err error) {
	userID = job.UserID
	projectID = store.CronJobRepo.ResolvedStorjProjectID(job)
	email = job.Name
	if cid := repo.JobCredentialID(job); cid > 0 {
		cred, credErr := store.CredentialRepo.GetByID(cid)
		if credErr == nil && cred != nil {
			if projectID == "" {
				projectID = cred.StorjProjectID
			}
			if e := cred.Email; e != "" {
				email = e
			}
		}
	}
	if projectID == "" {
		return "", "", "", fmt.Errorf("storj project_id required for usage-limits")
	}
	return userID, projectID, email, nil
}

// EnsureStorage blocks if estimate exceeds remaining storage.
func EnsureStorage(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB, estimateBytes int64) error {
	usage, err := FetchUsageLimits(ctx, store, job)
	if err != nil {
		logger.Warn(ctx, "quota storage pre-check: usage-limits unavailable; allowing start (layer-2 still enforces)",
			logger.ErrorField(err))
		return nil
	}
	required := RequiredBytes(estimateBytes)
	remaining := usage.RemainingStorage()
	method := ""
	if job != nil {
		method = job.Method
	}
	if required > remaining {
		return &ErrStorageQuota{
			EstimateBytes:  estimateBytes,
			RequiredBytes:  required,
			RemainingBytes: remaining,
			UsedBytes:      usage.StorageUsed,
			LimitBytes:     usage.StorageLimit,
			Method:         method,
			MidRun:         false,
		}
	}
	logger.Info(ctx, "quota storage pre-check passed",
		logger.String("method", method),
		logger.Int64("estimate_bytes", estimateBytes),
		logger.Int64("required_bytes", required),
		logger.Int64("remaining_bytes", remaining),
	)
	return nil
}

// EnsureBandwidth blocks if downloadEstimate exceeds remaining bandwidth.
func EnsureBandwidth(ctx context.Context, userID, projectID string, estimateBytes int64) error {
	if estimateBytes <= 0 {
		return nil
	}
	usage, err := FetchUsageLimitsForRestore(ctx, userID, projectID)
	if err != nil {
		logger.Warn(ctx, "quota bandwidth pre-check: usage-limits unavailable; allowing start",
			logger.ErrorField(err))
		return nil
	}
	remaining := usage.RemainingBandwidth()
	if estimateBytes > remaining {
		return &ErrBandwidthQuota{
			EstimateBytes:  estimateBytes,
			RemainingBytes: remaining,
			UsedBytes:      usage.BandwidthUsed,
			LimitBytes:     usage.BandwidthLimit,
		}
	}
	return nil
}

// MidRunStorageQuota wraps an uplink storage-limit error with quota metadata.
func MidRunStorageQuota(method string, cause error) error {
	if cause == nil {
		return nil
	}
	if !storx.IsStorageLimitError(cause) && !IsStorageQuota(cause) {
		return cause
	}
	return &ErrStorageQuota{
		Method: method,
		MidRun: true,
	}
}

// PersistFailureFields returns a map suitable for CronJobRepo.UpdateCronJobByID / UpdateCronJobFieldsForCron.
func PersistFailureFields(code string, estimate, remaining int64, kind string) map[string]interface{} {
	m := map[string]interface{}{
		"failure_code": code,
		"quota_kind":   kind,
	}
	if estimate > 0 {
		m["estimate_bytes"] = estimate
	}
	if remaining >= 0 {
		m["remaining_bytes"] = remaining
	}
	return m
}
