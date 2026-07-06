package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
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

	err = handler.UploadObjectAndSync(context.Background(), input.Database, storxToken, satellite.ReserveBucket_Gmail, pathPrefix+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery)
	if err != nil {
		return err
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

	emptyLoopCount := 0

	for {
		res, err := gmailClient.GetUserMessagesWithUserID(gmailAPIUser, *input.Job.TaskMemory.GmailNextToken, "CATEGORY_PERSONAL", 500, nil)
		if err != nil {
			return err
		}

		syncedData := false
		for _, message := range res.Messages {
			err := input.HeartBeatFunc()
			if err != nil {
				return err
			}

			if !utils.Contains(message.LabelIds, "CATEGORY_PERSONAL") {
				// only sync personal emails
				continue
			}

			messagePath := pathPrefix + "/" + utils.GenerateTitleFromGmailMessage(message)
			_, synced := emailListFromBucket[messagePath]
			if synced {
				continue
			}

			b, err := json.Marshal(message)
			if err != nil {
				return err
			}

			syncedData = true
			// Legacy direct upload (all payloads in memory):
			// err = handler.UploadObjectAndSync(context.TODO(), input.Database, storxToken, "gmail", messagePath, b, input.Job.UserID, input.StorxRecovery)
			err = handler.UploadBufferedObjectAndSync(context.TODO(), input.Database, storxToken, "gmail", messagePath, b, input.Job.UserID, input.StorxRecovery)
			if err != nil {
				return err
			}

			input.Job.TaskMemory.GmailSyncCount++
			emptyLoopCount = 0
		}

		if !syncedData {
			// if we don't get any new data, we can break
			emptyLoopCount++
		}

		if emptyLoopCount > 20 {
			// repeated empty pages — stop pagination
			*input.Job.TaskMemory.GmailNextToken = ""
			break
		}

		*input.Job.TaskMemory.GmailNextToken = res.NextPageToken
		if *input.Job.TaskMemory.GmailNextToken == "" {
			break
		}
	}
	return nil
}
