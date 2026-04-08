package crons

import (
	"fmt"

	"github.com/StorX2-0/Backup-Tools/repo"
)

// Centralized user-facing copy for cron failure emails (determineErrorMessage).
const (
	cronEmailStorxInsufficient = "Your automatic backup has been temporarily disabled due to insufficient permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	cronEmailDelegationFinal = "Google Workspace blocked access to a mailbox (domain-wide delegation). Ask your admin to add this app's OAuth client in Admin Console → Security → API controls → Domain-wide delegation, with the required Gmail scopes, or remove mailboxes that cannot be delegated."

	cronEmailGoogleAuthFinal = "Your automatic backup has been temporarily disabled due to invalid Google credentials. Please update your Google account permissions and reactivate the backup from your dashboard."

	cronEmailGoogleInsufficientScope = "Your automatic backup has been temporarily disabled because this app no longer has the Gmail access it needs (for example some permissions were removed or not fully granted). Please open your dashboard, reconnect your Google account, and accept all requested Gmail permissions, then reactivate the backup."

	cronEmailOutlookAuthFinal = "Your automatic backup has been temporarily disabled due to invalid Microsoft Outlook credentials. Please update your Outlook account permissions and reactivate the backup from your dashboard."

	cronEmailStorxUplinkFinal = "Your automatic backup has been temporarily disabled due to insufficient StorX permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	cronEmailNetworkFinal = "Your automatic backup has been temporarily disabled due to network connectivity issues. Please check your internet connection and reactivate the backup from your dashboard."

	cronEmailFailurePeriodsExhausted = "Your automatic backup still failed after we retried each run up to 3 times, on 3 separate scheduled runs (daily, weekly, or monthly—depending on your settings). The backup has been turned off to avoid endless errors. Check the last error in your backup history, fix the underlying issue if you can, then reactivate the backup from your dashboard."
)

const (
	cronTplEmailGoogleAuthRetry = "Your automatic backup encountered an authentication issue with Google. We're retrying automatically (attempt %d of %d)."

	cronTplEmailOutlookAuthRetry = "Your automatic backup encountered an authentication issue with Microsoft Outlook. We're retrying automatically (attempt %d of %d)."

	cronTplEmailGenericRetry = "Your automatic backup hit an unexpected technical issue. This is attempt %d of %d for the current run—we retry automatically up to 3 times per scheduled run. If this run still fails, we will try again on your next scheduled backup (daily/weekly/monthly). If failures continue across several scheduled runs, the backup will be paused until you reactivate it."
)

// Job list / detail messages (handleErrorScenarios job.Message).
const (
	cronJobStorxInsufficientShort = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"

	cronJobDelegationFinal = "Google Workspace denied access to this mailbox (delegation). Ask your admin to enable domain-wide delegation for this app or adjust which accounts are backed up."

	cronJobGoogleAuthDeactivate = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"

	cronJobGoogleInsufficientScope = "Gmail access for this backup is incomplete (missing Google permissions). Reconnect your Google account and grant all requested Gmail scopes, then reactivate the automatic backup."

	cronJobOutlookAuthDeactivate = "Invalid Microsoft Outlook credentials. Please update the credentials and reactivate the automatic backup"

	cronJobNetworkFinal = "Automatic backup failed due to network issues. Please check your connection and reactivate."

	cronJobFailurePeriodsExhausted = "Backup failed after 3 scheduled runs, each with up to 3 automatic retries. Job has been deactivated—reactivate from your dashboard after fixing the issue."
)

const (
	cronTplJobGoogleAuthRetry = "Invalid Google credentials. Attempt %d of %d failed. Retrying automatically..."

	cronTplJobOutlookAuthRetry = "Invalid Microsoft Outlook credentials. Attempt %d of %d failed. Retrying automatically..."
)

// Task history messages (handleErrorScenarios task.Message).
const (
	cronTaskStorxInsufficientDeactivated = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"

	cronTaskDelegationFinal = "Delegation denied by Google Workspace. Your admin must authorize this app for domain-wide delegation (Gmail scopes) for the affected users."

	cronTaskGoogleAuthDeactivated = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"

	cronTaskGoogleInsufficientScope = "Gmail backup stopped: the connected Google account does not include the required Gmail permissions. Reconnect Google with full app permissions and reactivate the job."

	cronTaskOutlookAuthDeactivated = "Microsoft Outlook Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"

	cronTaskNetworkDeactivated = "Task failed due to network connectivity issues. Job has been deactivated."

	cronTaskFailurePeriodsExhausted = "Failed after multiple scheduled runs (3 runs × up to 3 retries each). Automatic backup has been deactivated."
)

const (
	cronTplTaskGoogleAuthRetry = "Google credentials invalid. Attempt %d of %d failed. Retrying automatically..."

	cronTplTaskOutlookAuthRetry = "Microsoft Outlook credentials invalid. Attempt %d of %d failed. Retrying automatically..."
)

const (
	cronTplJobGenericRetry = "Technical issue (attempt %d of %d this run). Retrying automatically—up to 3 tries per scheduled run; next run per your schedule if this run exhausts retries."

	cronTplTaskGenericRetry = "Attempt %d of %d failed (technical). Automatic retries continue for this run (max 3); if the run still fails, the next try is on your next scheduled backup."
)

func cronAttempt(attempt uint) (uint, uint) {
	return attempt, repo.MaxRetryCount
}

func cronEmailGoogleAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplEmailGoogleAuthRetry, a, m)
}

func cronEmailOutlookAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplEmailOutlookAuthRetry, a, m)
}

func cronEmailGenericRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplEmailGenericRetry, a, m)
}

func cronJobGenericRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplJobGenericRetry, a, m)
}

func cronTaskGenericRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplTaskGenericRetry, a, m)
}

func cronJobGoogleAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplJobGoogleAuthRetry, a, m)
}

func cronJobOutlookAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplJobOutlookAuthRetry, a, m)
}

func cronTaskGoogleAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplTaskGoogleAuthRetry, a, m)
}

func cronTaskOutlookAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplTaskOutlookAuthRetry, a, m)
}
