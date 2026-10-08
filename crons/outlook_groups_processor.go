package crons

import (
	"context"
	"encoding/json"
	"fmt"
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

	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return err
	}
	accessToken, storx := auth.AccessToken, auth.StorxToken

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
	groupKey, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}

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

	filesCovered := groupFilesCoveredBySharePoint(ctx, input, accessToken, groupID)
	if !filesCovered {
		var driveState repo.GroupsDriveSyncState
		driveState, driveErr = syncGroupDrive(ctx, input, task, accessToken, groupID, groupKey, input.Job.TaskMemory.GroupsSync.Drive)
		if driveErr != nil {
			logger.Warn(ctx, "groups drive sync failed", logger.ErrorField(driveErr))
		} else {
			input.Job.TaskMemory.GroupsSync.Drive = driveState
		}
	}

	if convErr != nil && calErr != nil && (filesCovered || driveErr != nil) {
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

// syncGroupDrive backs up the group's document library in the OneDrive folder layout.
func syncGroupDrive(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, groupID, groupKey string,
	state repo.GroupsDriveSyncState,
) (repo.GroupsDriveSyncState, error) {
	driveRoot := outlook.GroupDriveRootURL(groupID)
	driveID, err := outlook.FetchDriveID(ctx, accessToken, driveRoot)
	if err != nil {
		return state, fmt.Errorf("group drive: %w", err)
	}
	if err := runLibraryDriveSync(ctx, input, libraryDrive{
		storx: task.StorxToken, token: accessToken,
		bucket: satellite.ReserveBucket_OutlookGroups, service: "outlook_groups",
		driveRoot: driveRoot, driveID: driveID, prefix: groupKey,
	}, &state.DeltaLink, &state.Layout); err != nil {
		return state, err
	}
	state.BaselineDone = true
	return state, nil
}

// msGroupRootSiteFn finds a group's SharePoint site (overridden in tests).
var msGroupRootSiteFn = outlook.GroupRootSiteID

// groupFilesCoveredBySharePoint reports whether the group's files are already backed up by an
// active SharePoint job for the group's site; that job owns the files so they are not stored twice.
// Any lookup failure keeps the drive in the groups job.
func groupFilesCoveredBySharePoint(ctx context.Context, input ProcessorInput, accessToken, groupID string) bool {
	siteID, err := msGroupRootSiteFn(ctx, accessToken, groupID)
	if err != nil || siteID == "" {
		return false
	}
	job, err := input.Database.CronJobRepo.FindMicrosoftResourceJob(input.Job.UserID, input.Job.TenantID,
		repo.ResourceTypeSite, siteID, "outlook_sharepoint", input.Job.SyncType)
	return err == nil && job != nil && job.Active
}
