package microsoft

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
)

// evaluateMicrosoftReadiness mirrors Google prepare checks for Microsoft Graph restore-all.
func EvaluateReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID, projectID, loginID, service, method, targetEmail, tenantID string,
) (*restore.ReadinessResult, error) {
	cronJob, err := restore.FindMicrosoftRestoreJob(store, userID, method, tenantID, loginID)
	if errors.Is(err, restore.ErrMicrosoftTenantRequired) {
		out.Ready = false
		out.Reason = restore.ReadinessReasonTenantRequired
		out.Message = err.Error()
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if cronJob == nil {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoBackupJob
		out.Message = restore.MsgReadinessNoBackupJob
		return out, nil
	}
	out.CronJobID = cronJob.ID
	out.TenantID = strings.ToLower(cronJob.TenantID)

	credID := repo.JobCredentialID(cronJob)
	if credID == 0 {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoCredential
		out.Message = restore.MsgReadinessNoCredential
		return out, nil
	}
	sourceCred, err := store.CredentialRepo.GetByID(credID)
	if err != nil {
		return nil, err
	}
	out.AccountType = strings.TrimSpace(sourceCred.AccountType)
	if out.AccountType == "" {
		out.AccountType = outlook.AccountTypePersonal
	}
	out.OAuthHolderEmail = strings.TrimSpace(sourceCred.Email)
	out.AuthMode = restore.RestoreAuthModeOAuth

	cfg, ok := restore.ConfigForMethod(method)
	if !ok {
		return nil, fmt.Errorf("unknown method %s", method)
	}
	prefix := restore.RestoreKeyPrefix(&repo.RestoreJobListingDB{Method: method, LoginID: loginID}, cronJob)
	count, err := store.SyncedObjectRepo.CountSyncedObjectsForRestore(
		userID, cfg.Bucket, cfg.Source, cfg.ObjectType, prefix)
	if err != nil {
		return nil, err
	}
	out.BackupItemCount = uint(count)
	if count == 0 {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoBackupData
		out.Message = restore.MsgReadinessNoBackupData
		return out, nil
	}

	storx := strings.TrimSpace(store.CronJobRepo.ResolvedStorxToken(cronJob))
	if storx == "" {
		storx = strings.TrimSpace(sourceCred.StorxToken)
	}
	if storx == "" {
		recovery := storxrefresh.NewRecovery(store, cronJob)
		grant, continueOK, refreshErr := recovery.OnStorxError(ctx, fmt.Errorf("storx access grant not found"))
		if refreshErr != nil || !continueOK {
			out.Ready = false
			out.Reason = restore.ReadinessReasonStorxMissing
			if refreshErr != nil {
				out.Message = refreshErr.Error()
			} else {
				out.Message = restore.MsgReadinessStorxMissing
			}
			out.ReconnectHint = "Please contact support if StorX access cannot be restored"
			return out, nil
		}
		storx = strings.TrimSpace(grant)
		if storx == "" {
			storx = strings.TrimSpace(store.CronJobRepo.ResolvedStorxToken(cronJob))
		}
	}
	if storx == "" {
		out.Ready = false
		out.Reason = restore.ReadinessReasonStorxMissing
		out.Message = restore.MsgReadinessStorxMissing
		out.MissingPermissions = []restore.MissingPermission{{Type: "storx", Service: service, Description: "storx_token required"}}
		out.ReconnectHint = "Use dashboard auto-sync reconnect to update StorX grant"
		return out, nil
	}

	if restore.IsMigrationRestore(loginID, targetEmail) {
		out.Migration = true
		out.TargetEmail = targetEmail
		return evaluateMigrationReadiness(ctx, store, out, userID, projectID, service, method, cronJob, targetEmail)
	}

	writeCred := sourceCred
	out.CredentialID = writeCred.ID
	if err := restore.CheckMicrosoftTenantGuard(store, out.TenantID, cronJob, writeCred); err != nil {
		if !errors.Is(err, restore.ErrMicrosoftTenantMismatch) {
			return nil, err
		}
		out.Ready = false
		out.Reason = restore.ReadinessReasonTenantMismatch
		out.Message = err.Error()
		out.ReconnectHint = "Microsoft restore stays inside the backup's tenant; connect the target account to that tenant"
		return out, nil
	}
	return evaluateCredentialReadiness(ctx, store, out, userID, service, method, writeCred, out.TenantID)
}

func evaluateCredentialReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID, service, method string,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
) (*restore.ReadinessResult, error) {
	tc, err := resolveRestoreTenant(ctx, store, userID, cred, tenantID, method, "")
	if err != nil {
		out.Ready = false
		out.Reason = restore.ReadinessReasonTokenRefreshFailed
		out.Message = restore.MsgReadinessRefreshInvalid
		out.ReconnectHint = "Reconnect the Microsoft account (auto-sync) or complete restore OAuth so write scopes are granted"
		return out, nil
	}

	if tc.Application {
		if method == "outlook_teams" {
			grant, hint := microsoftRestoreGrantScopes(method, nil)
			markMissingRestorePermissions(out, service, grant, hint)
			out.Message = ErrTeamsRestoreNeedsUser.Error()
			return out, nil
		}
		// App-only consent is often read-only. Require the write role, the same gate a
		// migration already uses, before the job is marked ready.
		missing := outlook.MissingRestoreWriteRoles(tc.Token, method)
		if len(missing) > 0 {
			grant, hint := microsoftRestoreGrantScopes(method, missing)
			markMissingRestorePermissions(out, service, grant, hint)
			return out, nil
		}
		out.Ready = true
		return out, nil
	}
	required := microsoftRestoreScopesForMethod(method)
	// Personal Outlook access tokens are often opaque (not JWT) — use token-endpoint scope.
	granted := microsoftGrantedScopes(tc.Token, tc.TokenScope)
	out.GrantedScopes = granted
	if !delegatedRestoreScopesOK(method, granted, required) {
		missing := microsoftMissingDelegatedScopes(granted, required)
		if len(missing) == 0 {
			missing = microsoftRestoreScopesForMethod(method)
		}
		markMissingRestorePermissions(out, service, missing, "Grant Access with "+strings.Join(missing, ", ")+", then retry")
		return out, nil
	}

	out.Ready = true
	return out, nil
}

