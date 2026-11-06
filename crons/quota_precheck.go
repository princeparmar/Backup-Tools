package crons

import (
	"context"
	"errors"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/storx"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/gmail/v1"
	people "google.golang.org/api/people/v1"
)

// deactivateJobStorageQuota deactivates one job for STORAGE_QUOTA (pre-check, mid-run, or Drive skips).
// Does not clear Redis. Sibling jobs are untouched.
func deactivateJobStorageQuota(store *db.PostgresDb, job *repo.CronJobListingDB, msg, msgStatus string, estimateBytes, remainingBytes int64) {
	if store == nil || job == nil {
		return
	}
	job.Active = false
	job.AutoDeactivated = true
	job.Message = msg
	job.MessageStatus = msgStatus
	job.FailureCode = quota.FailureCodeStorageQuota
	job.QuotaKind = "storage"
	job.EstimateBytes = estimateBytes
	job.RemainingBytes = remainingBytes

	fields := quota.PersistFailureFields(quota.FailureCodeStorageQuota, estimateBytes, remainingBytes, "storage")
	fields["message"] = msg
	fields["message_status"] = msgStatus
	fields["active"] = false
	fields["auto_deactivated"] = true
	_ = store.CronJobRepo.UpdateCronJobByID(job.ID, fields)
}

// clearJobStorageQuotaMarkers clears prior STORAGE_QUOTA fields after a successful pre-check.
func clearJobStorageQuotaMarkers(store *db.PostgresDb, job *repo.CronJobListingDB) {
	if store == nil || job == nil {
		return
	}
	_ = store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"failure_code":    "",
		"estimate_bytes":  0,
		"remaining_bytes": 0,
		"quota_kind":      "",
	})
}

// enforceStoragePrecheck blocks when required > remaining. Persist/deactivate is done once in
// failJobForStorageQuotaPrecheck (jobs.go) when this error is handled — avoid double-write here.
func enforceStoragePrecheck(ctx context.Context, input ProcessorInput, estimateBytes int64) error {
	if input.Job == nil || input.Database == nil {
		return nil
	}
	err := quota.EnsureStorage(ctx, input.Database, input.Job, estimateBytes)
	if err == nil {
		clearJobStorageQuotaMarkers(input.Database, input.Job)
		return nil
	}
	return err
}

func estimateAndEnforceGmail(ctx context.Context, input ProcessorInput, svc *gmail.Service, apiUser string) error {
	est, err := google.EstimateGmailBytes(ctx, svc, apiUser)
	if err != nil {
		logger.Warn(ctx, "gmail size estimate failed; allowing start (layer-2 still enforces)", logger.ErrorField(err))
		return nil
	}
	return enforceStoragePrecheck(ctx, input, est)
}

func estimateAndEnforceContacts(ctx context.Context, input ProcessorInput, svc *people.Service) error {
	est, err := google.EstimateContactsBytes(ctx, svc)
	if err != nil {
		logger.Warn(ctx, "contacts size estimate failed; allowing start", logger.ErrorField(err))
		est = google.ContactsMinEstimateBytes
	}
	return enforceStoragePrecheck(ctx, input, est)
}

func estimateAndEnforceCalendar(ctx context.Context, input ProcessorInput, svc *calendar.Service) error {
	est, err := google.EstimateCalendarBytes(ctx, svc)
	if err != nil {
		logger.Warn(ctx, "calendar size estimate failed; allowing start", logger.ErrorField(err))
		est = google.CalendarMinEstimateBytes
	}
	return enforceStoragePrecheck(ctx, input, est)
}

// mapUploadErr converts mid-run storage limit errors to ErrStorageQuota (keep data/Redis).
func mapUploadErr(method string, err error) error {
	if err == nil {
		return nil
	}
	if storx.IsStorageLimitError(err) || quota.IsStorageQuota(err) {
		return &quota.ErrStorageQuota{Method: method, MidRun: true}
	}
	return err
}

// shouldAbortOnItemError returns true when a per-item sync failure must stop the whole job.
func shouldAbortOnItemError(err error) bool {
	return err != nil && (quota.IsStorageQuota(err) || storx.IsStorageLimitError(err))
}

func wrapStorageAbort(method string, err error) error {
	if err == nil {
		return nil
	}
	if mapped := mapUploadErr(method, err); mapped != err {
		return mapped
	}
	return fmt.Errorf("%s sync aborted: %w", method, err)
}

func asErrStorageQuota(err error) *quota.ErrStorageQuota {
	var sq *quota.ErrStorageQuota
	if errors.As(err, &sq) {
		return sq
	}
	return nil
}
