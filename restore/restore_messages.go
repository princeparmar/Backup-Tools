package restore

import (
	"fmt"

	"github.com/StorX2-0/Backup-Tools/repo"
)

// User-facing restore job messages (failed / terminal states).
const (
	restoreJobStorxMissing = "Restore failed: StorX access grant is missing. Reconnect StorX via auto-sync and try again."

	restoreJobStorxUplink = "Restore failed: StorX permissions are insufficient. Update StorX permissions via auto-sync and try again."

	restoreJobStorxSatelliteRefresh = "Restore failed: StorX access could not be renewed from Satellite. Please try again later or contact support."

	restoreJobStorxRefreshLimit = "StorX access could not be restored after 3 attempts. Automatic backup has been paused. Please contact support."

	restoreJobGoogleAuth = "Restore failed: Google credentials are invalid or expired. Reconnect your Google account via auto-sync and try again."

	restoreJobGoogleInsufficientScope = "Restore failed: Google account is missing permissions required for this service. Reconnect Google with full app permissions and try again."

	restoreJobDelegation = "Restore failed: Google Workspace blocked access (domain-wide delegation). Ask your admin to authorize this app in Admin Console, or restore using the admin OAuth account."

	restoreJobNetwork = "Restore failed due to network connectivity issues. Check your connection and try again."

	restoreJobGeneric = "Restore failed due to an unexpected error. Check restore history for details and try again."
)

// Batch / task history messages.
const (
	restoreTaskStorxMissing = "Restore batch stopped: StorX access grant missing — reconnect via auto-sync"

	restoreTaskGoogleAuth = "Restore batch stopped: Google credentials invalid — reconnect via auto-sync"

	restoreTaskGoogleInsufficientScope = "Restore batch stopped: missing Google OAuth scopes — reconnect with full permissions"

	restoreTaskDelegation = "Restore batch stopped: domain-wide delegation not configured for this mailbox"

	restoreTaskStorxUplink = "Restore batch stopped: StorX uplink permission denied — update StorX grant"

	restoreTaskStorxSatelliteRefresh = "Restore batch stopped: StorX token could not be renewed from Satellite"

	restoreTaskStorxRefreshLimit = "Restore stopped: StorX access could not be restored after 3 attempts — automatic backup paused, contact support"

	restoreTaskNetwork = "Restore batch stopped: network connectivity issue"
)

const (
	restoreTplTaskBatchRetry = "Restore batch failed (attempt %d of %d). Retrying automatically..."
)

// Readiness / prepare API messages.
const (
	restoreReadinessNoCredential      = "No backup credential found for this project"
	restoreReadinessNoTargetCredential = "No credential found for target account — connect via PUT /auto-sync/job/project"
	restoreReadinessNoBackupJob      = "No backup job found for this account and service"
	restoreReadinessNoBackupData     = "No backed-up items found for this account and service"
	restoreReadinessStorxMissing     = "StorX access grant is missing — reconnect via auto-sync"
	restoreReadinessRefreshMissing   = "Google refresh token is missing"
	restoreReadinessRefreshInvalid   = "Google refresh token is invalid or expired"
	restoreReadinessTokenValidation  = "Google token validation failed"
	restoreReadinessMissingScopes    = "Missing OAuth scopes required for restore"
	restoreReadinessDWDNotConfigured = "Ask your Google Workspace admin to authorize all restore scopes in Admin Console → Domain-wide delegation"
)

// Notification bodies (Satellite push).
const (
	restoreNotifyFailedTitle = "Restore failed"
)

func restoreBatchRetryMessage(attempt uint) string {
	return fmt.Sprintf(restoreTplTaskBatchRetry, attempt, repo.RestoreMaxRetryCount)
}
