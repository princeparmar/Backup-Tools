package crons

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/mstenant"
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

// msJobResolveFn is the tenant resolver (overridden in tests).
var msJobResolveFn = mstenant.Resolve

// microsoftJobAccessToken resolves StorX + Graph access for a Microsoft job through the tenant
// resolver: the job's credential and tenant_id select the link, whose auth mode picks the app-only
// or delegated token. There is no fallback between the two.
func microsoftJobAccessToken(input ProcessorInput) (*microsoftJobAuth, error) {
	storx := input.Database.CronJobRepo.ResolvedStorxToken(input.Job)
	if storx == "" {
		return nil, fmt.Errorf("storx token not found")
	}
	credID := repo.JobCredentialID(input.Job)
	if credID == 0 {
		return nil, fmt.Errorf("microsoft job %d has no credential", input.Job.ID)
	}
	tc, err := msJobResolveFn(context.Background(), input.Database, mstenant.Request{
		UserID:       input.Job.UserID,
		CredentialID: credID,
		TenantID:     input.Job.TenantID,
		Capability:   mstenant.CapabilityForService(input.Job.Method),
		RefreshToken: input.Database.CronJobRepo.ResolvedRefreshToken(input.Job),
	})
	if err != nil {
		return nil, fmt.Errorf("microsoft tenant access: %w", err)
	}
	return &microsoftJobAuth{AccessToken: tc.Token, StorxToken: storx, Application: tc.Application, TenantID: tc.TenantID}, nil
}

// microsoftJobKeyPrefix is the storage prefix of the job's resource ({tenant}/{type}/{id}). Jobs
// without a tenant identity cannot write tenant-isolated keys and fail.
func microsoftJobKeyPrefix(job *repo.CronJobListingDB) (string, error) {
	prefix := outlook.ResourceKeyPrefix(job.TenantID, job.ResourceType, job.ResourceID)
	if prefix == "" {
		return "", fmt.Errorf("microsoft job %d has no tenant/resource identity", job.ID)
	}
	return prefix, nil
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
