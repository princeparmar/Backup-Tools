package restore

import (
	"context"
	"fmt"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"golang.org/x/time/rate"
)

// StorxGrantSession holds the storx grant loaded from DB for manual item restore (no auto-refresh).
type StorxGrantSession struct {
	Grant string
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
	return &StorxGrantSession{Grant: grant}, nil
}

// DownloadObject downloads one object from StorX using the DB grant (single attempt).
func (s *StorxGrantSession) DownloadObject(ctx context.Context, bucket, objectKey string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("storx session required")
	}
	return satellite.DownloadObject(ctx, s.Grant, bucket, objectKey)
}

// AuthModeForJob derives oauth vs dwd from account_type and linked credential/cron.
func AuthModeForJob(store *db.PostgresDb, job *repo.RestoreJobListingDB) string {
	if job == nil {
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

	var cronJob *repo.CronJobListingDB
	var cred *repo.GoogleBackupCredentialDB
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
		deps.RefreshToken = strings.TrimSpace(store.CronJobRepo.ResolvedRefreshToken(cronJob))
	}
	if deps.AccessGrant == "" && job.CredentialID > 0 {
		c, err := store.CredentialRepo.GetByID(job.CredentialID)
		if err == nil {
			cred = c
			deps.AccessGrant = strings.TrimSpace(cred.StorxToken)
			if deps.RefreshToken == "" {
				deps.RefreshToken = strings.TrimSpace(cred.RefreshToken)
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

	deps.AuthMode = AuthModeForJob(store, job)

	if deps.AuthMode == RestoreAuthModeDWD {
		return deps, nil
	}

	if err := deps.mintGoogleAccessToken(ctx); err != nil {
		return nil, err
	}
	return deps, nil
}

func (d *RestoreDeps) mintGoogleAccessToken(ctx context.Context) error {
	_ = ctx
	if strings.TrimSpace(d.GoogleToken) != "" {
		return nil
	}
	rt := strings.TrimSpace(d.RefreshToken)
	if rt == "" {
		return fmt.Errorf("google refresh token missing")
	}
	tok, err := google.AuthTokenUsingRefreshToken(rt)
	if err != nil {
		return fmt.Errorf("google token refresh: %w", err)
	}
	d.GoogleToken = tok
	return nil
}

// RefreshGoogleAccessToken forces a new access token (401 retry path).
func (d *RestoreDeps) RefreshGoogleAccessToken(ctx context.Context) error {
	d.GoogleToken = ""
	return d.mintGoogleAccessToken(ctx)
}
