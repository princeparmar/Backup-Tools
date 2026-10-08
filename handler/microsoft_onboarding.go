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

// GroupsOnboardingInput selects an M365 Group for outlook_groups jobs.
type GroupsOnboardingInput struct {
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name,omitempty"`
}

// MicrosoftBackupOnboardingRequest is the Satellite → Backup-Tools MS job create body.
// Both POST /microsoft/auto-sync/job and POST /microsoft/backup/onboarding/jobs use this.
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
	AuthMode        string                          `json:"auth_mode"`
	BackupMode      string                          `json:"backup_mode"`
	AllUsers        bool                            `json:"all_users"`
	UserIDs         []string                        `json:"user_ids"`
	Emails          []string                        `json:"emails"`
	SharedMailboxes []string                        `json:"shared_mailboxes"`
	BackupScope     string                          `json:"backup_scope"`
	Sites           []SharePointSiteOnboardingInput `json:"sites"`
	Teams           []TeamsOnboardingInput          `json:"teams"`
	Groups          []GroupsOnboardingInput         `json:"groups"`
	PolicyID        *uint                           `json:"policy_id,omitempty"`
	PolicyName      string                          `json:"policy_name,omitempty"`
	PolicyScope     string                          `json:"policy_scope,omitempty"`
	// EmailOrgUnits overrides the directory org unit per mailbox (application mode fills it from the directory).
	EmailOrgUnits    map[string]string               `json:"email_org_units,omitempty"`
	OrgUnitSchedules map[string]OrgUnitScheduleInput `json:"org_unit_schedules,omitempty"`
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
	r.PolicyScope = strings.TrimSpace(r.PolicyScope)
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

func (r *MicrosoftBackupOnboardingRequest) wantsAllUsers() bool {
	return r.AllUsers || strings.EqualFold(r.BackupScope, "all_tenant")
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
	if !r.hasPolicyID() && r.Interval == "" && !strings.EqualFold(r.PolicyScope, OnboardingPolicyScopeOrgUnit) {
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

func validateMicrosoftOnboardingServiceNames(raw []string) error {
	for _, svc := range normalizeOnboardingServices(raw) {
		if _, ok := microsoftOnboardingServiceToMethod[svc]; !ok {
			return fmt.Errorf("unknown service %q", svc)
		}
	}
	return nil
}

func (r *MicrosoftBackupOnboardingRequest) toGoogleShape() *GoogleBackupOnboardingRequest {
	return r.toGoogleShapeWithAccountType(r.AccountType)
}

func (r *MicrosoftBackupOnboardingRequest) toGoogleShapeWithAccountType(accountType string) *GoogleBackupOnboardingRequest {
	acct := strings.TrimSpace(accountType)
	if acct == "" {
		acct = "personal"
	}
	return &GoogleBackupOnboardingRequest{
		Services:         r.Services,
		Interval:         r.Interval,
		On:               r.On,
		GoogleEmail:      r.MicrosoftEmail,
		AccountType:      acct,
		ProjectID:        r.ProjectID,
		SatelliteUserID:  r.SatelliteUserID,
		RefreshToken:     r.RefreshToken,
		StorxToken:       r.StorxToken,
		Emails:           r.Emails,
		EmailOrgUnits:    r.EmailOrgUnits,
		PolicyID:         r.PolicyID,
		PolicyName:       r.PolicyName,
		PolicyScope:      r.PolicyScope,
		OrgUnitSchedules: r.OrgUnitSchedules,
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
	OrgUnits    map[string]string
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
	out := &microsoftApplicationOnboarding{AccessToken: token, Tenant: tenant, OrgUnits: map[string]string{}, Users: map[string]outlook.DirectoryUser{}}
	if !needsUsers {
		return out, nil
	}

	directory, err := msListDirectoryUsersFn(ctx, token)
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
		out.OrgUnits[email] = u.OrgUnitPath()
		out.Users[strings.ToLower(email)] = u
	}
	if len(out.Emails) == 0 {
		return nil, &MicrosoftOrgAccessError{HTTPStatus: http.StatusBadRequest, Code: "no_users",
			Message: "select users with all_users, user_ids or emails"}
	}
	return out, nil
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

// expandMicrosoftApplicationResources fills teams/groups/sites with every tenant resource when the
// request asks for all users and listed none (app-only, full pagination).
func expandMicrosoftApplicationResources(ctx context.Context, req *MicrosoftBackupOnboardingRequest, token string, services []string) error {
	if !req.wantsAllUsers() {
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
		// Groups backup is hidden for now.
		// case "groups":
		// 	if len(req.Groups) > 0 {
		// 		continue
		// 	}
		// 	groups, err := outlook.ListTenantGroups(ctx, token, 0)
		// 	if err != nil {
		// 		return err
		// 	}
		// 	for _, g := range groups {
		// 		req.Groups = append(req.Groups, GroupsOnboardingInput{GroupID: g.ID, GroupName: g.DisplayName})
		// 	}
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
		}
	}
	return nil
}

// HandleMicrosoftAutomaticSyncCreate and HandleMicrosoftBackupOnboardingJobs share one create path.
func HandleMicrosoftAutomaticSyncCreate(c echo.Context) error {
	return handleMicrosoftOnboardingCreate(c)
}

// HandleMicrosoftBackupOnboardingJobs is an alias of HandleMicrosoftAutomaticSyncCreate.
func HandleMicrosoftBackupOnboardingJobs(c echo.Context) error {
	return handleMicrosoftOnboardingCreate(c)
}

func handleMicrosoftOnboardingCreate(c echo.Context) error {
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
		if len(appCtx.OrgUnits) > 0 {
			merged := make(map[string]string, len(appCtx.OrgUnits)+len(req.EmailOrgUnits))
			for k, v := range appCtx.OrgUnits {
				merged[k] = v
			}
			for k, v := range req.EmailOrgUnits {
				merged[k] = v
			}
			req.EmailOrgUnits = merged
		}
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
	if err := validateOrgUnitOnboardingSchedules(gReq, emails); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	hasJobs, herr := database.CronJobRepo.HasLinkedJobsForCredential(userID, cred.ID)
	if herr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": herr.Error()})
	}
	userHasPolicies, perr := database.PolicyRepo.HasPoliciesForUser(userID)
	if perr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": perr.Error()})
	}
	isFirstConnection := isFirstOnboardingConnection(cred, hasJobs, userHasPolicies)
	if !isFirstConnection && !req.hasPolicyID() && req.PolicyName == "" && !gReq.isOrgUnitPolicyScope() {
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
	if !gReq.isOrgUnitPolicyScope() && !req.hasPolicyID() {
		policyBatch.allID = credentialPolicyIDByName(database, userID, cred.ID, req.PolicyName)
	}
	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	servicesOut := make([]string, 0)
	seenSvc := make(map[string]struct{})

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
			// Groups backup is hidden for now.
			// case "outlook_groups":
			// 	j, f := createMicrosoftJobsForGroups(c, ctx, userID, svc, syncType, schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, req.Groups, resourceToken, database)
			// 	jobs = append(jobs, j...)
			// 	failed = append(failed, f...)
			// 	continue
		}
		targetEmails := emails
		if gReq.isOrgUnitPolicyScope() {
			targetEmails = emailsWithOrgUnitService(gReq, emails, svc)
		}
		j, f := createMicrosoftJobsForServiceEmails(c, userID, method, svc, syncType, schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, targetEmails, users, database)
		jobs = append(jobs, j...)
		failed = append(failed, f...)
	}
	if application && syncType != "one_time" {
		saveMicrosoftTenantScopes(database, userID, cred, tenantID, syncType, servicesOut, req.wantsAllUsers(), policyBatch.allID)
	}

	policies := onboardingPoliciesFromBatch(database, policyBatch)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":   len(failed) == 0,
		"message":   syncCreateMessage(syncType),
		"auth_mode": tc.Link.AuthMode,
		"tenant_id": tenantID,
		"jobs":      nullSliceJSON(jobs),
		"failed":    nullSliceJSON(failed),
		"services":  nullSliceJSON(servicesOut),
		"policies":  nullSliceJSON(policies),
	})
}

