package restore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/people/v1"
)

// RestoreGmailKeyWithSession downloads using DB storx grant (manual restore, single attempt).
func RestoreGmailKeyWithSession(ctx context.Context, sess *StorxGrantSession, client *google.GmailClient, objectKey string) error {
	data, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Gmail, objectKey)
	if err != nil {
		return err
	}
	var msg gmailapi.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	return RetryGoogle(ctx, func() error {
		return client.InsertMessage(&msg)
	})
}

// RestoreGmailKey downloads one message from Satellite and inserts into Gmail.
func RestoreGmailKey(ctx context.Context, accessGrant string, client *google.GmailClient, objectKey string) error {
	data, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Gmail, objectKey)
	if err != nil {
		return err
	}
	var msg gmailapi.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	return RetryGoogle(ctx, func() error {
		return client.InsertMessage(&msg)
	})
}

// RestoreDriveKeyWithSession restores one Drive object using DB storx grant (manual restore).
func RestoreDriveKeyWithSession(ctx context.Context, sess *StorxGrantSession, srv *drive.Service, userEmail, objectKey string) error {
	return restoreDriveObjectKey(ctx, func(bucket, key string) ([]byte, error) {
		return sess.DownloadObject(ctx, bucket, key)
	}, srv, userEmail, objectKey)
}

// RestoreDriveKey restores one Drive backup object.
func RestoreDriveKey(ctx context.Context, accessGrant string, srv *drive.Service, userEmail, objectKey string) error {
	return restoreDriveObjectKey(ctx, func(bucket, key string) ([]byte, error) {
		return satellite.DownloadObject(ctx, accessGrant, bucket, key)
	}, srv, userEmail, objectKey)
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
		return RetryGoogle(ctx, func() error {
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
	return RetryGoogle(ctx, func() error {
		return google.RestoreFromBackup(ctx, srv, userEmail, metadataJSON, backupItem.Content)
	})
}

// RestoreCalendarKeyWithSession restores one calendar event using DB storx grant (manual restore).
func RestoreCalendarKeyWithSession(ctx context.Context, sess *StorxGrantSession, service *calendar.Service, objectKey string) error {
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
	return RetryGoogle(ctx, func() error {
		return google.RestoreCalendarEventFromBackup(ctx, service, calendarID, data)
	})
}

// RestoreCalendarKey restores one calendar event JSON blob.
func RestoreCalendarKey(ctx context.Context, accessGrant string, service *calendar.Service, objectKey string) error {
	if !google.IsCalendarEventRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid calendar restore key")
	}
	calendarID, _, ok := google.ParseCalendarEventObjectKey(objectKey)
	if !ok {
		return fmt.Errorf("parse calendar key")
	}
	data, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Calendar, objectKey)
	if err != nil {
		return err
	}
	return RetryGoogle(ctx, func() error {
		return google.RestoreCalendarEventFromBackup(ctx, service, calendarID, data)
	})
}

// RestoreContactKeyWithSession restores one contact using DB storx grant (manual restore).
func RestoreContactKeyWithSession(ctx context.Context, sess *StorxGrantSession, service *people.Service, objectKey string) error {
	if !google.IsContactsRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid contacts restore key")
	}
	data, err := sess.DownloadObject(ctx, satellite.ReserveBucket_Contacts, objectKey)
	if err != nil {
		return err
	}
	return RetryGoogle(ctx, func() error {
		return google.RestoreContactFromBackup(ctx, service, data)
	})
}

// RestoreContactKey restores one contact JSON blob.
func RestoreContactKey(ctx context.Context, accessGrant string, service *people.Service, objectKey string) error {
	if !google.IsContactsRestoreObjectKey(objectKey) {
		return fmt.Errorf("invalid contacts restore key")
	}
	data, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Contacts, objectKey)
	if err != nil {
		return err
	}
	return RetryGoogle(ctx, func() error {
		return google.RestoreContactFromBackup(ctx, service, data)
	})
}

// RestorePhotosKey restores one photo (legacy or ID-based satellite keys).
func RestorePhotosKey(ctx context.Context, deps *RestoreDeps, objectKey string) error {
	if strings.Contains(objectKey, "/.file_placeholder") {
		return fmt.Errorf("skip placeholder")
	}

	var (
		data       []byte
		albumID    string
		albumTitle string
		filename   string
		err        error
	)
	if dataKey, metaKey, idBased := parsePhotosIDBasedRestoreKeys(objectKey); idBased {
		data, filename, err = downloadPhotosIDBasedRestorePayloadWithGrant(ctx, deps.AccessGrant, objectKey, dataKey, metaKey)
		if err != nil {
			return err
		}
	} else {
		data, err = satellite.DownloadObject(ctx, deps.AccessGrant, satellite.ReserveBucket_Photos, objectKey)
		if err != nil {
			return err
		}
		albumID, albumTitle, filename = parseGooglePhotosKey(objectKey)
	}

	if strings.TrimSpace(filename) == "" {
		filename = "restored_photo"
	}

	tempDir := filepath.Join("./cache", "restore", fmt.Sprintf("job-%d", deps.Job.ID))
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return err
	}
	tempPath := filepath.Join(tempDir, filepath.Base(filename))
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}
	defer os.Remove(tempPath)

	targetAlbumID := ""
	if albumTitle != "" {
		alb, err := deps.getOrCreateAlbum(ctx, albumID, albumTitle)
		if err != nil {
			return err
		}
		if alb != nil {
			targetAlbumID = alb.ID
		}
	}

	return RetryGoogle(ctx, func() error {
		_, err := deps.PhotosClient.UploadFileToAlbum(ctx, targetAlbumID, tempPath)
		return err
	})
}

func (d *RestoreDeps) getOrCreateAlbum(ctx context.Context, albumID, albumTitle string) (*albums.Album, error) {
	cacheKey := albumID
	if cacheKey == "" {
		cacheKey = "title:" + albumTitle
	}
	d.photosAlbumMu.Lock()
	if alb, ok := d.PhotosAlbumCache[cacheKey]; ok {
		d.photosAlbumMu.Unlock()
		return alb, nil
	}
	d.photosAlbumMu.Unlock()

	var alb *albums.Album
	var err error
	if albumID != "" {
		alb, _ = d.PhotosClient.Albums.GetById(ctx, albumID)
	}
	if alb == nil {
		alb, err = d.PhotosClient.Albums.Create(ctx, albumTitle)
		if err != nil {
			return nil, err
		}
	}

	d.photosAlbumMu.Lock()
	d.PhotosAlbumCache[cacheKey] = alb
	d.photosAlbumMu.Unlock()
	return alb, nil
}

// photos parse helpers (same semantics as handler/google_photos_handlers.go)

type photosRestoreMeta struct {
	MediaItemID   string `json:"media_item_id"`
	Filename      string `json:"filename"`
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
func DownloadPhotosIDBasedPayloadWithSession(ctx context.Context, sess *StorxGrantSession, triggerKey, dataKey, metaKey string) ([]byte, string, error) {
	return downloadPhotosIDBasedRestorePayload(ctx, sess, triggerKey, dataKey, metaKey)
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

func downloadPhotosIDBasedRestorePayload(ctx context.Context, sess *StorxGrantSession, triggerKey, dataKey, metaKey string) ([]byte, string, error) {
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
