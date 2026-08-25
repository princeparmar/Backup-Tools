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

type outlookGroupsProcessor struct{}

func NewOutlookGroupsProcessor() *outlookGroupsProcessor {
	return &outlookGroupsProcessor{}
}

func (p *outlookGroupsProcessor) Run(input ProcessorInput) error {
	return runOutlookGroupsAutosync(input)
}

func runOutlookGroupsAutosync(input ProcessorInput) error {
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
			logger.Warn(processCtx, "Failed to process webhook events from groups auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	groupID := repo.JobGroupsGroupID(input.Job)
	if groupID == "" {
		return fmt.Errorf("group_id is required on job for groups backup")
	}
	groupKey := outlook.SanitizeGroupsGroupKey(groupID)

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	task.LoginId = groupKey
	task.StorxToken = storx

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookGroups, groupKey+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	resolved, rerr := outlook.ResolveGroup(ctx, accessToken, groupID)
	if rerr == nil {
		snap, _ := outlook.GroupsTeamSnapshotJSON(resolved)
		_ = handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookGroups, groupKey+"/_group.json", snap, input.Job.UserID, input.StorxRecovery)
	}

	var convErr, calErr, driveErr error
	convState, convErr := syncGroupConversations(ctx, input, task, accessToken, groupID, groupKey, input.Job.TaskMemory.GroupsSync.Conversations)
	if convErr != nil {
		logger.Warn(ctx, "groups conversations sync failed", logger.ErrorField(convErr))
	} else {
		input.Job.TaskMemory.GroupsSync.Conversations = convState
	}

	calState, calErr := syncGroupCalendar(ctx, input, task, accessToken, groupID, groupKey, input.Job.TaskMemory.GroupsSync.Calendar)
	if calErr != nil {
		logger.Warn(ctx, "groups calendar sync failed", logger.ErrorField(calErr))
	} else {
		input.Job.TaskMemory.GroupsSync.Calendar = calState
	}

	driveState, driveErr := syncGroupDrive(ctx, input, task, accessToken, groupID, groupKey, input.Job.TaskMemory.GroupsSync.Drive)
	if driveErr != nil {
		logger.Warn(ctx, "groups drive sync failed", logger.ErrorField(driveErr))
	} else {
		input.Job.TaskMemory.GroupsSync.Drive = driveState
	}

	if convErr != nil && calErr != nil && driveErr != nil {
		return fmt.Errorf("groups sync failed: conversations=%v calendar=%v drive=%v", convErr, calErr, driveErr)
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func syncGroupConversations(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, groupID, groupKey string,
	state repo.GroupsConversationSyncState,
) (repo.GroupsConversationSyncState, error) {
	now := time.Now().UTC()
	requestURL := strings.TrimSpace(state.NextLink)
	threads, next, err := outlook.FetchGroupConversationsPage(ctx, accessToken, groupID, requestURL)
	if err != nil {
		return state, err
	}
	for _, thread := range threads {
		if err := input.HeartBeatFunc(); err != nil {
			return state, err
		}
		postURL := ""
		for {
			posts, postNext, perr := outlook.FetchGroupThreadPostsPage(ctx, accessToken, groupID, thread.ID, postURL)
			if perr != nil {
				logger.Warn(ctx, "group thread posts failed", logger.String("thread_id", thread.ID), logger.ErrorField(perr))
				break
			}
			for _, post := range posts {
				payload, _ := json.Marshal(map[string]interface{}{
					"group_id":   groupID,
					"thread_id":  thread.ID,
					"topic":      thread.Topic,
					"post_id":    post.ID,
					"body":       post.BodyPreview,
					"received":   post.ReceivedDateTime,
					"modified":   post.LastModifiedDateTime,
					"updated_at": time.Now().UTC().Format(time.RFC3339),
				})
				key := outlook.GroupConversationPostKey(groupKey, thread.ID, post.ID)
				_ = handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookGroups, key, payload, task.UserID, input.StorxRecovery)
			}
			postURL = postNext
			if postURL == "" {
				break
			}
		}
	}
	state.NextLink = next
	if next == "" {
		state.BaselineDone = true
	}
	state.LastSyncAt = &now
	return state, nil
}

func syncGroupCalendar(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, groupID, groupKey string,
	state repo.GroupsCalendarSyncState,
) (repo.GroupsCalendarSyncState, error) {
	requestURL := strings.TrimSpace(state.NextLink)
	events, next, err := outlook.FetchGroupCalendarEventsPage(ctx, accessToken, groupID, requestURL)
	if err != nil {
		return state, err
	}
	for _, ev := range events {
		if err := input.HeartBeatFunc(); err != nil {
			return state, err
		}
		tz := strings.TrimSpace(ev.TimeZone)
		if tz == "" {
			tz = "UTC"
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"group_id": groupID,
			"id":       ev.ID,
			"subject":  ev.Subject,
			"start": map[string]string{
				"dateTime": ev.StartDateTime,
				"timeZone": tz,
			},
			"end": map[string]string{
				"dateTime": ev.EndDateTime,
				"timeZone": tz,
			},
			"lastModifiedDateTime": ev.LastModifiedDateTime,
		})
		key := outlook.GroupCalendarEventKey(groupKey, ev.ID)
		_ = handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookGroups, key, payload, task.UserID, input.StorxRecovery)
	}
	state.NextLink = next
	if next == "" {
		state.BaselineDone = true
	}
	return state, nil
}

