package storx

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// IsUplinkError reports whether err is a missing/invalid storx grant or uplink permission failure.
func IsUplinkError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "uplink: permission") ||
		strings.Contains(msg, "uplink: invalid access") ||
		strings.Contains(msg, "storx access grant not found") ||
		strings.Contains(msg, "storx_token is required") ||
		strings.Contains(msg, "parse access grant")
}

// IsStorageLimitError reports whether err is a CyberLS/uplink quota exhaustion failure.
func IsStorageLimitError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "storage limit exceeded")
}

// RefreshAndSave mints a new storx grant via Satellite and persists it on the backup job/credential.
func RefreshAndSave(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB) (string, error) {
	if store == nil || job == nil {
		return "", fmt.Errorf("storx refresh: missing store or cron job")
	}
	userID, projectID, email, err := resolveRefreshParams(store, job)
	if err != nil {
		return "", err
	}
	grant, err := satellite.RefreshStorxToken(ctx, userID, projectID, email)
	if err != nil {
		return "", fmt.Errorf("storx refresh for user_id=%s project_id=%s: %w", userID, projectID, err)
	}
	grant = strings.TrimSpace(grant)
	if grant == "" {
		return "", fmt.Errorf("empty storx grant")
	}
	if err := SaveGrant(ctx, store, job, grant); err != nil {
		return "", err
	}
	return grant, nil
}

// SaveGrant writes a storx access grant to the linked credential or cron job row.
func SaveGrant(ctx context.Context, store *db.PostgresDb, job *repo.CronJobListingDB, grant string) error {
	grant = strings.TrimSpace(grant)
	if grant == "" {
		return fmt.Errorf("empty storx grant")
	}
	if cid := repo.JobCredentialID(job); cid > 0 {
		if err := store.CredentialRepo.UpdateTokens(cid, nil, &grant); err != nil {
			return fmt.Errorf("update credential storx_token: %w", err)
		}
		if pid, err := satellite.GetProjectIDFromAccessGrant(ctx, grant); err == nil && pid != "" {
			_ = store.CredentialRepo.UpdateStorjProjectID(cid, pid)
		}
		return nil
	}
	return store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"storx_token": grant,
	})
}

// ClearGrant removes the storx access grant from the linked credential or cron job row.
func ClearGrant(store *db.PostgresDb, job *repo.CronJobListingDB) error {
	if store == nil || job == nil {
		return nil
	}
	job.StorxToken = ""
	if cid := repo.JobCredentialID(job); cid > 0 {
		return store.CredentialRepo.ClearStorxToken(cid)
	}
	return store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{"storx_token": ""})
}

func resolveRefreshParams(store *db.PostgresDb, job *repo.CronJobListingDB) (userID, projectID, email string, err error) {
	userID = strings.TrimSpace(job.UserID)
	projectID = strings.TrimSpace(store.CronJobRepo.ResolvedStorjProjectID(job))
	email = strings.TrimSpace(job.Name)
	if cid := repo.JobCredentialID(job); cid > 0 {
		cred, credErr := store.CredentialRepo.GetByID(cid)
		if credErr == nil && cred != nil {
			if projectID == "" {
				projectID = strings.TrimSpace(cred.StorjProjectID)
			}
			if e := strings.TrimSpace(cred.Email); e != "" {
				email = e
			}
		}
	}
	if projectID == "" {
		return "", "", "", fmt.Errorf("storj project_id required for storx refresh")
	}
	return userID, projectID, email, nil
}
