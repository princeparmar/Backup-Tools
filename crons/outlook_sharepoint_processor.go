package crons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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

func runOutlookSharePointAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := outlookAutosyncPreflight(input)
	if err != nil {
		return err
	}

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

	siteKey := outlook.SanitizeSharePointSiteKey(siteID)
	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	task.LoginId = siteKey
	task.StorxToken = storx

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookSharePoint, siteKey+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	baselineDone := input.Job.TaskMemory.SharePointBaselineDone
	deltaLink := ""
	if input.Job.TaskMemory.SharePointDeltaLink != nil {
		deltaLink = strings.TrimSpace(*input.Job.TaskMemory.SharePointDeltaLink)
	}

	if baselineDone && deltaLink != "" {
		newLink, syncErr := runSharePointDeltaSync(ctx, input, task, accessToken, driveID, siteID, deltaLink)
		if errors.Is(syncErr, outlook.ErrSharePointDeltaInvalid) {
			logger.Warn(ctx, "sharepoint delta invalid; resetting to baseline", logger.String("site_id", siteID))
			input.Job.TaskMemory.SharePointDeltaLink = nil
			input.Job.TaskMemory.SharePointBaselineDone = false
			newLink, syncErr = runSharePointDeltaSync(ctx, input, task, accessToken, driveID, siteID, outlook.SharePointInitialDeltaURL(driveID))
		}
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.SharePointDeltaLink = &newLink
		input.Job.TaskMemory.SharePointBaselineDone = true
	} else {
		input.Job.TaskMemory.SharePointBaselineDone = false
		newLink, syncErr := runSharePointDeltaSync(ctx, input, task, accessToken, driveID, siteID, outlook.SharePointInitialDeltaURL(driveID))
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.SharePointDeltaLink = &newLink
		input.Job.TaskMemory.SharePointBaselineDone = true
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func runSharePointDeltaSync(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, driveID, siteID, startURL string,
) (string, error) {
	requestURL := strings.TrimSpace(startURL)
	if requestURL == "" {
		return "", fmt.Errorf("sharepoint delta start url is empty")
	}
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		page, err := outlook.FetchSharePointDeltaPage(ctx, accessToken, requestURL)
		if err != nil {
			return "", err
		}
		for i := range page.Items {
			if err := input.HeartBeatFunc(); err != nil {
				return "", err
			}
			if err := syncSharePointItem(ctx, input, task, accessToken, driveID, siteID, &page.Items[i]); err != nil {
				logger.Warn(ctx, "sharepoint item sync failed",
					logger.String("site_id", siteID),
					logger.String("item_id", page.Items[i].ID),
					logger.ErrorField(err),
				)
			}
		}
		if strings.TrimSpace(page.NextLink) != "" {
			requestURL = strings.TrimSpace(page.NextLink)
			continue
		}
		final := strings.TrimSpace(page.DeltaLink)
		if final == "" {
			return "", fmt.Errorf("sharepoint delta finished without @odata.deltaLink")
		}
		return final, nil
	}
}

func syncSharePointItem(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, driveID, siteID string,
	item *outlook.OneDriveItem,
) error {
	if item == nil || strings.TrimSpace(item.ID) == "" {
		return nil
	}
	if item.IsDeleted {
		return writeSharePointRemovedMetadata(ctx, input, task, driveID, siteID, item)
	}
	if item.IsFolder {
		return nil
	}
	if item.MimeType == "" && item.Size == 0 && item.Name == "" {
		return nil
	}

	displayName := outlook.SanitizeOneDrivePathSegment(item.Name)
	created := item.CreatedDateTime
	metaKey := outlook.SharePointIDBasedMetaKey(siteID, item.ID, displayName, created)
	dataKey := outlook.SharePointIDBasedDataKey(siteID, item.ID, displayName, created)

	meta := outlook.SharePointCronBackupMeta{
		ItemID:               item.ID,
		Name:                 item.Name,
		MimeType:             item.MimeType,
		ParentID:             item.ParentID,
		ParentPath:           item.ParentPath,
		WebURL:               item.WebURL,
		Size:                 item.Size,
		CreatedDateTime:      item.CreatedDateTime,
		LastModifiedDateTime: item.LastModifiedDateTime,
		ETag:                 item.ETag,
		CTag:                 item.CTag,
		SiteID:               siteID,
		DriveID:              driveID,
		IsFolder:             false,
		DataObjectKey:        dataKey,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}

	if skip, _ := shouldSkipSharePointContentUpload(ctx, task, siteID, meta); skip {
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookSharePoint, metaKey, b, task.UserID, input.StorxRecovery)
	}

	if handler.ShouldUseStreamingUpload(item.Size, item.MimeType) {
		body, _, err := outlook.OpenSharePointItemContentStream(ctx, accessToken, driveID, item.ID)
		if err != nil {
			return err
		}
		defer body.Close()
		if err := handler.UploadObjectStreamAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookSharePoint, dataKey, body, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
	} else {
		body, _, err := outlook.OpenSharePointItemContentStream(ctx, accessToken, driveID, item.ID)
		if err != nil {
			return err
		}
		content, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			return err
		}
		if err := handler.UploadBufferedObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookSharePoint, dataKey, content, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
	}

	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookSharePoint, metaKey, b, task.UserID, input.StorxRecovery)
}

func shouldSkipSharePointContentUpload(ctx context.Context, task *repo.ScheduledTasks, siteID string, next outlook.SharePointCronBackupMeta) (bool, error) {
	key := outlook.SharePointIDBasedMetaKey(siteID, next.ItemID, outlook.SanitizeOneDrivePathSegment(next.Name), next.CreatedDateTime)
	oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_OutlookSharePoint, key)
	if err != nil {
		return false, nil
	}
	var prev outlook.SharePointCronBackupMeta
	if err := json.Unmarshal(oldBytes, &prev); err != nil {
		return false, err
	}
	if prev.RemovedFromSharePoint || next.RemovedFromSharePoint {
		return false, nil
	}
	if strings.TrimSpace(prev.DataObjectKey) == "" {
		return false, nil
	}
	return strings.TrimSpace(prev.ETag) != "" &&
		strings.TrimSpace(prev.ETag) == strings.TrimSpace(next.ETag) &&
		strings.TrimSpace(prev.LastModifiedDateTime) == strings.TrimSpace(next.LastModifiedDateTime) &&
		prev.Size == next.Size, nil
}

func writeSharePointRemovedMetadata(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, driveID, siteID string, item *outlook.OneDriveItem) error {
	displayName := outlook.SanitizeOneDrivePathSegment(item.Name)
	if displayName == "" || displayName == "untitled" {
		displayName = item.ID
	}
	created := item.CreatedDateTime
	metaKey := outlook.SharePointIDBasedMetaKey(siteID, item.ID, displayName, created)
	meta := outlook.SharePointCronBackupMeta{
		ItemID:                item.ID,
		Name:                  item.Name,
		ParentID:              item.ParentID,
		ParentPath:            item.ParentPath,
		SiteID:                siteID,
		DriveID:               driveID,
		RemovedFromSharePoint: true,
		DeletedAt:             time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:             time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookSharePoint, metaKey, b, task.UserID, input.StorxRecovery)
}
