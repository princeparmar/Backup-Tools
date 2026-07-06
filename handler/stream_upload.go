package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// AutosyncStreamThresholdBytes is the size above which Google autosync streams file content to StorX.
const AutosyncStreamThresholdBytes = 10 * 1024 * 1024 // 10 MB

// ShouldUseStreamingUpload reports whether autosync should stream content instead of buffering in RAM.
func ShouldUseStreamingUpload(contentLength int64, mimeType string) bool {
	if strings.HasPrefix(mimeType, "application/vnd.google-apps") {
		// Google Workspace exports do not expose reliable pre-download sizes.
		return true
	}
	if contentLength > AutosyncStreamThresholdBytes {
		return true
	}
	if contentLength <= 0 && strings.HasPrefix(strings.ToLower(mimeType), "video/") {
		// Photos/videos may omit Content-Length; stream large video types safely.
		return true
	}
	return false
}

// ShouldStreamBufferedPayload reports whether an in-memory payload (JSON, etc.) should use stream upload.
func ShouldStreamBufferedPayload(payloadLen int) bool {
	return ShouldUseStreamingUpload(int64(payloadLen), "application/json")
}

// UploadBufferedObjectAndSync uploads autosync payloads using the 10MB rule (stream vs direct).
func UploadBufferedObjectAndSync(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, objectKey string,
	data []byte,
	userID string,
	recovery ...*StorxRecovery,
) error {
	if ShouldStreamBufferedPayload(len(data)) {
		return UploadObjectStreamAndSync(ctx, database, accessGrant, bucketName, objectKey, bytes.NewReader(data), userID, recovery...)
	}
	return UploadObjectAndSync(ctx, database, accessGrant, bucketName, objectKey, data, userID, recovery...)
}

// UploadObjectStreamAndSync streams content to Satellite storage and creates/updates synced_objects.
// Optional recovery is used by cron autosync only. On grant refresh the caller must supply a new reader.
func UploadObjectStreamAndSync(
	ctx context.Context,
	database *db.PostgresDb,
	accessGrant, bucketName, objectKey string,
	body io.Reader,
	userID string,
	recovery ...*StorxRecovery,
) error {
	rec := storxRecoveryFrom(recovery...)
	if err := satellite.UploadObjectFromReader(ctx, accessGrant, bucketName, objectKey, body); err != nil {
		uploadErr := fmt.Errorf("failed to upload object to Satellite: %w", err)
		logger.Error(ctx, "Failed to stream object to Satellite",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		if rec != nil && IsStorxUplinkError(uploadErr) {
			grant, continueOK, recErr := rec.OnStorxError(ctx, uploadErr)
			if !continueOK {
				if recErr != nil {
					return recErr
				}
				return uploadErr
			}
			return UploadObjectStreamAndSync(ctx, database, grant, bucketName, objectKey, body, userID, rec)
		}
		return uploadErr
	}

	source := deriveSource(bucketName)
	objectType := deriveType(bucketName)

	if err := database.SyncedObjectRepo.CreateSyncedObject(userID, bucketName, objectKey, source, objectType); err != nil {
		logger.Error(ctx, "Failed to create synced object entry after successful stream upload",
			logger.String("bucket", bucketName),
			logger.String("object_key", objectKey),
			logger.ErrorField(err),
		)
		return nil
	}

	return nil
}