// emailsWithOrgUnitService keeps mailboxes whose org unit schedule includes svc.
func emailsWithOrgUnitService(req *GoogleBackupOnboardingRequest, emails []string, svc string) []string {
	out := make([]string, 0, len(emails))
	for _, email := range emails {
		path := orgUnitPathForEmail(req, email)
		if path == "" {
			path = "/"
		}
		svcs, err := servicesForOnboardingOrgUnit(req, path)
		if err != nil {
			continue
		}
		for _, s := range svcs {
			if s == svc {
				out = append(out, email)
				break
			}
		}
	}
	return out
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
		job, fail := onboardMicrosoftJob(c, database, userID, svc, targetEmail, method, syncType, orgUnitInputData(req, targetEmail),
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
		if policyID == 0 {
			// A previous attempt created the job but failed to assign its policy.
			if err := applyOnboardingJobSchedule(database, userID, existing.ID, name, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
				return fail(err.Error())
			}
			if latest, _ := database.CronJobRepo.GetCronJobByID(existing.ID); latest != nil {
				policyID = latest.PolicyID
			}
		}
		return &onboardingJobResult{
			Service: svc, Email: name, JobID: existing.ID, PolicyID: policyID,
			Existing: true, Active: existing.Active,
		}, nil
	}
	if err := applyOnboardingJobSchedule(database, userID, cronJob.ID, name, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
		if errors.Is(err, repo.ErrPolicyNameExists) {
			return fail("policy name already exists for user")
		}
		return fail(err.Error())
	}
	if syncType != "one_time" && hasStorxTokenAtJobCreate(req, cred) {
		if err := database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(true)); err != nil {
			return fail(fmt.Sprintf("job %d created but activation failed: %v", cronJob.ID, err))
		}
	}
	entry := &onboardingJobResult{Service: svc, Email: name, JobID: cronJob.ID}
	if latest, _ := database.CronJobRepo.GetCronJobByID(cronJob.ID); latest != nil {
		entry.PolicyID = latest.PolicyID
	}
	if syncType == "one_time" {
		if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
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

	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	seen := make(map[string]struct{})

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
				jobs = append(jobs, onboardingJobResult{Service: svc, Email: label, Skipped: rerr.Error()})
			} else {
				failed = append(failed, onboardingFailedResult{Service: svc, Email: label, Error: rerr.Error()})
			}
			continue
		}
		if _, dup := seen[resolved.SiteID]; dup {
			continue
		}
		seen[resolved.SiteID] = struct{}{}

		jobName := strings.TrimSpace(resolved.SiteName)
		if jobName == "" {
			jobName = outlook.SanitizeSharePointSiteKey(resolved.SiteID)
		}
		extra := map[string]interface{}{
			"site_id":   resolved.SiteID,
			"drive_id":  resolved.DriveID,
			"site_name": resolved.SiteName,
			"site_url":  resolved.SiteURL,
		}
		if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: tenantID, ResourceType: repo.ResourceTypeSite, ExternalID: resolved.SiteID, DisplayName: resolved.SiteName,
			Metadata: dbjson.NewDbJsonFromValue(map[string]interface{}{"site_url": resolved.SiteURL, "drive_id": resolved.DriveID}),
		}); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: err.Error()})
			continue
		}
		identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeSite, ResourceID: resolved.SiteID}
		job, fail := onboardMicrosoftJob(c, database, userID, svc, jobName, "outlook_sharepoint", syncType, extra,
			identity, schedule, req, cred, isFirstConnection, policyBatch)
		jobs, failed = appendMicrosoftJobResult(jobs, failed, job, fail)
	}
	return jobs, failed
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

	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	seen := make(map[string]struct{})

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

		jobName := strings.TrimSpace(resolved.TeamName)
		if jobName == "" {
			jobName = outlook.SanitizeTeamsTeamKey(resolved.TeamID)
		}
		extra := map[string]interface{}{
			"team_id":      resolved.TeamID,
			"team_name":    resolved.TeamName,
			"team_web_url": resolved.TeamWebURL,
			"group_id":     resolved.GroupID,
		}
		if len(resolved.ChannelIDs) > 0 {
			extra["channel_ids"] = resolved.ChannelIDs
		}
		if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: tenantID, ResourceType: repo.ResourceTypeTeam, ExternalID: resolved.TeamID, DisplayName: resolved.TeamName,
			Metadata: dbjson.NewDbJsonFromValue(map[string]interface{}{"web_url": resolved.TeamWebURL, "group_id": resolved.GroupID}),
		}); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: err.Error()})
			continue
		}
		identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeTeam, ResourceID: resolved.TeamID}
		job, fail := onboardMicrosoftJob(c, database, userID, svc, jobName, "outlook_teams", syncType, extra,
			identity, schedule, req, cred, isFirstConnection, policyBatch)
		jobs, failed = appendMicrosoftJobResult(jobs, failed, job, fail)
	}
	return jobs, failed
}

