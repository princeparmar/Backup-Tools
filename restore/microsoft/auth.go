package microsoft

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
)

var resolveTenantFn = mstenant.Resolve

// delegatedRestoreTokenFn mints a signed-in user token (overridden in tests).
var delegatedRestoreTokenFn = outlook.AuthTokenForTenantScope

// MintAccessToken fills MicrosoftToken for the write credential in the restore job's tenant.
// In place that tenant is the backup's tenant. A migration uses the destination tenant.
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
	if err := prepareMigrationWriteUser(ctx, d); err != nil {
		d.MicrosoftToken = ""
		return err
	}
	return nil
}

// prepareMigrationWriteUser resolves the destination mailbox once for an app-only migration.
// SharePoint names a site in TargetEmail, not a user. Delegated migrations write /me.
func prepareMigrationWriteUser(ctx context.Context, d *restore.RestoreDeps) error {
	if d == nil {
		return nil
	}
	if d.Job == nil || !repo.IsRestoreJobMigration(d.Job) || !d.MicrosoftApplication || d.Job.Method == "outlook_sharepoint" {
		d.MicrosoftWriteUserBase = ""
		return nil
	}
	if d.Store == nil {
		return fmt.Errorf("migration restore has no destination lookup")
	}
	_, user, err := findOrganizationTargetFn(ctx, d.Store, d.Job.UserID, d.Job.TargetEmail)
	if err != nil {
		return err
	}
	if user == nil || strings.TrimSpace(user.ObjectID) == "" {
		return fmt.Errorf("restore destination %s was not found in a connected organization", d.Job.TargetEmail)
	}
	base, err := outlook.UserBaseURL(user.ObjectID, "", "", true)
	if err != nil {
		return err
	}
	d.MicrosoftWriteUserBase = base
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
	if del := delegatedRestoreWrite(tc, cred, tenantID, method, refreshFallback); del != nil {
		return del, nil
	}
	return tc, nil
}

// delegatedRestoreWrite uses the signed-in mailbox token when the organization app token cannot
// write (backup is often Mail.Read only; restore needs Mail.ReadWrite). Grant Access stores that
// write grant on the refresh token; admin consent of read-only app roles does not.
func delegatedRestoreWrite(app *mstenant.Context, cred *repo.GoogleBackupCredentialDB, tenantID, method, refreshFallback string) *mstenant.Context {
	if app == nil || !app.Application {
		return nil
	}
	if method != "outlook_teams" && len(outlook.MissingRestoreWriteRoles(app.Token, method)) == 0 {
		return nil
	}
	refresh := strings.TrimSpace(refreshFallback)
	if refresh == "" && cred != nil {
		refresh = strings.TrimSpace(cred.RefreshToken)
	}
	if refresh == "" {
		return nil
	}
	required := microsoftRestoreScopesForMethod(method)
	if len(required) == 0 {
		return nil
	}
	tok, err := delegatedRestoreTokenFn(refresh, tenantID, strings.Join(required, " "))
	if err != nil {
		tok, err = delegatedRestoreTokenFn(refresh, tenantID, "")
	}
	if err != nil || tok == nil || strings.TrimSpace(tok.AccessToken) == "" {
		return nil
	}
	granted := microsoftGrantedScopes(tok.AccessToken, tok.Scope)
	if method == "outlook_teams" {
		missing := microsoftMissingDelegatedScopes(granted, required)
		if len(missing) == len(required) {
			return nil
		}
	} else if !delegatedRestoreScopesOK(method, granted, required) {
		return nil
	}
	out := *app
	out.Token = tok.AccessToken
	out.TokenScope = tok.Scope
	out.Application = false
	return &out
}

func RequireToken(deps *restore.RestoreDeps) error {
	if deps == nil || strings.TrimSpace(deps.MicrosoftToken) == "" {
		return fmt.Errorf("microsoft access token missing")
	}
	return nil
}
