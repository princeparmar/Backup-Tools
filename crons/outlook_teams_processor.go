package crons

import (
	"context"
	"encoding/json"
	"errors"
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

type outlookTeamsProcessor struct{}

func NewOutlookTeamsProcessor() *outlookTeamsProcessor {
	return &outlookTeamsProcessor{}
}

func (p *outlookTeamsProcessor) Run(input ProcessorInput) error {
	return runOutlookTeamsAutosync(input)
}

func runOutlookTeamsAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := outlookAutosyncPreflight(input)
	if err != nil {
		return err
	}
	delegatedToken := accessToken

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from teams auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	teamID := repo.JobTeamsTeamID(input.Job)
	if teamID == "" {
		return fmt.Errorf("team_id is required on job for teams backup")
	}
	teamKey := outlook.SanitizeTeamsTeamKey(teamID)

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	task.LoginId = teamKey
	task.StorxToken = storx

	if appToken, appErr := resolveOutlookMailAccessToken(input); appErr == nil && appToken != "" {
		if exportErr := runTeamsAppOnlyExport(ctx, input, task, appToken, teamID, teamKey); exportErr != nil {
			logger.Warn(ctx, "teams app-only export failed", logger.ErrorField(exportErr))
		}
	}
	accessToken = delegatedToken

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookTeams, teamKey+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	if resolved, rerr := outlook.ResolveTeam(ctx, accessToken, teamID, nil); rerr == nil {
		channels, _ := outlook.ListTeamChannels(ctx, accessToken, teamID)
		if snap, serr := outlook.TeamsTeamSnapshotJSON(resolved, channels); serr == nil {
			_ = handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookTeams, teamKey+"/_team.json", snap, input.Job.UserID, input.StorxRecovery)
		}
	}

	channelIDs := jobTeamsChannelIDs(input.Job)
	if len(channelIDs) == 0 {
		channels, lerr := outlook.ListTeamChannels(ctx, accessToken, teamID)
		if lerr != nil {
			return lerr
		}
		for _, ch := range channels {
			channelIDs = append(channelIDs, ch.ID)
		}
		chSnap, _ := json.Marshal(channels)
		_ = handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookTeams, teamKey+"/channels/_channels.json", chSnap, input.Job.UserID, input.StorxRecovery)
	}

	if input.Job.TaskMemory.TeamsChannels == nil {
		input.Job.TaskMemory.TeamsChannels = make(map[string]repo.TeamsChannelSyncState)
	}

	for _, channelID := range channelIDs {
		channelID = strings.TrimSpace(channelID)
		if channelID == "" {
			continue
		}
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		state := input.Job.TaskMemory.TeamsChannels[channelID]
		newState, syncErr := syncTeamsChannel(ctx, input, task, accessToken, teamID, teamKey, channelID, state)
		if syncErr != nil {
			logger.Warn(ctx, "teams channel sync failed",
				logger.String("team_id", teamID),
				logger.String("channel_id", channelID),
				logger.ErrorField(syncErr),
			)
			continue
		}
		input.Job.TaskMemory.TeamsChannels[channelID] = newState
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func jobTeamsChannelIDs(job *repo.CronJobListingDB) []string {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return nil
	}
	raw, ok := (*job.InputData.Json())["channel_ids"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

func syncTeamsChannel(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, teamID, teamKey, channelID string,
	state repo.TeamsChannelSyncState,
) (repo.TeamsChannelSyncState, error) {
	now := time.Now().UTC()
	startURL := ""
	if state.BaselineDone && strings.TrimSpace(state.DeltaLink) != "" {
		startURL = strings.TrimSpace(state.DeltaLink)
	} else if strings.TrimSpace(state.NextLink) != "" && !state.BaselineDone {
		startURL = strings.TrimSpace(state.NextLink)
	} else if state.BaselineDone {
		startURL = outlook.TeamsChannelMessagesInitialURL(teamID, channelID)
	} else {
		startURL = outlook.TeamsChannelMessagesDeltaInitialURL(teamID, channelID)
	}

	requestURL := startURL
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return state, err
		}
		page, err := outlook.FetchTeamsChannelMessagesPage(ctx, accessToken, requestURL)
		if errors.Is(err, outlook.ErrTeamsDeltaInvalid) {
			state.DeltaLink = ""
			state.NextLink = ""
			state.BaselineDone = false
			requestURL = outlook.TeamsChannelMessagesInitialURL(teamID, channelID)
			continue
		}
		if err != nil {
			if !state.BaselineDone && strings.Contains(err.Error(), "404") {
				requestURL = outlook.TeamsChannelMessagesInitialURL(teamID, channelID)
				continue
			}
			return state, err
		}
		for i := range page.Messages {
			if err := input.HeartBeatFunc(); err != nil {
				return state, err
			}
			if err := syncTeamsMessage(ctx, input, task, accessToken, teamID, teamKey, channelID, &page.Messages[i]); err != nil {
				logger.Warn(ctx, "teams message sync failed",
					logger.String("message_id", page.Messages[i].ID),
					logger.ErrorField(err),
				)
			}
		}
		if strings.TrimSpace(page.DeltaLink) != "" {
			state.DeltaLink = strings.TrimSpace(page.DeltaLink)
			state.NextLink = ""
			state.BaselineDone = true
			state.LastSyncAt = &now
			return state, nil
		}
		if strings.TrimSpace(page.NextLink) != "" {
			requestURL = strings.TrimSpace(page.NextLink)
			state.NextLink = requestURL
			continue
		}
		state.BaselineDone = true
		state.NextLink = ""
		state.LastSyncAt = &now
		return state, nil
	}
}

