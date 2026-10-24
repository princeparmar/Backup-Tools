package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	job.Message = microsoftUnavailableMessage(job, runErr)
	job.MessageStatus = repo.JobMessageStatusWarning
	return nil
}

// microsoftUnavailableWho is the address or name shown to the customer.
func microsoftUnavailableWho(job *repo.CronJobListingDB) string {
	if who := strings.TrimSpace(jobOutlookMailbox(job)); who != "" {
		return who
	}
	return "this account"
}

// microsoftUnavailableMessage tells the customer which person's backup was skipped and why.
func microsoftUnavailableMessage(job *repo.CronJobListingDB, runErr error) string {
	who := microsoftUnavailableWho(job)
	reason := microsoftDenyReason(job.Method, runErr)
	const again = " The next backup will try again."
	switch job.Method {
	case "outlook":
		return "Mail for " + who + " was not backed up. " + reason + again
	case "outlook_calendar":
		return "Calendar for " + who + " was not backed up. " + reason + again
	case "outlook_contacts":
		return "Contacts for " + who + " were not backed up. " + reason + again
	case "outlook_onedrive":
		return "OneDrive for " + who + " was not backed up. " + reason + again
	case "outlook_teams":
		return "Teams for " + who + " was not backed up. " + reason + again
	}
	return fmt.Sprintf("%s for %s was not backed up. %s", job.Method, who, reason) + again
}

// microsoftDenyReason is the customer-facing cause. Microsoft's own message wins when Graph sent one.
func microsoftDenyReason(method string, runErr error) string {
	if reason := microsoftGraphReason(runErr); reason != "" {
		return reason
	}
	switch method {
	case "outlook", "outlook_calendar", "outlook_contacts":
		return "Microsoft did not allow access to this mailbox."
	case "outlook_onedrive":
		return "This account has no OneDrive."
	case "outlook_teams":
		return "This team has no Teams data."
	}
	return "Microsoft did not allow this backup."
}

func microsoftGraphReason(err error) string {
	if err == nil {
		return ""
	}
	code, message := microsoftGraphError(err)
	if sentence := cleanGraphMessage(message); sentence != "" {
		return "Microsoft says " + sentence
	}
	switch strings.ToLower(code) {
	case "mailboxnotenabledforrestapi":
		return "Microsoft says this mailbox is inactive, stored on-premises, or not ready for backup."
	case "errornonexistentmailbox":
		return "Microsoft says this account has no mailbox."
	case "errorinvaliduser":
		return "Microsoft does not recognize this mailbox address."
	case "request_resourcenotfound":
		return "Microsoft could not find this account."
	}
	if strings.Contains(strings.ToLower(err.Error()), "mysite not found") {
		return "Microsoft says this account has no OneDrive."
	}
	return ""
}

func microsoftGraphError(err error) (code, message string) {
	raw := err.Error()
	start := strings.Index(raw, "{")
	if start < 0 {
		return "", ""
	}
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.NewDecoder(strings.NewReader(raw[start:])).Decode(&parsed) != nil {
		return "", ""
	}
	return strings.TrimSpace(parsed.Error.Code), strings.TrimSpace(parsed.Error.Message)
}

func cleanGraphMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if message == "" || strings.Contains(message, "{") || len(message) > 300 {
		return ""
	}
	message = strings.TrimRight(message, ".")
	if message == "" {
		return ""
	}
	return strings.ToLower(message[:1]) + message[1:] + "."
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
