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
	inviteRestore bool,
	inviteAccessToken string,
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
		if inviteRestore {
			// Invited members cannot use the owner's admin consent; they sign in as the destination.
			return evaluateInviteMicrosoftReadiness(ctx, out, service, method, targetEmail, inviteAccessToken)
		}
		return evaluateMigrationReadiness(ctx, store, out, userID, projectID, service, method, cronJob, targetEmail)
	}

	if inviteRestore {
		// Same as Google Workspace invite: member signs in as the mailbox (no admin consent).
		return evaluateInviteMicrosoftReadiness(ctx, out, service, method, loginID, inviteAccessToken)
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

// evaluateInviteMicrosoftReadiness checks the Microsoft account the invited member just signed into.
// It never asks for admin consent / application roles.
func evaluateInviteMicrosoftReadiness(
	ctx context.Context,
	out *restore.ReadinessResult,
	service, method, probeEmail, accessToken string,
) (*restore.ReadinessResult, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		out.Ready = false
		out.Reason = restore.ReadinessReasonInviteOAuthRequired
		out.Message = restore.MsgReadinessInviteOAuthRequiredMicrosoft
		out.ReconnectHint = "Sign in with Microsoft as the mailbox being restored."
		return out, nil
	}

	required := microsoftRestoreScopesForMethod(method)
	granted := microsoftGrantedScopes(accessToken, "")
	out.GrantedScopes = granted
	if len(required) > 0 && !delegatedRestoreScopesOK(method, granted, required) {
		missing := microsoftMissingDelegatedScopes(granted, required)
		if len(missing) == 0 {
			missing = required
		}
		out.Ready = false
		out.Reason = restore.ReadinessReasonMissingPermissions
		out.MissingPermissions = restore.OAuthMissingList(service, missing)
		out.Message = "Microsoft sign-in is missing permission to restore this service. Sign in again and allow the requested access."
		out.ReconnectHint = "Sign in with Microsoft as the mailbox being restored."
		return out, nil
	}

	if err := outlook.ValidateGraphAccessTokenForRestore(ctx, accessToken); err != nil {
		out.Ready = false
		out.Reason = restore.ReadinessReasonTokenRefreshFailed
		out.Message = "Microsoft sign-in could not be verified. Sign in with Microsoft again, then retry restore."
		return out, nil
	}
	_ = probeEmail // mailbox match is enforced at Graph write time for opaque MSA tokens
	out.Ready = true
	return out, nil
}

func evaluateCredentialReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID, service, method string,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
) (*restore.ReadinessResult, error) {
	// Corporate child mailboxes need application Mail.ReadWrite; Grant Access only writes the admin.
	// Migrations never use delegated Grant Access (destination is another mailbox).
	allowDelegated := !out.Migration && !isOtherMailboxRestore(out.LoginID, cred)
	tc, err := resolveRestoreTenant(ctx, store, userID, cred, tenantID, method, "", allowDelegated)
	if err != nil {
		out.Ready = false
		out.Reason = restore.ReadinessReasonTokenRefreshFailed
		out.Message = restore.MsgReadinessMicrosoftRefreshInvalid
		out.ReconnectHint = "Reconnect the Microsoft account (auto-sync) or complete restore Grant Access so write scopes are granted"
		if restore.IsMicrosoftRefreshRevoked(err) {
			restore.ApplyMicrosoftRestoreRevoked(ctx, store, userID, out)
		}
		return out, nil
	}

	if tc.Application {
		// Temporarily disabled — Teams restore needs a user token (keep for later).
		// if method == "outlook_teams" {
		// 	grant, hint := microsoftRestoreGrantScopes(method, nil)
		// 	markMissingRestorePermissions(out, service, grant, hint)
		// 	out.Message = ErrTeamsRestoreNeedsUser.Error()
		// 	return out, nil
		// }
		if method == "outlook_teams" || method == "outlook_sharepoint" {
			out.Ready = false
			out.Reason = restore.ReadinessReasonMigrationUnsupported
			out.Message = "Teams and SharePoint restore is temporarily disabled"
			return out, nil
		}
		// App-only consent is often read-only. Require the write role, the same gate a
		// migration already uses, before the job is marked ready.
		missing := outlook.MissingRestoreWriteRoles(tc.Token, method)
		if len(missing) > 0 {
			// In-place corporate restore needs application write roles (not delegated Grant Access).
			markMigrationNeedsAppWrite(out, service, method, out.LoginID, missing)
			return out, nil
		}
		// Confirm the corporate mailbox/OneDrive exists before queueing restore-all.
		if err := probeApplicationMailboxTarget(ctx, tc.Token, method, out.LoginID); err != nil {
			if errors.Is(err, outlook.ErrRestoreTargetUnavailable) {
				markRestoreTargetUnavailable(out, method, out.LoginID, err)
				return out, nil
			}
			if isRestoreDestinationAccessDenied(err) {
				roles := outlook.RestoreWriteRoles[method]
				markMigrationNeedsAppWrite(out, service, method, out.LoginID, roles)
				return out, nil
			}
			return nil, err
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

// probeApplicationMailboxTarget checks Graph can open the corporate mailbox (or calendar /
// contacts / OneDrive) for loginID before restore-all is marked ready.
func probeApplicationMailboxTarget(ctx context.Context, accessToken, method, loginID string) error {
	loginID = strings.TrimSpace(loginID)
	if loginID == "" {
		return nil
	}
	switch method {
	case "outlook", "outlook_calendar", "outlook_contacts", "outlook_onedrive":
	default:
		return nil
	}
	userBase, err := outlook.UserBaseURL(loginID, "", "", true)
	if err != nil {
		return err
	}
	return checkRestoreTargetFn(ctx, accessToken, userBase, method)
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
	// Temporarily disabled — Teams / SharePoint / tenant site restore (keep for later).
	// if method == "outlook_sharepoint" {
	// 	return evaluateSharePointSiteReadiness(ctx, store, out, userID, job, targetEmail)
	// }
	// if method == "outlook_teams" {
	// 	out.Ready = false
	// 	out.Reason = restore.ReadinessReasonMigrationUnsupported
	// 	out.Message = "Teams only restores into the team it was backed up from"
	// 	return out, nil
	// }
	if method == "outlook_sharepoint" || method == "outlook_teams" {
		out.Ready = false
		out.Reason = restore.ReadinessReasonMigrationUnsupported
		out.Message = "Teams and SharePoint restore is temporarily disabled"
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

	var cred *repo.GoogleBackupCredentialDB
	if org.CredentialID > 0 {
		cred, _ = store.CredentialRepo.GetByID(org.CredentialID)
	}
	if cred != nil {
		out.OAuthHolderEmail = strings.TrimSpace(cred.Email)
	}
	// Other-mailbox restore must use the org app-only token with application write roles
	// (e.g. Mail.ReadWrite). Delegated Grant Access on admin only writes admin's own mailbox —
	// Graph AccessDenied on /users/{B} is expected and must not loop Grant Access.
	if missing := outlook.MissingRestoreWriteRoles(org.Token, method); len(missing) > 0 {
		markMigrationNeedsAppWrite(out, service, method, out.TargetEmail, missing)
		return out, nil
	}
	userBase, err := outlook.UserBaseURL(user.ObjectID, "", "", true)
	if err != nil {
		return nil, err
	}
	if err := checkRestoreTargetFn(ctx, org.Token, userBase, method); err != nil {
		if errors.Is(err, outlook.ErrRestoreTargetUnavailable) {
			markRestoreTargetUnavailable(out, method, out.TargetEmail, err)
			return out, nil
		}
		if isRestoreDestinationAccessDenied(err) {
			roles := outlook.RestoreWriteRoles[method]
			markMigrationNeedsAppWrite(out, service, method, out.TargetEmail, roles)
			return out, nil
		}
		return nil, err
	}
	out.Ready = true
	return out, nil
}

func isRestoreDestinationAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "erroraccessdenied") ||
		strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "accessdenied")
}

// markRestoreTargetUnavailable sets a clear Microsoft-user message (no raw Graph codes).
func markRestoreTargetUnavailable(out *restore.ReadinessResult, method, targetEmail string, err error) {
	out.Ready = false
	out.Reason = restore.ReadinessReasonTargetUnavailable
	out.Message, out.ReconnectHint = restoreTargetUnavailableCopy(method, targetEmail, err)
}

// markMigrationNeedsAppWrite asks for admin consent of application write roles (not Grant Access).
func markMigrationNeedsAppWrite(out *restore.ReadinessResult, service, method, targetEmail string, roles []string) {
	if len(roles) == 0 {
		roles = outlook.RestoreWriteRoles[method]
	}
	if len(roles) == 0 {
		roles = []string{"Mail.ReadWrite"}
	}
	out.Ready = false
	out.Reason = restore.ReadinessReasonMissingPermissions
	perms := make([]restore.MissingPermission, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		perms = append(perms, restore.MissingPermission{
			Type:        "application",
			Service:     service,
			Scope:       role,
			Description: "Application permission required to restore into another mailbox",
		})
	}
	out.MissingPermissions = perms
	who := strings.TrimSpace(targetEmail)
	if who == "" {
		who = "another user's mailbox"
	}
	roleList := strings.Join(roles, ", ")
	out.Message = "Restoring into " + who + " needs organization application permission " + roleList +
		". Grant Access on the admin sign-in only writes the admin mailbox. Org backup consent is often Mail.Read only — that is not enough for another user's mailbox."
	out.ReconnectHint = "In the StorX Azure app add " + roleList +
		" as Application permissions, then Grant admin consent again for this tenant. Re-consent without those app roles will keep asking."
}

func restoreTargetUnavailableCopy(method, targetEmail string, err error) (message, hint string) {
	who := strings.TrimSpace(targetEmail)
	if who == "" {
		who = "That Microsoft user"
	}
	raw := ""
	if err != nil {
		raw = strings.ToLower(err.Error())
	}
	switch method {
	case "outlook_onedrive":
		message = who + " has no OneDrive for Business yet (missing license or not provisioned)."
		hint = "Ask your Microsoft admin to assign a Microsoft 365 license that includes OneDrive, wait for OneDrive to finish setting up, then pick this user again — or choose another destination."
	case "outlook_calendar":
		message = who + " has no Exchange Online mailbox for Calendar."
		hint = "Ask your Microsoft admin to assign Exchange Online (or a Microsoft 365 license with Exchange), then pick this user again — or choose a licensed cloud mailbox."
	case "outlook_contacts":
		message = who + " has no Exchange Online mailbox for Contacts."
		hint = "Ask your Microsoft admin to assign Exchange Online (or a Microsoft 365 license with Exchange), then pick this user again — or choose a licensed cloud mailbox."
	default:
		message = who + " has no Exchange Online mailbox (no license, inactive, soft-deleted, or on-premises)."
		hint = "Ask your Microsoft admin to assign Exchange Online (or a Microsoft 365 license with Exchange), wait for the mailbox to finish creating, then pick this user again — or choose a licensed cloud mailbox like admin@ or sales@."
	}
	// Soft-deleted / inactive: slightly sharper hint when Graph says so.
	if strings.Contains(raw, "soft-deleted") || strings.Contains(raw, "inactive") {
		hint = "This mailbox looks inactive or soft-deleted in Microsoft 365. Restore it in the Microsoft 365 admin center, or choose another licensed cloud mailbox."
	}
	if strings.Contains(raw, "on-premise") || strings.Contains(raw, "on-premises") {
		hint = "This mailbox is hosted on-premises, so Microsoft Graph cannot restore into it. Use an Exchange Online (cloud) mailbox instead."
	}
	return message, hint
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
