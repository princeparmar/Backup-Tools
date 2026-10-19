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
		if cronJob.ResourceType == repo.ResourceTypeTenant {
			return teamsTenantRestorePrefix(cronJob.TenantID, job.LoginID)
		}
		if p := outlook.ResourceKeyPrefix(cronJob.TenantID, cronJob.ResourceType, cronJob.ResourceID); p != "" {
			return p + "/"
		}
	}
	if job == nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(job.LoginID), "/") + "/"
}

// teamsTenantRestorePrefix is the prefix a restore from the tenant Teams job reads: the requested
// team (a GUID) or chat (a thread id), or the whole tenant when the request names the tenant.
func teamsTenantRestorePrefix(tenantID, loginID string) string {
	tenant := strings.ToLower(strings.TrimSpace(tenantID))
	login := strings.TrimSpace(loginID)
	if login == "" || strings.EqualFold(login, tenant) {
		return tenant + "/"
	}
	kind := outlook.TeamsKeyChat
	if isGUID(login) {
		kind = outlook.TeamsKeyTeam
	}
	if p := outlook.ResourceKeyPrefix(tenant, kind, login); p != "" {
		return p + "/"
	}
	return tenant + "/"
}

func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

func microsoftTenantMismatch(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrMicrosoftTenantMismatch, fmt.Sprintf(format, args...))
}

// checkBackupTenant requires the backup job and its resource to belong to one tenant.
func checkBackupTenant(store *db.PostgresDb, cronJob *repo.CronJobListingDB) (string, error) {
	if cronJob == nil {
		return "", microsoftTenantMismatch("the restore has no source backup job")
	}
	tenant := strings.ToLower(strings.TrimSpace(cronJob.TenantID))
	if tenant == "" {
		return "", microsoftTenantMismatch("backup job %d has no tenant", cronJob.ID)
	}
	if cronJob.ResourceType == repo.ResourceTypeTenant {
		if !strings.EqualFold(strings.TrimSpace(cronJob.ResourceID), tenant) {
			return "", microsoftTenantMismatch("tenant job %d is for %s, not %s", cronJob.ID, cronJob.ResourceID, tenant)
		}
		return tenant, nil
	}
	res, err := store.MicrosoftResourceRepo.Get(tenant, cronJob.ResourceType, cronJob.ResourceID)
	if err != nil {
		return "", err
	}
	if res == nil || strings.ToLower(strings.TrimSpace(res.TenantID)) != tenant {
		return "", microsoftTenantMismatch("the backed-up %s is not recorded in tenant %s", cronJob.ResourceType, tenant)
	}
	return tenant, nil
}

func requireWriteLink(store *db.PostgresDb, writeCred *repo.GoogleBackupCredentialDB, tenant string) error {
	if writeCred == nil {
		return microsoftTenantMismatch("the restore has no write account")
	}
	link, err := store.MicrosoftLinkRepo.Get(writeCred.ID, tenant)
	if err != nil {
		return err
	}
	if !link.Connected() {
		return microsoftTenantMismatch("the write account is not connected to tenant %s", tenant)
	}
	return nil
}

// CheckMicrosoftTenantGuard requires one tenant across the backup job, its resource, the restore's
// selected target tenant and the write credential's connected link. An in-place restore cannot
// cross tenants; a restore into another account uses CheckMicrosoftMigrationWrite.
func CheckMicrosoftTenantGuard(store *db.PostgresDb, targetTenantID string, cronJob *repo.CronJobListingDB, writeCred *repo.GoogleBackupCredentialDB) error {
	tenant, err := checkBackupTenant(store, cronJob)
	if err != nil {
		return err
	}
	if target := strings.ToLower(strings.TrimSpace(targetTenantID)); target != tenant {
		return microsoftTenantMismatch("restore target tenant %q differs from backup tenant %q", target, tenant)
	}
	return requireWriteLink(store, writeCred, tenant)
}

// CheckMicrosoftMigrationWrite checks a restore into another mailbox. The backup must still be a
// coherent tenant resource. The write account must be connected to targetTenantID, which is the
// destination tenant and may differ from the backup's tenant.
func CheckMicrosoftMigrationWrite(store *db.PostgresDb, targetTenantID string, cronJob *repo.CronJobListingDB, writeCred *repo.GoogleBackupCredentialDB) error {
	if _, err := checkBackupTenant(store, cronJob); err != nil {
		return err
	}
	target := strings.ToLower(strings.TrimSpace(targetTenantID))
	if target == "" {
		return microsoftTenantMismatch("the restore has no destination tenant")
	}
	return requireWriteLink(store, writeCred, target)
}

// ErrMicrosoftTenantRequired: the login has Microsoft backups in several tenants and none was selected.
var ErrMicrosoftTenantRequired = errors.New("this account has Microsoft backups in several tenants; select the tenant (MICROSOFT_TENANT_ID)")

// FindMicrosoftRestoreJob picks the backup job for a Microsoft restore of loginID (mailbox or
// resource ID) inside tenantID. Without a tenant the match must be in one tenant only.
//
// Teams: the tenant job (matched by tenant id) holds every team and chat; an old per-team job is
// used only when no tenant job exists (its keys are read until the tenant job converts them).
func FindMicrosoftRestoreJob(store *db.PostgresDb, userID, method, tenantID, loginID string) (*repo.CronJobListingDB, error) {
	if method == "outlook_teams" {
		if j, err := findTeamsTenantRestoreJob(store, userID, tenantID, loginID); err != nil || j != nil {
			return j, err
		}
	}
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

func findTeamsTenantRestoreJob(store *db.PostgresDb, userID, tenantID, loginID string) (*repo.CronJobListingDB, error) {
	tenants := []string{}
	if t := strings.ToLower(strings.TrimSpace(tenantID)); t != "" {
		tenants = append(tenants, t)
	} else {
		if isGUID(strings.TrimSpace(loginID)) {
			tenants = append(tenants, strings.ToLower(strings.TrimSpace(loginID)))
		}
		old, err := store.CronJobRepo.FindMicrosoftJobsForRestore(userID, "outlook_teams", "", loginID)
		if err != nil {
			return nil, err
		}
		for _, j := range old {
			tenants = append(tenants, strings.ToLower(strings.TrimSpace(j.TenantID)))
		}
	}
	var found *repo.CronJobListingDB
	for _, t := range tenants {
		jobs, err := store.CronJobRepo.FindMicrosoftJobsForRestore(userID, "outlook_teams", t, t)
		if err != nil {
			return nil, err
		}
		for i := range jobs {
			if jobs[i].ResourceType != repo.ResourceTypeTenant {
				continue
			}
			if found != nil && !strings.EqualFold(found.TenantID, jobs[i].TenantID) {
				return nil, ErrMicrosoftTenantRequired
			}
			if found == nil {
				found = &jobs[i]
			}
		}
	}
	return found, nil
}