func syncGroupDrive(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, groupID, groupKey string,
	state repo.GroupsDriveSyncState,
) (repo.GroupsDriveSyncState, error) {
	deltaLink := ""
	if state.DeltaLink != nil {
		deltaLink = strings.TrimSpace(*state.DeltaLink)
	}
	startURL := outlook.GroupDriveInitialDeltaURL(groupID)
	if state.BaselineDone && deltaLink != "" {
		startURL = deltaLink
	}
	newLink, err := runGroupDriveDeltaSync(ctx, input, task, accessToken, groupID, groupKey, startURL)
	if errors.Is(err, outlook.ErrOneDriveDeltaInvalid) {
		state.DeltaLink = nil
		state.BaselineDone = false
		newLink, err = runGroupDriveDeltaSync(ctx, input, task, accessToken, groupID, groupKey, outlook.GroupDriveInitialDeltaURL(groupID))
	}
	if err != nil {
		return state, err
	}
	state.DeltaLink = &newLink
	state.BaselineDone = true
	return state, nil
}

func runGroupDriveDeltaSync(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, groupID, groupKey, startURL string,
) (string, error) {
	requestURL := strings.TrimSpace(startURL)
	if requestURL == "" {
		return "", fmt.Errorf("group drive delta start url is empty")
	}
	driveRoot := outlook.GroupDriveRootURL(groupID)
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
			if err := syncGroupDriveItem(ctx, input, task, accessToken, driveRoot, groupKey, groupID, &page.Items[i]); err != nil {
				logger.Warn(ctx, "group drive item sync failed",
					logger.String("group_id", groupID),
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
			return "", fmt.Errorf("group drive delta finished without @odata.deltaLink")
		}
		return final, nil
	}
}

func syncGroupDriveItem(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, driveRoot, groupKey, groupID string,
	item *outlook.OneDriveItem,
) error {
	if item == nil || strings.TrimSpace(item.ID) == "" {
		return nil
	}
	if item.IsDeleted {
		displayName := outlook.SanitizeOneDrivePathSegment(item.Name)
		if displayName == "" {
			displayName = item.ID
		}
		metaKey := outlook.SharePointIDBasedMetaKey(groupKey, item.ID, displayName, item.CreatedDateTime)
		meta := outlook.SharePointCronBackupMeta{
			ItemID:                item.ID,
			Name:                  item.Name,
			RemovedFromSharePoint: true,
			DeletedAt:             time.Now().UTC().Format(time.RFC3339),
			UpdatedAt:             time.Now().UTC().Format(time.RFC3339),
		}
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookGroups, metaKey, b, task.UserID, input.StorxRecovery)
	}
	if item.IsFolder || (item.MimeType == "" && item.Size == 0 && item.Name == "") {
		return nil
	}

	displayName := outlook.SanitizeOneDrivePathSegment(item.Name)
	created := item.CreatedDateTime
	metaKey := outlook.SharePointIDBasedMetaKey(groupKey, item.ID, displayName, created)
	dataKey := outlook.SharePointIDBasedDataKey(groupKey, item.ID, displayName, created)

	meta := outlook.SharePointCronBackupMeta{
		ItemID:               item.ID,
		Name:                 item.Name,
		MimeType:             item.MimeType,
		Size:                 item.Size,
		CreatedDateTime:      item.CreatedDateTime,
		LastModifiedDateTime: item.LastModifiedDateTime,
		ETag:                 item.ETag,
		DataObjectKey:        dataKey,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}

	body, _, err := outlook.OpenOneDriveItemContentStream(ctx, accessToken, driveRoot, item.ID)
	if err != nil {
		return err
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return err
	}
	if err := handler.UploadBufferedObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookGroups, dataKey, content, task.UserID, input.StorxRecovery); err != nil {
		return err
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookGroups, metaKey, b, task.UserID, input.StorxRecovery)
}
