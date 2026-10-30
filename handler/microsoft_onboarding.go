package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	dbjson "github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

// SharePointSiteOnboardingInput selects a SharePoint site for outlook_sharepoint jobs.
type SharePointSiteOnboardingInput struct {
	SiteID  string `json:"site_id"`
	SiteURL string `json:"site_url"`
}

// TeamsOnboardingInput selects a Team for outlook_teams jobs.
type TeamsOnboardingInput struct {
	TeamID     string   `json:"team_id"`
	TeamName   string   `json:"team_name,omitempty"`
	ChannelIDs []string `json:"channel_ids,omitempty"`
}

// MicrosoftBackupOnboardingRequest is the Satellite → Backup-Tools MS job create body
// (POST /microsoft/auto-sync/job).
//
// auth_mode=delegated (backup_mode=self): the caller's own mailbox with their refresh token.
// auth_mode=application (backup_mode=organization): tenant app-only token; authorized only by tenant
// consent + capabilities, never by account_type or is_admin. No refresh token is sent or stored.
type MicrosoftBackupOnboardingRequest struct {
	Services        []string                        `json:"services"`
	Interval        string                          `json:"interval"`
	On              string                          `json:"on"`
	MicrosoftEmail  string                          `json:"microsoft_email"`
	AccountType     string                          `json:"account_type"`
	TenantID        string                          `json:"tenant_id"`
	TenantName      string                          `json:"tenant_name"`
	ProjectID       string                          `json:"project_id"`
	SatelliteUserID string                          `json:"satellite_user_id"`
	RefreshToken    string                          `json:"refresh_token"`
	StorxToken      string                          `json:"storx_token,omitempty"`
	Active          *bool                           `json:"active,omitempty"`
	AuthMode        string                          `json:"auth_mode"`
	BackupMode      string                          `json:"backup_mode"`
	AllUsers        bool                            `json:"all_users"`
	UserIDs         []string                        `json:"user_ids"`
	Emails          []string                        `json:"emails"`
	SharedMailboxes []string                        `json:"shared_mailboxes"`
	BackupScope     string                          `json:"backup_scope"`
	Sites           []SharePointSiteOnboardingInput `json:"sites"`
	Teams           []TeamsOnboardingInput          `json:"teams"`
	PolicyID        *uint                           `json:"policy_id,omitempty"`
	PolicyName      string                          `json:"policy_name,omitempty"`
	// SystemJobID is the access-check system job from Access Check start; Continue materializes
	// backup jobs from it (finals if done, drafts if still probing).
	SystemJobID uint `json:"system_job_id,omitempty"`

	// allSites is set when every tenant site was selected (no site named); reconcile then adds
	// jobs for new sites.
	allSites bool
}

const (
	microsoftBackupModeSelf         = "self"
	microsoftBackupModeOrganization = "organization"
)

// microsoftUserServices are per-mailbox services (one job per directory user).
var microsoftUserServices = map[string]bool{"outlook": true, "mail": true, "calendar": true, "contacts": true, "onedrive": true}

func (r *MicrosoftBackupOnboardingRequest) trim() {
	r.MicrosoftEmail = strings.TrimSpace(r.MicrosoftEmail)
	r.RefreshToken = strings.TrimSpace(r.RefreshToken)
	r.StorxToken = strings.TrimSpace(r.StorxToken)
	r.ProjectID = strings.TrimSpace(r.ProjectID)
	r.Interval = strings.TrimSpace(r.Interval)
	r.On = strings.TrimSpace(r.On)
	r.SatelliteUserID = strings.TrimSpace(r.SatelliteUserID)
	r.PolicyName = strings.TrimSpace(r.PolicyName)
	r.AccountType = strings.TrimSpace(r.AccountType)
	r.TenantID = strings.ToLower(strings.TrimSpace(r.TenantID))
	r.TenantName = strings.TrimSpace(r.TenantName)
	r.BackupScope = strings.TrimSpace(r.BackupScope)
	r.AuthMode = strings.ToLower(strings.TrimSpace(r.AuthMode))
	r.BackupMode = strings.ToLower(strings.TrimSpace(r.BackupMode))
}

func (r *MicrosoftBackupOnboardingRequest) hasPolicyID() bool {
	return r.PolicyID != nil && *r.PolicyID > 0
}

// applicationMode reports whether the request is an organization (app-only) backup.
func (r *MicrosoftBackupOnboardingRequest) applicationMode() bool {
	if r.AuthMode != "" {
		return r.AuthMode == outlook.MicrosoftAuthModeApplication
	}
	return r.BackupMode == microsoftBackupModeOrganization
}

// wantsAllUsers reports whether every directory user is selected. backup_scope=all_tenant only
// means all users when the request names no users; with user_ids, emails or shared mailboxes it
// covers tenant resources (teams, sites) and the named users stay the only mailboxes.
func (r *MicrosoftBackupOnboardingRequest) wantsAllUsers() bool {
	if r.AllUsers {
		return true
	}
	return r.wantsAllTenantResources() && !r.namesUsers()
}

