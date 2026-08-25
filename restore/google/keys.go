package googlestore

import (
	"github.com/StorX2-0/Backup-Tools/restore"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/people/v1"
)

// RestoreGmailKeyWithSession downloads using DB storx grant (manual restore, single attempt).
func RestoreGmailKeyWithSession(ctx context.Context, sess *restore.StorxGrantSession, client *google.GmailClient, objectKey string) error {
	data, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Gmail, objectKey)
	if err != nil {
		return err
	}
	var msg gmailapi.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	return restore.RetryGoogle(ctx, func() error {
		return client.InsertMessage(&msg)
	})
}

// RestoreGmailKey downloads one message from Satellite and inserts into Gmail (restore-all).
func RestoreGmailKey(ctx context.Context, accessGrant string, client *google.GmailClient, objectKey string) error {
	data, err := restore.DownloadBytes(ctx, accessGrant, satellite.ReserveBucket_Gmail, objectKey, restore.DownloadHints{
		MimeType: "application/json",
	})
	if err != nil {
		return err
	}
	var msg gmailapi.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	return restore.RetryGoogle(ctx, func() error {
		return client.InsertMessage(&msg)
	})
}

// RestoreDriveKeyWithSession restores one Drive object using DB storx grant (manual restore).
func RestoreDriveKeyWithSession(ctx context.Context, sess *restore.StorxGrantSession, srv *drive.Service, userEmail, objectKey string) error {
	return restoreDriveObjectKey(ctx, func(bucket, key string) ([]byte, error) {
		return sess.DownloadObject(ctx, bucket, key)
	}, srv, userEmail, objectKey)
}

// RestoreDriveKey restores one Drive backup object (restore-all with streaming for large files).
func RestoreDriveKey(ctx context.Context, accessGrant string, srv *drive.Service, userEmail, objectKey string) error {
	return restoreDriveObjectKeyRestoreAll(ctx, accessGrant, srv, userEmail, objectKey)
}

func restoreDriveObjectKey(
	ctx context.Context,
	download func(bucket, key string) ([]byte, error),
	srv *drive.Service,
	userEmail, objectKey string,
) error {
	if google.IsDriveIDBasedMetaKey(objectKey) {
		metaBytes, err := download(satellite.ReserveBucket_Drive, objectKey)
		if err != nil {
			return err
		}
		var meta google.DriveCronBackupMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return fmt.Errorf("parse drive cron meta: %w", err)
		}
		if meta.RemovedFromDrive {
			return fmt.Errorf("skip removed drive file %s", strings.TrimSpace(meta.FileID))
		}
		dataKey := strings.TrimSpace(meta.DataObjectKey)
		if dataKey == "" && strings.TrimSpace(meta.FileID) != "" {
			displayName := google.DriveBackupDisplayName(meta.Name, meta.MimeType)
			if displayName != "" && displayName != "untitled" && strings.TrimSpace(meta.CreatedTime) != "" {
				dataKey = google.DriveIDBasedDataKey(strings.TrimSpace(userEmail), meta.FileID, displayName, meta.CreatedTime)
			}
		}
		if dataKey == "" {
			return fmt.Errorf("missing data_object_key for %s", objectKey)
		}
		fileBytes, err := download(satellite.ReserveBucket_Drive, dataKey)
		if err != nil {
			return err
		}
		driveMeta := google.CronMetaToDriveFileMetadata(&meta, dataKey)
		metadataJSON, err := json.Marshal(driveMeta)
		if err != nil {
			return err
		}
		return restore.RetryGoogle(ctx, func() error {
			return google.RestoreFromBackup(ctx, srv, userEmail, metadataJSON, fileBytes)
		})
	}

	data, err := download(satellite.ReserveBucket_Drive, objectKey)
	if err != nil {
		return err
	}
	var backupItem google.DriveBackupItem
	if err := json.Unmarshal(data, &backupItem); err != nil {
		return fmt.Errorf("parse drive backup: %w", err)
	}
	metadataJSON, _ := json.Marshal(backupItem.Metadata)
	return restore.RetryGoogle(ctx, func() error {
		return google.RestoreFromBackup(ctx, srv, userEmail, metadataJSON, backupItem.Content)
	})
}

