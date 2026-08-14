package restore

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/drive/v3"
)

// restoreStreamThresholdBytes matches handler.AutosyncStreamThresholdBytes (10 MB).
const restoreStreamThresholdBytes = 10 * 1024 * 1024

// restoreDownloadHints carries optional size/mime for restore-all streaming decisions.
type restoreDownloadHints struct {
	size     int64 // negative when unknown
	mimeType string
}

// shouldStreamRestoreDownload mirrors handler.ShouldUseStreamingUpload for StorX → Google restore-all.
func shouldStreamRestoreDownload(h restoreDownloadHints) bool {
	mime := strings.TrimSpace(h.mimeType)
	if strings.HasPrefix(mime, "application/vnd.google-apps") {
		return true
	}
	if h.size > restoreStreamThresholdBytes {
		return true
	}
	if h.size <= 0 && strings.HasPrefix(strings.ToLower(mime), "video/") {
		return true
	}
	return false
}

func downloadBytesRestoreAll(ctx context.Context, grant, bucket, key string, h restoreDownloadHints) ([]byte, error) {
	if shouldStreamRestoreDownload(h) {
		var buf bytes.Buffer
		if err := satellite.DownloadObjectTo(ctx, grant, bucket, key, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return satellite.DownloadObject(ctx, grant, bucket, key)
}

func downloadToFileRestoreAll(ctx context.Context, grant, bucket, key, destPath string, h restoreDownloadHints) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	if shouldStreamRestoreDownload(h) {
		f, err := os.Create(destPath)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := satellite.DownloadObjectTo(ctx, grant, bucket, key, f); err != nil {
			return err
		}
		return f.Close()
	}
	data, err := satellite.DownloadObject(ctx, grant, bucket, key)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o644)
}

func streamFromStorxRestoreAll(ctx context.Context, grant, bucket, key string) (io.Reader, <-chan error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := satellite.DownloadObjectTo(ctx, grant, bucket, key, pw)
		_ = pw.CloseWithError(err)
		errCh <- err
	}()
	return pr, errCh
}

func mimeFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".mkv":
		return "video/x-matroska"
	default:
		return ""
	}
}

func awaitStorxStream(errCh <-chan error, restoreErr error) error {
	dlErr := <-errCh
	if restoreErr != nil {
		return restoreErr
	}
	return dlErr
}

// streamToFileRestoreAll writes a StorX object to disk via io.Copy (restore-all photos; avoids RAM).
func streamToFileRestoreAll(ctx context.Context, grant, bucket, key, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := satellite.DownloadObjectTo(ctx, grant, bucket, key, f); err != nil {
		return err
	}
	return f.Close()
}

// restoreDriveDataFromStorxStream pipes StorX file bytes into Google Drive (restore-all; no full RAM buffer).
func restoreDriveDataFromStorxStream(
	ctx context.Context,
	accessGrant string,
	srv *drive.Service,
	userEmail, dataKey string,
	metadataJSON []byte,
) error {
	content, errCh := streamFromStorxRestoreAll(ctx, accessGrant, satellite.ReserveBucket_Drive, dataKey)
	if pr, ok := content.(*io.PipeReader); ok {
		defer pr.Close()
	}
	restoreErr := RetryGoogle(ctx, func() error {
		return google.RestoreFromBackupReader(ctx, srv, userEmail, metadataJSON, content)
	})
	return awaitStorxStream(errCh, restoreErr)
}