// wantsAllTenantResources reports whether teams and sites expand to every tenant resource.
func (r *MicrosoftBackupOnboardingRequest) wantsAllTenantResources() bool {
	return r.AllUsers || strings.EqualFold(r.BackupScope, "all_tenant")
}

func (r *MicrosoftBackupOnboardingRequest) namesUsers() bool {
	for _, list := range [][]string{r.UserIDs, r.Emails, r.SharedMailboxes} {
		for _, s := range list {
			if strings.TrimSpace(s) != "" {
				return true
			}
		}
	}
	return false
}

func (r *MicrosoftBackupOnboardingRequest) validate(userID string) error {
	switch r.AuthMode {
	case "", outlook.MicrosoftAuthModeApplication, outlook.MicrosoftAuthModeDelegated:
	default:
		return fmt.Errorf("auth_mode must be %q or %q", outlook.MicrosoftAuthModeDelegated, outlook.MicrosoftAuthModeApplication)
	}
	switch r.BackupMode {
	case "", microsoftBackupModeSelf, microsoftBackupModeOrganization:
	default:
		return fmt.Errorf("backup_mode must be %q or %q", microsoftBackupModeSelf, microsoftBackupModeOrganization)
	}
	if (r.AuthMode == outlook.MicrosoftAuthModeDelegated && r.BackupMode == microsoftBackupModeOrganization) ||
		(r.AuthMode == outlook.MicrosoftAuthModeApplication && r.BackupMode == microsoftBackupModeSelf) {
		return errors.New("auth_mode and backup_mode conflict")
	}
	if !r.applicationMode() && r.RefreshToken == "" {
		return errors.New("refresh_token is required")
	}
	if r.MicrosoftEmail == "" {
		return errors.New("microsoft_email is required")
	}
	if len(normalizeOnboardingServices(r.Services)) == 0 {
		return errors.New("services is required")
	}
	if err := validateMicrosoftOnboardingServiceNames(r.Services); err != nil {
		return err
	}
	if !r.hasPolicyID() && r.Interval == "" {
		return errors.New("interval is required")
	}
	if r.ProjectID == "" {
		return errors.New("project_id is required")
	}
	if r.SatelliteUserID != "" && r.SatelliteUserID != userID {
		return errors.New("satellite_user_id does not match token_key session")
	}
	return nil
}

// validateForAccessPreview checks fields needed to probe Graph access before job create.
// Schedule (interval/policy) and project_id are not required — preview creates nothing.
func (r *MicrosoftBackupOnboardingRequest) validateForAccessPreview(userID string) error {
	switch r.AuthMode {
	case "", outlook.MicrosoftAuthModeApplication, outlook.MicrosoftAuthModeDelegated:
	default:
		return fmt.Errorf("auth_mode must be %q or %q", outlook.MicrosoftAuthModeDelegated, outlook.MicrosoftAuthModeApplication)
	}
	switch r.BackupMode {
	case "", microsoftBackupModeSelf, microsoftBackupModeOrganization:
	default:
		return fmt.Errorf("backup_mode must be %q or %q", microsoftBackupModeSelf, microsoftBackupModeOrganization)
	}
	if (r.AuthMode == outlook.MicrosoftAuthModeDelegated && r.BackupMode == microsoftBackupModeOrganization) ||
		(r.AuthMode == outlook.MicrosoftAuthModeApplication && r.BackupMode == microsoftBackupModeSelf) {
		return errors.New("auth_mode and backup_mode conflict")
	}
	if !r.applicationMode() && r.RefreshToken == "" {
		return errors.New("refresh_token is required")
	}
	if r.MicrosoftEmail == "" {
		return errors.New("microsoft_email is required")
	}
	if len(normalizeOnboardingServices(r.Services)) == 0 {
		return errors.New("services is required")
	}
	if err := validateMicrosoftOnboardingServiceNames(r.Services); err != nil {
		return err
	}
	if r.SatelliteUserID != "" && r.SatelliteUserID != userID {
		return errors.New("satellite_user_id does not match token_key session")
	}
	return nil
}

func validateMicrosoftOnboardingServiceNames(raw []string) error {
	for _, svc := range normalizeOnboardingServices(raw) {
		if _, ok := microsoftOnboardingServiceToMethod[svc]; !ok {
			return fmt.Errorf("unknown service %q", svc)
		}
	}
	return nil
}

func (r *MicrosoftBackupOnboardingRequest) toGoogleShapeWithAccountType(accountType string) *GoogleBackupOnboardingRequest {
	acct := strings.TrimSpace(accountType)
	if acct == "" {
		acct = "personal"
	}
	return &GoogleBackupOnboardingRequest{
		Services:        r.Services,
		Interval:        r.Interval,
		On:              r.On,
		GoogleEmail:     r.MicrosoftEmail,
		AccountType:     acct,
		ProjectID:       r.ProjectID,
		SatelliteUserID: r.SatelliteUserID,
		RefreshToken:    r.RefreshToken,
		StorxToken:      r.StorxToken,
		Active:          r.Active,
		Emails:          r.Emails,
		PolicyID:        r.PolicyID,
		PolicyName:      r.PolicyName,
	}
}