func markMissingRestorePermissions(out *restore.ReadinessResult, service string, missing []string, hint string) {
	out.Ready = false
	out.Reason = restore.ReadinessReasonMissingPermissions
	out.MissingPermissions = restore.OAuthMissingList(service, missing)
	out.Message = restore.MsgReadinessMissingScopes
	out.ReconnectHint = hint
}

// evaluateMigrationReadiness checks a restore into another mailbox. A connected sign-in for that
// address writes with its own token. Otherwise an organization the user connected in admin mode
// can receive the restore for any enabled member, written with that tenant's app-only token.
// Teams stays in the team it was backed up from. SharePoint can be written into another site.
func evaluateMigrationReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID, projectID, service, method string,
	job *repo.CronJobListingDB,
	targetEmail string,
) (*restore.ReadinessResult, error) {
	if job == nil {
		return nil, fmt.Errorf("microsoft migration restore has no source backup job")
	}
	out.Migration = true
	out.TargetEmail = strings.TrimSpace(targetEmail)
	if method == "outlook_sharepoint" {
		return evaluateSharePointSiteReadiness(ctx, store, out, userID, job, targetEmail)
	}
	if method == "outlook_teams" {
		out.Ready = false
		out.Reason = restore.ReadinessReasonMigrationUnsupported
		out.Message = "Teams only restores into the team it was backed up from"
		return out, nil
	}

	targetCred, credOK, credErr := store.CredentialRepo.FindByUserProjectAndEmail(userID, projectID, targetEmail)
	if credErr != nil {
		return nil, credErr
	}
	if credOK && targetCred != nil && targetCred.IsMicrosoft() {
		out.CredentialID = targetCred.ID
		out.TenantID = strings.ToLower(strings.TrimSpace(targetCred.HomeTenantID()))
		out.AccountType = strings.TrimSpace(targetCred.AccountType)
		if out.AccountType == "" {
			out.AccountType = outlook.AccountTypePersonal
		}
		out.OAuthHolderEmail = strings.TrimSpace(targetCred.Email)
		out.AuthMode = restore.RestoreAuthModeOAuth
		return evaluateCredentialReadiness(ctx, store, out, userID, service, method, targetCred, out.TenantID)
	}

	org, user, err := findOrganizationTargetFn(ctx, store, userID, targetEmail)
	if err != nil {
		return nil, err
	}
	if org == nil || user == nil || strings.TrimSpace(user.ObjectID) == "" {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoCredential
		out.Message = restore.MsgReadinessNoTargetCredential
		out.ReconnectHint = "Connect the target Microsoft account, or connect its organization with admin consent"
		return out, nil
	}
	if !user.AccountEnabled {
		out.Ready = false
		out.Reason = restore.ReadinessReasonTargetUnavailable
		out.Message = "the destination user is disabled"
		return out, nil
	}

	out.CredentialID = org.CredentialID
	out.TenantID = strings.ToLower(strings.TrimSpace(org.TenantID))
	out.AuthMode = restore.RestoreAuthModeOAuth
	writeToken := org.Token
	if missing := outlook.MissingRestoreWriteRoles(org.Token, method); len(missing) > 0 {
		var cred *repo.GoogleBackupCredentialDB
		if org.CredentialID > 0 {
			cred, _ = store.CredentialRepo.GetByID(org.CredentialID)
		}
		del := delegatedRestoreWrite(&mstenant.Context{
			Token: org.Token, Application: true, TenantID: org.TenantID,
		}, cred, org.TenantID, method, "")
		if del == nil {
			grant, hint := microsoftRestoreGrantScopes(method, missing)
			markMissingRestorePermissions(out, service, grant, hint)
			return out, nil
		}
		writeToken = del.Token
	}
	userBase, err := outlook.UserBaseURL(user.ObjectID, "", "", true)
	if err != nil {
		return nil, err
	}
	if err := checkRestoreTargetFn(ctx, writeToken, userBase, method); err != nil {
		if errors.Is(err, outlook.ErrRestoreTargetUnavailable) {
			out.Ready = false
			out.Reason = restore.ReadinessReasonTargetUnavailable
			out.Message = err.Error()
			out.ReconnectHint = "The destination user has no mailbox or OneDrive for this service"
			return out, nil
		}
		return nil, err
	}
	out.Ready = true
	return out, nil
}

