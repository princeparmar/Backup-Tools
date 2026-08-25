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
	TeamID      string   `json:"team_id"`
	TeamName    string   `json:"team_name,omitempty"`
	ChannelIDs  []string `json:"channel_ids,omitempty"`
}

// GroupsOnboardingInput selects an M365 Group for outlook_groups jobs.
type GroupsOnboardingInput struct {
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name,omitempty"`
}

// MicrosoftBackupOnboardingRequest is the Satellite → Backup-Tools MS job create body.
// Both POST /microsoft/auto-sync/job and POST /microsoft/backup/onboarding/jobs use this.
type MicrosoftBackupOnboardingRequest struct {
	Services        []string `json:"services"`
	Interval        string   `json:"interval"`
	On              string   `json:"on"`
	MicrosoftEmail  string   `json:"microsoft_email"`
	AccountType     string   `json:"account_type"`
	TenantID        string   `json:"tenant_id"`
	TenantName      string   `json:"tenant_name"`
	ProjectID       string   `json:"project_id"`
	SatelliteUserID string   `json:"satellite_user_id"`
	RefreshToken    string   `json:"refresh_token"`
	StorxToken      string   `json:"storx_token,omitempty"`
	Emails          []string                        `json:"emails"`
	SharedMailboxes []string                        `json:"shared_mailboxes"`
	BackupScope     string                          `json:"backup_scope"`
	MicrosoftAppClientID     string                 `json:"microsoft_app_client_id"`
	MicrosoftAppClientSecret string                 `json:"microsoft_app_client_secret"`
	Sites           []SharePointSiteOnboardingInput `json:"sites"`
	Teams           []TeamsOnboardingInput          `json:"teams"`
	Groups          []GroupsOnboardingInput         `json:"groups"`
	PolicyID        *uint                           `json:"policy_id,omitempty"`
	PolicyName      string   `json:"policy_name,omitempty"`
}

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
	r.TenantID = strings.TrimSpace(r.TenantID)
	r.TenantName = strings.TrimSpace(r.TenantName)
	r.BackupScope = strings.TrimSpace(r.BackupScope)
	r.MicrosoftAppClientID = strings.TrimSpace(r.MicrosoftAppClientID)
	r.MicrosoftAppClientSecret = strings.TrimSpace(r.MicrosoftAppClientSecret)
}

func (r *MicrosoftBackupOnboardingRequest) hasPolicyID() bool {
	return r.PolicyID != nil && *r.PolicyID > 0
}

