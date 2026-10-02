package restore

import (
	"context"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"storj.io/uplink"
)

const restoreBandwidthListTimeout = 20 * time.Second

// estimateRestoreDownloadBytes sums ContentLength for objects under login prefix (capped by timeout).
func estimateRestoreDownloadBytes(ctx context.Context, accessGrant, method, loginID string) (int64, error) {
	bucket := bucketForRestoreMethod(method)
	if bucket == "" || strings.TrimSpace(accessGrant) == "" {
		return 0, nil
	}
	prefix := strings.TrimSpace(loginID)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	ctx, cancel := context.WithTimeout(ctx, restoreBandwidthListTimeout)
	defer cancel()

	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		return 0, err
	}
	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return 0, err
	}
	defer project.Close()

	iter := project.ListObjects(ctx, bucket, &uplink.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
		System:    true,
	})
	var total int64
	for iter.Next() {
		obj := iter.Item()
		if obj == nil || obj.IsPrefix {
			continue
		}
		if strings.HasSuffix(obj.Key, "/.file_placeholder") {
			continue
		}
		total += obj.System.ContentLength
	}
	if err := iter.Err(); err != nil {
		return total, err
	}
	return total, nil
}

func bucketForRestoreMethod(method string) string {
	switch method {
	case "gmail":
		return satellite.ReserveBucket_Gmail
	case "google_drive":
		return satellite.ReserveBucket_Drive
	case "google_photos":
		return satellite.ReserveBucket_Photos
	case "google_calendar":
		return satellite.ReserveBucket_Calendar
	case "google_contacts":
		return satellite.ReserveBucket_Contacts
	default:
		return ""
	}
}

// EnforceRestoreBandwidthPrecheck blocks restore start when download estimate exceeds remaining bandwidth.
// Does not clear Redis. Persists BANDWIDTH_QUOTA on the restore job when blocked.
func EnforceRestoreBandwidthPrecheck(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB, accessGrant string) error {
	if job == nil || store == nil {
		return nil
	}
	projectID := strings.TrimSpace(job.StorjProjectID)
	if projectID == "" {
		return nil
	}
	est, err := estimateRestoreDownloadBytes(ctx, accessGrant, job.Method, job.LoginID)
	if err != nil {
		logger.Warn(ctx, "restore bandwidth estimate failed; allowing start", logger.ErrorField(err))
		return nil
	}
	if est <= 0 {
		usage, uerr := quota.FetchUsageLimitsForRestore(ctx, job.UserID, projectID)
		if uerr != nil {
			return nil
		}
		if usage.BandwidthLimit > 0 && usage.RemainingBandwidth() <= 0 {
			bq := &quota.ErrBandwidthQuota{
				EstimateBytes:  0,
				RemainingBytes: 0,
				UsedBytes:      usage.BandwidthUsed,
				LimitBytes:     usage.BandwidthLimit,
			}
			persistRestoreBandwidthFailure(store, job, bq)
			return bq
		}
		return nil
	}
	if err := quota.EnsureBandwidth(ctx, job.UserID, projectID, est); err != nil {
		if bq, ok := err.(*quota.ErrBandwidthQuota); ok {
			persistRestoreBandwidthFailure(store, job, bq)
		}
		return err
	}
	return nil
}

func persistRestoreBandwidthFailure(store *db.PostgresDb, job *repo.RestoreJobListingDB, bq *quota.ErrBandwidthQuota) {
	if store == nil || job == nil || bq == nil {
		return
	}
	job.FailureCode = quota.FailureCodeBandwidthQuota
	job.EstimateBytes = bq.EstimateBytes
	job.RemainingBytes = bq.RemainingBytes
	job.QuotaKind = "bandwidth"
	job.Message = bq.Error()
	job.MessageStatus = repo.JobMessageStatusError
	_ = store.RestoreJobRepo.UpdateJob(job.ID, map[string]interface{}{
		"failure_code":    quota.FailureCodeBandwidthQuota,
		"estimate_bytes":  bq.EstimateBytes,
		"remaining_bytes": bq.RemainingBytes,
		"quota_kind":      "bandwidth",
		"message":         bq.Error(),
		"message_status":  repo.JobMessageStatusError,
		"status":          repo.RestoreJobStatusFailed,
	})
}