func normalizeCredentialAccountTypeForMicrosoft(s string) string {
	return outlook.NormalizeAccountType(s)
}

// validateMicrosoftDelegatedOnboarding enforces self-only delegated backup: the caller's own mailbox
// services. Other users, all-users and tenant resources require backup_mode=organization.
func validateMicrosoftDelegatedOnboarding(req *MicrosoftBackupOnboardingRequest, services, emails []string) *echo.HTTPError {
	forbidden := func(msg string) *echo.HTTPError {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{"error": msg, "code": "organization_backup_required"})
	}
	if req.wantsAllUsers() || len(req.UserIDs) > 0 {
		return forbidden("backing up all users requires backup_mode=organization with tenant admin consent")
	}
	for _, svc := range services {
		if !microsoftUserServices[svc] {
			return forbidden(fmt.Sprintf("%s backup requires backup_mode=organization with tenant admin consent", svc))
		}
	}
	for _, e := range append(append([]string{}, emails...), req.SharedMailboxes...) {
		if !strings.EqualFold(strings.TrimSpace(e), req.MicrosoftEmail) {
			return forbidden("backing up other users' mailboxes requires backup_mode=organization with tenant admin consent")
		}
	}
	return nil
}

// microsoftApplicationOnboarding is the authorized org-backup context.
type microsoftApplicationOnboarding struct {
	AccessToken string
	Tenant      *repo.MicrosoftTenantDB
	Emails      []string
	// Users maps lower-case email to the directory user (object ID is the job resource ID).
	Users map[string]outlook.DirectoryUser
}

// authorizeMicrosoftApplicationOnboarding checks every requested service's capability on the
// resolved tenant and resolves target users from the live tenant directory.
func authorizeMicrosoftApplicationOnboarding(
	ctx context.Context, tc *mstenant.Context, req *MicrosoftBackupOnboardingRequest, services []string,
) (*microsoftApplicationOnboarding, error) {
	token, tenant := tc.Token, tc.Tenant
	needsUsers := false
	for _, svc := range services {
		if microsoftUserServices[svc] {
			needsUsers = true
		}
		if capName := MicrosoftCapabilityForService(svc); capName != "" && !tenant.Capability(capName) {
			return nil, mstenant.CapabilityDenied(tenant, capName)
		}
	}
	out := &microsoftApplicationOnboarding{AccessToken: token, Tenant: tenant, Users: map[string]outlook.DirectoryUser{}}
	if !needsUsers {
		return out, nil
	}

	// Fast path: UI already resolved object IDs (directory step). Skip a full Graph /users crawl.
	if users, ok := applicationUsersFromRequestIDs(req); ok {
		for _, u := range users {
			email := u.Email()
			if email == "" {
				continue
			}
			key := strings.ToLower(email)
			if _, dup := out.Users[key]; dup {
				continue
			}
			out.Emails = append(out.Emails, email)
			out.Users[key] = u
		}
		if len(out.Emails) == 0 {
			return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadRequest, Code: "no_users",
				Message: "select users with all_users, user_ids or emails"}
		}
		return out, nil
	}

	directory, err := msListDirectoryUsersOptsFn(ctx, token, outlook.ListTenantDirectoryUsersOptions{})
	if err != nil {
		return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadGateway, Code: "directory_unavailable",
			Message: "could not list tenant users: " + err.Error()}
	}
	byID := make(map[string]outlook.DirectoryUser, len(directory))
	byEmail := make(map[string]outlook.DirectoryUser, 2*len(directory))
	for _, u := range directory {
		byID[u.ObjectID] = u
		for _, e := range []string{u.Mail, u.UPN} {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				byEmail[e] = u
			}
		}
	}

	var users []outlook.DirectoryUser
	if req.wantsAllUsers() {
		users = filterMicrosoftDirectoryUsers(directory, microsoftDirectoryFilter{EnabledOnly: true})
	} else {
		for _, id := range dedupeStrings(req.UserIDs) {
			u, ok := byID[id]
			if !ok || !u.AccountEnabled {
				return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadRequest, Code: "unknown_users",
					Message: "user_ids contains users that are not active in the tenant directory"}
			}
			users = append(users, u)
		}
		for _, email := range req.Emails {
			if strings.TrimSpace(email) == "" {
				continue
			}
			u, ok := byEmail[strings.ToLower(strings.TrimSpace(email))]
			if !ok || !u.AccountEnabled {
				return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadRequest, Code: "unknown_users",
					Message: fmt.Sprintf("%s is not an active user in the tenant directory", email)}
			}
			users = append(users, u)
		}
		// Shared mailboxes are usually backed by disabled accounts, so only existence is required.
		for _, email := range req.SharedMailboxes {
			if strings.TrimSpace(email) == "" {
				continue
			}
			u, ok := byEmail[strings.ToLower(strings.TrimSpace(email))]
			if !ok {
				return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadRequest, Code: "unknown_users",
					Message: fmt.Sprintf("%s is not a mailbox in the tenant directory", email)}
			}
			users = append(users, u)
		}
	}
	for _, u := range users {
		email := u.Email()
		if email == "" {
			continue
		}
		if _, dup := out.Users[strings.ToLower(email)]; dup {
			continue
		}
		out.Emails = append(out.Emails, email)
		out.Users[strings.ToLower(email)] = u
	}
	if len(out.Emails) == 0 {
		return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadRequest, Code: "no_users",
			Message: "select users with all_users, user_ids or emails"}
	}
	return out, nil
}