func (r *MicrosoftBackupOnboardingRequest) validate(userID string) error {
	if r.RefreshToken == "" {
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

func validateMicrosoftOnboardingServiceNames(raw []string) error {
	for _, svc := range normalizeOnboardingServices(raw) {
		if _, ok := microsoftOnboardingServiceToMethod[svc]; !ok {
			return fmt.Errorf("unknown service %q", svc)
		}
	}
	return nil
}

func rejectPersonalOrgServices(accountType string, services []string) error {
	if strings.ToLower(strings.TrimSpace(accountType)) != "personal" {
		return nil
	}
	for _, svc := range services {
		switch svc {
		case "teams", "groups", "sharepoint":
			return fmt.Errorf("service %q is not available for personal Microsoft accounts", svc)
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
		Services:        r.Services,
		Interval:        r.Interval,
		On:              r.On,
		GoogleEmail:     r.MicrosoftEmail,
		AccountType:     acct,
		ProjectID:       r.ProjectID,
		SatelliteUserID: r.SatelliteUserID,
		RefreshToken:    r.RefreshToken,
		StorxToken:      r.StorxToken,
		Emails:          r.Emails,
		PolicyID:        r.PolicyID,
		PolicyName:      r.PolicyName,
	}
}

func normalizeCredentialAccountTypeForMicrosoft(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "personal", "employee_workspace", "admin_workspace":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return ""
	}
}

// validateMicrosoftAdminForOnboarding enforces org-backup rules using stored account_type (no Graph re-detect).
func validateMicrosoftAdminForOnboarding(
	services, emails []string,
	sites []SharePointSiteOnboardingInput,
	teams []TeamsOnboardingInput,
	groups []GroupsOnboardingInput,
	connectedEmail, accountType, tenantID string,
	existingCred *repo.GoogleBackupCredentialDB,
) error {
	connectedEmail = strings.TrimSpace(connectedEmail)
	accountType = strings.ToLower(strings.TrimSpace(accountType))
	if accountType == "" {
		accountType = "personal"
	}
	if existingCred != nil && strings.TrimSpace(existingCred.TenantID) != "" && strings.TrimSpace(tenantID) == "" {
		tenantID = strings.TrimSpace(existingCred.TenantID)
	}

	hasSharePoint := false
	for _, svc := range services {
		if svc == "sharepoint" {
			hasSharePoint = true
			break
		}
	}
	if hasSharePoint {
		if accountType != outlook.AccountTypeAdminWorkspace {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "SharePoint backup requires an organization admin account",
			})
		}
		if len(sites) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "sites is required when sharepoint service is selected",
			})
		}
	}

	hasTeams := false
	for _, svc := range services {
		if svc == "teams" {
			hasTeams = true
			break
		}
	}
	if hasTeams {
		if accountType == "personal" {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "Teams backup is not available for personal Microsoft accounts",
			})
		}
		if len(teams) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "teams is required when teams service is selected",
			})
		}
	}

	hasGroups := false
	for _, svc := range services {
		if svc == "groups" {
			hasGroups = true
			break
		}
	}
	if hasGroups {
		if accountType == "personal" {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "Groups backup is not available for personal Microsoft accounts",
			})
		}
		if len(groups) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, map[string]interface{}{
				"error": "groups is required when groups service is selected",
			})
		}
	}

	needsOtherMailboxes := false
	for _, e := range emails {
		if !strings.EqualFold(strings.TrimSpace(e), connectedEmail) {
			needsOtherMailboxes = true
			break
		}
	}
	if !needsOtherMailboxes {
		return nil
	}
	if accountType != outlook.AccountTypeAdminWorkspace {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{
			"error": "Only Microsoft 365 admins can backup other users' accounts",
		})
	}
	if strings.TrimSpace(tenantID) == "" {
		return echo.NewHTTPError(http.StatusForbidden, map[string]interface{}{
			"error": "tenant_id is required for multi-mailbox organization backup",
		})
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

	emails, normErr := normalizeGmailEmails(req.Emails, req.MicrosoftEmail)
	if normErr != nil {
		return normErr
	}

	services := normalizeOnboardingServices(req.Services)

	existingCred, found, findErr := database.CredentialRepo.FindByUserProjectAndEmail(userID, req.ProjectID, req.MicrosoftEmail)
	if findErr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": findErr.Error()})
	}

	effectiveAccountType := strings.TrimSpace(req.AccountType)
	effectiveTenantID := strings.TrimSpace(req.TenantID)
	effectiveTenantName := strings.TrimSpace(req.TenantName)
	if found && existingCred != nil {
		if bodyType := normalizeCredentialAccountTypeForMicrosoft(req.AccountType); bodyType != "" &&
			!strings.EqualFold(bodyType, strings.TrimSpace(existingCred.AccountType)) {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "account_type mismatch with connected credential",
			})
		}
		effectiveAccountType = strings.TrimSpace(existingCred.AccountType)
		if effectiveTenantID == "" {
			effectiveTenantID = strings.TrimSpace(existingCred.TenantID)
		}
		if effectiveTenantName == "" {
			effectiveTenantName = strings.TrimSpace(existingCred.TenantName)
		}
	}
	if effectiveAccountType == "" {
		effectiveAccountType = "personal"
	}
	if strings.EqualFold(strings.TrimSpace(req.BackupScope), "all_tenant") {
		if effectiveAccountType != outlook.AccountTypeAdminWorkspace {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "all_tenant backup requires admin_workspace account",
			})
		}
		expanded, expandErr := expandMicrosoftTenantMailboxEmails(ctx, req.RefreshToken, effectiveTenantID)
		if expandErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": expandErr.Error()})
		}
		emails = expanded
		for _, svc := range services {
			switch svc {
			case "teams":
				if len(req.Teams) == 0 {
					tenantTeams, terr := expandMicrosoftTenantTeams(ctx, req.RefreshToken)
					if terr != nil {
						return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": terr.Error()})
					}
					req.Teams = tenantTeams
				}
			case "groups":
				if len(req.Groups) == 0 {
					tenantGroups, gerr := expandMicrosoftTenantGroups(ctx, req.RefreshToken)
					if gerr != nil {
						return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": gerr.Error()})
					}
					req.Groups = tenantGroups
				}
			}
		}
	}

	if err := rejectPersonalOrgServices(effectiveAccountType, services); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}

	if err := validateMicrosoftAdminForOnboarding(services, emails, req.Sites, req.Teams, req.Groups, req.MicrosoftEmail, effectiveAccountType, effectiveTenantID, existingCred); err != nil {
		if he, ok := err.(*echo.HTTPError); ok {
			return c.JSON(he.Code, he.Message)
		}
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	}

	gReq := req.toGoogleShapeWithAccountType(effectiveAccountType)
	cred, err := database.CredentialRepo.FindOrCreateForUserWithTenant(
		userID, req.MicrosoftEmail, req.ProjectID, effectiveAccountType,
		effectiveTenantID, effectiveTenantName, req.RefreshToken, req.StorxToken,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	if storx := strings.TrimSpace(req.StorxToken); storx != "" {
		if pid := extractProjectIDFromStorxGrant(ctx, storx); pid != "" {
			if uerr := database.CredentialRepo.UpdateStorjProjectID(cred.ID, pid); uerr == nil {
				if reloaded, rerr := database.CredentialRepo.GetByID(cred.ID); rerr == nil {
					cred = reloaded
				}
			}
		}
	}

	if req.MicrosoftAppClientID != "" && req.MicrosoftAppClientSecret != "" {
		if uerr := database.CredentialRepo.UpdateMicrosoftAppCredentials(
			ctx, cred.ID, outlook.MicrosoftAuthModeApplication, req.MicrosoftAppClientID, req.MicrosoftAppClientSecret,
		); uerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": uerr.Error()})
		}
		if reloaded, rerr := database.CredentialRepo.GetByID(cred.ID); rerr == nil {
			cred = reloaded
		}
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
	if !isFirstConnection && (req.PolicyID == nil || *req.PolicyID == 0) && strings.TrimSpace(req.PolicyName) == "" {
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

	policyBatch := &onboardingPolicyBatch{}
	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	servicesOut := make([]string, 0)
	seenSvc := make(map[string]struct{})

	for _, svc := range normalizeOnboardingServices(req.Services) {
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
		if method == "outlook_sharepoint" {
			j, f := createMicrosoftJobsForSharePointSites(c, ctx, userID, svc, syncType, schedule, gReq, cred, isFirstConnection, policyBatch, req.Sites, req.RefreshToken, database)
			jobs = append(jobs, j...)
			failed = append(failed, f...)
			continue
		}
		if method == "outlook_teams" {
			j, f := createMicrosoftJobsForTeams(c, ctx, userID, svc, syncType, schedule, gReq, cred, isFirstConnection, policyBatch, req.Teams, req.RefreshToken, database)
			jobs = append(jobs, j...)
			failed = append(failed, f...)
			continue
		}
		if method == "outlook_groups" {
			j, f := createMicrosoftJobsForGroups(c, ctx, userID, svc, syncType, schedule, gReq, cred, isFirstConnection, policyBatch, req.Groups, req.RefreshToken, database)
			jobs = append(jobs, j...)
			failed = append(failed, f...)
			continue
		}
		targetEmails := emails
		if method == "outlook" && len(req.SharedMailboxes) > 0 {
			targetEmails = mergeOnboardingEmails(emails, req.SharedMailboxes)
		}
		j, f := createMicrosoftJobsForServiceEmails(c, userID, method, svc, syncType, schedule, gReq, cred, isFirstConnection, policyBatch, targetEmails, database)
		jobs = append(jobs, j...)
		failed = append(failed, f...)
	}

	policies := onboardingPoliciesFromBatch(database, policyBatch)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":  len(failed) == 0,
		"message":  syncCreateMessage(syncType),
		"jobs":     nullSliceJSON(jobs),
		"failed":   nullSliceJSON(failed),
		"services": nullSliceJSON(servicesOut),
		"policies": nullSliceJSON(policies),
	})
}

