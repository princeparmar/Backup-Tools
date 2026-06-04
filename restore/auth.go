package restore

import (
	"context"
	"fmt"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"golang.org/x/time/rate"
)

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
		AuthMode:         job.AuthMode,
		PhotosAlbumCache: make(map[string]*albums.Album),
		googleLimiter:    rate.NewLimiter(rate.Limit(cfg.RateLimitPerSec), int(cfg.RateLimitPerSec)+1),
		vaultSem:         make(chan struct{}, cfg.VaultConcurrency),
	}

	// Legacy fallback: tokens on job row
	if job.CredentialID == 0 {
		deps.AccessGrant = strings.TrimSpace(job.StorxToken)
		if job.InputData != nil && job.InputData.Json() != nil {
			if t, ok := (*job.InputData.Json())["google_access_token"].(string); ok {
				deps.GoogleToken = strings.TrimSpace(t)
			}
		}
		if deps.AccessGrant != "" && (deps.AuthMode == "" || deps.GoogleToken != "" || deps.AuthMode == repo.RestoreAuthModeDWD) {
			return deps, nil
		}
	}

	var cronJob *repo.CronJobListingDB
	if job.CronJobID > 0 {
		j, err := store.CronJobRepo.GetCronJobByID(job.CronJobID)
		if err == nil {
			cronJob = j
		}
	}

	if cronJob != nil {
		deps.AccessGrant = strings.TrimSpace(store.CronJobRepo.ResolvedStorxToken(cronJob))
		deps.RefreshToken = strings.TrimSpace(store.CronJobRepo.ResolvedRefreshToken(cronJob))
	}
	if deps.AccessGrant == "" && job.CredentialID > 0 {
		cred, err := store.CredentialRepo.GetByID(job.CredentialID)
		if err == nil {
			deps.AccessGrant = strings.TrimSpace(cred.StorxToken)
			if deps.RefreshToken == "" {
				deps.RefreshToken = strings.TrimSpace(cred.RefreshToken)
			}
		}
	}
	if deps.AccessGrant == "" {
		deps.AccessGrant = strings.TrimSpace(job.StorxToken)
	}

	if deps.AuthMode == repo.RestoreAuthModeDWD {
		return deps, nil
	}

	if err := deps.mintGoogleAccessToken(ctx); err != nil {
		// Legacy inline token
		if deps.GoogleToken != "" {
			return deps, nil
		}
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
