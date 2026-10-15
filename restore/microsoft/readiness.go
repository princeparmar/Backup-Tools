package microsoft

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
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

	out.CredentialID = sourceCred.ID
	if res, failed, err := tenantGuardResult(store, out, cronJob, sourceCred, false); failed || err != nil {
		return res, err
	}
	return evaluateCredentialReadiness(ctx, store, out, userID, service, method, sourceCred, out.TenantID)
}

// findOrganizationTargetFn is FindOrganizationTarget (overridden in tests).
var findOrganizationTargetFn = FindOrganizationTarget

// evaluateMigrationReadiness picks how a restore into another account writes, as Google does:
// first the destination's organization (app-only token of a tenant the user connected for
// organization backup), then the destination's own Microsoft sign-in.
func evaluateMigrationReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID, projectID, service, method string,
	cronJob *repo.CronJobListingDB,
	targetEmail string,
) (*restore.ReadinessResult, error) {
	if method == "outlook_teams" || method == "outlook_sharepoint" {
		out.Ready = false
		out.Reason = restore.ReadinessReasonMigrationUnsupported
		out.Message = "Teams and SharePoint restore back to where they were backed up from; another destination is available for mail, calendar, contacts and OneDrive."
		return out, nil
	}

	var orgResult *restore.ReadinessResult
	org, user, err := findOrganizationTargetFn(ctx, store, userID, targetEmail)
	if err != nil {
		out.Ready = false
		out.Reason = restore.ReadinessReasonAPIProbeFailed
		out.Message = fmt.Sprintf("Could not look up %s in the connected Microsoft organizations: %v", targetEmail, err)
		return out, nil
	}
	if org != nil {
		res := *out
		orgResult, err = evaluateOrganizationTarget(ctx, store, &res, service, method, cronJob, org, user)
		if err != nil || orgResult.Ready {
			return orgResult, err
		}
	}

	targetCred, credOK, err := store.CredentialRepo.FindByUserProjectAndEmail(userID, projectID, targetEmail)
	if err != nil {
		return nil, err
	}
	if credOK && targetCred.IsMicrosoft() {
		tenantID, err := delegatedRestoreTenant(store, targetCred)
		if err != nil {
			return nil, err
		}
		if tenantID != "" {
			res := *out
			res.CredentialID = targetCred.ID
			res.TenantID = tenantID
			res.AccountType = strings.TrimSpace(targetCred.AccountType)
			if res.AccountType == "" {
				res.AccountType = outlook.AccountTypePersonal
			}
			res.OAuthHolderEmail = strings.TrimSpace(targetCred.Email)
			if r, failed, err := tenantGuardResult(store, &res, cronJob, targetCred, true); failed || err != nil {
				return r, err
			}
			return evaluateCredentialReadiness(ctx, store, &res, userID, service, method, targetCred, tenantID)
		}
	}
	if orgResult != nil {
		return orgResult, nil
	}
	out.Ready = false
	out.Reason = restore.ReadinessReasonNoCredential
	out.Message = restore.MsgReadinessNoTargetCredential
	out.ReconnectHint = "Connect " + targetEmail + " in StorX, or connect its Microsoft organization for organization backup"
	return out, nil
}

// evaluateOrganizationTarget checks an app-only restore into user of org: the app has the write
// role for the service and the user has the mailbox or OneDrive it restores into.
func evaluateOrganizationTarget(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	service, method string,
	cronJob *repo.CronJobListingDB,
	org *OrganizationTenant,
	user *outlook.DirectoryUser,
) (*restore.ReadinessResult, error) {
	cred, err := store.CredentialRepo.GetByID(org.CredentialID)
	if err != nil {
		return nil, err
	}
	out.CredentialID = cred.ID
	out.TenantID = org.TenantID
	out.AccountType = strings.TrimSpace(cred.AccountType)
	out.OAuthHolderEmail = strings.TrimSpace(cred.Email)
	if r, failed, err := tenantGuardResult(store, out, cronJob, cred, true); failed || err != nil {
		return r, err
	}
	if missing := outlook.MissingRestoreWriteRoles(org.Token, method); len(missing) > 0 {
		return missingWriteRolesResult(out, service, org.Name, missing), nil
	}
	userBase, err := outlook.UserBaseURL(user.ObjectID, "", "", true)
	if err != nil {
		return nil, err
	}
	if err := checkRestoreTargetFn(ctx, org.Token, userBase, method); err != nil {
		out.Ready = false
		if errors.Is(err, outlook.ErrRestoreTargetUnavailable) {
			out.Reason = restore.ReadinessReasonTargetUnavailable
			out.Message = targetUnavailableMessage(out.TargetEmail, method)
		} else {
			out.Reason = restore.ReadinessReasonAPIProbeFailed
			out.Message = err.Error()
		}
		return out, nil
	}
	out.Ready = true
	return out, nil
}