func createMicrosoftJobsForGroups(
	c echo.Context,
	ctx context.Context,
	userID, svc, syncType string,
	schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	groups []GroupsOnboardingInput,
	accessToken string,
	database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	if len(groups) == 0 {
		return nil, []onboardingFailedResult{{Service: svc, Error: "groups is required when groups service is selected"}}
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, []onboardingFailedResult{{Service: svc, Error: "organization resources require backup_mode=organization"}}
	}

	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	seen := make(map[string]struct{})

	for _, groupIn := range groups {
		groupID := strings.TrimSpace(groupIn.GroupID)
		if groupID == "" {
			failed = append(failed, onboardingFailedResult{Service: svc, Error: "each group requires group_id"})
			continue
		}
		resolved, rerr := outlook.ResolveGroup(ctx, accessToken, groupID)
		if rerr != nil {
			label := groupID
			if strings.TrimSpace(groupIn.GroupName) != "" {
				label = strings.TrimSpace(groupIn.GroupName)
			}
			failed = append(failed, onboardingFailedResult{Service: svc, Email: label, Error: rerr.Error()})
			continue
		}
		if _, dup := seen[resolved.GroupID]; dup {
			continue
		}
		seen[resolved.GroupID] = struct{}{}

		jobName := strings.TrimSpace(resolved.GroupName)
		if jobName == "" {
			jobName = outlook.SanitizeGroupsGroupKey(resolved.GroupID)
		}
		extra := map[string]interface{}{
			"group_id":   resolved.GroupID,
			"group_name": resolved.GroupName,
			"group_mail": resolved.GroupMail,
		}
		if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
			TenantID: tenantID, ResourceType: repo.ResourceTypeGroup, ExternalID: resolved.GroupID, DisplayName: resolved.GroupName, Mail: resolved.GroupMail,
		}); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: err.Error()})
			continue
		}
		identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeGroup, ResourceID: resolved.GroupID}
		job, fail := onboardMicrosoftJob(c, database, userID, svc, jobName, "outlook_groups", syncType, extra,
			identity, schedule, req, cred, isFirstConnection, policyBatch)
		jobs, failed = appendMicrosoftJobResult(jobs, failed, job, fail)
	}
	return jobs, failed
}

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
