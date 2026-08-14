package restore

import (
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func EmailDomain(email string) string {
	return emailDomain(email)
}

func IsConsumerMailboxDomain(domain string) bool {
	return isConsumerMailboxDomain(domain)
}

func emailDomain(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.LastIndex(email, "@"); at >= 0 && at < len(email)-1 {
		return strings.ToLower(email[at+1:])
	}
	return ""
}

func isConsumerMailboxDomain(domain string) bool {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "gmail.com", "googlemail.com", "":
		return true
	default:
		return false
	}
}

func isWorkspaceAccountType(accountType string) bool {
	switch google.NormalizeAccountType(accountType) {
	case google.AccountTypeAdminWorkspace, google.AccountTypeEmployeeWorkspace:
		return true
	default:
		return false
	}
}

// migrationDWDAttemptEligible reports whether migration should try DWD on the target mailbox.
// Delegation is confirmed at prepare time via ProbeDWDRestore, not here.
func migrationDWDAttemptEligible(targetEmail string) bool {
	if !google.WorkspaceServiceAccountConfigured() {
		return false
	}
	domain := emailDomain(targetEmail)
	return domain != "" && !isConsumerMailboxDomain(domain)
}

// migrationDWDJobTarget reports migration jobs that use DWD for the write path.
// OAuth migration stores the target credential_id; DWD migration stores the source credential_id.
func migrationDWDJobTarget(store *db.PostgresDb, job *repo.RestoreJobListingDB) bool {
	if job == nil || !repo.IsRestoreJobMigration(job) {
		return false
	}
	credEmail := ""
	if job.CredentialID > 0 && store != nil && store.CredentialRepo != nil {
		if c, err := store.CredentialRepo.GetByID(job.CredentialID); err == nil && c != nil {
			credEmail = strings.TrimSpace(c.Email)
		}
	}
	return migrationDWDWriteForJob(job.TargetEmail, credEmail)
}

// migrationDWDWriteForJob decides DWD vs OAuth migration write from target domain and stored credential email.
func migrationDWDWriteForJob(targetEmail, credentialEmail string) bool {
	if !migrationDWDAttemptEligible(targetEmail) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(credentialEmail), strings.TrimSpace(targetEmail)) {
		return false
	}
	return true
}
