package restore

import "strings"

// IsMigrationRestore reports whether prepare/all is a cross-mailbox migration.
func IsMigrationRestore(loginID, targetEmail string) bool {
	return isMigrationRestore(loginID, targetEmail)
}

// OAuthMissingList builds MissingPermission rows for OAuth scope gaps.
func OAuthMissingList(service string, scopes []string) []MissingPermission {
	return oauthMissingList(service, scopes)
}

// Readiness message constants (shared by Google + Microsoft prepare).
const (
	MsgReadinessNoCredential       = restoreReadinessNoCredential
	MsgReadinessNoTargetCredential = restoreReadinessNoTargetCredential
	MsgReadinessNoBackupJob        = restoreReadinessNoBackupJob
	MsgReadinessNoBackupData       = restoreReadinessNoBackupData
	MsgReadinessStorxMissing       = restoreReadinessStorxMissing
	MsgReadinessRefreshMissing     = restoreReadinessRefreshMissing
	MsgReadinessRefreshInvalid     = restoreReadinessRefreshInvalid
	MsgReadinessTokenValidation    = restoreReadinessTokenValidation
	MsgReadinessMissingScopes      = restoreReadinessMissingScopes
	MsgReadinessDWDNotConfigured   = restoreReadinessDWDNotConfigured
)

// TrimLoginPrefix is a shared vault prefix helper.
func TrimLoginPrefix(loginID string) string {
	return strings.TrimSuffix(strings.TrimSpace(loginID), "/") + "/"
}
