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

type outlookOneDriveProcessor struct{}

func NewOutlookOneDriveProcessor() *outlookOneDriveProcessor {
	return &outlookOneDriveProcessor{}
}

func (p *outlookOneDriveProcessor) Run(input ProcessorInput) error {
	return runOutlookOneDriveAutosync(input)
}

func runOutlookOneDriveAutosync(input ProcessorInput) error {
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
			logger.Warn(processCtx, "Failed to process webhook events from onedrive auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return fmt.Errorf("create outlook client: %w", err)
	}

	mailbox := strings.TrimSpace(input.Job.Name)
	if mailbox == "" {
		user, uerr := client.GetCurrentUser()
		if uerr != nil {
			return fmt.Errorf("resolve mailbox: %w", uerr)
		}
		mailbox = strings.TrimSpace(user.Mail)
	}
	if mailbox == "" {
		return fmt.Errorf("mailbox email is required for onedrive backup")
	}

	driveRoot, err := client.OneDriveDriveRootURL(mailbox)
	if err != nil {
		return err
	}

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	task.LoginId = mailbox
	task.StorxToken = storx

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookOneDrive, mailbox+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	baselineDone := input.Job.TaskMemory.OneDriveBaselineDone
	deltaLink := ""
	if input.Job.TaskMemory.OneDriveDeltaLink != nil {
		deltaLink = strings.TrimSpace(*input.Job.TaskMemory.OneDriveDeltaLink)
	}

	// Incremental only when baseline completed AND we have a persisted deltaLink.
	if baselineDone && deltaLink != "" {
		newLink, syncErr := runOneDriveDeltaSync(ctx, input, task, accessToken, driveRoot, deltaLink)
		if errors.Is(syncErr, outlook.ErrOneDriveDeltaInvalid) {
			logger.Warn(ctx, "onedrive delta invalid; resetting to baseline",
				logger.String("mailbox", mailbox),
			)
			input.Job.TaskMemory.OneDriveDeltaLink = nil
			input.Job.TaskMemory.OneDriveBaselineDone = false
			newLink, syncErr = runOneDriveDeltaSync(ctx, input, task, accessToken, driveRoot, outlook.OneDriveInitialDeltaURL(driveRoot))
		}
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.OneDriveDeltaLink = &newLink
		input.Job.TaskMemory.OneDriveBaselineDone = true
	} else {
		// Ensure flag stays false until final deltaLink is persisted.
		input.Job.TaskMemory.OneDriveBaselineDone = false
		newLink, syncErr := runOneDriveDeltaSync(ctx, input, task, accessToken, driveRoot, outlook.OneDriveInitialDeltaURL(driveRoot))
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.OneDriveDeltaLink = &newLink
		input.Job.TaskMemory.OneDriveBaselineDone = true
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

// runOneDriveDeltaSync walks nextLink pages until a final deltaLink is returned.
// Only the final deltaLink is returned; nextLink is never treated as the cursor.
func runOneDriveDeltaSync(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, driveRoot, startURL string,
) (string, error) {
	requestURL := strings.TrimSpace(startURL)
	if requestURL == "" {
		return "", fmt.Errorf("onedrive delta start url is empty")
	}

	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		page, err := outlook.FetchOneDriveDeltaPage(ctx, accessToken, requestURL)
		if err != nil {
			return "", err
		}
		for i := range page.Items {
			if err := input.HeartBeatFunc(); err != nil {
				return "", err
			}
			if err := syncOneDriveItem(ctx, input, task, accessToken, driveRoot, &page.Items[i]); err != nil {
				logger.Warn(ctx, "onedrive item sync failed",
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
			return "", fmt.Errorf("onedrive delta finished without @odata.deltaLink")
		}
		return final, nil
	}
}

func syncOneDriveItem(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, driveRoot string,
	item *outlook.OneDriveItem,
) error {
	if item == nil || strings.TrimSpace(item.ID) == "" {
		return nil
	}
	if item.IsDeleted {
		return writeOneDriveRemovedMetadata(ctx, input, task, item)
	}
	if item.IsFolder {
		// Folders are not uploaded as objects; parent_id is kept on child file meta.
		return nil
	}
	// Skip folder-less stubs without file payload (e.g. root).
	if item.MimeType == "" && item.Size == 0 && item.Name == "" {
		return nil
	}

	displayName := outlook.SanitizeOneDrivePathSegment(item.Name)
	created := item.CreatedDateTime
	metaKey := outlook.OneDriveIDBasedMetaKey(task.LoginId, item.ID, displayName, created)
	dataKey := outlook.OneDriveIDBasedDataKey(task.LoginId, item.ID, displayName, created)

	meta := outlook.OneDriveCronBackupMeta{
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
		IsFolder:             false,
		DataObjectKey:        dataKey,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}

	if skip, _ := shouldSkipOneDriveContentUpload(ctx, task, meta); skip {
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookOneDrive, metaKey, b, task.UserID, input.StorxRecovery)
	}

	if handler.ShouldUseStreamingUpload(item.Size, item.MimeType) {
		body, _, err := outlook.OpenOneDriveItemContentStream(ctx, accessToken, driveRoot, item.ID)
		if err != nil {
			return err
		}
		defer body.Close()
		if err := handler.UploadObjectStreamAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookOneDrive, dataKey, body, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
	} else {
		body, _, err := outlook.OpenOneDriveItemContentStream(ctx, accessToken, driveRoot, item.ID)
		if err != nil {
			return err
		}
		content, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			return err
		}
		if err := handler.UploadBufferedObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookOneDrive, dataKey, content, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
	}

	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookOneDrive, metaKey, b, task.UserID, input.StorxRecovery)
}

func shouldSkipOneDriveContentUpload(ctx context.Context, task *repo.ScheduledTasks, next outlook.OneDriveCronBackupMeta) (bool, error) {
	key := outlook.OneDriveIDBasedMetaKey(task.LoginId, next.ItemID, outlook.SanitizeOneDrivePathSegment(next.Name), next.CreatedDateTime)
	oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_OutlookOneDrive, key)
	if err != nil {
		return false, nil
	}
	var prev outlook.OneDriveCronBackupMeta
	if err := json.Unmarshal(oldBytes, &prev); err != nil {
		return false, err
	}
	if prev.RemovedFromOneDrive || next.RemovedFromOneDrive {
		return false, nil
	}
	if strings.TrimSpace(prev.DataObjectKey) == "" {
		return false, nil
	}
	contentSame := strings.TrimSpace(prev.ETag) != "" &&
		strings.TrimSpace(prev.ETag) == strings.TrimSpace(next.ETag) &&
		strings.TrimSpace(prev.LastModifiedDateTime) == strings.TrimSpace(next.LastModifiedDateTime) &&
		prev.Size == next.Size
	return contentSame, nil
}

func writeOneDriveRemovedMetadata(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item *outlook.OneDriveItem) error {
	displayName := outlook.SanitizeOneDrivePathSegment(item.Name)
	if displayName == "" || displayName == "untitled" {
		displayName = item.ID
	}
	created := item.CreatedDateTime
	metaKey := outlook.OneDriveIDBasedMetaKey(task.LoginId, item.ID, displayName, created)
	meta := outlook.OneDriveCronBackupMeta{
		ItemID:              item.ID,
		Name:                item.Name,
		ParentID:            item.ParentID,
		ParentPath:          item.ParentPath,
		RemovedFromOneDrive: true,
		DeletedAt:           time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookOneDrive, metaKey, b, task.UserID, input.StorxRecovery)
}
