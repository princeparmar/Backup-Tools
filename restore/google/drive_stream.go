package googlestore

import (
	"context"
	"io"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/drive/v3"
)

// restoreDriveDataFromStorxStream pipes StorX file bytes into Google Drive (restore-all; no full RAM buffer).
func RestoreDriveDataFromStorxStream(
	ctx context.Context,
	accessGrant string,
	srv *drive.Service,
	userEmail, dataKey string,
	metadataJSON []byte,
) error {
	content, errCh := restore.StreamFromStorx(ctx, accessGrant, satellite.ReserveBucket_Drive, dataKey)
	if pr, ok := content.(*io.PipeReader); ok {
		defer pr.Close()
	}
	restoreErr := restore.RetryGoogle(ctx, func() error {
		return google.RestoreFromBackupReader(ctx, srv, userEmail, metadataJSON, content)
	})
	return restore.AwaitStorxStream(errCh, restoreErr)
}
