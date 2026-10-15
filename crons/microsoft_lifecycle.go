package crons

import (
	"context"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// microsoftRunOutcome handles a Microsoft job run that failed because Graph reports its user,
// group, mailbox or OneDrive missing. A deleted object pauses the object's jobs; an object that
// still exists has no data for this service, which is recorded and the run is skipped. If the
// directory cannot be read the original error stands and nothing changes.
func (a *AutosyncManager) microsoftRunOutcome(ctx context.Context, job *repo.CronJobListingDB, runErr error) error {
	if job == nil || job.Provider != repo.CredentialProviderMicrosoft || !outlook.IsResourceMissingError(runErr) {
		return runErr
	}
	kind := outlook.DirectoryKindUser
	switch job.ResourceType {
	case repo.ResourceTypeUser:
	case repo.ResourceTypeTeam:
		kind = outlook.DirectoryKindGroup
	default:
		return runErr
	}

	state := outlook.ObjectStateActive
	tc, err := msReconcileResolveFn(ctx, a.store, mstenant.Request{
		UserID:             job.UserID,
		CredentialID:       repo.JobCredentialID(job),
		TenantID:           job.TenantID,
		RequireApplication: true,
	})
	if err == nil {
		state, err = msObjectStateFn(ctx, tc.Token, kind, job.ResourceID)
		if err != nil {
			logger.Warn(ctx, "microsoft lifecycle check", logger.Int("job_id", int(job.ID)), logger.ErrorField(err))
			return runErr
		}
	}
	// Delegated jobs back up the signed-in account itself: a token was minted, so it exists.

	jobs, err := a.store.CronJobRepo.ListMicrosoftResourceJobs(job.UserID, job.TenantID, job.ResourceType, job.ResourceID)
	if err != nil {
		return runErr
	}
	ApplyMicrosoftObjectState(a.store, job.TenantID, job.ResourceType, job.ResourceID, state, jobs)
	switch state {
	case outlook.ObjectStateDeleted:
		return fmt.Errorf("%s: %w", msgPausedDeleted, runErr)
	case outlook.ObjectStateGone:
		return fmt.Errorf("%s: %w", msgPausedGone, runErr)
	}

	_ = a.store.MicrosoftResourceRepo.AddUnavailableService(job.TenantID, job.ResourceType, job.ResourceID, job.Method)
	job.Message = microsoftUnavailableMessage(job)
	job.MessageStatus = repo.JobMessageStatusWarning
	return nil
}

// microsoftUnavailableMessage says which service was skipped and why, by method.
func microsoftUnavailableMessage(job *repo.CronJobListingDB) string {
	const again = " It is checked again on every run."
	switch job.Method {
	case "outlook":
		return "Mail not backed up: this user has no Exchange Online mailbox." + again
	case "outlook_calendar":
		return "Calendar not backed up: this user has no Exchange Online mailbox." + again
	case "outlook_contacts":
		return "Contacts not backed up: this user has no Exchange Online mailbox." + again
	case "outlook_onedrive":
		return "OneDrive not backed up: this user has no OneDrive." + again
	case "outlook_teams":
		return "Teams not backed up: Microsoft 365 has no Teams data for this team." + again
	}
	return fmt.Sprintf("%s not backed up: not available for this %s.", job.Method, job.ResourceType) + again
}

// microsoftRunSucceeded clears the job's service from the resource's unavailable services, so a
// user who got a mailbox or OneDrive is no longer recorded without one.
func (a *AutosyncManager) microsoftRunSucceeded(job *repo.CronJobListingDB) {
	if job == nil || job.Provider != repo.CredentialProviderMicrosoft || a.store == nil || a.store.MicrosoftResourceRepo == nil {
		return
	}
	if job.ResourceType != repo.ResourceTypeUser && job.ResourceType != repo.ResourceTypeTeam {
		return
	}
	if err := a.store.MicrosoftResourceRepo.RemoveUnavailableService(job.TenantID, job.ResourceType, job.ResourceID, job.Method); err != nil {
		logger.Warn(context.Background(), "clear unavailable service", logger.Int("job_id", int(job.ID)), logger.ErrorField(err))
	}
}