// applicationUsersFromRequestIDs builds directory users from paired emails+user_ids without a
// Graph directory crawl. Used when the UI already listed the directory (preview / selected mailboxes).
func applicationUsersFromRequestIDs(req *MicrosoftBackupOnboardingRequest) ([]outlook.DirectoryUser, bool) {
	if req == nil || req.wantsAllUsers() || len(req.SharedMailboxes) > 0 {
		return nil, false
	}
	if len(req.UserIDs) == 0 || len(req.Emails) == 0 || len(req.UserIDs) != len(req.Emails) {
		return nil, false
	}
	seen := map[string]struct{}{}
	out := make([]outlook.DirectoryUser, 0, len(req.UserIDs))
	for i := range req.UserIDs {
		email := strings.TrimSpace(req.Emails[i])
		id := strings.TrimSpace(req.UserIDs[i])
		if email == "" || id == "" {
			return nil, false
		}
		key := strings.ToLower(email)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, outlook.DirectoryUser{
			ObjectID: id, Mail: email, AccountEnabled: true,
		})
	}
	return out, len(out) > 0
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if _, ok := seen[s]; ok || s == "" {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// expandMicrosoftApplicationResources fills teams/sites with every tenant resource when the
// request asks for all users and listed none (app-only, full pagination).
func expandMicrosoftApplicationResources(ctx context.Context, req *MicrosoftBackupOnboardingRequest, token string, services []string) error {
	if !req.wantsAllTenantResources() {
		return nil
	}
	for _, svc := range services {
		switch svc {
		case "teams":
			if len(req.Teams) > 0 {
				continue
			}
			teams, err := outlook.ListTenantTeams(ctx, token, 0)
			if err != nil {
				return err
			}
			for _, t := range teams {
				req.Teams = append(req.Teams, TeamsOnboardingInput{TeamID: t.ID, TeamName: t.DisplayName})
			}
		case "sharepoint":
			if len(req.Sites) > 0 {
				continue
			}
			sites, err := outlook.ListTenantSharePointSites(ctx, token, "", 0)
			if err != nil {
				return err
			}
			for _, s := range sites {
				req.Sites = append(req.Sites, SharePointSiteOnboardingInput{SiteID: s.ID})
			}
			req.allSites = true
		}
	}
	return nil
}

// HandleMicrosoftAutomaticSyncCreate creates Microsoft backup jobs (POST /microsoft/auto-sync/job).
func HandleMicrosoftAutomaticSyncCreate(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}

	var req MicrosoftBackupOnboardingRequest
	if err := c.Bind(&req); err != nil {
		return jsonError(http.StatusBadRequest, "Invalid Request", err)
	}
	req.trim()
	return runMicrosoftOnboardingCreate(c, ctx, userID, &req)
}