// checkSharePointSiteFn confirms the destination site has a document library (overridden in tests).
var checkSharePointSiteFn = outlook.SiteDefaultDriveID

// evaluateSharePointSiteReadiness checks a restore of a site's libraries into another SharePoint
// site. The destination site must belong to an organization connected with admin consent, and that
// connection must be allowed to write site libraries.
func evaluateSharePointSiteReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID string,
	job *repo.CronJobListingDB,
	siteID string,
) (*restore.ReadinessResult, error) {
	siteID = outlook.GraphSharePointSiteID(siteID)
	if siteID == "" || strings.Contains(siteID, "@") {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoCredential
		out.Message = "Choose a SharePoint site to restore into"
		out.ReconnectHint = "Pick a site in an organization connected with admin consent"
		return out, nil
	}
	tenants, err := ListOrganizationTenants(ctx, store, userID)
	if err != nil {
		return nil, err
	}
	var candidates []OrganizationTenant
	for _, t := range tenants {
		if job != nil && strings.EqualFold(t.TenantID, job.TenantID) {
			candidates = append([]OrganizationTenant{t}, candidates...)
			continue
		}
		candidates = append(candidates, t)
	}
	if len(candidates) == 0 {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoCredential
		out.Message = restore.MsgReadinessNoTargetCredential
		out.ReconnectHint = "Connect the destination organization with admin consent"
		return out, nil
	}
	var (
		lastErr       error
		sawNotFound   bool
		firstMissing  bool
		missingTenant *OrganizationTenant
		missingRoles  []string
	)
	for i := range candidates {
		tenant := &candidates[i]
		writeToken := tenant.Token
		if missing := outlook.MissingRestoreWriteRoles(tenant.Token, "outlook_sharepoint"); len(missing) > 0 {
			var cred *repo.GoogleBackupCredentialDB
			if tenant.CredentialID > 0 {
				cred, _ = store.CredentialRepo.GetByID(tenant.CredentialID)
			}
			del := delegatedRestoreWrite(&mstenant.Context{
				Token: tenant.Token, Application: true, TenantID: tenant.TenantID,
			}, cred, tenant.TenantID, "outlook_sharepoint", "")
			if del == nil {
				if missingTenant == nil {
					firstMissing = i == 0
					missingTenant = tenant
					missingRoles, _ = microsoftRestoreGrantScopes("outlook_sharepoint", missing)
				}
				continue
			}
			writeToken = del.Token
		}
		if _, err := checkSharePointSiteFn(ctx, writeToken, siteID); err != nil {
			if errors.Is(err, outlook.ErrSharePointSiteNoDocumentLibrary) {
				out.Ready = false
				out.Reason = restore.ReadinessReasonTargetUnavailable
				out.Message = "The destination site has no document library"
				out.ReconnectHint = "Choose a SharePoint site that has a document library"
				return out, nil
			}
			if strings.Contains(err.Error(), "http 404") {
				sawNotFound = true
				continue
			}
			lastErr = err
			continue
		}
		out.Ready = true
		out.Reason = ""
		out.Message = ""
		out.MissingPermissions = nil
		out.CredentialID = tenant.CredentialID
		out.TenantID = strings.ToLower(strings.TrimSpace(tenant.TenantID))
		out.TargetEmail = siteID
		out.AuthMode = restore.RestoreAuthModeOAuth
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	// The organization that owns the backup is tried first. If that connection cannot write
	// libraries, say so. A later 404 only means some other organization does not contain the site.
	if missingTenant != nil && (firstMissing || !sawNotFound) {
		out.CredentialID = missingTenant.CredentialID
		out.TenantID = strings.ToLower(strings.TrimSpace(missingTenant.TenantID))
		grant, hint := microsoftRestoreGrantScopes("outlook_sharepoint", missingRoles)
		markMissingRestorePermissions(out, out.Service, grant, hint)
		return out, nil
	}
	out.Ready = false
	out.Reason = restore.ReadinessReasonTargetUnavailable
	out.Message = "The destination site was not found in a connected organization"
	return out, nil
}
