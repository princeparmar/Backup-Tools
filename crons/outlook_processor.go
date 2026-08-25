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

type outlookProcessor struct{}

func NewOutlookProcessor() *outlookProcessor {
	return &outlookProcessor{}
}

// outlookAutosyncPreflight resolves StorX + Microsoft access token from credential_id (with legacy fallback).
func outlookAutosyncPreflight(input ProcessorInput) (accessToken, storx string, err error) {
	storx = input.Database.CronJobRepo.ResolvedStorxToken(input.Job)
	if storx == "" {
		return "", "", fmt.Errorf("storx token not found")
	}
	refresh := input.Database.CronJobRepo.ResolvedRefreshToken(input.Job)
	if refresh == "" {
		return "", "", fmt.Errorf("refresh token not found")
	}
	accessToken, err = outlook.AuthTokenUsingRefreshToken(refresh)
	if err != nil {
		return "", "", fmt.Errorf("error while getting token from refresh token: %w", err)
	}
	return accessToken, storx, nil
}

func (p *outlookProcessor) Run(input ProcessorInput) error {
	return runOutlookMailAutosync(input)
}

func jobOutlookMailbox(job *repo.CronJobListingDB) string {
	if job == nil {
		return ""
	}
	mailbox := strings.TrimSpace(job.Name)
	if job.InputData != nil && job.InputData.Json() != nil {
		if email, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(email) != "" {
			mailbox = strings.TrimSpace(email)
		}
	}
	return mailbox
}

func normalizeOutlookMailTaskMemory(tm *repo.TaskMemory) {
	if tm == nil {
		return
	}
	if tm.OutlookMailDeltaLink == nil && tm.ExchangeDeltaLink != nil {
		tm.OutlookMailDeltaLink = tm.ExchangeDeltaLink
	}
	if !tm.OutlookMailBaselineDone && tm.ExchangeBaselineDone {
		tm.OutlookMailBaselineDone = true
	}
	if tm.OutlookMailFolderDeltas == nil && len(tm.ExchangeFolderDeltas) > 0 {
		tm.OutlookMailFolderDeltas = tm.ExchangeFolderDeltas
	}
}

func runOutlookMailAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := outlookAutosyncPreflight(input)
	if err != nil {
		return err
	}
	if appToken, appErr := resolveOutlookMailAccessToken(input); appErr == nil && appToken != "" {
		accessToken = appToken
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from outlook auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return fmt.Errorf("create outlook client: %w", err)
	}

	mailbox := jobOutlookMailbox(input.Job)
	if mailbox == "" {
		user, uerr := client.GetCurrentUser()
		if uerr != nil {
			return fmt.Errorf("resolve mailbox: %w", uerr)
		}
		mailbox = strings.TrimSpace(user.Mail)
	}
	if mailbox == "" {
		return fmt.Errorf("mailbox email is required for outlook backup")
	}

	userBase, err := client.MailUserBaseURL(mailbox)
	if err != nil {
		return err
	}

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	task.LoginId = mailbox
	task.StorxToken = storx

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Outlook, mailbox+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	normalizeOutlookMailTaskMemory(&input.Job.TaskMemory)

	baselineDone := input.Job.TaskMemory.OutlookMailBaselineDone
	deltaLink := ""
	if input.Job.TaskMemory.OutlookMailDeltaLink != nil {
		deltaLink = strings.TrimSpace(*input.Job.TaskMemory.OutlookMailDeltaLink)
	}

	initialURL := outlook.InboxMessagesDeltaInitialURL(userBase)
	if baselineDone && deltaLink != "" {
		newLink, syncErr := runOutlookMailDeltaSync(ctx, input, task, accessToken, userBase, deltaLink)
		if errors.Is(syncErr, outlook.ErrOutlookMailDeltaInvalid) {
			logger.Warn(ctx, "outlook mail delta invalid; resetting to baseline",
				logger.String("mailbox", mailbox),
			)
			input.Job.TaskMemory.OutlookMailDeltaLink = nil
			input.Job.TaskMemory.OutlookMailBaselineDone = false
			newLink, syncErr = runOutlookMailDeltaSync(ctx, input, task, accessToken, userBase, initialURL)
		}
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.OutlookMailDeltaLink = &newLink
		input.Job.TaskMemory.OutlookMailBaselineDone = true
	} else {
		input.Job.TaskMemory.OutlookMailBaselineDone = false
		newLink, syncErr := runOutlookMailDeltaSync(ctx, input, task, accessToken, userBase, initialURL)
		if syncErr != nil {
			return syncErr
		}
		input.Job.TaskMemory.OutlookMailDeltaLink = &newLink
		input.Job.TaskMemory.OutlookMailBaselineDone = true
	}

	if err := syncOutlookMailAdditionalFolders(ctx, input, task, accessToken, userBase); err != nil {
		return err
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func runOutlookMailDeltaSync(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, userBase, startURL string,
) (string, error) {
	requestURL := strings.TrimSpace(startURL)
	if requestURL == "" {
		return "", fmt.Errorf("outlook mail delta start url is empty")
	}

	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		page, err := outlook.FetchOutlookMailMessagesDeltaPage(ctx, accessToken, requestURL)
		if err != nil {
			return "", err
		}
		for i := range page.Messages {
			if err := input.HeartBeatFunc(); err != nil {
				return "", err
			}
			if err := syncOutlookMailMessage(ctx, input, task, accessToken, userBase, &page.Messages[i]); err != nil {
				logger.Warn(ctx, "outlook mail message sync failed",
					logger.String("message_id", page.Messages[i].ID),
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
			return "", fmt.Errorf("outlook mail delta finished without @odata.deltaLink")
		}
		return final, nil
	}
}

func syncOutlookMailMessage(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, userBase string,
	msg *outlook.OutlookMailDeltaMessage,
) error {
	if msg == nil || strings.TrimSpace(msg.ID) == "" {
		return nil
	}
	if msg.IsRemoved {
		return writeOutlookMailRemovedMetadata(ctx, input, task, msg)
	}

	received := msg.ReceivedDateTime
	if received == "" {
		received = msg.LastModifiedDateTime
	}
	metaKey := outlook.OutlookMailIDBasedMetaKey(task.LoginId, msg.ID, received)
	dataKey := outlook.OutlookMailIDBasedDataKey(task.LoginId, msg.ID, received)

	meta := outlook.OutlookMailCronBackupMeta{
		MessageID:            msg.ID,
		Subject:              msg.Subject,
		From:                 msg.From,
		ReceivedDateTime:     msg.ReceivedDateTime,
		LastModifiedDateTime: msg.LastModifiedDateTime,
		ChangeKey:            msg.ChangeKey,
		HasAttachments:       msg.HasAttachments,
		DataObjectKey:        dataKey,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}

	if skip, prev, _ := shouldSkipOutlookMailContentUpload(ctx, task, meta); skip {
		// Keep prior subject/from when delta stub omits them (avoid wiping browse metadata).
		mergeOutlookMailMetaDisplayFields(&meta, prev)
		b, _ := json.Marshal(meta)
		return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Outlook, metaKey, b, task.UserID, input.StorxRecovery)
	}

	raw, detail, err := outlook.FetchOutlookMailMessageRaw(ctx, accessToken, userBase, msg.ID)
	if err != nil {
		return err
	}
	if detail != nil {
		enrichOutlookMailMetaFromDetail(&meta, detail)
		metaKey = outlook.OutlookMailIDBasedMetaKey(task.LoginId, msg.ID, meta.ReceivedDateTime)
		dataKey = outlook.OutlookMailIDBasedDataKey(task.LoginId, msg.ID, meta.ReceivedDateTime)
		meta.DataObjectKey = dataKey
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Outlook, dataKey, raw, task.UserID, input.StorxRecovery); err != nil {
		return err
	}

	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Outlook, metaKey, b, task.UserID, input.StorxRecovery)
}

func shouldSkipOutlookMailContentUpload(ctx context.Context, task *repo.ScheduledTasks, next outlook.OutlookMailCronBackupMeta) (bool, *outlook.OutlookMailCronBackupMeta, error) {
	key := outlook.OutlookMailIDBasedMetaKey(task.LoginId, next.MessageID, next.ReceivedDateTime)
	oldBytes, err := satellite.DownloadObject(ctx, task.StorxToken, satellite.ReserveBucket_Outlook, key)
	if err != nil {
		return false, nil, nil
	}
	var prev outlook.OutlookMailCronBackupMeta
	if err := json.Unmarshal(oldBytes, &prev); err != nil {
		return false, nil, err
	}
	if prev.RemovedFromMailbox || next.RemovedFromMailbox {
		return false, &prev, nil
	}
	if strings.TrimSpace(prev.DataObjectKey) == "" {
		return false, &prev, nil
	}
	skip := strings.TrimSpace(prev.ChangeKey) != "" &&
		strings.TrimSpace(prev.ChangeKey) == strings.TrimSpace(next.ChangeKey) &&
		strings.TrimSpace(prev.LastModifiedDateTime) == strings.TrimSpace(next.LastModifiedDateTime)
	return skip, &prev, nil
}

