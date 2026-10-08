package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

var msMeIdentityFn = outlook.GraphMeIdentity

// resolveMicrosoftOnboardingTenant stores the caller's credential, connects the selected tenant's
// link in the requested backup mode when it was only discovered, and resolves the tenant. A link
// already connected in the other mode is refused: the mode only changes through connect.
func resolveMicrosoftOnboardingTenant(
	c echo.Context, ctx context.Context, database *db.PostgresDb, userID string, req *MicrosoftBackupOnboardingRequest, application bool,
) (*mstenant.Context, *repo.GoogleBackupCredentialDB, error) {
	id, err := microsoftIdentityFromRequest(c)
	if err != nil {
		return nil, nil, err
	}
	id.UserID = userID
	if req.TenantID != "" {
		if id.TenantID != "" && id.TenantID != req.TenantID {
			return nil, nil, &mstenant.Error{HTTPStatus: http.StatusForbidden, Code: mstenant.CodeTenantMismatch,
				Message: "tenant_id does not match MICROSOFT_TENANT_ID"}
		}
		id.TenantID = req.TenantID
	}
	if req.RefreshToken != "" {
		id.RefreshToken = req.RefreshToken
		if id.AccountID == "" {
			if in, serr := msSignInFn(req.RefreshToken); serr == nil {
				id.AccountID = in.ObjectID
			}
		}
	}
	cred, err := microsoftOnboardingCredential(ctx, database, id, req, application)
	if err != nil {
		return nil, nil, err
	}
	tid := id.TenantID
	if tid == "" {
		tid = cred.HomeTenantID()
	}
	if application && outlook.IsMSATenant(tid) {
		return nil, nil, &mstenant.Error{HTTPStatus: http.StatusForbidden, Code: mstenant.CodeOrganizationModeRequired,
			Message: "organization backup requires a Microsoft 365 work or school tenant"}
	}

	backupMode, authMode := repo.MicrosoftBackupModePersonal, repo.MicrosoftAuthModeDelegated
	if application {
		backupMode, authMode = repo.MicrosoftBackupModeOrganization, repo.MicrosoftAuthModeApplication
	}
	links := database.MicrosoftLinkRepo
	link, err := links.Get(cred.ID, tid)
	if err != nil {
		return nil, nil, err
	}
	if link == nil {
		home := cred.HomeTenantID()
		category := repo.MicrosoftTenantCategoryGuest
		switch {
		case outlook.IsMSATenant(tid):
			category = repo.MicrosoftTenantCategoryPersonal
		case tid == home:
			category = repo.MicrosoftTenantCategoryHome
		}
		name := ""
		if tid == home {
			name = cred.TenantName
		} else if !application || req.TenantName != "" {
			name = req.TenantName
		}
		if _, err := links.UpsertDiscovered(cred.ID, repo.MicrosoftTenantDiscovery{TenantID: tid, TenantName: name, Category: category, HomeTenantID: home}); err != nil {
			return nil, nil, err
		}
		msCheckTenantAccessFn(ctx, database, cred, tid, id.RefreshToken)
		if link, err = links.Get(cred.ID, tid); err != nil {
			return nil, nil, err
		}
	}
	switch link.ConnectionState {
	case repo.MicrosoftConnectionConnected:
		if link.BackupMode != backupMode {
			return nil, nil, &mstenant.Error{HTTPStatus: http.StatusConflict, Code: "backup_mode_mismatch",
				Message: "tenant " + tid + " is connected for " + link.BackupMode + " backup; reconnect it to change the backup mode"}
		}
	case repo.MicrosoftConnectionDisconnected:
		return nil, nil, &mstenant.Error{HTTPStatus: http.StatusConflict, Code: mstenant.CodeTenantDisconnected,
			Message: "tenant " + tid + " is disconnected for this Microsoft account; reconnect it first"}
	default:
		if err := links.Connect(cred.ID, tid, backupMode, authMode); err != nil {
			return nil, nil, err
		}
	}

	tc, err := msResolveTenantFn(ctx, database, mstenant.Request{
		UserID:             userID,
		CredentialID:       cred.ID,
		TenantID:           tid,
		RequireApplication: application,
		RefreshToken:       id.RefreshToken,
	})
	if err != nil {
		return nil, nil, err
	}
	return tc, tc.Credential, nil
}

