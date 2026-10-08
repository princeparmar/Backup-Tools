package microsoft

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
)

var resolveTenantFn = mstenant.Resolve

// MintAccessToken fills MicrosoftToken for the write credential in the restore job's tenant
// (equal to the backup job's tenant; see restore.CheckMicrosoftTenantGuard).
func MintAccessToken(ctx context.Context, d *restore.RestoreDeps) error {
	if strings.TrimSpace(d.MicrosoftToken) != "" {
		return nil
	}
	cred := d.WriteCred
	if cred == nil && d.Job != nil && d.Job.CredentialID > 0 && d.Store != nil {
		if c, err := d.Store.CredentialRepo.GetByID(d.Job.CredentialID); err == nil {
			cred = c
			d.WriteCred = c
		}
	}
	userID, method, tenantID := "", "", ""
	if d.Job != nil {
		userID, method, tenantID = d.Job.UserID, d.Job.Method, d.Job.TenantID
	}
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("%w: the restore job has no tenant", restore.ErrMicrosoftTenantMismatch)
	}
	tc, err := resolveRestoreTenant(ctx, d.Store, userID, cred, tenantID, method, d.RefreshToken)
	if err != nil {
		return err
	}
	d.MicrosoftToken = tc.Token
	d.MicrosoftApplication = tc.Application
	return nil
}

// RefreshAccessToken forces a new Graph access token (401 retry path).
func RefreshAccessToken(ctx context.Context, d *restore.RestoreDeps) error {
	d.MicrosoftToken = ""
	return MintAccessToken(ctx, d)
}

func resolveRestoreTenant(ctx context.Context, store *db.PostgresDb, userID string, cred *repo.GoogleBackupCredentialDB, tenantID, method, refreshFallback string) (*mstenant.Context, error) {
	if cred == nil {
		return nil, fmt.Errorf("microsoft write credential missing")
	}
	tc, err := resolveTenantFn(ctx, store, mstenant.Request{
		UserID:       userID,
		CredentialID: cred.ID,
		TenantID:     tenantID,
		Capability:   mstenant.CapabilityForService(method),
		RefreshToken: refreshFallback,
	})
	if err != nil {
		return nil, fmt.Errorf("microsoft token: %w", err)
	}
	return tc, nil
}

func RequireToken(deps *restore.RestoreDeps) error {
	if deps == nil || strings.TrimSpace(deps.MicrosoftToken) == "" {
		return fmt.Errorf("microsoft access token missing")
	}
	return nil
}
