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

// RestoreDriveKey restores one Drive backup object.
func RestoreDriveKey(ctx context.Context, accessGrant string, srv *drive.Service, userEmail, objectKey string) error {
	data, err := satellite.DownloadObject(ctx, accessGrant, satellite.ReserveBucket_Drive, objectKey)
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
		data, filename, err = downloadPhotosIDBasedRestorePayload(ctx, deps.AccessGrant, objectKey, dataKey, metaKey)
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
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	const metaMarker = "/meta/"
	const dataMarker = "/data/"
	if idx := strings.Index(key, metaMarker); idx >= 0 && strings.HasSuffix(key, ".json") {
		return "", key, true
	}
	if idx := strings.Index(key, dataMarker); idx >= 0 {
		segment := strings.TrimSpace(key[idx+len(dataMarker):])
		if segment == "" || strings.Contains(segment, "/") {
			return "", "", false
		}
		id := google.MediaItemIDFromPhotosObjectSegment(segment)
		if id == "" {
			id = segment
		}
		prefix := strings.TrimSpace(key[:idx])
		return key, google.PhotosIDBasedMetaKey(prefix, id), true
	}
	return "", "", false
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

func downloadPhotosIDBasedRestorePayload(ctx context.Context, accessGrant, triggerKey, dataKey, metaKey string) ([]byte, string, error) {
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
