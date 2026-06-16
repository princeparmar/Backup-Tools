package restore

import (
	"context"
	"fmt"
	"strings"

	google "github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
)

// MissingPermission describes a scope or grant the user must fix before restore.
type MissingPermission struct {
	Type        string `json:"type"`
	Service     string `json:"service"`
	Scope       string `json:"scope,omitempty"`
	Description string `json:"description,omitempty"`
}

// ReadinessResult is returned by GET /restore/prepare and inline validation on POST /restore/all.
type ReadinessResult struct {
	Ready              bool                   `json:"ready"`
	Reason             string                 `json:"reason,omitempty"`
	AuthMode           string                 `json:"auth_mode,omitempty"`
	AccountType        string                 `json:"account_type,omitempty"`
	Service            string                 `json:"service,omitempty"`
	ProjectID          string                 `json:"project_id,omitempty"`
	LoginID            string                 `json:"login_id,omitempty"`
	CronJobID          uint                   `json:"cron_job_id,omitempty"`
	CredentialID       uint                   `json:"credential_id,omitempty"`
	BackupItemCount    uint                   `json:"backup_item_count,omitempty"`
	OAuthHolderEmail   string                 `json:"oauth_holder_email,omitempty"`
	MissingPermissions []MissingPermission      `json:"missing_permissions,omitempty"`
	DelegationSetup    interface{}            `json:"delegation_setup,omitempty"`
	ReconnectHint      string                 `json:"reconnect_hint,omitempty"`
	GrantedScopes      []string               `json:"granted_scopes,omitempty"`
	RequiredDWDScopes  []string               `json:"required_dwd_scopes,omitempty"`
	Message            string                 `json:"message,omitempty"`
}

const (
	RestoreAuthModeOAuth = "oauth"
	RestoreAuthModeDWD   = "dwd"
)

const (
	ReadinessReasonNoBackupJob       = "no_backup_job"
	ReadinessReasonNoBackupData      = "no_backup_data"
	ReadinessReasonNoCredential      = "no_credential"
	ReadinessReasonTokenRefreshFailed = "token_refresh_failed"
	ReadinessReasonMissingPermissions = "missing_permissions"
	ReadinessReasonDWDNotConfigured  = "dwd_not_configured"
	ReadinessReasonAPIProbeFailed    = "api_probe_failed"
	ReadinessReasonStorxMissing      = "storx_missing"
)