func runMicrosoftOnboardingCreate(c echo.Context, ctx context.Context, userID string, req *MicrosoftBackupOnboardingRequest) error {
	if err := req.validate(userID); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	syncType, err := syncTypeFromQuery(c)
	if err != nil {
		return err
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	services := normalizeOnboardingServices(req.Services)
	application := req.applicationMode()

	tc, cred, err := resolveMicrosoftOnboardingTenant(c, ctx, database, userID, req, application)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	tenantID := tc.TenantID

	var emails []string
	var effectiveAccountType string
	var orgToken string
	users := map[string]outlook.DirectoryUser{}

	if application {
		appCtx, aerr := authorizeMicrosoftApplicationOnboarding(ctx, tc, req, services)
		if aerr != nil {
			return orgAccessErrorJSON(c, aerr)
		}
		orgToken = appCtx.AccessToken
		emails = appCtx.Emails
		users = appCtx.Users
		if err := expandMicrosoftApplicationResources(ctx, req, orgToken, services); err != nil {
			return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
		}
		effectiveAccountType = outlook.AccountTypeAdminWorkspace
	} else {
		var normErr error
		emails, normErr = normalizeGmailEmails(req.Emails, req.MicrosoftEmail)
		if normErr != nil {
			return normErr
		}
		if he := validateMicrosoftDelegatedOnboarding(req, services, emails); he != nil {
			return c.JSON(he.Code, he.Message)
		}
		self, serr := microsoftSelfDirectoryUser(ctx, database, tc, req.MicrosoftEmail)
		if serr != nil {
			return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": serr.Error()})
		}
		for _, e := range emails {
			users[strings.ToLower(e)] = self
		}
		effectiveAccountType = normalizeCredentialAccountTypeForMicrosoft(cred.AccountType)
		if effectiveAccountType == "" || effectiveAccountType == outlook.AccountTypeAdminWorkspace {
			effectiveAccountType = outlook.AccountTypeWorkAccount
		}
		if outlook.IsMSATenant(tenantID) {
			effectiveAccountType = outlook.AccountTypePersonal
		}
	}

	gReq := req.toGoogleShapeWithAccountType(effectiveAccountType)

	hasJobs, herr := database.CronJobRepo.HasLinkedJobsForCredential(userID, cred.ID)
	if herr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": herr.Error()})
	}
	userHasPolicies, perr := database.PolicyRepo.HasPoliciesForUser(userID)
	if perr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": perr.Error()})
	}
	isFirstConnection := isFirstOnboardingConnection(cred, hasJobs, userHasPolicies)
	if !isFirstConnection && !req.hasPolicyID() && req.PolicyName == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "policy_id or policy_name is required for subsequent connections",
		})
	}

	var schedule onboardingSchedule
	if onboardingNeedsScheduleInBody(isFirstConnection, gReq) {
		if strings.TrimSpace(req.Interval) == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "interval is required"})
		}
		schedule, err = parseOnboardingSchedule(req.Interval, req.On)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}
	}

	// Delegated jobs never resolve tenant resources at create time; application jobs use the app-only token.
	resourceToken := orgToken

	policyBatch := &onboardingPolicyBatch{}
	if !req.hasPolicyID() {
		policyBatch.allID = credentialPolicyIDByName(database, userID, cred.ID, req.PolicyName)
	}
	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	var skipped []onboardingSkippedResult
	var unverified []onboardingUnverifiedResult
	servicesOut := make([]string, 0)
	seenSvc := make(map[string]struct{})
	var verification map[string]interface{}

	needsMailboxMaterialize := false
	if application {
		for _, svc := range services {
			method := microsoftOnboardingServiceToMethod[svc]
			if outlook.IsAccessVerifyUserMethod(method) {
				needsMailboxMaterialize = true
				break
			}
		}
	}
	if needsMailboxMaterialize && req.SystemJobID == 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "system_job_id is required after Access Check for organization mailbox services",
		})
	}

	// Org mailbox jobs: materialize from the Access Check system job when provided.
	if application && req.SystemJobID > 0 {
		mat, merr := materializeAccessCheckJobs(c, ctx, database, userID, req.SystemJobID, syncType, schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, users, services)
		if merr != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": merr.Error()})
		}
		jobs = append(jobs, mat.Jobs...)
		failed = append(failed, mat.Failed...)
		skipped = append(skipped, mat.Skipped...)
		unverified = append(unverified, mat.Unverified...)
		for _, j := range mat.Jobs {
			if j.Service != "" {
				if _, dup := seenSvc[j.Service]; !dup {
					seenSvc[j.Service] = struct{}{}
					servicesOut = append(servicesOut, j.Service)
				}
			}
		}
		mode := mat.Mode
		status := "completed"
		if mode == repo.AccessCheckMaterializeDraft {
			status = "queued"
		}
		verification = map[string]interface{}{
			"status":         status,
			"mode":           mode,
			"system_job_id":  req.SystemJobID,
			"already_done":   mat.AlreadyDone,
		}
	}

	for _, svc := range services {
		if _, dup := seenSvc[svc]; dup {
			continue
		}
		seenSvc[svc] = struct{}{}
		servicesOut = append(servicesOut, svc)
		method, ok := microsoftOnboardingServiceToMethod[svc]
		if !ok {
			failed = append(failed, onboardingFailedResult{Service: svc, Error: "unknown service"})
			continue
		}
		if !allowedMethods[method] {
			failed = append(failed, onboardingFailedResult{Service: svc, Error: "method not enabled"})
			continue
		}
		switch method {
		case "outlook_sharepoint":
			j, f := createMicrosoftJobsForSharePointSites(c, ctx, userID, svc, syncType, schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, req.Sites, resourceToken, database)
			jobs = append(jobs, j...)
			failed = append(failed, f...)
			continue
		case "outlook_teams":
			j, f := createMicrosoftJobsForTeams(c, ctx, userID, svc, syncType, schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, req.Teams, resourceToken, database)
			jobs = append(jobs, j...)
			failed = append(failed, f...)
			continue
		}
		// Mailbox services already materialized from system_job_id.
		if application && req.SystemJobID > 0 && outlook.IsAccessVerifyUserMethod(method) {
			continue
		}
		j, f := createMicrosoftJobsForServiceEmails(c, userID, method, svc, syncType, schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, emails, users, database)
		jobs = append(jobs, j...)
		failed = append(failed, f...)
	}
	if application && syncType != "one_time" {
		allTypes := map[string]bool{repo.ResourceTypeUser: req.wantsAllUsers(), repo.ResourceTypeSite: req.allSites}
		saveMicrosoftTenantScopes(database, userID, cred, tenantID, syncType, servicesOut, allTypes, policyBatch.allID)
	}

	policies := onboardingPoliciesFromBatch(database, policyBatch)
	resp := map[string]interface{}{
		"success":    len(failed) == 0,
		"message":    syncCreateMessage(syncType),
		"auth_mode":  tc.Link.AuthMode,
		"tenant_id":  tenantID,
		"jobs":       nullSliceJSON(jobs),
		"failed":     nullSliceJSON(failed),
		"skipped":    nullSliceJSON(skipped),
		"unverified": nullSliceJSON(unverified),
		"services":   nullSliceJSON(servicesOut),
		"policies":   nullSliceJSON(policies),
	}
	if verification != nil {
		resp["verification"] = verification
	}
	return c.JSON(http.StatusOK, resp)
}