func syncTeamsMessage(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, teamID, teamKey, channelID string,
	msg *outlook.TeamsDeltaMessage,
) error {
	if msg == nil || strings.TrimSpace(msg.ID) == "" {
		return nil
	}
	if msg.IsRemoved {
		return writeTeamsRemovedMetadata(ctx, input, task, teamID, teamKey, channelID, msg)
	}

	created := msg.CreatedDateTime
	if created == "" {
		created = msg.LastModifiedDateTime
	}
	metaKey := outlook.TeamsIDBasedMetaKey(teamKey, channelID, msg.ID, created)
	dataKey := outlook.TeamsIDBasedDataKey(teamKey, channelID, msg.ID, created)

	meta := outlook.TeamsCronBackupMeta{
		MessageID:            msg.ID,
		TeamID:               teamID,
		ChannelID:            channelID,
		Subject:              msg.Subject,
		From:                 msg.From,
		CreatedDateTime:      msg.CreatedDateTime,
		LastModifiedDateTime: msg.LastModifiedDateTime,
		ChangeKey:            msg.ChangeKey,
		HasAttachments:       msg.HasAttachments,
		DataObjectKey:        dataKey,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}

	if skip, _ := shouldSkipTeamsContentUpload(ctx, task, teamKey, channelID, meta); skip {
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookTeams, metaKey, b, task.UserID, input.StorxRecovery)
	}

	raw, err := outlook.FetchTeamsMessageRaw(ctx, accessToken, teamID, channelID, msg.ID)
	if err != nil {
		return err
	}
	if err := handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookTeams, dataKey, raw, task.UserID, input.StorxRecovery); err != nil {
		return err
	}

	replies, rerr := outlook.ListTeamsMessageReplies(ctx, accessToken, teamID, channelID, msg.ID)
	if rerr == nil {
		for _, reply := range replies {
			_ = syncTeamsMessage(ctx, input, task, accessToken, teamID, teamKey, channelID, &reply)
		}
	}

	if msg.HasAttachments {
		hostedIDs, herr := outlook.ListTeamsMessageHostedContentIDs(ctx, accessToken, teamID, channelID, msg.ID)
		if herr == nil {
			for _, contentID := range hostedIDs {
				bytes, berr := outlook.FetchTeamsHostedContentBytes(ctx, accessToken, teamID, channelID, msg.ID, contentID)
				if berr != nil {
					continue
				}
				key := outlook.TeamsHostedContentKey(teamKey, channelID, msg.ID, contentID)
				_ = handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookTeams, key, bytes, task.UserID, input.StorxRecovery)
			}
		}
	}

	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookTeams, metaKey, b, task.UserID, input.StorxRecovery)
}

func shouldSkipTeamsContentUpload(ctx context.Context, task *repo.ScheduledTasks, teamKey, channelID string, next outlook.TeamsCronBackupMeta) (bool, error) {
	key := outlook.TeamsIDBasedMetaKey(teamKey, channelID, next.MessageID, next.CreatedDateTime)
	oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_OutlookTeams, key)
	if err != nil {
		return false, nil
	}
	var prev outlook.TeamsCronBackupMeta
	if err := json.Unmarshal(oldBytes, &prev); err != nil {
		return false, err
	}
	if prev.RemovedFromTeams || next.RemovedFromTeams {
		return false, nil
	}
	if strings.TrimSpace(prev.DataObjectKey) == "" {
		return false, nil
	}
	return strings.TrimSpace(prev.ChangeKey) != "" &&
		strings.TrimSpace(prev.ChangeKey) == strings.TrimSpace(next.ChangeKey) &&
		strings.TrimSpace(prev.LastModifiedDateTime) == strings.TrimSpace(next.LastModifiedDateTime), nil
}

func writeTeamsRemovedMetadata(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, teamID, teamKey, channelID string, msg *outlook.TeamsDeltaMessage) error {
	created := msg.CreatedDateTime
	if created == "" {
		created = msg.LastModifiedDateTime
	}
	metaKey := outlook.TeamsIDBasedMetaKey(teamKey, channelID, msg.ID, created)
	meta := outlook.TeamsCronBackupMeta{
		MessageID:        msg.ID,
		TeamID:           teamID,
		ChannelID:        channelID,
		Subject:          msg.Subject,
		From:             msg.From,
		CreatedDateTime:  msg.CreatedDateTime,
		RemovedFromTeams: true,
		DeletedAt:        time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_OutlookTeams, metaKey, b, task.UserID, input.StorxRecovery)
}

// runTeamsAppOnlyExport uses getAllMessages when application token is available (Phase 4).
func runTeamsAppOnlyExport(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, accessToken, teamID, teamKey string) error {
	url := outlook.TeamsGetAllMessagesURL(teamID)
	for url != "" {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := outlook.FetchTeamsChannelMessagesPage(ctx, accessToken, url)
		if err != nil {
			return err
		}
		for i := range page.Messages {
			channelID := ""
			if input.Job.InputData != nil && input.Job.InputData.Json() != nil {
				if v, ok := (*input.Job.InputData.Json())["default_channel_id"].(string); ok {
					channelID = v
				}
			}
			if channelID == "" {
				channelID = "unknown"
			}
			_ = syncTeamsMessage(ctx, input, task, accessToken, teamID, teamKey, channelID, &page.Messages[i])
		}
		url = strings.TrimSpace(page.NextLink)
	}
	return nil
}