func createMicrosoftJobsForServiceEmails(
	c echo.Context, userID, method, svc, syncType string, schedule onboardingSchedule,
	req *GoogleBackupOnboardingRequest, cred *repo.GoogleBackupCredentialDB, isFirstConnection bool, policyBatch *onboardingPolicyBatch,
	emails []string, database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	emails = dedupeEmailsPreservingOrder(emails)
	var jobs []onboardingJobResult
	var failed []onboardingFailedResult
	for _, targetEmail := range emails {
		cronJob, createErr := createSyncJobWithCredential(userID, targetEmail, method, syncType, cred.ID, nil, c)
		if createErr != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: extractCreateJobError(createErr)})
			continue
		}
		if err := applyOnboardingJobSchedule(database, userID, cronJob.ID, targetEmail, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
			if errors.Is(err, repo.ErrPolicyNameExists) {
				failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: "policy name already exists for user"})
				continue
			}
			failed = append(failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: err.Error()})
			continue
		}
		if syncType != "one_time" && hasStorxTokenAtJobCreate(req, cred) {
			if err := database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(true)); err != nil {
				failed = append(failed, onboardingFailedResult{
					Service: svc, Email: targetEmail,
					Error: fmt.Sprintf("job %d created but activation failed: %v", cronJob.ID, err),
				})
				continue
			}
		}
		latestJob, _ := database.CronJobRepo.GetCronJobByID(cronJob.ID)
		var policy *repo.AutosyncBackupPolicyDB
		if latestJob != nil && latestJob.PolicyID > 0 {
			policy, _ = database.PolicyRepo.GetByID(latestJob.PolicyID)
		}
		entry := onboardingJobResult{Service: svc, Email: targetEmail, JobID: cronJob.ID}
		if policy != nil {
			entry.PolicyID = policy.ID
		}
		if syncType == "one_time" {
			if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
				entry.TaskID = task.ID
			}
		}
		jobs = append(jobs, entry)
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
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	sites []SharePointSiteOnboardingInput,
	refreshToken string,
	database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	if len(sites) == 0 {
		return nil, []onboardingFailedResult{{Service: svc, Error: "sites is required when sharepoint service is selected"}}
	}
	accessToken, err := outlook.AuthTokenUsingRefreshToken(strings.TrimSpace(refreshToken))
	if err != nil {
		return nil, []onboardingFailedResult{{Service: svc, Error: fmt.Sprintf("token refresh failed: %v", err)}}
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
			label := siteID
			if label == "" {
				label = siteURL
			}
			failed = append(failed, onboardingFailedResult{Service: svc, Email: label, Error: rerr.Error()})
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
		cronJob, createErr := createSyncJobWithCredential(userID, jobName, "outlook_sharepoint", syncType, cred.ID, extra, c)
		if createErr != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: extractCreateJobError(createErr)})
			continue
		}
		if err := applyOnboardingJobSchedule(database, userID, cronJob.ID, jobName, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: err.Error()})
			continue
		}
		if syncType != "one_time" && hasStorxTokenAtJobCreate(req, cred) {
			if err := database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(true)); err != nil {
				failed = append(failed, onboardingFailedResult{
					Service: svc, Email: jobName,
					Error: fmt.Sprintf("job %d created but activation failed: %v", cronJob.ID, err),
				})
				continue
			}
		}
		entry := onboardingJobResult{Service: svc, Email: jobName, JobID: cronJob.ID}
		if syncType == "one_time" {
			if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
				entry.TaskID = task.ID
			}
		}
		jobs = append(jobs, entry)
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
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	teams []TeamsOnboardingInput,
	refreshToken string,
	database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	if len(teams) == 0 {
		return nil, []onboardingFailedResult{{Service: svc, Error: "teams is required when teams service is selected"}}
	}
	accessToken, err := outlook.AuthTokenUsingRefreshToken(strings.TrimSpace(refreshToken))
	if err != nil {
		return nil, []onboardingFailedResult{{Service: svc, Error: fmt.Sprintf("token refresh failed: %v", err)}}
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
			"team_id":     resolved.TeamID,
			"team_name":   resolved.TeamName,
			"team_web_url": resolved.TeamWebURL,
			"group_id":    resolved.GroupID,
		}
		if len(resolved.ChannelIDs) > 0 {
			extra["channel_ids"] = resolved.ChannelIDs
		}
		cronJob, createErr := createSyncJobWithCredential(userID, jobName, "outlook_teams", syncType, cred.ID, extra, c)
		if createErr != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: extractCreateJobError(createErr)})
			continue
		}
		if err := applyOnboardingJobSchedule(database, userID, cronJob.ID, jobName, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: err.Error()})
			continue
		}
		if syncType != "one_time" && hasStorxTokenAtJobCreate(req, cred) {
			if err := database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(true)); err != nil {
				failed = append(failed, onboardingFailedResult{
					Service: svc, Email: jobName,
					Error: fmt.Sprintf("job %d created but activation failed: %v", cronJob.ID, err),
				})
				continue
			}
		}
		entry := onboardingJobResult{Service: svc, Email: jobName, JobID: cronJob.ID}
		if syncType == "one_time" {
			if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
				entry.TaskID = task.ID
			}
		}
		jobs = append(jobs, entry)
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
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	groups []GroupsOnboardingInput,
	refreshToken string,
	database *db.PostgresDb,
) ([]onboardingJobResult, []onboardingFailedResult) {
	if len(groups) == 0 {
		return nil, []onboardingFailedResult{{Service: svc, Error: "groups is required when groups service is selected"}}
	}
	accessToken, err := outlook.AuthTokenUsingRefreshToken(strings.TrimSpace(refreshToken))
	if err != nil {
		return nil, []onboardingFailedResult{{Service: svc, Error: fmt.Sprintf("token refresh failed: %v", err)}}
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
		cronJob, createErr := createSyncJobWithCredential(userID, jobName, "outlook_groups", syncType, cred.ID, extra, c)
		if createErr != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: extractCreateJobError(createErr)})
			continue
		}
		if err := applyOnboardingJobSchedule(database, userID, cronJob.ID, jobName, schedule, cred, req, isFirstConnection, policyBatch); err != nil {
			failed = append(failed, onboardingFailedResult{Service: svc, Email: jobName, Error: err.Error()})
			continue
		}
		if syncType != "one_time" && hasStorxTokenAtJobCreate(req, cred) {
			if err := database.CronJobRepo.UpdateCronJobByID(cronJob.ID, activeStateUpdateFields(true)); err != nil {
				failed = append(failed, onboardingFailedResult{
					Service: svc, Email: jobName,
					Error: fmt.Sprintf("job %d created but activation failed: %v", cronJob.ID, err),
				})
				continue
			}
		}
		entry := onboardingJobResult{Service: svc, Email: jobName, JobID: cronJob.ID}
		if syncType == "one_time" {
			if task, taskErr := database.TaskRepo.CreateTaskForCronJob(cronJob.ID); taskErr == nil {
				entry.TaskID = task.ID
			}
		}
		jobs = append(jobs, entry)
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

