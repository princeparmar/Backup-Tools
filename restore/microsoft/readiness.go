package microsoft

import (
	"github.com/StorX2-0/Backup-Tools/restore"
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
)

// evaluateMicrosoftReadiness mirrors Google prepare checks for Microsoft Graph restore-all.
func EvaluateReadiness(
	ctx context.Context,
	store *db.PostgresDb,
	out *restore.ReadinessResult,
	userID, projectID, loginID, service, method, targetEmail string,
) (*restore.ReadinessResult, error) {
	cronJob, jobOK, err := store.CronJobRepo.FindJobForRestore(userID, method, loginID)
	if err != nil {
		return nil, err
	}
	if !jobOK {
		out.Ready = false
		out.Reason = restore.ReadinessReasonNoBackupJob
		out.Message = restore.MsgReadinessNoBackupJob
		return out, nil
	}
	out.CronJobID = cronJob.ID

	var cred *repo.GoogleBackupCredentialDB
	if credID := repo.JobCredentialID(cronJob); credID > 0 {
		cred, err = store.CredentialRepo.GetByID(credID)
		if err != nil {
			return nil, err
		}
	} else {
		credID, credOK, err := store.CredentialRepo.FindIDForUserProjectAndEmail(userID, projectID, loginID)
		if err != nil {
			return nil, err
		}
		if !credOK {
			out.Ready = false
			out.Reason = restore.ReadinessReasonNoCredential
			out.Message = restore.MsgReadinessNoCredential
			return out, nil
		}
		cred, err = store.CredentialRepo.GetByID(credID)
		if err != nil {
			return nil, err
		}
	}
	sourceCred := cred
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
	prefix := strings.TrimSuffix(loginID, "/") + "/"
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
	return evaluateCredentialReadiness(ctx, out, service, method, writeCred)
}

func evaluateCredentialReadiness(
	ctx context.Context,
	out *restore.ReadinessResult,
	service, method string,
	cred *repo.GoogleBackupCredentialDB,
) (*restore.ReadinessResult, error) {
	accessToken, endpointScope, err := mintMicrosoftTokenAndScopeFromCredential(ctx, cred, "")
	if err != nil {
		out.Ready = false
		out.Reason = restore.ReadinessReasonTokenRefreshFailed
		out.Message = restore.MsgReadinessRefreshInvalid
		out.ReconnectHint = "Reconnect the Microsoft account (auto-sync) or complete restore OAuth so write scopes are granted"
		return out, nil
	}

	required := microsoftRestoreScopesForMethod(method)
	// Personal Outlook access tokens are often opaque (not JWT) — use token-endpoint scope.
	granted := microsoftGrantedScopes(accessToken, endpointScope)
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
