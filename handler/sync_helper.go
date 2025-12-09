package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// deriveSource derives source (provider) from bucket name
// Currently only supports Google services: gmail, google-photos, google-drive
func deriveSource(bucketName string) string {
	if bucketName == "gmail" || bucketName == "google-photos" || bucketName == "google-drive" {
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
	default:
		if strings.HasPrefix(bucketName, "google-") {
			return strings.TrimPrefix(bucketName, "google-")
		}
		return bucketName
	}
}

// UploadObjectAndSync uploads data to Satellite storage and creates/updates the synced_objects table entry.
// Returns error only if upload fails. Database tracking failures are logged but don't fail the operation.
func UploadObjectAndSync(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, objectKey string,
	data []byte,
	userID string,
) error {
	// Step 1: Upload to Satellite
	if err := satellite.UploadObject(ctx, accessGrant, bucketName, objectKey, data); err != nil {
		logger.Error(ctx, "Failed to upload object to Satellite",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		return fmt.Errorf("failed to upload object to Satellite: %w", err)
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