func expandMicrosoftTenantMailboxEmails(ctx context.Context, refreshToken, tenantID string) ([]string, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token is required for all_tenant backup")
	}
	accessToken, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("resolve access token for directory listing: %w", err)
	}
	client, err := outlook.NewOutlookClientUsingToken(accessToken)
	if err != nil {
		return nil, err
	}
	users, err := client.ListDomainUsers(999)
	if err != nil {
		return nil, fmt.Errorf("list tenant mailboxes: %w", err)
	}
	out := make([]string, 0, len(users))
	for _, u := range users {
		email := strings.TrimSpace(u.Mail)
		if email == "" {
			email = strings.TrimSpace(u.UserPrincipalName)
		}
		if email != "" {
			out = append(out, email)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tenant mailboxes found")
	}
	_ = tenantID
	_ = ctx
	return out, nil
}

func expandMicrosoftTenantTeams(ctx context.Context, refreshToken string) ([]TeamsOnboardingInput, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token is required for all_tenant teams backup")
	}
	accessToken, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("resolve access token for teams listing: %w", err)
	}
	teams, err := outlook.ListTeams(ctx, accessToken, 999)
	if err != nil {
		return nil, fmt.Errorf("list tenant teams: %w", err)
	}
	out := make([]TeamsOnboardingInput, 0, len(teams))
	for _, t := range teams {
		if strings.TrimSpace(t.ID) == "" {
			continue
		}
		out = append(out, TeamsOnboardingInput{
			TeamID:   t.ID,
			TeamName: t.DisplayName,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tenant teams found")
	}
	_ = ctx
	return out, nil
}

func expandMicrosoftTenantGroups(ctx context.Context, refreshToken string) ([]GroupsOnboardingInput, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token is required for all_tenant groups backup")
	}
	accessToken, err := outlook.AuthTokenUsingRefreshToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("resolve access token for groups listing: %w", err)
	}
	groups, err := outlook.ListGroups(ctx, accessToken, 999)
	if err != nil {
		return nil, fmt.Errorf("list tenant groups: %w", err)
	}
	out := make([]GroupsOnboardingInput, 0, len(groups))
	for _, g := range groups {
		if strings.TrimSpace(g.ID) == "" {
			continue
		}
		out = append(out, GroupsOnboardingInput{
			GroupID:   g.ID,
			GroupName: g.DisplayName,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tenant groups found")
	}
	_ = ctx
	return out, nil
}
