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
	return storxrefresh.IsStorageLimitError(err)
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
// Currently only supports Google services: gmail, google-photos, google-drive
func deriveSource(bucketName string) string {
	if bucketName == "gmail" || bucketName == "google-photos" || bucketName == "google-drive" || bucketName == "google-contacts" || bucketName == "google-calendar" {
		return "google"
	}
	if strings.HasPrefix(bucketName, "google-") {
		return "google"
	}
	return bucketName
}

// deriveType derives type from bucket name
// Currently only supports: gmail, google-photos, google-drive
func deriveType(bucketName string) string {
	switch bucketName {
	case "gmail":
		return "gmail"
	case "google-photos":
		return "photos"
	case "google-drive":
		return "drive"
	case "google-contacts":
		return "contacts"
	case "google-calendar":
		return "calendar"
	default:
		if strings.HasPrefix(bucketName, "google-") {
			return strings.TrimPrefix(bucketName, "google-")
		}
		return bucketName
	}
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
	r := storxRecoveryFrom(recovery...)
	if err := satellite.UploadObject(ctx, accessGrant, bucketName, objectKey, data); err != nil {
		uploadErr := fmt.Errorf("failed to upload object to Satellite: %w", err)
		logger.Error(ctx, "Failed to upload object to Satellite",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		if r != nil && IsStorxUplinkError(uploadErr) {
			grant, continueOK, recErr := r.OnStorxError(ctx, uploadErr)
			if !continueOK {
				if recErr != nil {
					return recErr
				}
				return uploadErr
			}
			return UploadObjectAndSync(ctx, database, grant, bucketName, objectKey, data, userID, r)
		}
		return uploadErr
	}

	// Step 2: Derive source and type from bucket name
	source := deriveSource(bucketName)
	objectType := deriveType(bucketName)

	// Step 3: Update synced_objects table (non-blocking - log but don't fail)
	if err := database.SyncedObjectRepo.CreateSyncedObject(userID, bucketName, objectKey, source, objectType); err != nil {
		logger.Error(ctx, "Failed to create synced object entry after successful upload",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		// Note: Object is already uploaded to Satellite, but database tracking failed
		// This is logged but we don't fail the entire operation
		return nil
	}

	return nil
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
	r := storxRecoveryFrom(recovery...)
	access, err := uplink.ParseAccess(accessGrant)
	if err != nil {
		parseErr := fmt.Errorf("parse access grant: %w", err)
		if r != nil && IsStorxUplinkError(parseErr) {
			grant, continueOK, recErr := r.OnStorxError(ctx, parseErr)
			if !continueOK {
				if recErr != nil {
					return nil, recErr
				}
				return nil, parseErr
			}
			return GetSyncedObjectsWithPrefix(ctx, database, grant, bucketName, prefix, userID, source, objectType, r)
		}
		return nil, parseErr
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		openErr := fmt.Errorf("open project: %w", err)
		if r != nil && IsStorxUplinkError(openErr) {
			grant, continueOK, recErr := r.OnStorxError(ctx, openErr)
			if !continueOK {
				if recErr != nil {
					return nil, recErr
				}
				return nil, openErr
			}
			return GetSyncedObjectsWithPrefix(ctx, database, grant, bucketName, prefix, userID, source, objectType, r)
		}
		return nil, openErr
	}
	defer project.Close()

	_, err = project.EnsureBucket(ctx, bucketName)
	if err != nil {
		_, err = project.CreateBucket(ctx, bucketName)
		if err != nil {
			bucketErr := fmt.Errorf("could not create bucket: %w", err)
			if r != nil && IsStorxUplinkError(bucketErr) {
				grant, continueOK, recErr := r.OnStorxError(ctx, bucketErr)
				if !continueOK {
					if recErr != nil {
						return nil, recErr
					}
					return nil, bucketErr
				}
				return GetSyncedObjectsWithPrefix(ctx, database, grant, bucketName, prefix, userID, source, objectType, r)
			}
			logger.Warn(ctx, "Failed to create bucket, will be created on first upload if needed",
				logger.String("bucket", bucketName),
				logger.ErrorField(err))
		}
	}

	// Step 2: Get synced objects from database
	syncedObjects, err := database.SyncedObjectRepo.GetSyncedObjectsByUserAndBucket(userID, bucketName, source, objectType)
	if err != nil {
		logger.Warn(ctx, "Failed to get synced objects from database, returning empty map",
			logger.String("bucket", bucketName),
			logger.String("user_id", userID),
			logger.ErrorField(err))
		return make(map[string]bool), nil
	}

	// Step 3: Build map with prefix filtering
	objects := make(map[string]bool)
	for _, obj := range syncedObjects {
		if prefix == "" || strings.HasPrefix(obj.ObjectKey, prefix) {
			objects[obj.ObjectKey] = true
		}
	}

	return objects, nil
}
