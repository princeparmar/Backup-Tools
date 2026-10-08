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

	writeCred := sourceCred
	if restore.IsMigrationRestore(loginID, targetEmail) {
				out.TargetEmail = targetEmail
		targetCred, credOK, credErr := store.CredentialRepo.FindByUserProjectAndEmail(userID, projectID, targetEmail)
		if credErr != nil {
			return nil, credErr
		}
		if !credOK {
			out.Ready = false
			out.Reason = restore.ReadinessReasonNoCredential
			out.Message = restore.MsgReadinessNoTargetCredential
			out.ReconnectHint = "Connect the target Microsoft account before migration restore"
			return out, nil
		}
		writeCred = targetCred
		out.AccountType = strings.TrimSpace(targetCred.AccountType)
		if out.AccountType == "" {
			out.AccountType = outlook.AccountTypePersonal
		}
		out.OAuthHolderEmail = strings.TrimSpace(targetCred.Email)
	}

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