func restoreDriveObjectKeyRestoreAll(
	ctx context.Context,
	accessGrant string,
	srv *drive.Service,
	userEmail, objectKey string,
) error {
	if google.IsDriveIDBasedMetaKey(objectKey) {
		metaBytes, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Drive, objectKey)
		if err != nil {
			return err
		}
		var meta google.DriveCronBackupMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return fmt.Errorf("parse drive cron meta: %w", err)
		}
		if meta.RemovedFromDrive {
			return fmt.Errorf("skip removed drive file %s", strings.TrimSpace(meta.FileID))
		}
		dataKey := strings.TrimSpace(meta.DataObjectKey)
		if dataKey == "" && strings.TrimSpace(meta.FileID) != "" {
			displayName := google.DriveBackupDisplayName(meta.Name, meta.MimeType)
			if displayName != "" && displayName != "untitled" && strings.TrimSpace(meta.CreatedTime) != "" {
				dataKey = google.DriveIDBasedDataKey(strings.TrimSpace(userEmail), meta.FileID, displayName, meta.CreatedTime)
			}
		}
		if dataKey == "" {
			return fmt.Errorf("missing data_object_key for %s", objectKey)
		}
		driveMeta := google.CronMetaToDriveFileMetadata(&meta, dataKey)
		metadataJSON, err := json.Marshal(driveMeta)
		if err != nil {
			return err
		}
		return RestoreDriveDataFromStorxStream(ctx, accessGrant, srv, userEmail, dataKey, metadataJSON)
	}

	data, err := restore.DownloadBytes(ctx, accessGrant, satellite.ReserveBucket_Drive, objectKey, restore.DownloadHints{
		MimeType: "application/json",
	})
	if err != nil {
		return err
	}
	var backupItem google.DriveBackupItem
	if err := json.Unmarshal(data, &backupItem); err != nil {
		return fmt.Errorf("parse drive backup: %w", err)
	}
	metadataJSON, _ := json.Marshal(backupItem.Metadata)
	return restore.RetryGoogle(ctx, func() error {
		return google.RestoreFromBackup(ctx, srv, userEmail, metadataJSON, backupItem.Content)
	})
}

// RestoreCalendarKeyWithSession restores one calendar event using DB storx grant (manual restore).
func RestoreCalendarKeyWithSession(ctx context.Context, sess *restore.StorxGrantSession, service *calendar.Service, objectKey string) error {
	if !google.IsCalendarEventRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid calendar restore key")
	}
	calendarID, _, ok := google.ParseCalendarEventObjectKey(objectKey)
	if !ok {
		return fmt.Errorf("parse calendar key")
	}
	data, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Calendar, objectKey)
	if err != nil {
		return err
	}
	return restore.RetryGoogle(ctx, func() error {
		return google.RestoreCalendarEventFromBackup(ctx, service, calendarID, data)
	})
}

// RestoreCalendarKey restores one calendar event JSON blob (restore-all).
func RestoreCalendarKey(ctx context.Context, accessGrant string, service *calendar.Service, objectKey string) error {
	if !google.IsCalendarEventRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid calendar restore key")
	}
	calendarID, _, ok := google.ParseCalendarEventObjectKey(objectKey)
	if !ok {
		return fmt.Errorf("parse calendar key")
	}
	data, err := restore.DownloadBytes(ctx, accessGrant, satellite.ReserveBucket_Calendar, objectKey, restore.DownloadHints{
		MimeType: "application/json",
	})
	if err != nil {
		return err
	}
	return restore.RetryGoogle(ctx, func() error {
		return google.RestoreCalendarEventFromBackup(ctx, service, calendarID, data)
	})
}

// RestoreContactKeyWithSession restores one contact using DB storx grant (manual restore).
func RestoreContactKeyWithSession(ctx context.Context, sess *restore.StorxGrantSession, service *people.Service, objectKey string) error {
	if !google.IsContactsRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid contacts restore key")
	}
	data, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Contacts, objectKey)
	if err != nil {
		return err
	}
	return restore.RetryGoogle(ctx, func() error {
		return google.RestoreContactFromBackup(ctx, service, data)
	})
}

// RestoreContactKey restores one contact JSON blob (restore-all).
func RestoreContactKey(ctx context.Context, accessGrant string, service *people.Service, objectKey string) error {
	if !google.IsContactsRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid contacts restore key")
	}
	data, err := restore.DownloadBytes(ctx, accessGrant, satellite.ReserveBucket_Contacts, objectKey, restore.DownloadHints{
		MimeType: "application/json",
	})
	if err != nil {
		return err
	}
	return restore.RetryGoogle(ctx, func() error {
		return google.RestoreContactFromBackup(ctx, service, data)
	})
}

// RestorePhotosKey restores one photo (legacy or ID-based satellite keys, restore-all).
func RestorePhotosKey(ctx context.Context, deps *restore.RestoreDeps, objectKey string) error {
	if strings.Contains(objectKey, "/.file_placeholder") {
		return fmt.Errorf("skip placeholder")
	}

	var (
		albumID    string
		albumTitle string
		filename   string
		mimeType   string
		dataKey    string
		err        error
	)

	tempDir := filepath.Join("./cache", "restore", fmt.Sprintf("job-%d", deps.Job.ID))
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return err
	}

	if dataKey, metaKey, idBased := parsePhotosIDBasedRestoreKeys(objectKey); idBased {
		filename, mimeType, dataKey, err = resolvePhotosIDBasedRestoreGrant(ctx, deps.AccessGrant, objectKey, dataKey, metaKey)
		if err != nil {
			return err
		}
	} else {
		albumID, albumTitle, filename = parseGooglePhotosKey(objectKey)
		dataKey = objectKey
		mimeType = restore.MimeFromFilename(filename)
	}

	if strings.TrimSpace(filename) == "" {
		filename = "restored_photo"
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = restore.MimeFromFilename(filename)
	}

	tempPath := filepath.Join(tempDir, filepath.Base(filename))
	if err := restore.StreamToFile(ctx, deps.AccessGrant, satellite.ReserveBucket_Photos, dataKey, tempPath); err != nil {
		return err
	}
	defer os.Remove(tempPath)

	targetAlbumID := ""
	if albumTitle != "" {
		alb, err := GetOrCreateAlbum(ctx, deps, albumID, albumTitle)
		if err != nil {
			return err
		}
		if alb != nil {
			targetAlbumID = alb.ID
		}
	}

	return restore.RetryGoogle(ctx, func() error {
		_, err := deps.PhotosClient.UploadFileToAlbum(ctx, targetAlbumID, tempPath)
		return err
	})
}