func enrichOutlookMailMetaFromDetail(meta *outlook.OutlookMailCronBackupMeta, detail *outlook.GraphOutlookMailMessageDetail) {
	if meta == nil || detail == nil {
		return
	}
	if s := strings.TrimSpace(detail.Subject); s != "" {
		meta.Subject = s
	}
	if detail.From != nil && detail.From.EmailAddress != nil {
		if addr := strings.TrimSpace(detail.From.EmailAddress.Address); addr != "" {
			meta.From = addr
		}
	}
	if detail.ReceivedDateTime != "" {
		meta.ReceivedDateTime = detail.ReceivedDateTime
	}
	if detail.LastModifiedDateTime != "" {
		meta.LastModifiedDateTime = detail.LastModifiedDateTime
	}
	if detail.ChangeKey != "" {
		meta.ChangeKey = detail.ChangeKey
	}
	meta.HasAttachments = detail.HasAttachments
}

func mergeOutlookMailMetaDisplayFields(dst *outlook.OutlookMailCronBackupMeta, prev *outlook.OutlookMailCronBackupMeta) {
	if dst == nil || prev == nil {
		return
	}
	if strings.TrimSpace(dst.Subject) == "" {
		dst.Subject = prev.Subject
	}
	if strings.TrimSpace(dst.From) == "" {
		dst.From = prev.From
	}
	if strings.TrimSpace(dst.DataObjectKey) == "" {
		dst.DataObjectKey = prev.DataObjectKey
	}
}

func writeOutlookMailRemovedMetadata(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, msg *outlook.OutlookMailDeltaMessage) error {
	received := msg.ReceivedDateTime
	if received == "" {
		received = msg.LastModifiedDateTime
	}
	metaKey := outlook.OutlookMailIDBasedMetaKey(task.LoginId, msg.ID, received)
	meta := outlook.OutlookMailCronBackupMeta{
		MessageID:           msg.ID,
		Subject:             msg.Subject,
		From:                msg.From,
		ReceivedDateTime:    msg.ReceivedDateTime,
		RemovedFromMailbox:  true,
		DeletedAt:           time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(meta)
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Outlook, metaKey, b, task.UserID, input.StorxRecovery)
}

func resolveOutlookMailAccessToken(input ProcessorInput) (string, error) {
	credID := repo.JobCredentialID(input.Job)
	if credID == 0 {
		return "", nil
	}
	cred, err := input.Database.CredentialRepo.GetByID(credID)
	if err != nil || cred == nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(cred.MicrosoftAuthMode), outlook.MicrosoftAuthModeApplication) {
		return "", nil
	}
	return outlook.AcquireMicrosoftAppOnlyToken(context.Background(), cred.TenantID, cred.MicrosoftAppClientID, cred.MicrosoftAppClientSecret)
}

func syncOutlookMailAdditionalFolders(
	ctx context.Context,
	input ProcessorInput,
	task *repo.ScheduledTasks,
	accessToken, userBase string,
) error {
	if input.Job.TaskMemory.OutlookMailFolderDeltas == nil {
		input.Job.TaskMemory.OutlookMailFolderDeltas = make(map[string]string)
	}
	for _, folderID := range outlook.OutlookMailAdditionalFolderIDs {
		startURL := outlook.MessagesDeltaURL(userBase, folderID)
		if saved := strings.TrimSpace(input.Job.TaskMemory.OutlookMailFolderDeltas[folderID]); saved != "" && input.Job.TaskMemory.OutlookMailBaselineDone {
			startURL = saved
		}
		newLink, err := runOutlookMailDeltaSync(ctx, input, task, accessToken, userBase, startURL)
		if errors.Is(err, outlook.ErrOutlookMailDeltaInvalid) {
			input.Job.TaskMemory.OutlookMailFolderDeltas[folderID] = ""
			newLink, err = runOutlookMailDeltaSync(ctx, input, task, accessToken, userBase, outlook.MessagesDeltaURL(userBase, folderID))
		}
		if err != nil {
			return fmt.Errorf("outlook mail folder %s: %w", folderID, err)
		}
		input.Job.TaskMemory.OutlookMailFolderDeltas[folderID] = newLink
	}
	return nil
}