func createMicrosoftJobsForServiceEmails(
	c echo.Context, userID, method, svc, syncType string, schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest, cred *repo.GoogleBackupCredentialDB, tenantID string, isFirstConnection bool, policyBatch *onboardingPolicyBatch,
	emails []string, users map[string]outlook.DirectoryUser, database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	emails = dedupeEmailsPreservingOrder(emails)
	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	for _, targetEmail := range emails {
		u, ok := users[strings.ToLower(targetEmail)]
		if !ok || strings.TrimSpace(u.ObjectID) == "" {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: "user object ID is unknown in the tenant directory"})
			continue
		}
		if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: tenantID, ResourceType: repo.ResourceTypeUser, ExternalID: u.ObjectID,
			DisplayName: u.DisplayName, Mail: targetEmail, UPN: u.UPN,
		}); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: err.Error()})
			continue
		}
		identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeUser, ResourceID: u.ObjectID}
		job, fail := onboardMicrosoftJob(c, database, userID, svc, targetEmail, method, syncType, nil,
			identity, schedule, req, cred, isFirstConnection, policyBatch)
		jobs, failed = appendMicrosoftJobResult(jobs, failed, job, fail)
	}
	return jobs, failed
}

// onboardMicrosoftJob creates one job keyed by tenant + resource, applies its schedule, activates it
// and (one_time) queues a task. A job with the same identity is returned as existing so a retried
// onboarding succeeds; the display name is only a label.
func onboardMicrosoftJob(
	c echo.Context, database *db.PostgresDb, userID, svc, name, method, syncType string, extra map[string]interface{},
	identity repo.MicrosoftJobIdentity, schedule onboardingSchedule, req *GoogleBackupOnboardingRequest, cred *repo.GoogleBackupCredentialDB,
	isFirstConnection bool, policyBatch *onboardingPolicyBatch,
) (*onboardingJobResult, *onboardingFailedResult) {
	fail := func(msg string) (*onboardingJobResult, *onboardingFailedResult) {
		return nil, &onboardingFailedResult{Service: svc, Email: name, Error: msg}
	}

	existing, err := database.CronJobRepo.FindMicrosoftResourceJob(userID, identity.TenantID, identity.ResourceType, identity.ResourceID, method, syncType)
	if err != nil {
		return fail(err.Error())
	}
	var cronJob *repo.CronJobListingDB
	if existing == nil {
		created, createErr := database.CronJobRepo.CreateMicrosoftResourceJob(userID, name, method, syncType, cred.ID, identity, extra)
		if createErr != nil {
			if existing, _ = database.CronJobRepo.FindMicrosoftResourceJob(userID, identity.TenantID, identity.ResourceType, identity.ResourceID, method, syncType); existing == nil {
				return fail(extractCreateJobError(createErr))
			}
		}
		cronJob = created
	}
	if existing != nil {
		policyID := existing.PolicyID
		needSchedule := policyID == 0
		if !needSchedule && req != nil && req.hasPolicyID() && req.PolicyID != nil && *req.PolicyID != policyID {
			needSchedule = true
		}
		if needSchedule {
			// Retry after a failed policy assign, or reconnect onto a different policy_id.
			if err := applyOnboardingJobSchedule(database, userID, existing.ID, name, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
				return fail(err.Error())
			}
			if latest, _ := database.CronJobRepo.GetCronJobByID(existing.ID); latest != nil {
				policyID = latest.PolicyID
			}
		}
		active := existing.Active
		if syncType != "one_time" {
			if shouldActivateOnboardingJob(req, cred) {
				if err := database.CronJobRepo.UpdateCronJobByID(existing.ID, activeStateUpdateFields(true)); err != nil {
					return fail(fmt.Sprintf("job %d exists but activation failed: %v", existing.ID, err))
				}
				active = true
			} else if isForcedInactiveOnboarding(req) {
				_ = database.CronJobRepo.UpdateCronJobByID(existing.ID, activeStateUpdateFields(false))
				active = false
			}
		}
		if strings.TrimSpace(name) != "" && strings.TrimSpace(existing.Name) != strings.TrimSpace(name) {
			_ = database.CronJobRepo.UpdateCronJobByID(existing.ID, map[string]interface{}{"name": strings.TrimSpace(name)})
		}
		return &onboardingJobResult{
			Service: svc, Email: name, JobID: existing.ID, PolicyID: policyID,
			Existing: true, Active: active,
		}, nil
	}
	if err := applyOnboardingJobSchedule(database, userID, cronJob.ID, name, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
		if errors.Is(err, repo.ErrPolicyNameExists) {
			return fail("policy name already exists for user")
		}
		return fail(err.Error())
	}
	entry := &onboardingJobResult{Service: svc, Email: name, JobID: cronJob.ID}
	if syncType != "one_time" {
		if shouldActivateOnboardingJob(req, cred) {
			if err := database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(true)); err != nil {
				return fail(fmt.Sprintf("job %d created but activation failed: %v", cronJob.ID, err))
			}
			entry.Active = true
		} else if isForcedInactiveOnboarding(req) {
			_ = database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(false))
		}
	}
	if latest, _ := database.CronJobRepo.GetCronJobByID(cronJob.ID); latest != nil {
		entry.PolicyID = latest.PolicyID
	}
	if syncType == "one_time" {
		if isForcedInactiveOnboarding(req) {
			_ = database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(false))
		} else if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
			entry.TaskID = task.ID
		}
	}
	return entry, nil
}

