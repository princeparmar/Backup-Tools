package crons

import (
	"context"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type outlookSharePointProcessor struct{}

func NewOutlookSharePointProcessor() *outlookSharePointProcessor {
	return &outlookSharePointProcessor{}
}

func (p *outlookSharePointProcessor) Run(input ProcessorInput) error {
	return runOutlookSharePointAutosync(input)
}

// runOutlookSharePointAutosync backs up the site's default document library in the OneDrive
// folder layout (MY_DRIVE / BIN) under the site's resource prefix.
func runOutlookSharePointAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return err
	}
	accessToken, storx := auth.AccessToken, auth.StorxToken

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from sharepoint auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	siteID := repo.JobSharePointSiteID(input.Job)
	driveID := repo.JobSharePointDriveID(input.Job)
	if siteID == "" || driveID == "" {
		return fmt.Errorf("site_id and drive_id are required on job for sharepoint backup")
	}
	siteKey, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookSharePoint, siteKey+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("outlook_sharepoint", fmt.Errorf("setup storage placeholder: %w", err))
	}

	tm := &input.Job.TaskMemory
	if err := runLibraryDriveSync(ctx, input, libraryDrive{
		storx: storx, token: accessToken,
		bucket: satellite.ReserveBucket_OutlookSharePoint, service: "outlook_sharepoint",
		driveRoot: outlook.DriveRootURLFromDriveID(driveID), driveID: driveID, prefix: siteKey,
	}, &tm.SharePointDeltaLink, &tm.SharePointLayout); err != nil {
		return err
	}
	tm.SharePointBaselineDone = true

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

// libraryDrive is a SharePoint or group document library backed up in the OneDrive layout.
type libraryDrive struct {
	storx, token    string
	bucket, service string
	driveRoot       string
	driveID         string
	prefix          string
}

// runLibraryDriveSync syncs one library: each file is one object in its folder tree, deleted
// files move to BIN, and the first run rewrites the old meta/data backup in place. Every object
// records the drive id so a restore returns it to the same library.
func runLibraryDriveSync(ctx context.Context, input ProcessorInput, d libraryDrive, deltaLink **string, layout *int) error {
	keys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, d.storx, d.bucket, d.prefix+"/", input.Job.UserID, "outlook", d.service, input.StorxRecovery)
	if err != nil {
		return fmt.Errorf("list backed-up %s files: %w", d.service, err)
	}
	store := &satelliteOneDriveStore{
		input: input, storx: d.storx, bucket: d.bucket,
		extraMeta: map[string]string{outlook.OneDriveMetaDriveID: d.driveID},
	}
	run := newOneDriveRun(input, store, d.token, d.driveRoot, d.prefix, keys)
	run.service = d.service
	run.quota = newDriveQuotaSession(ctx, input)
	if err := run.syncDrive(ctx, deltaLink, layout); err != nil {
		return err
	}
	if q := run.quota; q != nil && q.skippedQuota > 0 {
		ApplyDriveStorageSkipMessage(input, q.synced, q.skippedQuota, q.skippedBytes, q.remaining)
	}
	return nil
}