// photos parse helpers (same semantics as handler/google_photos_handlers.go)

type photosRestoreMeta struct {
	MediaItemID   string `json:"media_item_id"`
	Filename      string `json:"filename"`
	MimeType      string `json:"mime_type"`
	DataObjectKey string `json:"data_object_key"`
}

func parsePhotosIDBasedRestoreKeys(key string) (dataKey, metaKey string, ok bool) {
	return google.PhotosMetaKeyFromDataOrMetaKey(key)
}

func parseGooglePhotosKey(key string) (albumID, albumTitle, filename string) {
	if key == "" {
		return
	}
	parts := strings.SplitN(key, "/", 3)
	switch len(parts) {
	case 3:
		albumFolder := parts[1]
		filename = parts[2]
		if len(albumFolder) > 76 && albumFolder[76] == '_' {
			albumID = albumFolder[:76]
			albumTitle = albumFolder[77:]
		} else if idx := strings.Index(albumFolder, "_"); idx > 0 {
			albumID = albumFolder[:idx]
			albumTitle = albumFolder[idx+1:]
		}
	case 2:
		filename = parts[1]
	default:
		filename = key
	}
	if len(filename) > 98 && filename[98] == '_' {
		filename = filename[99:]
	} else if idx := strings.Index(filename, "_"); idx > 0 {
		filename = filename[idx+1:]
	}
	return
}

// DownloadPhotosIDBasedPayloadWithSession downloads ID-based photo backup using DB storx grant (manual restore).
func DownloadPhotosIDBasedPayloadWithSession(ctx context.Context, sess *restore.StorxGrantSession, triggerKey, dataKey, metaKey string) ([]byte, string, error) {
	return downloadPhotosIDBasedRestorePayload(ctx, sess, triggerKey, dataKey, metaKey)
}

func resolvePhotosIDBasedRestoreGrant(ctx context.Context, accessGrant, triggerKey, dataKey, metaKey string) (filename, mimeType, resolvedDataKey string, err error) {
	dataKey = strings.TrimSpace(dataKey)
	metaKey = strings.TrimSpace(metaKey)
	if metaKey != "" {
		metaBytes, metaErr := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Photos, metaKey)
		if metaErr == nil {
			var meta photosRestoreMeta
			if json.Unmarshal(metaBytes, &meta) == nil {
				filename = strings.TrimSpace(meta.Filename)
				mimeType = strings.TrimSpace(meta.MimeType)
				if dataKey == "" {
					dataKey = strings.TrimSpace(meta.DataObjectKey)
				}
			}
		}
	}
	if dataKey == "" {
		return "", "", "", fmt.Errorf("missing data object key for %s", triggerKey)
	}
	return filename, mimeType, dataKey, nil
}

func downloadPhotosIDBasedRestorePayloadWithGrant(ctx context.Context, accessGrant, triggerKey, dataKey, metaKey string) ([]byte, string, error) {
	dataKey = strings.TrimSpace(dataKey)
	metaKey = strings.TrimSpace(metaKey)
	filename := ""
	if metaKey != "" {
		metaBytes, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Photos, metaKey)
		if err == nil {
			var meta photosRestoreMeta
			if json.Unmarshal(metaBytes, &meta) == nil {
				filename = strings.TrimSpace(meta.Filename)
				if dataKey == "" {
					dataKey = strings.TrimSpace(meta.DataObjectKey)
				}
			}
		}
	}
	if dataKey == "" {
		return nil, "", fmt.Errorf("missing data object key for %s", triggerKey)
	}
	body, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Photos, dataKey)
	if err != nil {
		return nil, "", err
	}
	return body, filename, nil
}

func downloadPhotosIDBasedRestorePayload(ctx context.Context, sess *restore.StorxGrantSession, triggerKey, dataKey, metaKey string) ([]byte, string, error) {
	dataKey = strings.TrimSpace(dataKey)
	metaKey = strings.TrimSpace(metaKey)
	filename := ""
	if metaKey != "" {
		metaBytes, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Photos, metaKey)
		if err == nil {
			var meta photosRestoreMeta
			if json.Unmarshal(metaBytes, &meta) == nil {
				filename = strings.TrimSpace(meta.Filename)
				if dataKey == "" {
					dataKey = strings.TrimSpace(meta.DataObjectKey)
				}
			}
		}
	}
	if dataKey == "" {
		return nil, "", fmt.Errorf("missing data object key for %s", triggerKey)
	}
	body, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Photos, dataKey)
	if err != nil {
		return nil, "", err
	}
	return body, filename, nil
}
