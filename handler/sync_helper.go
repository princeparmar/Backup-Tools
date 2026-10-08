package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
	"storj.io/uplink"
)

const (
	StorxRefreshLimitJobMessage     = storxrefresh.RefreshLimitJobMessage
	StorxSatelliteRefreshJobMessage = storxrefresh.RefreshLimitJobMessage
)

// ErrStorxGrantMissing is returned when no storx grant is available before backup starts.
var ErrStorxGrantMissing = errors.New("storx access grant not found")

// ErrStorxSatelliteRefreshFailed is returned when storx refresh failures reached the deactivate threshold.
var ErrStorxSatelliteRefreshFailed = storxrefresh.ErrRefreshLimitExceeded

// StorxRecovery is the shared storx refresh policy used by cron autosync.
type StorxRecovery = storxrefresh.Recovery

// NewStorxRecovery creates a per-task storx recovery helper for a cron job.
func NewStorxRecovery(store *db.PostgresDb, job *repo.CronJobListingDB) *StorxRecovery {
	return storxrefresh.NewRecovery(store, job)
}

// IsStorxStorageLimitError reports whether err is a CyberLS storage quota exhaustion failure.
func IsStorxStorageLimitError(err error) bool {
	if storxrefresh.IsStorageLimitError(err) {
		return true
	}
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "storage_quota") || strings.Contains(msg, "storage limit exceeded")
}

// IsStorxUplinkError reports whether err is a missing/invalid storx grant or uplink permission failure.
func IsStorxUplinkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStorxGrantMissing) {
		return true
	}
	return storxrefresh.IsUplinkError(err)
}

// IsStorxSatelliteRefreshError reports whether err deactivated the job after repeated refresh failures.
func IsStorxSatelliteRefreshError(err error) bool {
	return storxrefresh.IsRefreshLimitError(err)
}

// IsStorxRefreshFailedError reports whether Satellite refresh failed with the job still active.
func IsStorxRefreshFailedError(err error) bool {
	return storxrefresh.IsRefreshFailedError(err)
}

// MaxStorxUplinkRecoveriesPerRun is the per-task uplink refresh retry cap for cron autosync.
func MaxStorxUplinkRecoveriesPerRun() int {
	return storxrefresh.MaxUplinkRecoveriesPerRun()
}

// deriveSource derives source (provider) from bucket name
// Currently only supports Google services: cyberls-gmail, cyberls-drive, google-photos, ...
func deriveSource(bucketName string) string {
	if bucketName == "cyberls-gmail" || bucketName == "gmail" || bucketName == "cyberls-drive" || bucketName == "google-drive" || bucketName == "google-photos" || bucketName == "cyberls-contacts" || bucketName == "google-contacts" || bucketName == "cyberls-calendar" || bucketName == "google-calendar" {
		return "google"
	}
	if bucketName == "outlook" || bucketName == "outlook-calendar" || bucketName == "outlook-contacts" || bucketName == "outlook-sharepoint" || bucketName == "outlook-teams" || bucketName == "outlook-groups" {
		return "outlook"
	}
	if strings.HasPrefix(bucketName, "google-") || strings.HasPrefix(bucketName, "cyberls-") {
		return "google"
	}
	if strings.HasPrefix(bucketName, "outlook-") {
		return "outlook"
	}
	return bucketName
}

// deriveType derives type from bucket name
// Currently only supports: cyberls-gmail (legacy: gmail), cyberls-drive (legacy: google-drive), ...
func deriveType(bucketName string) string {
	switch bucketName {
	case "cyberls-gmail", "gmail":
		return "gmail"
	case "google-photos":
		return "photos"
	case "cyberls-drive", "google-drive":
		return "drive"
	case "cyberls-contacts", "google-contacts":
		return "contacts"
	case "cyberls-calendar", "google-calendar":
		return "calendar"
	case "outlook":
		return "outlook"
	case "outlook-calendar":
		return "outlook_calendar"
	case "outlook-contacts":
		return "outlook_contacts"
	case "outlook-onedrive":
		return "outlook_onedrive"
	case "outlook-sharepoint":
		return "outlook_sharepoint"
	default:
		if strings.HasPrefix(bucketName, "google-") {
			return strings.TrimPrefix(bucketName, "google-")
		}
		if strings.HasPrefix(bucketName, "outlook-") {
			return "outlook_" + strings.ReplaceAll(strings.TrimPrefix(bucketName, "outlook-"), "-", "_")
		}
		if strings.HasPrefix(bucketName, "cyberls-") {
			return strings.TrimPrefix(bucketName, "cyberls-")
		}
		return bucketName
	}
}

