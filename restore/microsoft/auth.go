package microsoft

import (
	"github.com/StorX2-0/Backup-Tools/restore"
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// mintMicrosoftAccessToken fills MicrosoftToken from stored refresh or app-only credentials.
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
	tok, err := mintMicrosoftTokenFromCredential(ctx, cred, d.RefreshToken)
	if err != nil {
		return err
	}
	d.MicrosoftToken = tok
	return nil
}

// RefreshMicrosoftAccessToken forces a new Graph access token (401 retry path).
func RefreshAccessToken(ctx context.Context, d *restore.RestoreDeps) error {
	d.MicrosoftToken = ""
	return MintAccessToken(ctx, d)
}

func mintMicrosoftTokenFromCredential(ctx context.Context, cred *repo.GoogleBackupCredentialDB, refreshFallback string) (string, error) {
	tok, _, err := mintMicrosoftTokenAndScopeFromCredential(ctx, cred, refreshFallback)
	return tok, err
}

// mintMicrosoftTokenAndScopeFromCredential returns access token plus OAuth token-endpoint scope
// (needed when the access token is opaque and has no JWT scp claim).
func mintMicrosoftTokenAndScopeFromCredential(ctx context.Context, cred *repo.GoogleBackupCredentialDB, refreshFallback string) (string, string, error) {
	if cred != nil && strings.EqualFold(strings.TrimSpace(cred.MicrosoftAuthMode), outlook.MicrosoftAuthModeApplication) {
		tenant := strings.TrimSpace(cred.TenantID)
		clientID := strings.TrimSpace(cred.MicrosoftAppClientID)
		secret := strings.TrimSpace(cred.MicrosoftAppClientSecret)
		tok, err := outlook.AcquireMicrosoftAppOnlyToken(ctx, tenant, clientID, secret)
		if err != nil {
			return "", "", fmt.Errorf("microsoft app-only token: %w", err)
		}
		return tok, "", nil
	}
	rt := strings.TrimSpace(refreshFallback)
	if rt == "" && cred != nil {
		rt = strings.TrimSpace(cred.RefreshToken)
	}
	if rt == "" {
		return "", "", fmt.Errorf("microsoft refresh token missing")
	}
	tokRes, err := outlook.AuthTokenResponseUsingRefreshToken(rt)
	if err != nil {
		return "", "", fmt.Errorf("microsoft token refresh: %w", err)
	}
	return tokRes.AccessToken, tokRes.Scope, nil
}

func RequireToken(deps *restore.RestoreDeps) error {
	if deps == nil || strings.TrimSpace(deps.MicrosoftToken) == "" {
		return fmt.Errorf("microsoft access token missing")
	}
	return nil
}

