package googlestore

import (
	"context"
	"io"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/drive/v3"
)

// RestoreDriveDataFromStorxStream pipes StorX file bytes into Google Drive (restore-all; no full RAM buffer).
func RestoreDriveDataFromStorxStream(
	ctx context.Context,
	accessGrant string,
	srv *drive.Service,
	userEmail, dataKey string,
	metadataJSON []byte,
) error {
	// Each RetryGoogle attempt needs a fresh pipe: restore may return without reading
	// (file already exists) or fail mid-stream after consuming bytes.
	return restore.RetryGoogle(ctx, func() error {
		content, errCh := restore.StreamFromStorx(ctx, accessGrant, satellite.ReserveBucket_Drive, dataKey)
		pr, isPipe := content.(*io.PipeReader)
		if isPipe {
			defer pr.Close()
		}
		restoreErr := google.RestoreFromBackupReader(ctx, srv, userEmail, metadataJSON, content)
		// Unblock the download goroutine BEFORE waiting on errCh. RestoreFile often
		// returns nil without reading when the owned file already exists in Drive.
		if isPipe {
			_ = pr.Close()
		}
		return restore.AwaitStorxStream(errCh, restoreErr)
	})
}