func targetUnavailableMessage(email, method string) string {
	if method == "outlook_onedrive" {
		return email + " has no OneDrive. Assign a license with OneDrive, or choose another destination."
	}
	return email + " has no Exchange Online mailbox. Assign a license with Exchange Online, or choose another destination."
}

// missingWriteRolesResult: the tenant consented to the read-only backup permissions only.
func missingWriteRolesResult(out *restore.ReadinessResult, service, orgName string, missing []string) *restore.ReadinessResult {
	out.Ready = false
	out.Reason = restore.ReadinessReasonMissingPermissions
	out.MissingPermissions = make([]restore.MissingPermission, 0, len(missing))
	for _, role := range missing {
		out.MissingPermissions = append(out.MissingPermissions, restore.MissingPermission{
			Type: "application_role", Service: service, Scope: role,
			Description: "Microsoft Graph application permission " + role,
		})
	}
	where := "the organization"
	if strings.TrimSpace(orgName) != "" {
		where = orgName
	}
	out.Message = fmt.Sprintf("StorX can only read %s's data. Restoring needs the Microsoft Graph application permission %s.",
		where, strings.Join(missing, ", "))
	out.ReconnectHint = "A global administrator must grant admin consent for " + strings.Join(missing, ", ") +
		" to the StorX app, then reconnect the organization in StorX."
	return out
}

// tenantGuardResult runs CheckMicrosoftTenantGuard; failed is true when out now holds the refusal.
func tenantGuardResult(store *db.PostgresDb, out *restore.ReadinessResult, cronJob *repo.CronJobListingDB, writeCred *repo.GoogleBackupCredentialDB, migration bool) (*restore.ReadinessResult, bool, error) {
	err := restore.CheckMicrosoftTenantGuard(store, out.TenantID, cronJob, writeCred, migration)
	if err == nil {
		return out, false, nil
	}
	if !errors.Is(err, restore.ErrMicrosoftTenantMismatch) {
		return nil, true, err
	}
	out.Ready = false
	out.Reason = restore.ReadinessReasonTenantMismatch
	out.Message = err.Error()
	out.ReconnectHint = "Reconnect the Microsoft account that writes the restore to its tenant"
	return out, true, nil
}

// delegatedRestoreTenant is the tenant a destination's own sign-in writes in: its home tenant
// when connected with delegated access, else its first connected delegated tenant. "" when none.
func delegatedRestoreTenant(store *db.PostgresDb, cred *repo.GoogleBackupCredentialDB) (string, error) {
	links, err := store.MicrosoftLinkRepo.ListByCredential(cred.ID)
	if err != nil {
		return "", err
	}
	home := strings.ToLower(cred.HomeTenantID())
	first := ""
	for i := range links {
		l := &links[i]
		if !l.Connected() || l.Application() {
			continue
		}
		tid := strings.ToLower(strings.TrimSpace(l.TenantID))
		if tid == home {
			return tid, nil
		}
		if first == "" {
			first = tid
		}
	}
	return first, nil
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
			out.Ready = false
			out.Reason = restore.ReadinessReasonMissingPermissions
			out.Message = ErrTeamsRestoreNeedsUser.Error()
			out.ReconnectHint = "Run Microsoft restore OAuth with a user account; Teams messages are reposted as that user."
			return out, nil
		}
		if missing := outlook.MissingRestoreWriteRoles(tc.Token, method); len(missing) > 0 {
			name := ""
			if tc.Link != nil {
				name = tc.Link.TenantName
			}
			return missingWriteRolesResult(out, service, name, missing), nil
		}
		out.Ready = true
		return out, nil
	}
	required := microsoftRestoreScopesForMethod(method)
	// Personal Outlook access tokens are often opaque (not JWT) — use token-endpoint scope.
	granted := microsoftGrantedScopes(tc.Token, tc.TokenScope)
	out.GrantedScopes = granted
	missing := microsoftMissingScopes(granted, required)
	if len(missing) > 0 {
		out.Ready = false
		out.Reason = restore.ReadinessReasonMissingPermissions
		out.MissingPermissions = restore.OAuthMissingList(service, missing)
		out.Message = restore.MsgReadinessMissingScopes
		out.ReconnectHint = "Run Microsoft restore OAuth (write scopes) and reconnect so the stored credential can mint Graph write tokens."
		return out, nil
	}

	out.Ready = true
	return out, nil
}