// EvaluateReadiness runs prepare checks for Satellite restore-all.
func EvaluateReadiness(ctx context.Context, store *db.PostgresDb, userID, projectID, loginID, service string) (*ReadinessResult, error) {
	service = strings.TrimSpace(strings.ToLower(service))
	projectID = strings.TrimSpace(projectID)
	loginID = strings.TrimSpace(loginID)
	userID = strings.TrimSpace(userID)

	out := &ReadinessResult{
		Service:   service,
		ProjectID: projectID,
		LoginID:   loginID,
	}

	method, ok := APIServiceToMethod[APIService(service)]
	if !ok {
		return nil, fmt.Errorf("unsupported service")
	}

	cronJob, jobOK, err := store.CronJobRepo.FindJobForRestore(userID, method, loginID)
	if err != nil {
		return nil, err
	}
	if !jobOK {
		out.Ready = false
		out.Reason = ReadinessReasonNoBackupJob
		out.Message = restoreReadinessNoBackupJob
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
			out.Reason = ReadinessReasonNoCredential
			out.Message = restoreReadinessNoCredential
			return out, nil
		}
		cred, err = store.CredentialRepo.GetByID(credID)
		if err != nil {
			return nil, err
		}
	}
	out.CredentialID = cred.ID
	out.AccountType = google.NormalizeAccountType(cred.AccountType)
	out.OAuthHolderEmail = strings.TrimSpace(cred.Email)

	cfg, ok := ConfigForMethod(method)
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
		out.Reason = ReadinessReasonNoBackupData
		out.Message = restoreReadinessNoBackupData
		return out, nil
	}

	holder := store.CronJobRepo.ResolvedOAuthHolderEmail(cronJob)
	if holder == "" {
		holder = out.OAuthHolderEmail
	}
	authMode := ResolveRestoreAuthMode(service, out.AccountType, loginID, holder, cred, cronJob)
	out.AuthMode = authMode

	storx := strings.TrimSpace(store.CronJobRepo.ResolvedStorxToken(cronJob))
	if storx == "" {
		storx = strings.TrimSpace(cred.StorxToken)
	}
	if storx == "" {
		recovery := storxrefresh.NewRecovery(store, cronJob)
		grant, continueOK, refreshErr := recovery.OnStorxError(ctx, fmt.Errorf("storx access grant not found"))
		if refreshErr != nil || !continueOK {
			out.Ready = false
			out.Reason = ReadinessReasonStorxMissing
			if refreshErr != nil {
				out.Message = refreshErr.Error()
			} else {
				out.Message = restoreReadinessStorxMissing
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
		out.Reason = ReadinessReasonStorxMissing
		out.Message = restoreReadinessStorxMissing
		out.MissingPermissions = []MissingPermission{{Type: "storx", Service: service, Description: "storx_token required"}}
		out.ReconnectHint = "Use dashboard auto-sync reconnect to update StorX grant"
		return out, nil
	}

	switch authMode {
	case RestoreAuthModeDWD:
		return evaluateDWDReadiness(ctx, out, service, loginID)
	default:
		return evaluateOAuthReadiness(ctx, store, out, service, cronJob, cred)
	}
}

// ResolveRestoreAuthMode picks oauth vs dwd from account_type (primary) with legacy fallback.
func ResolveRestoreAuthMode(service, accountType, loginID, holderEmail string, cred *repo.GoogleBackupCredentialDB, cronJob *repo.CronJobListingDB) string {
	accountType = google.NormalizeAccountType(accountType)
	if accountType == "" && cred != nil {
		accountType = google.NormalizeAccountType(cred.AccountType)
	}

	switch accountType {
	case google.AccountTypePersonal, google.AccountTypeEmployeeWorkspace:
		return RestoreAuthModeOAuth
	case google.AccountTypeAdminWorkspace:
		if !strings.EqualFold(strings.TrimSpace(loginID), strings.TrimSpace(holderEmail)) {
			return RestoreAuthModeDWD
		}
		return RestoreAuthModeOAuth
	}

	// Legacy rows without account_type — child mailbox + SA configured
	if google.WorkspaceServiceAccountConfigured() &&
		google.GmailJobUsesDelegationWithoutOAuth(loginID, holderEmail) &&
		!strings.EqualFold(strings.TrimSpace(loginID), strings.TrimSpace(holderEmail)) {
		return RestoreAuthModeDWD
	}
	_ = cronJob
	return RestoreAuthModeOAuth
}

func evaluateDWDReadiness(ctx context.Context, out *ReadinessResult, service, loginID string) (*ReadinessResult, error) {
	setup, err := google.GetWorkspaceRestoreDelegationSetup()
	if err != nil {
		out.Ready = false
		out.Reason = ReadinessReasonDWDNotConfigured
		out.Message = err.Error()
		return out, nil
	}
	out.DelegationSetup = setup
	out.RequiredDWDScopes = google.AllRestoreDWDScopeURLs()

	if err := google.ProbeDWDRestore(ctx, service, loginID); err != nil {
		out.Ready = false
		out.Reason = ReadinessReasonDWDNotConfigured
		out.Message = restoreReadinessDWDNotConfigured
		out.MissingPermissions = dwdMissingList(service, err)
		out.ReconnectHint = "Workspace admin must add all restore scopes for gmail, drive, calendar, contacts, and photos (OAuth reconnect does not apply to delegated mailboxes)"
		return out, nil
	}
	out.Ready = true
	return out, nil
}

func dwdMissingList(service string, probeErr error) []MissingPermission {
	scope := google.RestoreDWDScopeForService(service)
	desc := "Domain-wide delegation — authorize all restore scopes in Admin Console (see required_dwd_scopes)"
	if probeErr != nil {
		desc = desc + ": " + probeErr.Error()
	}
	perService := []MissingPermission{{
		Type: "dwd", Service: service, Scope: scope,
		Description: desc,
	}}
	all := make([]MissingPermission, 0, len(google.RestoreDWDScopesMap())+1)
	all = append(all, perService...)
	for svc, sc := range google.RestoreDWDScopesMap() {
		if svc == service {
			continue
		}
		all = append(all, MissingPermission{
			Type: "dwd", Service: svc, Scope: sc,
			Description: "Also required in Admin Console for restore-all on other services",
		})
	}
	return all
}

func evaluateOAuthReadiness(ctx context.Context, store *db.PostgresDb, out *ReadinessResult, service string, cronJob *repo.CronJobListingDB, cred *repo.GoogleBackupCredentialDB) (*ReadinessResult, error) {
	refresh := strings.TrimSpace(store.CronJobRepo.ResolvedRefreshToken(cronJob))
	if refresh == "" {
		refresh = strings.TrimSpace(cred.RefreshToken)
	}
	if refresh == "" {
		out.Ready = false
		out.Reason = ReadinessReasonTokenRefreshFailed
		out.Message = restoreReadinessRefreshMissing
		out.ReconnectHint = "Use dashboard Google reconnect (PUT /auto-sync/job) to grant tokens"
		return out, nil
	}

	accessToken, err := google.AuthTokenUsingRefreshToken(refresh)
	if err != nil {
		out.Ready = false
		out.Reason = ReadinessReasonTokenRefreshFailed
		out.Message = restoreReadinessRefreshInvalid
		out.ReconnectHint = "Use dashboard Google reconnect (PUT /auto-sync/job) with full scopes"
		return out, nil
	}

	details, err := google.GetGoogleAccountDetailsFromAccessToken(accessToken)
	if err != nil {
		out.Ready = false
		out.Reason = ReadinessReasonTokenRefreshFailed
		out.Message = restoreReadinessTokenValidation
		return out, nil
	}

	required := google.RestoreOAuthScopesForService(service)
	missing := google.TokenInfoMissingScopes(details.Scope, required)
	if len(missing) > 0 {
		out.Ready = false
		out.Reason = ReadinessReasonMissingPermissions
		out.GrantedScopes = strings.Fields(details.Scope)
		out.MissingPermissions = oauthMissingList(service, missing)
		out.ReconnectHint = "Use dashboard Google reconnect (auto-sync) to grant missing OAuth scopes"
		out.Message = restoreReadinessMissingScopes
		return out, nil
	}
	out.GrantedScopes = strings.Fields(details.Scope)

	if err := probeOAuthRestore(ctx, service, accessToken, out.LoginID); err != nil {
		out.Ready = false
		out.Reason = ReadinessReasonAPIProbeFailed
		out.Message = err.Error()
		return out, nil
	}

	out.Ready = true
	return out, nil
}

func oauthMissingList(service string, scopes []string) []MissingPermission {
	out := make([]MissingPermission, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, MissingPermission{
			Type: "oauth", Service: service, Scope: s,
			Description: "OAuth scope required for restore",
		})
	}
	return out
}

