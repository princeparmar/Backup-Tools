package restore

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"golang.org/x/time/rate"
)

// StorxGrantSession holds the storx grant loaded from DB for manual item restore.
// On the first uplink/grant error per request it refreshes via Satellite once, then retries the download.
type StorxGrantSession struct {
	Grant     string
	store     *db.PostgresDb
	cronJob   *repo.CronJobListingDB
	refreshed bool
}

// NewStorxGrantSession loads storx from the linked backup job/credential only.
func NewStorxGrantSession(ctx context.Context, store *db.PostgresDb, userID, method, loginID string) (*StorxGrantSession, error) {
	_ = ctx
	cronJob, ok, err := store.CronJobRepo.FindJobForRestore(userID, method, loginID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("storx access grant not found: no backup job for %s", loginID)
	}
	grant := strings.TrimSpace(store.CronJobRepo.ResolvedStorxToken(cronJob))
	if cid := repo.JobCredentialID(cronJob); cid > 0 {
		if cred, credErr := store.CredentialRepo.GetByID(cid); credErr == nil && cred != nil {
			if grant == "" {
				grant = strings.TrimSpace(cred.StorxToken)
			}
		}
	}
	if grant == "" {
		return nil, fmt.Errorf("storx access grant not found")
	}
	return &StorxGrantSession{
		Grant:   grant,
		store:   store,
		cronJob: cronJob,
	}, nil
}

// DownloadObject downloads one object from StorX using the DB grant.
// On the first storx/uplink error in this session it calls Satellite storx-token/refresh once and retries.
func (s *StorxGrantSession) DownloadObject(ctx context.Context, bucket, objectKey string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("storx session required")
	}
	data, err := satellite.DownloadObject(ctx, s.Grant, bucket, objectKey)
	if err == nil {
		return data, nil
	}
	if s.refreshed || s.store == nil || s.cronJob == nil {
		return nil, err
	}
	if !storxrefresh.IsUplinkError(err) {
		return nil, err
	}
	s.refreshed = true
	grant, refreshErr := storxrefresh.RefreshAndSave(ctx, s.store, s.cronJob)
	if refreshErr != nil {
		return nil, err
	}
	s.Grant = grant
	return satellite.DownloadObject(ctx, s.Grant, bucket, objectKey)
}

// AuthModeForJob derives oauth vs dwd from account_type and linked credential/cron.
func AuthModeForJob(store *db.PostgresDb, job *repo.RestoreJobListingDB) string {
	if job == nil {
		return RestoreAuthModeOAuth
	}
	if IsMicrosoftRestoreMethod(job.Method) {
		return RestoreAuthModeOAuth
	}
	if repo.IsRestoreJobMigration(job) {
		if migrationDWDJobTarget(store, job) {
			return RestoreAuthModeDWD
		}
		return RestoreAuthModeOAuth
	}
	service := repo.APIServiceFromMethod(job.Method)
	var cred *repo.GoogleBackupCredentialDB
	var cronJob *repo.CronJobListingDB
	holder := ""
	if job.CronJobID > 0 {
		if j, err := store.CronJobRepo.GetCronJobByID(job.CronJobID); err == nil {
			cronJob = j
			holder = store.CronJobRepo.ResolvedOAuthHolderEmail(j)
		}
	}
	if job.CredentialID > 0 {
		if c, err := store.CredentialRepo.GetByID(job.CredentialID); err == nil {
			cred = c
			if holder == "" {
				holder = strings.TrimSpace(c.Email)
			}
		}
	}
	return ResolveRestoreAuthMode(service, job.AccountType, job.LoginID, holder, cred, cronJob)
}