// credentialPolicyIDByName returns the policy named policyName that already holds this
// credential's jobs (a retried onboarding), so new jobs join it instead of failing on the name.
func credentialPolicyIDByName(database *db.PostgresDb, userID string, credentialID uint, policyName string) uint {
	policyName = strings.TrimSpace(policyName)
	if policyName == "" {
		return 0
	}
	jobs, err := database.CronJobRepo.ListJobsByCredentialID(userID, credentialID)
	if err != nil || len(jobs) == 0 {
		return 0
	}
	ids := make([]uint, 0, len(jobs))
	for _, j := range jobs {
		if j.PolicyID > 0 {
			ids = append(ids, j.PolicyID)
		}
	}
	policies, err := database.PolicyRepo.GetByIDs(ids)
	if err != nil {
		return 0
	}
	for id, p := range policies {
		if p != nil && strings.EqualFold(strings.TrimSpace(p.Name), policyName) {
			return id
		}
	}
	return 0
}

func appendMicrosoftJobResult(jobs []onboardingJobResult, failed []onboardingFailedResult,
	job *onboardingJobResult, fail *onboardingFailedResult) ([]onboardingJobResult, []onboardingFailedResult) {
	if job != nil {
		jobs = append(jobs, *job)
	}
	if fail != nil {
		failed = append(failed, *fail)
	}
	return jobs, failed
}

const sharePointTenantJobName = "Microsoft SharePoint"

func createMicrosoftJobsForSharePointSites(
	c echo.Context,
	ctx context.Context,
	userID, svc, syncType string,
	schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	sites []SharePointSiteOnboardingInput,
	accessToken string,
	database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	if len(sites) == 0 {
		return nil, []onboardingFailedResult{{Service: svc, Error: "sites is required when sharepoint service is selected"}}
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, []onboardingFailedResult{{Service: svc, Error: "organization resources require backup_mode=organization"}}
	}

	var failed []onboardingFailedResult
	var skipped []onboardingJobResult
	seen := make(map[string]struct{})
	var resolvedSites []repo.SharePointSiteSelection

	for _, siteIn := range sites {
		siteID := strings.TrimSpace(siteIn.SiteID)
		siteURL := strings.TrimSpace(siteIn.SiteURL)
		if siteID == "" && siteURL == "" {
			failed = append(failed, onboardingFailedResult{Service: svc, Error: "each site requires site_id or site_url"})
			continue
		}
		resolved, rerr := outlook.ResolveSharePointSite(ctx, accessToken, siteID, siteURL)
		if rerr != nil {
			label := siteURL
			if label == "" {
				label = siteID
			}
			if errors.Is(rerr, outlook.ErrSharePointSiteNoDocumentLibrary) {
				skipped = append(skipped, onboardingJobResult{Service: svc, Email: label, Skipped: rerr.Error()})
			} else {
				failed = append(failed, onboardingFailedResult{Service: svc, Email: label, Error: rerr.Error()})
			}
			continue
		}
		if _, dup := seen[resolved.SiteID]; dup {
			continue
		}
		seen[resolved.SiteID] = struct{}{}

		if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: tenantID, ResourceType: repo.ResourceTypeSite, ExternalID: resolved.SiteID, DisplayName: resolved.SiteName,
			Metadata: dbjson.NewDbJsonFromValue(map[string]interface{}{"site_url": resolved.SiteURL, "drive_id": resolved.DriveID}),
		}); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: resolved.SiteName, Error: err.Error()})
			continue
		}
		resolvedSites = append(resolvedSites, repo.SharePointSiteSelection{
			SiteID: resolved.SiteID, DriveID: resolved.DriveID,
			SiteName: resolved.SiteName, SiteURL: resolved.SiteURL,
		})
	}
	if len(resolvedSites) == 0 {
		return skipped, failed
	}

	// One SharePoint job per tenant (same pattern as Teams): selection lives in input_data.sites.
	first := resolvedSites[0]
	extra := map[string]interface{}{
		"sites": []repo.SharePointSiteSelection{first},
		// Keep legacy fields for the first site so older readers still find site_id/drive_id.
		"site_id": first.SiteID, "drive_id": first.DriveID,
		"site_name": first.SiteName, "site_url": first.SiteURL,
	}
	for _, s := range resolvedSites[1:] {
		extra = repo.SharePointSelectionAddSite(extra, s)
	}
	identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeTenant, ResourceID: tenantID}
	job, fail := onboardMicrosoftJob(c, database, userID, svc, sharePointTenantJobName, "outlook_sharepoint", syncType, extra,
		identity, schedule, req, cred, isFirstConnection, policyBatch)
	if job != nil && job.Existing {
		err := database.CronJobRepo.UpdateJobInputData(job.JobID, func(in map[string]interface{}) map[string]interface{} {
			for _, s := range resolvedSites {
				in = repo.SharePointSelectionAddSite(in, s)
			}
			if sites := parseSharePointSitesFromInput(in); len(sites) > 0 {
				in["site_id"], in["drive_id"] = sites[0].SiteID, sites[0].DriveID
				in["site_name"], in["site_url"] = sites[0].SiteName, sites[0].SiteURL
			}
			return in
		})
		if err != nil {
			return skipped, append(failed, onboardingFailedResult{Service: svc, Email: sharePointTenantJobName, Error: err.Error()})
		}
	}
	jobs, failed := appendMicrosoftJobResult(skipped, failed, job, fail)
	return jobs, failed
}

