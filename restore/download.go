package restore

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/StorX2-0/Backup-Tools/satellite"
)

// StreamThresholdBytes matches handler.AutosyncStreamThresholdBytes (10 MB).
const StreamThresholdBytes = 10 * 1024 * 1024

// DownloadHints carries optional size/mime for restore-all streaming decisions.
type DownloadHints struct {
	Size     int64 // negative when unknown; <=0 means unknown
	MimeType string
}

// shouldStreamRestoreDownload mirrors handler.ShouldUseStreamingUpload for StorX → Google restore-all.
func shouldStreamRestoreDownload(h DownloadHints) bool {
	mime := strings.TrimSpace(h.MimeType)
	if strings.HasPrefix(mime, "application/vnd.google-apps") {
		return true
	}
	if h.Size > StreamThresholdBytes {
		return true
	}
	if h.Size <= 0 && strings.HasPrefix(strings.ToLower(mime), "video/") {
		return true
	}
	return false
}

func DownloadBytes(ctx context.Context, grant, bucket, key string, h DownloadHints) ([]byte, error) {
	if shouldStreamRestoreDownload(h) {
		var buf bytes.Buffer
		if err := satellite.DownloadObjectTo(ctx, grant, bucket, key, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return satellite.DownloadObject(ctx, grant, bucket, key)
}

func DownloadToFile(ctx context.Context, grant, bucket, key, destPath string, h DownloadHints) error {
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

func StreamFromStorx(ctx context.Context, grant, bucket, key string) (io.Reader, <-chan error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := satellite.DownloadObjectTo(ctx, grant, bucket, key, pw)
		_ = pw.CloseWithError(err)
		errCh <- err
	}()
	return pr, errCh
}

func MimeFromFilename(filename string) string {
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

func AwaitStorxStream(errCh <-chan error, restoreErr error) error {
	dlErr := <-errCh
	if restoreErr != nil {
		return restoreErr
	}
	return dlErr
}

// streamToFileRestoreAll writes a StorX object to disk via io.Copy (restore-all photos; avoids RAM).
func StreamToFile(ctx context.Context, grant, bucket, key, destPath string) error {
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
