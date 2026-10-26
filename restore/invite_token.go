package restore

import "sync"

// inviteRestoreTokens keeps the invited member's Google access token for the
// in-process restore worker. The token is not written on the job row.
var inviteRestoreTokens sync.Map

// RememberInviteAccessToken stores the Google access token for an invited restore job.
func RememberInviteAccessToken(jobID uint, token string) {
	if jobID == 0 || token == "" {
		return
	}
	inviteRestoreTokens.Store(jobID, token)
}

// PeekInviteAccessToken returns the token without removing it, so retries in this process can reuse it.
func PeekInviteAccessToken(jobID uint) string {
	v, ok := inviteRestoreTokens.Load(jobID)
	if !ok {
		return ""
	}
	token, _ := v.(string)
	return token
}

// ForgetInviteAccessToken drops the token after the job finishes.
func ForgetInviteAccessToken(jobID uint) {
	if jobID == 0 {
		return
	}
	inviteRestoreTokens.Delete(jobID)
}
