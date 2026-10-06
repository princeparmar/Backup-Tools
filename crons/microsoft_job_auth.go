package crons

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// microsoftJobAuth is the Microsoft Graph access for one job run.
type microsoftJobAuth struct {
	AccessToken string
	StorxToken  string
	// Application jobs use the tenant app-only token and must target /users/{id}, never /me.
	Application bool
	TenantID    string
}

// Seams (overridden in tests).
var (
	msJobOrgAccessFn       = handler.MicrosoftOrgAccess
	msJobRefreshToAccessFn = outlook.AuthTokenUsingRefreshToken
)

// microsoftJobAccessToken resolves StorX + Graph access for a Microsoft job. Application credentials
// use AppOnlyToken(tenant_id) gated by consent + the job's capability, with no delegated fallback;
// delegated credentials use the refresh token.
func microsoftJobAccessToken(input ProcessorInput) (*microsoftJobAuth, error) {
	storx := input.Database.CronJobRepo.ResolvedStorxToken(input.Job)
	if storx == "" {
		return nil, fmt.Errorf("storx token not found")
	}
	var cred *repo.GoogleBackupCredentialDB
	if credID := repo.JobCredentialID(input.Job); credID != 0 {
		c, err := input.Database.CredentialRepo.GetByID(credID)
		if err != nil {
			return nil, fmt.Errorf("load credential: %w", err)
		}
		cred = c
	}
	if cred != nil && strings.EqualFold(strings.TrimSpace(cred.MicrosoftAuthMode), outlook.MicrosoftAuthModeApplication) {
		tenantID := strings.ToLower(strings.TrimSpace(cred.TenantID))
		if tenantID == "" {
			return nil, fmt.Errorf("application job credential %d has no tenant_id", cred.ID)
		}
		token, _, err := msJobOrgAccessFn(context.Background(), input.Database, tenantID, handler.MicrosoftCapabilityForService(input.Job.Method))
		if err != nil {
			return nil, fmt.Errorf("tenant app-only access: %w", err)
		}
		return &microsoftJobAuth{AccessToken: token, StorxToken: storx, Application: true, TenantID: tenantID}, nil
	}

	refresh := input.Database.CronJobRepo.ResolvedRefreshToken(input.Job)
	if refresh == "" {
		return nil, fmt.Errorf("refresh token not found")
	}
	token, err := msJobRefreshToAccessFn(refresh)
	if err != nil {
		return nil, fmt.Errorf("error while getting token from refresh token: %w", err)
	}
	return &microsoftJobAuth{AccessToken: token, StorxToken: storx}, nil
}

// microsoftJobClient builds the Graph client for a mailbox job: bound to /users/{mailbox} for
// application jobs, the signed-in user otherwise.
func microsoftJobClient(auth *microsoftJobAuth, mailbox string) (*outlook.OutlookClient, error) {
	if auth.Application {
		return outlook.NewOutlookClientForUser(auth.AccessToken, mailbox)
	}
	return outlook.NewOutlookClientUsingToken(auth.AccessToken)
}

// microsoftJobMailbox resolves the mailbox a job backs up. Application jobs must name it on the job.
func microsoftJobMailbox(auth *microsoftJobAuth, job *repo.CronJobListingDB, client *outlook.OutlookClient) (string, error) {
	mailbox := jobOutlookMailbox(job)
	if mailbox != "" {
		return mailbox, nil
	}
	if auth.Application {
		return "", fmt.Errorf("application job %d has no mailbox", job.ID)
	}
	user, err := client.GetCurrentUser()
	if err != nil {
		return "", fmt.Errorf("resolve mailbox: %w", err)
	}
	return strings.TrimSpace(user.Mail), nil
}
