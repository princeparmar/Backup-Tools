package crons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

type gmailProcessor struct{}

func NewGmailProcessor() *gmailProcessor {
	return &gmailProcessor{}
}

// gmailMailboxPathAndOAuthHolder derives mailbox session user, StorX path prefix, and OAuth holder email for NewWorkspaceGmailSession.
func gmailMailboxPathAndOAuthHolder(job *repo.CronJobListingDB, hasRefreshToken bool) (mailboxForSession, storxPathPrefix, oauthAccountEmail string) {
	if job == nil {
		return "me", "", ""
	}
	mailbox := "me"
	path := job.Name
	if job.InputData != nil && job.InputData.Json() != nil {
		if email, ok := (*job.InputData.Json())["email"].(string); ok && email != "" {
			mailbox = email
			path = email
		}
	}
	mailboxForSession = strings.TrimSpace(mailbox)
	if strings.EqualFold(mailboxForSession, "me") && strings.Contains(path, "@") {
		mailboxForSession = strings.TrimSpace(path)
	}
	// DISABLED(parent_id): repo.GmailConnectedAccountEmail(job) — use ResolvedOAuthHolderEmail in Run().
	_ = hasRefreshToken
	if oauthAccountEmail == "" {
		if mailboxForSession != "" && !strings.EqualFold(mailboxForSession, "me") {
			oauthAccountEmail = mailboxForSession
		} else if strings.Contains(path, "@") {
			oauthAccountEmail = strings.TrimSpace(path)
		}
	}
	return mailboxForSession, path, oauthAccountEmail
}

func (g *gmailProcessor) Run(input ProcessorInput) error {

	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	storxToken := input.Database.CronJobRepo.ResolvedStorxToken(input.Job)
	if strings.TrimSpace(storxToken) == "" {
		return fmt.Errorf("storx access grant not found for this job (set storx_token on the shared credential or admin mailbox job)")
	}

	// Process webhook events using the same resolved access grant (non-blocking).
	go func(st string) {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, st, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync",
				logger.ErrorField(processErr))
		}
	}(storxToken)

	refreshToken := input.Database.CronJobRepo.GmailResolvedRefreshToken(input.Job)
	mailboxForSession, pathPrefix, _ := gmailMailboxPathAndOAuthHolder(input.Job, refreshToken != "")
	oauthAccountEmail := input.Database.CronJobRepo.ResolvedOAuthHolderEmail(input.Job)

	// JWT and requires DWD; without DWD you get unauthorized_client despite a valid admin refresh token.
	delegationOnly := refreshToken == "" && google.GmailJobUsesDelegationWithoutOAuth(mailboxForSession, oauthAccountEmail)
	var newToken string
	if !delegationOnly {
		if refreshToken == "" {
			return fmt.Errorf("refresh token not found in job input_data (required when not using domain-wide delegation only)")
		}
		var tokErr error
		newToken, tokErr = google.AuthTokenUsingRefreshToken(refreshToken)
		if tokErr != nil {
			return fmt.Errorf("error while generating auth token: %w", tokErr)
		}
		if strings.TrimSpace(newToken) == "" {
			return fmt.Errorf("error while generating auth token: empty access token after refresh (check refresh token and OAuth client)")
		}
	}

	gmailSession, err := google.NewWorkspaceGmailSession(ctx, newToken, oauthAccountEmail, mailboxForSession)
	if err != nil {
		return err
	}
	gmailClient := gmailSession.Client
	gmailAPIUser := gmailSession.APIUser

	if gmailClient != nil && gmailClient.Service != nil {
		if err := estimateAndEnforceGmail(ctx, input, gmailClient.Service, gmailAPIUser); err != nil {
			return err
		}
	}

	err = handler.UploadObjectAndSync(context.Background(), input.Database, storxToken, satellite.ReserveBucket_Gmail, pathPrefix+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery)
	if err != nil {
		return mapUploadErr("gmail", err)
	}

	// Get synced objects from database instead of listing from Satellite (OPTIMIZATION)
	// This is much faster and avoids unnecessary API calls to Satellite
	// Uses common function that ensures bucket exists and queries database
	prefix := pathPrefix + "/"
	emailListFromBucket, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storxToken, satellite.ReserveBucket_Gmail, prefix, input.Job.UserID, "google", "gmail", input.StorxRecovery)
	if err != nil {
		return fmt.Errorf("failed to get synced objects: %w", err)
	}

	err = input.HeartBeatFunc()
	if err != nil {
		return err
	}

	if input.Job.TaskMemory.GmailNextToken == nil {
		input.Job.TaskMemory.GmailNextToken = new(string)
	}

	const gmailAllMailPageSize int64 = 100

	syncOne := func(message *gmail.Message) error {
		return gmailSyncOneMessage(ctx, input, storxToken, pathPrefix, emailListFromBucket, message)
	}

	// All-mail crawl already includes every category. Do NOT re-list CATEGORY_* first —
	// that doubled Gmail API cost (list+get+attachments) and hit per-user query quota.

	for {
		res, err := gmailListMessagesWithBackoff(gmailClient, gmailAPIUser, *input.Job.TaskMemory.GmailNextToken, "", gmailAllMailPageSize)
		if err != nil {
			return err
		}

		for _, message := range res.Messages {
			if err := input.HeartBeatFunc(); err != nil {
				return err
			}
			if err := syncOne(message); err != nil {
				return err
			}
		}

		// Persist next page token only after this page finished successfully.
		*input.Job.TaskMemory.GmailNextToken = res.NextPageToken
		if *input.Job.TaskMemory.GmailNextToken == "" {
			break
		}
	}

	return nil
}