func parseSharePointSitesFromInput(in map[string]interface{}) []repo.SharePointSiteSelection {
	return repo.JobSharePointSites(&repo.CronJobListingDB{
		InputData: dbjson.NewDbJsonFromValue(in),
	})
}

func createMicrosoftJobsForTeams(
	c echo.Context,
	ctx context.Context,
	userID, svc, syncType string,
	schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	teams []TeamsOnboardingInput,
	accessToken string,
	database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	if len(teams) == 0 {
		return nil, []onboardingFailedResult{{Service: svc, Error: "teams is required when teams service is selected"}}
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, []onboardingFailedResult{{Service: svc, Error: "organization resources require backup_mode=organization"}}
	}

	var failed []onboardingFailedResult
	seen := make(map[string]struct{})
	var resolvedTeams []*outlook.ResolvedTeam

	for _, teamIn := range teams {
		teamID := strings.TrimSpace(teamIn.TeamID)
		if teamID == "" {
			failed = append(failed, onboardingFailedResult{Service: svc, Error: "each team requires team_id"})
			continue
		}
		resolved, rerr := outlook.ResolveTeam(ctx, accessToken, teamID, teamIn.ChannelIDs)
		if rerr != nil {
			label := teamID
			if strings.TrimSpace(teamIn.TeamName) != "" {
				label = strings.TrimSpace(teamIn.TeamName)
			}
			failed = append(failed, onboardingFailedResult{Service: svc, Email: label, Error: rerr.Error()})
			continue
		}
		if _, dup := seen[resolved.TeamID]; dup {
			continue
		}
		seen[resolved.TeamID] = struct{}{}

		if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: tenantID, ResourceType: repo.ResourceTypeTeam, ExternalID: resolved.TeamID, DisplayName: resolved.TeamName,
			Metadata: dbjson.NewDbJsonFromValue(map[string]interface{}{"web_url": resolved.TeamWebURL, "group_id": resolved.GroupID}),
		}); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: resolved.TeamID, Error: err.Error()})
			continue
		}
		resolvedTeams = append(resolvedTeams, resolved)
	}
	if len(resolvedTeams) == 0 {
		return nil, failed
	}

	// One Teams job per tenant: created with the first selection, or the selection of the
	// existing job is extended (union only, row locked).
	first := resolvedTeams[0]
	extra := map[string]interface{}{"team_ids": []string{first.TeamID}}
	if len(first.ChannelIDs) > 0 {
		extra["team_channel_ids"] = map[string][]string{first.TeamID: first.ChannelIDs}
	}
	for _, t := range resolvedTeams[1:] {
		extra = repo.TeamsSelectionAddTeam(extra, t.TeamID, t.ChannelIDs)
	}
	identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeTenant, ResourceID: tenantID}
	job, fail := onboardMicrosoftJob(c, database, userID, svc, teamsTenantJobName, "outlook_teams", syncType, extra,
		identity, schedule, req, cred, isFirstConnection, policyBatch)
	if job != nil && job.Existing {
		err := database.CronJobRepo.UpdateJobInputData(job.JobID, func(in map[string]interface{}) map[string]interface{} {
			for _, t := range resolvedTeams {
				in = repo.TeamsSelectionAddTeam(in, t.TeamID, t.ChannelIDs)
			}
			return in
		})
		if err != nil {
			return nil, append(failed, onboardingFailedResult{Service: svc, Email: teamsTenantJobName, Error: err.Error()})
		}
	}
	return appendMicrosoftJobResult(nil, failed, job, fail)
}

// teamsTenantJobName labels the tenant Teams job (its identity is the tenant id).
const teamsTenantJobName = "Microsoft Teams"

func mergeOnboardingEmails(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, e := range append(base, extra...) {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		key := strings.ToLower(e)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}
