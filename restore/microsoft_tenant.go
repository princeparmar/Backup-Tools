package restore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// ErrMicrosoftTenantMismatch rejects a Microsoft restore that would cross tenants.
var ErrMicrosoftTenantMismatch = errors.New("microsoft restore tenant mismatch")

// RestoreKeyPrefix is the synced-object prefix a restore reads: the backup job's Microsoft
// resource prefix ({tenant}/{type}/{id}/), else the login ID (Google).
func RestoreKeyPrefix(job *repo.RestoreJobListingDB, cronJob *repo.CronJobListingDB) string {
	if job != nil && IsMicrosoftRestoreMethod(job.Method) && cronJob != nil {
		if p := outlook.ResourceKeyPrefix(cronJob.TenantID, cronJob.ResourceType, cronJob.ResourceID); p != "" {
			return p + "/"
		}
	}
	if job == nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(job.LoginID), "/") + "/"
}

// CheckMicrosoftTenantGuard requires one tenant across the backup job, its resource, the restore's
// selected target tenant and the write credential's connected link. There is no exception;
// cross-tenant migration is not supported.
func CheckMicrosoftTenantGuard(store *db.PostgresDb, targetTenantID string, cronJob *repo.CronJobListingDB, writeCred *repo.GoogleBackupCredentialDB) error {
	mismatch := func(format string, args ...interface{}) error {
		return fmt.Errorf("%w: %s", ErrMicrosoftTenantMismatch, fmt.Sprintf(format, args...))
	}
	if cronJob == nil {
		return mismatch("the restore has no source backup job")
	}
	tenant := strings.ToLower(strings.TrimSpace(cronJob.TenantID))
	if tenant == "" {
		return mismatch("backup job %d has no tenant", cronJob.ID)
	}
	res, err := store.MicrosoftResourceRepo.Get(tenant, cronJob.ResourceType, cronJob.ResourceID)
	if err != nil {
		return err
	}
	if res == nil || strings.ToLower(strings.TrimSpace(res.TenantID)) != tenant {
		return mismatch("the backed-up %s is not recorded in tenant %s", cronJob.ResourceType, tenant)
	}
	if target := strings.ToLower(strings.TrimSpace(targetTenantID)); target != tenant {
		return mismatch("restore target tenant %q differs from backup tenant %q", target, tenant)
	}
	if writeCred == nil {
		return mismatch("the restore has no write account")
	}
	link, err := store.MicrosoftLinkRepo.Get(writeCred.ID, tenant)
	if err != nil {
		return err
	}
	if !link.Connected() {
		return mismatch("the write account is not connected to tenant %s", tenant)
	}
	return nil
}

// ErrMicrosoftTenantRequired: the login has Microsoft backups in several tenants and none was selected.
var ErrMicrosoftTenantRequired = errors.New("this account has Microsoft backups in several tenants; select the tenant (MICROSOFT_TENANT_ID)")

// FindMicrosoftRestoreJob picks the backup job for a Microsoft restore of loginID (mailbox or
// resource ID) inside tenantID. Without a tenant the match must be in one tenant only.
func FindMicrosoftRestoreJob(store *db.PostgresDb, userID, method, tenantID, loginID string) (*repo.CronJobListingDB, error) {
	jobs, err := store.CronJobRepo.FindMicrosoftJobsForRestore(userID, method, tenantID, loginID)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	first := strings.ToLower(jobs[0].TenantID)
	for _, j := range jobs[1:] {
		if strings.ToLower(j.TenantID) != first {
			return nil, ErrMicrosoftTenantRequired
		}
	}
	return &jobs[0], nil
}
