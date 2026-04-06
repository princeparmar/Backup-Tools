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

	cronEmailOutlookAuthFinal = "Your automatic backup has been temporarily disabled due to invalid Microsoft Outlook credentials. Please update your Outlook account permissions and reactivate the backup from your dashboard."

	cronEmailStorxUplinkFinal = "Your automatic backup has been temporarily disabled due to insufficient StorX permissions. Please update your StorX permissions and reactivate the backup from your dashboard."

	cronEmailNetworkFinal = "Your automatic backup has been temporarily disabled due to network connectivity issues. Please check your internet connection and reactivate the backup from your dashboard."
)

const (
	cronTplEmailDelegationRetry = "Google Workspace delegation issue while accessing a mailbox. Retrying (attempt %d of %d). If this continues, your admin must enable domain-wide delegation for this app."

	cronTplEmailGoogleAuthRetry = "Your automatic backup encountered an authentication issue with Google. We're retrying automatically (attempt %d of %d)."

	cronTplEmailOutlookAuthRetry = "Your automatic backup encountered an authentication issue with Microsoft Outlook. We're retrying automatically (attempt %d of %d)."

	cronTplEmailGenericRetry = "Your automatic backup encountered a technical issue. We're retrying automatically (attempt %d of %d)."
)

// Job list / detail messages (handleErrorScenarios job.Message).
const (
	cronJobStorxInsufficientShort = "Insufficient permissions to upload to storx. Please update the permissions and reactivate the automatic backup"

	cronJobDelegationFinal = "Google Workspace denied access to this mailbox (delegation). Ask your admin to enable domain-wide delegation for this app or adjust which accounts are backed up."

	cronJobGoogleAuthDeactivate = "Invalid google credentials. Please update the credentials and reactivate the automatic backup"

	cronJobOutlookAuthDeactivate = "Invalid Microsoft Outlook credentials. Please update the credentials and reactivate the automatic backup"

	cronJobNetworkFinal = "Automatic backup failed due to network issues. Please check your connection and reactivate."

	cronJobGenericRetry = "Automatic backup encountered an error. Job will be retried automatically..."
)

const (
	cronTplJobDelegationRetry = "Google Workspace delegation denied. Attempt %d of %d failed. Retrying..."

	cronTplJobGoogleAuthRetry = "Invalid Google credentials. Attempt %d of %d failed. Retrying automatically..."

	cronTplJobOutlookAuthRetry = "Invalid Microsoft Outlook credentials. Attempt %d of %d failed. Retrying automatically..."
)

// Task history messages (handleErrorScenarios task.Message).
const (
	cronTaskStorxInsufficientDeactivated = "Insufficient permissions to upload to storx. Please update the permissions. Automatic backup will be deactivated"

	cronTaskDelegationFinal = "Delegation denied by Google Workspace. Your admin must authorize this app for domain-wide delegation (Gmail scopes) for the affected users."

	cronTaskGoogleAuthDeactivated = "Google Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"

	cronTaskOutlookAuthDeactivated = "Microsoft Outlook Credentials are invalid. Please update the credentials. Automatic backup will be deactivated"

	cronTaskNetworkDeactivated = "Task failed due to network connectivity issues. Job has been deactivated."

	cronTaskGenericRetry = "Task encountered an error. Task will be retried automatically..."
)

const (
	cronTplTaskDelegationRetry = "Delegation denied for a mailbox (see logs). Attempt %d of %d. Retrying..."

	cronTplTaskGoogleAuthRetry = "Google credentials invalid. Attempt %d of %d failed. Retrying automatically..."

	cronTplTaskOutlookAuthRetry = "Microsoft Outlook credentials invalid. Attempt %d of %d failed. Retrying automatically..."
)

func cronAttempt(attempt uint) (uint, uint) {
	return attempt, repo.MaxRetryCount
}

func cronEmailDelegationRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplEmailDelegationRetry, a, m)
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

func cronJobDelegationRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplJobDelegationRetry, a, m)
}

func cronJobGoogleAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplJobGoogleAuthRetry, a, m)
}

func cronJobOutlookAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplJobOutlookAuthRetry, a, m)
}

func cronTaskDelegationRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplTaskDelegationRetry, a, m)
}

func cronTaskGoogleAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplTaskGoogleAuthRetry, a, m)
}

func cronTaskOutlookAuthRetry(attempt uint) string {
	a, m := cronAttempt(attempt)
	return fmt.Sprintf(cronTplTaskOutlookAuthRetry, a, m)
}