// refreshedStorxGrant refreshes a failed grant at most MaxStorxUplinkRecoveriesPerRun times.
// An empty return with a nil error means the original failure should be returned as-is.
func refreshedStorxGrant(ctx context.Context, r *StorxRecovery, currentGrant string, cause error, attempt int) (string, error) {
	if r == nil || !IsStorxUplinkError(cause) || attempt >= MaxStorxUplinkRecoveriesPerRun() {
		return "", nil
	}
	grant, continueOK, recErr := r.OnStorxError(ctx, cause)
	if !continueOK {
		if recErr != nil {
			return "", recErr
		}
		return "", nil
	}
	grant = strings.TrimSpace(grant)
	if grant == "" || grant == strings.TrimSpace(currentGrant) {
		return "", nil
	}
	return grant, nil
}

func storxRecoveryFrom(recovery ...*StorxRecovery) *StorxRecovery {
	if len(recovery) > 0 {
		return recovery[0]
	}
	return nil
}

// UploadObjectAndSync uploads data to Satellite storage and creates/updates the synced_objects table entry.
// Returns error only if upload fails. Database tracking failures are logged but don't fail the operation.
// Optional recovery is used by cron autosync only; scheduled tasks omit it.
func UploadObjectAndSync(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, objectKey string,
	data []byte,
	userID string,
	recovery ...*StorxRecovery,
) error {
	return UploadObjectWithMetadataAndSync(ctx, database, accessGrant, bucketName, objectKey, data, nil, userID, recovery...)
}

// UploadObjectWithMetadataAndSync uploads bytes with optional StorX custom metadata, then tracks synced_objects.
func UploadObjectWithMetadataAndSync(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, objectKey string,
	data []byte,
	meta map[string]string,
	userID string,
	recovery ...*StorxRecovery,
) error {
	return uploadObjectWithMetadataAndSync(ctx, database, accessGrant, bucketName, objectKey, data, meta, userID, storxRecoveryFrom(recovery...), 0)
}