func probeOAuthRestore(ctx context.Context, service, accessToken, loginID string) error {
	switch strings.ToLower(service) {
	case "gmail":
		client, err := google.NewGmailClientForRestore(accessToken)
		if err != nil {
			return err
		}
		_, err = client.Users.GetProfile("me").Do()
		return err
	case "drive":
		srv, err := google.GetDriveServiceUsingToken(accessToken)
		if err != nil {
			return err
		}
		_, err = srv.About.Get().Fields("user").Do()
		return err
	case "calendar":
		srv, err := google.NewCalendarServiceForRestore(ctx, accessToken)
		if err != nil {
			return err
		}
		_, err = srv.CalendarList.List().MaxResults(1).Do()
		return err
	case "contacts":
		srv, err := google.NewPeopleServiceForRestore(ctx, accessToken)
		if err != nil {
			return err
		}
		_, err = srv.People.Connections.List("people/me").PageSize(1).PersonFields("names").Do()
		return err
	case "photos":
		client, err := google.NewGPhotosClientForRestore(accessToken)
		if err != nil {
			return err
		}
		if client.Service == nil {
			return fmt.Errorf("photos service unavailable")
		}
		_, err = client.Service.Albums.List().PageSize(1).Do()
		return err
	default:
		return fmt.Errorf("unsupported service for probe")
	}
}

// CreateRestoreJobFromReadiness queues a job using credential/cron links (no tokens on the row).
func CreateRestoreJobFromReadiness(ctx context.Context, store *db.PostgresDb, userID string, prep *ReadinessResult) (*repo.RestoreJobListingDB, error) {
	if prep == nil || !prep.Ready {
		return nil, fmt.Errorf("restore not ready")
	}
	method, ok := APIServiceToMethod[APIService(prep.Service)]
	if !ok {
		return nil, fmt.Errorf("unsupported service")
	}

	active, err := store.RestoreJobRepo.HasActiveJob(userID, method, prep.LoginID)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, fmt.Errorf("a restore is already in progress for this account and service")
	}

	job := &repo.RestoreJobListingDB{
		UserID:         userID,
		StorjProjectID: prep.ProjectID,
		LoginID:        prep.LoginID,
		Method:         method,
		AccountType:    prep.AccountType,
		CredentialID:   prep.CredentialID,
		CronJobID:      prep.CronJobID,
		Status:         repo.RestoreJobStatusQueued,
		Message:        "restore queued",
		MessageStatus:  repo.JobMessageStatusInfo,
	}

	if err := store.RestoreJobRepo.Create(job); err != nil {
		return nil, err
	}
	logger.Info(ctx, "Restore job created",
		logger.Int("job_id", int(job.ID)),
		logger.String("method", method),
		logger.String("login_id", prep.LoginID),
		logger.String("user_id", userID))
	return job, nil
}