// microsoftOnboardingCredential finds the caller's credential by home oid (creating it from a
// verified refresh token) and stores the project, StorX grant and, for self backup, the refresh token.
func microsoftOnboardingCredential(ctx context.Context, database *db.PostgresDb, id *microsoftRequestIdentity, req *MicrosoftBackupOnboardingRequest, application bool) (*repo.GoogleBackupCredentialDB, error) {
	var cred *repo.GoogleBackupCredentialDB
	if id.AccountID != "" {
		found, ok, err := database.CredentialRepo.FindMicrosoftByAccount(id.UserID, id.AccountID)
		if err != nil {
			return nil, err
		}
		if ok {
			cred = found
		}
	}
	if cred == nil {
		if id.RefreshToken == "" {
			return nil, &mstenant.Error{HTTPStatus: http.StatusNotFound, Code: mstenant.CodeCredentialNotFound,
				Message: "no Microsoft credential for this account; MICROSOFT_ACCOUNT_ID and REFRESH_TOKEN are required"}
		}
		created, err := msUpsertCredentialFn(database, id.UserID, id.AccountID, req.ProjectID, id.RefreshToken)
		if err != nil {
			return nil, err
		}
		cred = created
	}
	upd := repo.MicrosoftAccountUpsert{
		UserID:            id.UserID,
		ExternalAccountID: cred.ExternalAccountID,
		StorjProjectID:    req.ProjectID,
		StorxToken:        req.StorxToken,
	}
	if strings.TrimSpace(cred.Email) == "" {
		upd.Email = req.MicrosoftEmail
	}
	if !application {
		upd.RefreshToken = req.RefreshToken
	}
	if req.TenantName != "" && (req.TenantID == "" || req.TenantID == cred.HomeTenantID()) && strings.TrimSpace(cred.TenantName) == "" {
		upd.HomeTenantName = req.TenantName
	}
	if storx := strings.TrimSpace(req.StorxToken); storx != "" {
		if pid := extractProjectIDFromStorxGrant(ctx, storx); pid != "" {
			upd.StorjProjectID = pid
		}
	}
	return database.CredentialRepo.UpsertMicrosoftAccount(upd)
}

// microsoftSelfDirectoryUser is the signed-in person in the resolved tenant (self backup).
func microsoftSelfDirectoryUser(ctx context.Context, database *db.PostgresDb, tc *mstenant.Context, email string) (outlook.DirectoryUser, error) {
	oid := tc.Link.ObjectIDValue()
	if oid == "" {
		me, err := msMeIdentityFn(ctx, tc.Token)
		if err != nil {
			return outlook.DirectoryUser{}, err
		}
		oid = me.ObjectID
		_ = database.MicrosoftLinkRepo.SaveObjectID(tc.Credential.ID, tc.TenantID, oid)
	}
	return outlook.DirectoryUser{ObjectID: oid, Mail: strings.TrimSpace(email), DisplayName: strings.TrimSpace(email), AccountEnabled: true}, nil
}

// microsoftScopeResourceType maps an onboarding service to the resource type its jobs back up.
func microsoftScopeResourceType(svc string) string {
	if method, ok := microsoftOnboardingServiceToMethod[svc]; ok {
		return repo.MicrosoftResourceTypeForMethod(method)
	}
	return ""
}

// saveMicrosoftTenantScopes records what organization onboarding selected per resource type. all
// (all_users) applies to the user resource type only and makes reconcile add jobs for new users;
// selected scopes never replace an all scope.
func saveMicrosoftTenantScopes(database *db.PostgresDb, userID string, cred *repo.GoogleBackupCredentialDB, tenantID, syncType string, services []string, allUsers bool, policyID uint) {
	seen := map[string]bool{}
	for _, svc := range services {
		rt := microsoftScopeResourceType(svc)
		if rt == "" || seen[rt] {
			continue
		}
		seen[rt] = true
		scope := repo.MicrosoftBackupScopeDB{
			UserID: userID, StorjProjectID: strings.TrimSpace(cred.StorjProjectID), CredentialID: cred.ID,
			TenantID: tenantID, ResourceType: rt, SyncType: syncType, PolicyID: policyID,
		}
		if allUsers && rt == repo.ResourceTypeUser {
			scope.SelectionMode = repo.MicrosoftScopeAll
			_, _ = database.MicrosoftScopeRepo.Upsert(scope)
		} else {
			_ = database.MicrosoftScopeRepo.EnsureSelected(scope)
		}
	}
}