func uploadObjectWithMetadataAndSync(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, objectKey string,
	data []byte,
	meta map[string]string,
	userID string,
	r *StorxRecovery,
	attempt int,
) error {
	bucketName = satellite.BucketForAccess(accessGrant, bucketName)
	var err error
	if len(meta) > 0 {
		err = satellite.UploadObjectWithMetadata(ctx, accessGrant, bucketName, objectKey, data, meta)
	} else {
		err = satellite.UploadObject(ctx, accessGrant, bucketName, objectKey, data)
	}
	if err != nil {
		uploadErr := fmt.Errorf("failed to upload object to Satellite: %w", err)
		logger.Error(ctx, "Failed to upload object to Satellite",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		grant, recErr := refreshedStorxGrant(ctx, r, accessGrant, uploadErr, attempt)
		if recErr != nil {
			return recErr
		}
		if grant == "" {
			return uploadErr
		}
		return uploadObjectWithMetadataAndSync(ctx, database, grant, bucketName, objectKey, data, meta, userID, r, attempt+1)
	}

	logObjectBackedUp(ctx, bucketName, objectKey, int64(len(data)), r)
	source := deriveSource(bucketName)
	objectType := deriveType(bucketName)
	if err := database.SyncedObjectRepo.CreateSyncedObject(userID, bucketName, objectKey, source, objectType); err != nil {
		logger.Error(ctx, "Failed to create synced object entry after successful upload",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		return nil
	}
	return nil
}

// logObjectBackedUp is the per-file backup log line shared by every service.
func logObjectBackedUp(ctx context.Context, bucketName, objectKey string, size int64, r *StorxRecovery) {
	fields := []logger.Field{
		logger.String("source", deriveSource(bucketName)),
		logger.String("bucket", bucketName),
		logger.String("object_key", objectKey),
		logger.Int64("bytes", size),
	}
	if r != nil && r.Job != nil {
		fields = append(fields, logger.Int64("job_id", int64(r.Job.ID)), logger.String("method", r.Job.Method))
	}
	logger.Info(ctx, "Backed up object", fields...)
}

// GetSyncedObjectsWithPrefix ensures bucket exists, then gets synced objects from database instead of Satellite
// This is a common function used by both cron processors and scheduled task processors
// Returns a map of object keys (with prefix filtering) for fast lookup
func GetSyncedObjectsWithPrefix(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, prefix, userID, source, objectType string,
	recovery ...*StorxRecovery,
) (map[string]bool, error) {
	return getSyncedObjectsWithPrefix(ctx, database, accessGrant, bucketName, prefix, userID, source, objectType, storxRecoveryFrom(recovery...), 0)
}

func getSyncedObjectsWithPrefix(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, prefix, userID, source, objectType string,
	r *StorxRecovery,
	attempt int,
) (map[string]bool, error) {
	bucketName = satellite.BucketForAccess(accessGrant, bucketName)

	// External gateway S3 tokens are not uplink access grants — skip EnsureBucket.
	if _, ok := satellite.ParseGatewayS3Token(accessGrant); ok {
		return getSyncedObjectsFromDB(database, bucketName, prefix, userID, source, objectType)
	}

	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		parseErr := fmt.Errorf("parse access grant: %w", err)
		grant, recErr := refreshedStorxGrant(ctx, r, accessGrant, parseErr, attempt)
		if recErr != nil {
			return nil, recErr
		}
		if grant == "" {
			return nil, parseErr
		}
		return getSyncedObjectsWithPrefix(ctx, database, grant, bucketName, prefix, userID, source, objectType, r, attempt+1)
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		openErr := fmt.Errorf("open project: %w", err)
		grant, recErr := refreshedStorxGrant(ctx, r, accessGrant, openErr, attempt)
		if recErr != nil {
			return nil, recErr
		}
		if grant == "" {
			return nil, openErr
		}
		return getSyncedObjectsWithPrefix(ctx, database, grant, bucketName, prefix, userID, source, objectType, r, attempt+1)
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		_, err = project.CreateBucket(ctx, bucketName)
		if err != nil {
			bucketErr := fmt.Errorf("could not create bucket: %w", err)
			if IsStorxUplinkError(bucketErr) {
				grant, recErr := refreshedStorxGrant(ctx, r, accessGrant, bucketErr, attempt)
				if recErr != nil {
					return nil, recErr
				}
				if grant == "" {
					return nil, bucketErr
				}
				return getSyncedObjectsWithPrefix(ctx, database, grant, bucketName, prefix, userID, source, objectType, r, attempt+1)
			}
			logger.Warn(ctx, "Failed to create bucket, will be created on first upload if needed",
				logger.String("bucket", bucketName),
				logger.ErrorField(err))
		}
	}

	return getSyncedObjectsFromDB(database, bucketName, prefix, userID, source, objectType)
}

func getSyncedObjectsFromDB(database *db.PostgresDb, bucketName, prefix, userID, source, objectType string) (map[string]bool, error) {
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, bucketName, source, objectType)
	if err != nil {
		logger.Warn(context.Background(), "Failed to get synced objects from database, returning empty map",
			logger.String("bucket", bucketName),
			logger.String("user_id", userID),
			logger.ErrorField(err))
		return make(map[string]bool), nil
	}

	objects := make(map[string]bool)
	for _, obj := range syncedObjects {
		if prefix == "" || strings.HasPrefix(obj.ObjectKey, prefix) {
			objects[obj.ObjectKey] = true
		}
	}

	return objects, nil
}