func gmailSyncOneMessage(
	ctx context.Context,
	input ProcessorInput,
	storxToken, pathPrefix string,
	emailListFromBucket map[string]bool,
	message *gmail.Message,
) error {
	if message == nil || strings.TrimSpace(message.Id) == "" {
		return nil
	}

	expectedKey := google.GmailObjectKey(pathPrefix, message)
	existingKey := google.FindExistingGmailKeyByMessageID(emailListFromBucket, pathPrefix, message.Id)

	b, err := json.Marshal(message)
	if err != nil {
		return err
	}

	if existingKey == expectedKey {
		isDraft := false
		for _, id := range message.LabelIds {
			if id == "DRAFT" {
				isDraft = true
				break
			}
		}
		if !isDraft {
			return nil
		}
	}

	if err := handler.UploadBufferedObjectAndSync(context.TODO(), input.Database, storxToken, satellite.ReserveBucket_Gmail, expectedKey, b, input.Job.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("gmail", err)
	}
	emailListFromBucket[expectedKey] = true
	if existingKey != "" && existingKey != expectedKey {
		if delErr := satellite.DeleteObject(context.TODO(), storxToken, satellite.ReserveBucket_Gmail, existingKey); delErr != nil {
			logger.Warn(ctx, "gmail rekey: failed to delete old object",
				logger.String("old_key", existingKey),
				logger.String("new_key", expectedKey),
				logger.ErrorField(delErr),
			)
		} else {
			_ = input.Database.SyncedObjectRepo.DeleteSyncedObject(satellite.ReserveBucket_Gmail, existingKey)
			delete(emailListFromBucket, existingKey)
		}
	}
	input.Job.TaskMemory.GmailSyncCount++
	return nil
}

func gmailListMessagesWithBackoff(client *google.GmailClient, apiUser, pageToken, label string, pageSize int64) (*google.MessagesResponse, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 5; attempt++ {
		res, err := client.GetUserMessagesWithUserID(apiUser, pageToken, label, pageSize, nil)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !gmailListRetryable(err) {
			return nil, err
		}
		time.Sleep(backoff)
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
	return nil, lastErr
}

func gmailListRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if ok := asGoogleAPIError(err, &apiErr); ok && apiErr != nil {
		if apiErr.Code == 429 || apiErr.Code >= 500 {
			return true
		}
		if apiErr.Code == 403 {
			msg := strings.ToLower(apiErr.Message)
			return strings.Contains(msg, "ratelimit") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota")
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "ratelimitexceeded") ||
		strings.Contains(msg, "rate limit")
}

func asGoogleAPIError(err error, out **googleapi.Error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		*out = apiErr
		return true
	}
	if e, ok := err.(*googleapi.Error); ok {
		*out = e
		return true
	}
	return false
}