// buildRestoreDeps resolves StorX + Google auth from job snapshot and DB credentials.
// credential_id on the job is the Google write cred (target for migration, source for in-place).
// StorX read always comes from cron_job_id (source backup).
func buildRestoreDeps(ctx context.Context, store *db.PostgresDb, job *repo.RestoreJobListingDB) (*RestoreDeps, error) {
	cfg, ok := ConfigForMethod(job.Method)
	if !ok {
		return nil, fmt.Errorf("unknown method %s", job.Method)
	}

	deps := &RestoreDeps{
		Store:            store,
		Job:              job,
		LoginID:          job.LoginID,
		Config:           cfg,
		PhotosAlbumCache: make(map[string]*albums.Album),
		googleLimiter:    rate.NewLimiter(rate.Limit(cfg.RateLimitPerSec), int(cfg.RateLimitPerSec)+1),
		vaultSem:         make(chan struct{}, cfg.VaultConcurrency),
	}

	isMigration := repo.IsRestoreJobMigration(job)

	var cronJob *repo.CronJobListingDB
	if job.CronJobID > 0 {
		j, err := store.CronJobRepo.GetCronJobByID(job.CronJobID)
		if err == nil {
			cronJob = j
		}
	}

	if cronJob != nil {
		deps.CronJob = cronJob
		deps.StorxRecovery = storxrefresh.NewRecovery(store, cronJob)
		deps.AccessGrant = strings.TrimSpace(store.CronJobRepo.ResolvedStorxToken(cronJob))
		if !isMigration {
			deps.RefreshToken = strings.TrimSpace(store.CronJobRepo.ResolvedRefreshToken(cronJob))
		}
		if deps.AccessGrant == "" {
			if cid := repo.JobCredentialID(cronJob); cid > 0 {
				if c, err := store.CredentialRepo.GetByID(cid); err == nil && c != nil {
					deps.AccessGrant = strings.TrimSpace(c.StorxToken)
				}
			}
		}
	}

	if !isMigration && deps.AccessGrant == "" && job.CredentialID > 0 {
		if c, err := store.CredentialRepo.GetByID(job.CredentialID); err == nil && c != nil {
			deps.AccessGrant = strings.TrimSpace(c.StorxToken)
			if deps.RefreshToken == "" {
				deps.RefreshToken = strings.TrimSpace(c.RefreshToken)
			}
		}
	}

	if job.CredentialID > 0 {
		if !(isMigration && migrationDWDJobTarget(store, job)) {
			writeCred, err := store.CredentialRepo.GetByID(job.CredentialID)
			if err != nil {
				return nil, fmt.Errorf("credential not found: %w", err)
			}
			deps.WriteCred = writeCred
			if isMigration {
				deps.RefreshToken = strings.TrimSpace(writeCred.RefreshToken)
			} else if deps.RefreshToken == "" {
				deps.RefreshToken = strings.TrimSpace(writeCred.RefreshToken)
			}
		}
	}

	if deps.AccessGrant == "" && deps.StorxRecovery != nil {
		if err := checkJobContinuable(store, job.ID); err != nil {
			return nil, err
		}
		grant, continueOK, refreshErr := deps.StorxRecovery.OnStorxError(ctx, fmt.Errorf("storx access grant not found"))
		if refreshErr != nil {
			return nil, refreshErr
		}
		if !continueOK || strings.TrimSpace(grant) == "" {
			return nil, fmt.Errorf("storx access grant not found")
		}
		deps.AccessGrant = strings.TrimSpace(grant)
	}

	if deps.AuthMode == "" {
		deps.AuthMode = AuthModeForJob(store, job)
	}

	if isMigration && deps.AuthMode == RestoreAuthModeDWD {
		deps.GoogleWriteSubject = strings.TrimSpace(job.TargetEmail)
	}

	if deps.AuthMode == RestoreAuthModeDWD {
		return deps, nil
	}

	if IsMicrosoftRestoreMethod(job.Method) {
		if err := deps.mintMicrosoftAccessToken(ctx); err != nil {
			return nil, err
		}
		return deps, nil
	}

	if err := deps.mintGoogleAccessToken(ctx); err != nil {
		return nil, err
	}
	return deps, nil
}

