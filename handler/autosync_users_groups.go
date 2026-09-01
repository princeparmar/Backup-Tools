package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// ---------------------------------------------------------------------------
// Constants & errors
// ---------------------------------------------------------------------------

const (
	usersGroupsDefaultLimit             = 10
	usersGroupsCredentialHealthy        = "healthy"
	usersGroupsCredentialReAuthRequired = "re_auth_required"
	usersGroupsAccountCorporate         = "corporate"
	usersGroupsAccountIndividual        = "individual"
)

var (
	errUsersGroupsMailboxEmailRequired = fmt.Errorf("email is required")
	errUsersGroupsMailboxNotFound      = fmt.Errorf("mailbox not found")

	workspaceServiceMethods = func() map[string]struct{} {
		set := make(map[string]struct{}, len(autosyncServiceMethodsOrder))
		for _, method := range autosyncServiceMethodsOrder {
			set[method] = struct{}{}
		}
		return set
	}()

	validUsersGroupsAccountTypes = map[string]struct{}{
		usersGroupsAccountCorporate:  {},
		usersGroupsAccountIndividual: {},
	}

	validUsersGroupsCredentialStatuses = map[string]struct{}{
		usersGroupsCredentialHealthy:        {},
		usersGroupsCredentialReAuthRequired: {},
	}
)

// ---------------------------------------------------------------------------
// Response types — list (GET /users-groups)
// ---------------------------------------------------------------------------

// UsersGroupsEntityServiceView is one workspace service row (connected or not) with all mailbox fields.
type UsersGroupsEntityServiceView struct {
	Method        string     `json:"method"`
	Connected     bool       `json:"connected"`
	JobID         uint       `json:"job_id,omitempty"`
	Active        *bool      `json:"active,omitempty"`
	LastBackupAt  *time.Time `json:"last_backup_at,omitempty"`
	NextBackupAt  *time.Time `json:"next_backup_at,omitempty"`
	PolicyID      uint       `json:"policy_id,omitempty"`
	PolicyName    string     `json:"policy_name,omitempty"`
	Interval      string     `json:"interval,omitempty"`
	On            string     `json:"on,omitempty"`
	RetentionType string     `json:"retention_type,omitempty"`
}

// UsersGroupsBulkActiveRequest is PUT /users-groups/jobs/active.
type UsersGroupsBulkActiveRequest struct {
	JobIDs []uint `json:"job_ids"`
	Active *bool  `json:"active"`
}

// UsersGroupsPaginationView is pagination metadata for GET /users-groups.
type UsersGroupsPaginationView struct {
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
}

// UsersGroupsEntityView is one mailbox row on GET /users-groups (list + side panel).
type UsersGroupsEntityView struct {
	Name             string                           `json:"name"`
	Email            string                           `json:"email"`
	AccountType      string                           `json:"account_type"`
	OrgUnitPath      string                           `json:"org_unit_path,omitempty"`
	CredentialStatus string                           `json:"credential_status"`
	Credential       UsersGroupsMailboxCredentialView `json:"credential"`
	Services         []UsersGroupsEntityServiceView   `json:"services"`
}

// ---------------------------------------------------------------------------
// Response types — mailbox tabs (GET /users-groups/mailbox/*)
// ---------------------------------------------------------------------------

// UsersGroupsMailboxHeader is shared across mailbox tab responses.
type UsersGroupsMailboxHeader struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	AccountType string `json:"account_type"`
}

// UsersGroupsMailboxOverviewService is one job row on the Overview tab.
type UsersGroupsMailboxOverviewService struct {
	JobID        uint       `json:"job_id"`
	Method       string     `json:"method"`
	Active       bool       `json:"active"`
	LastBackupAt *time.Time `json:"last_backup_at,omitempty"`
	NextBackupAt *time.Time `json:"next_backup_at,omitempty"`
}

// UsersGroupsMailboxOverviewResponse is GET /users-groups/mailbox/overview.
type UsersGroupsMailboxOverviewResponse struct {
	UsersGroupsMailboxHeader
	Services []UsersGroupsMailboxOverviewService `json:"services"`
}

// UsersGroupsMailboxServicesItem is one row on the Services tab.
type UsersGroupsMailboxServicesItem struct {
	Method    string `json:"method"`
	Connected bool   `json:"connected"`
	JobID     uint   `json:"job_id,omitempty"`
}

// UsersGroupsMailboxServicesResponse is GET /users-groups/mailbox/services.
type UsersGroupsMailboxServicesResponse struct {
	UsersGroupsMailboxHeader
	Services []UsersGroupsMailboxServicesItem `json:"services"`
}

// UsersGroupsMailboxScheduleItem is one schedule row on the Schedule tab.
type UsersGroupsMailboxScheduleItem struct {
	JobID         uint   `json:"job_id"`
	Method        string `json:"method"`
	PolicyID      uint   `json:"policy_id"`
	Interval      string `json:"interval"`
	On            string `json:"on,omitempty"`
	RetentionType string `json:"retention_type"`
}

// UsersGroupsMailboxScheduleResponse is GET /users-groups/mailbox/schedule.
type UsersGroupsMailboxScheduleResponse struct {
	UsersGroupsMailboxHeader
	Schedules []UsersGroupsMailboxScheduleItem `json:"schedules"`
}

// UsersGroupsMailboxCredentialView is the credential block on the Credentials tab.
type UsersGroupsMailboxCredentialView struct {
	CredentialID             uint   `json:"credential_id"`
	Email                    string `json:"email"`
	NeedsReconnectGoogleAuth bool   `json:"needs_reconnect_google_auth"`
}

// UsersGroupsMailboxCredentialsResponse is GET /users-groups/mailbox/credentials.
type UsersGroupsMailboxCredentialsResponse struct {
	UsersGroupsMailboxHeader
	Credential UsersGroupsMailboxCredentialView `json:"credential"`
}

// AutosyncDashboardAlertsResponse is GET /autosync/dashboard-alerts (Satellite alert cards).
type AutosyncDashboardAlertsResponse struct {
	ReAuthRequired          AutosyncDashboardAlertSection `json:"re_auth_required"`
	PausedBackups           AutosyncDashboardAlertSection `json:"paused_backups"`
	NewConnectedAccounts24h AutosyncDashboardAlertSection `json:"new_connected_accounts_24h"`
}

// AutosyncDashboardAlertSection is one alert card: count + mailbox rows (services grouped per email).
type AutosyncDashboardAlertSection struct {
	Count int                            `json:"count"`
	Items []AutosyncDashboardMailboxView `json:"items"`
}

// AutosyncDashboardMailboxView is one mailbox/account row with all connected services nested.
type AutosyncDashboardMailboxView struct {
	Email            string                          `json:"email"`
	AccountType      string                          `json:"account_type"`
	CredentialStatus string                          `json:"credential_status"`
	ConnectedAt      *time.Time                      `json:"connected_at,omitempty"`
	Credential       AutosyncDashboardCredentialView `json:"credential"`
	Services         []AutosyncDashboardServiceView  `json:"services"`
}

// AutosyncDashboardCredentialView is Google OAuth reconnect state for the mailbox.
type AutosyncDashboardCredentialView struct {
	CredentialID             uint `json:"credential_id,omitempty"`
	NeedsReconnectGoogleAuth bool `json:"needs_reconnect_google_auth"`
}

// AutosyncDashboardServiceView is one backup job under a mailbox.
type AutosyncDashboardServiceView struct {
	JobID     uint   `json:"job_id,omitempty"`
	Method    string `json:"method"`
	Connected bool   `json:"connected"`
	Active    *bool  `json:"active,omitempty"`
}

// ---------------------------------------------------------------------------
// Shared domain helpers
// ---------------------------------------------------------------------------

func nameFromMailboxEmail(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.LastIndex(email, "@"); at > 0 {
		return email[:at]
	}
	return email
}

func usersGroupsIsCorporateMailbox(mailboxEmail string, cred *repo.GoogleBackupCredentialDB) bool {
	mailboxEmail = strings.TrimSpace(mailboxEmail)
	if cred != nil {
		holder := strings.TrimSpace(cred.Email)
		if holder != "" && mailboxEmail != "" && !strings.EqualFold(holder, mailboxEmail) {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(cred.AccountType)) {
		case "admin_workspace", "employee_workspace":
			return true
		}
	}
	return false
}

func usersGroupsAccountType(mailboxEmail string, cred *repo.GoogleBackupCredentialDB) string {
	if usersGroupsIsCorporateMailbox(mailboxEmail, cred) {
		return usersGroupsAccountCorporate
	}
	return usersGroupsAccountIndividual
}

func usersGroupsCredentialStatus(needsGoogle, needsStorx bool) string {
	if needsGoogle || needsStorx {
		return usersGroupsCredentialReAuthRequired
	}
	return usersGroupsCredentialHealthy
}

func usersGroupsMailboxHeader(email string, cred *repo.GoogleBackupCredentialDB) UsersGroupsMailboxHeader {
	return UsersGroupsMailboxHeader{
		Name:        nameFromMailboxEmail(email),
		Email:       email,
		AccountType: usersGroupsAccountType(email, cred),
	}
}

func credentialForEmailJobs(jobs []repo.CronJobListingDB, credByID map[uint]*repo.GoogleBackupCredentialDB) *repo.GoogleBackupCredentialDB {
	for i := range jobs {
		if id := repo.JobCredentialID(&jobs[i]); id > 0 {
			if cred, ok := credByID[id]; ok {
				return cred
			}
		}
	}
	return nil
}

func uniqueCredentialIDsFromJobs(jobs []repo.CronJobListingDB) []uint {
	seen := make(map[uint]struct{})
	ids := make([]uint, 0, len(jobs))
	for i := range jobs {
		id := repo.JobCredentialID(&jobs[i])
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func loadCredentialsForJobs(database *db.PostgresDb, jobs []repo.CronJobListingDB) map[uint]*repo.GoogleBackupCredentialDB {
	empty := map[uint]*repo.GoogleBackupCredentialDB{}
	if database == nil || database.CredentialRepo == nil {
		return empty
	}
	ids := uniqueCredentialIDsFromJobs(jobs)
	if len(ids) == 0 {
		return empty
	}
	out, err := database.CredentialRepo.GetByIDs(ids)
	if err != nil {
		return empty
	}
	return out
}

func uniquePolicyIDsFromJobs(jobs []repo.CronJobListingDB) []uint {
	seen := make(map[uint]struct{})
	ids := make([]uint, 0, len(jobs))
	for i := range jobs {
		id := jobs[i].PolicyID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func loadPoliciesForJobs(database *db.PostgresDb, jobs []repo.CronJobListingDB) map[uint]*repo.AutosyncBackupPolicyDB {
	empty := map[uint]*repo.AutosyncBackupPolicyDB{}
	if database == nil || database.PolicyRepo == nil {
		return empty
	}
	ids := uniquePolicyIDsFromJobs(jobs)
	if len(ids) == 0 {
		return empty
	}
	out, err := database.PolicyRepo.GetByIDs(ids)
	if err != nil {
		return empty
	}
	return out
}

func enrichJobsFromPolicies(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) {
	for i := range jobs {
		if jobs[i].PolicyID == 0 {
			continue
		}
		policy, ok := policies[jobs[i].PolicyID]
		if !ok || policy == nil {
			continue
		}
		jobs[i].PolicyID = policy.ID
		jobs[i].Interval = policy.Interval
		jobs[i].On = policy.On
	}
}

func filterUsersGroupsWorkspaceJobs(jobs []repo.CronJobListingDB) []repo.CronJobListingDB {
	out := make([]repo.CronJobListingDB, 0, len(jobs))
	for i := range jobs {
		if _, ok := workspaceServiceMethods[strings.TrimSpace(jobs[i].Method)]; !ok {
			continue
		}
		out = append(out, jobs[i])
	}
	return out
}

func indexUsersGroupsJobsByMethod(jobs []repo.CronJobListingDB) map[string]repo.CronJobListingDB {
	byMethod := make(map[string]repo.CronJobListingDB, len(jobs))
	for i := range jobs {
		byMethod[strings.TrimSpace(jobs[i].Method)] = jobs[i]
	}
	return byMethod
}

func enrichUsersGroupsJobs(database *db.PostgresDb, jobs []repo.CronJobListingDB) map[uint]*repo.AutosyncBackupPolicyDB {
	for i := range jobs {
		database.CronJobRepo.EnrichCronJobFromCredential(&jobs[i])
	}
	policies := loadPoliciesForJobs(database, jobs)
	enrichJobsFromPolicies(jobs, policies)
	return policies
}

// ---------------------------------------------------------------------------
// List helpers (GET /users-groups, GET /users-groups/domains)
// ---------------------------------------------------------------------------

func buildUsersGroupsEntityServices(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) []UsersGroupsEntityServiceView {
	byMethod := indexUsersGroupsJobsByMethod(jobs)
	out := make([]UsersGroupsEntityServiceView, 0, len(autosyncServiceMethodsOrder))
	for _, method := range autosyncServiceMethodsOrder {
		job, ok := byMethod[method]
		if !ok {
			out = append(out, UsersGroupsEntityServiceView{Method: method, Connected: false})
			continue
		}
		var lastBackup *time.Time
		if job.LastRun != nil {
			t := *job.LastRun
			lastBackup = &t
		}
		retentionType := ""
		policyName := ""
		if job.PolicyID > 0 {
			if policy, ok := policies[job.PolicyID]; ok && policy != nil {
				retentionType = strings.TrimSpace(policy.RetentionType)
				policyName = strings.TrimSpace(policy.Name)
			}
		}
		active := job.Active
		out = append(out, UsersGroupsEntityServiceView{
			Method:        method,
			Connected:     true,
			JobID:         job.ID,
			Active:        &active,
			LastBackupAt:  lastBackup,
			NextBackupAt:  calculateNextBackup(job),
			PolicyID:      job.PolicyID,
			PolicyName:    policyName,
			Interval:      strings.TrimSpace(job.Interval),
			On:            strings.TrimSpace(job.On),
			RetentionType: retentionType,
		})
	}
	return out
}

func countPausedJobs(jobs []repo.CronJobListingDB) int {
	n := 0
	for i := range jobs {
		if !jobs[i].Active {
			n++
		}
	}
	return n
}

func filterPausedDashboardServices(services []AutosyncDashboardServiceView) []AutosyncDashboardServiceView {
	out := make([]AutosyncDashboardServiceView, 0, len(services))
	for i := range services {
		if !services[i].Connected {
			continue
		}
		if services[i].Active != nil && !*services[i].Active {
			out = append(out, services[i])
		}
	}
	return out
}

func mailboxFirstJobCreatedAt(jobs []repo.CronJobListingDB) time.Time {
	var first time.Time
	for i := range jobs {
		if jobs[i].CreatedAt.IsZero() {
			continue
		}
		if first.IsZero() || jobs[i].CreatedAt.Before(first) {
			first = jobs[i].CreatedAt
		}
	}
	return first
}

func indexUsersGroupsJobsByEmail(jobs []repo.CronJobListingDB) map[string][]repo.CronJobListingDB {
	byEmail := make(map[string][]repo.CronJobListingDB)
	for _, job := range filterUsersGroupsWorkspaceJobs(jobs) {
		email := jobMailboxEmail(&job)
		if email == "" {
			continue
		}
		byEmail[email] = append(byEmail[email], job)
	}
	return byEmail
}

func buildAutosyncDashboardServiceViews(services []UsersGroupsEntityServiceView) []AutosyncDashboardServiceView {
	out := make([]AutosyncDashboardServiceView, 0, len(services))
	for i := range services {
		out = append(out, AutosyncDashboardServiceView{
			JobID:     services[i].JobID,
			Method:    services[i].Method,
			Connected: services[i].Connected,
			Active:    services[i].Active,
		})
	}
	return out
}

func buildAutosyncDashboardMailboxView(
	email string,
	emailJobs []repo.CronJobListingDB,
	cronRepo *repo.CronJobRepository,
	credByID map[uint]*repo.GoogleBackupCredentialDB,
	policies map[uint]*repo.AutosyncBackupPolicyDB,
) AutosyncDashboardMailboxView {
	cred := credentialForEmailJobs(emailJobs, credByID)
	needsGoogle, needsStorx := credentialReconnectFlagsFromJobs(cronRepo, cred, emailJobs)

	credView := AutosyncDashboardCredentialView{NeedsReconnectGoogleAuth: needsGoogle}
	if cred != nil {
		credView.CredentialID = cred.ID
	}

	var connectedAt *time.Time
	if first := mailboxFirstJobCreatedAt(emailJobs); !first.IsZero() {
		t := first
		connectedAt = &t
	}

	return AutosyncDashboardMailboxView{
		Email:            email,
		AccountType:      usersGroupsAccountType(email, cred),
		CredentialStatus: usersGroupsCredentialStatus(needsGoogle, needsStorx),
		ConnectedAt:      connectedAt,
		Credential:       credView,
		Services:         buildAutosyncDashboardServiceViews(buildUsersGroupsEntityServices(emailJobs, policies)),
	}
}

func buildDashboardAlerts(
	jobs []repo.CronJobListingDB,
	cronRepo *repo.CronJobRepository,
	credByID map[uint]*repo.GoogleBackupCredentialDB,
	policies map[uint]*repo.AutosyncBackupPolicyDB,
) AutosyncDashboardAlertsResponse {
	byEmail := indexUsersGroupsJobsByEmail(jobs)
	emails := make([]string, 0, len(byEmail))
	for email := range byEmail {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	reAuthItems := make([]AutosyncDashboardMailboxView, 0)
	pausedItems := make([]AutosyncDashboardMailboxView, 0)
	pausedJobCount := 0
	newItems := make([]AutosyncDashboardMailboxView, 0)
	since := time.Now().Add(-24 * time.Hour)

	for _, email := range emails {
		emailJobs := byEmail[email]
		mailbox := buildAutosyncDashboardMailboxView(email, emailJobs, cronRepo, credByID, policies)

		if mailbox.CredentialStatus == usersGroupsCredentialReAuthRequired {
			reAuthItems = append(reAuthItems, mailbox)
		}
		if n := countPausedJobs(emailJobs); n > 0 {
			pausedJobCount += n
			mailbox.Services = filterPausedDashboardServices(mailbox.Services)
			pausedItems = append(pausedItems, mailbox)
		}
		if first := mailboxFirstJobCreatedAt(emailJobs); !first.IsZero() && !first.Before(since) {
			newItems = append(newItems, mailbox)
		}
	}

	return AutosyncDashboardAlertsResponse{
		ReAuthRequired: AutosyncDashboardAlertSection{
			Count: len(reAuthItems),
			Items: reAuthItems,
		},
		PausedBackups: AutosyncDashboardAlertSection{
			Count: pausedJobCount,
			Items: pausedItems,
		},
		NewConnectedAccounts24h: AutosyncDashboardAlertSection{
			Count: len(newItems),
			Items: newItems,
		},
	}
}

func buildUsersGroupsEntities(
	jobs []repo.CronJobListingDB,
	cronRepo *repo.CronJobRepository,
	credByID map[uint]*repo.GoogleBackupCredentialDB,
	policies map[uint]*repo.AutosyncBackupPolicyDB,
) []UsersGroupsEntityView {
	byEmail := make(map[string][]repo.CronJobListingDB)
	for _, job := range filterUsersGroupsWorkspaceJobs(jobs) {
		email := jobMailboxEmail(&job)
		if email == "" {
			continue
		}
		byEmail[email] = append(byEmail[email], job)
	}

	emails := make([]string, 0, len(byEmail))
	for email := range byEmail {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	out := make([]UsersGroupsEntityView, 0, len(emails))
	for _, email := range emails {
		emailJobs := byEmail[email]
		cred := credentialForEmailJobs(emailJobs, credByID)
		needsGoogle, needsStorx := credentialReconnectFlagsFromJobs(cronRepo, cred, emailJobs)
		out = append(out, UsersGroupsEntityView{
			Name:             nameFromMailboxEmail(email),
			Email:            email,
			AccountType:      usersGroupsAccountType(email, cred),
			OrgUnitPath:      mailboxOrgUnitPath(emailJobs),
			CredentialStatus: usersGroupsCredentialStatus(needsGoogle, needsStorx),
			Credential:       buildMailboxCredentialView(cronRepo, cred, emailJobs),
			Services:         buildUsersGroupsEntityServices(emailJobs, policies),
		})
	}
	return out
}

func parseUsersGroupsServiceMethod(c echo.Context) (string, error) {
	raw := strings.TrimSpace(c.QueryParam("method"))
	if raw == "" {
		return "", nil
	}
	method := strings.ToLower(raw)
	switch method {
	case "all", "all_services":
		return "", nil
	}
	if _, ok := workspaceServiceMethods[method]; !ok {
		return "", fmt.Errorf("method must be one of: gmail, google_drive, google_photos, google_contacts, google_calendar")
	}
	return method, nil
}

func parseUsersGroupsAccountType(c echo.Context) (string, error) {
	raw := strings.TrimSpace(c.QueryParam("account_type"))
	if raw == "" {
		return "", nil
	}
	accountType := strings.ToLower(raw)
	switch accountType {
	case "all", "all_types":
		return "", nil
	}
	if _, ok := validUsersGroupsAccountTypes[accountType]; !ok {
		return "", fmt.Errorf("account_type must be one of: corporate, individual")
	}
	return accountType, nil
}

func parseUsersGroupsActive(c echo.Context) (*bool, error) {
	raw := strings.TrimSpace(c.QueryParam("active"))
	if raw == "" {
		return nil, nil
	}
	switch strings.ToLower(raw) {
	case "all", "all_statuses":
		return nil, nil
	case "true", "1":
		v := true
		return &v, nil
	case "false", "0":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("active must be true or false")
	}
}

func parseUsersGroupsCredentialStatus(c echo.Context) (string, error) {
	raw := strings.TrimSpace(c.QueryParam("credential_status"))
	if raw == "" {
		return "", nil
	}
	status := strings.ToLower(raw)
	switch status {
	case "all", "all_statuses":
		return "", nil
	}
	if _, ok := validUsersGroupsCredentialStatuses[status]; !ok {
		return "", fmt.Errorf("credential_status must be one of: healthy, re_auth_required")
	}
	return status, nil
}

func parseUsersGroupsLimitOffset(c echo.Context) (limit, offset int, err error) {
	limit = usersGroupsDefaultLimit
	offset = 0
	if l := strings.TrimSpace(c.QueryParam("limit")); l != "" {
		limit, err = strconv.Atoi(l)
		if err != nil || limit < 1 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
	}
	if o := strings.TrimSpace(c.QueryParam("offset")); o != "" {
		offset, err = strconv.Atoi(o)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

func filterUsersGroupsEntitiesByMethod(entities []UsersGroupsEntityView, jobs []repo.CronJobListingDB, method string) []UsersGroupsEntityView {
	if method == "" {
		return entities
	}
	emailsWithMethod := make(map[string]struct{})
	for i := range jobs {
		if strings.TrimSpace(jobs[i].Method) != method {
			continue
		}
		if email := jobMailboxEmail(&jobs[i]); email != "" {
			emailsWithMethod[email] = struct{}{}
		}
	}
	out := make([]UsersGroupsEntityView, 0, len(entities))
	for i := range entities {
		if _, ok := emailsWithMethod[entities[i].Email]; ok {
			out = append(out, entities[i])
		}
	}
	return out
}

func filterUsersGroupsEntitiesByActive(entities []UsersGroupsEntityView, jobs []repo.CronJobListingDB, active *bool) []UsersGroupsEntityView {
	if active == nil {
		return entities
	}
	emailsMatching := make(map[string]struct{})
	for _, job := range filterUsersGroupsWorkspaceJobs(jobs) {
		if job.Active != *active {
			continue
		}
		if email := jobMailboxEmail(&job); email != "" {
			emailsMatching[email] = struct{}{}
		}
	}
	out := make([]UsersGroupsEntityView, 0, len(entities))
	for i := range entities {
		if _, ok := emailsMatching[entities[i].Email]; ok {
			out = append(out, entities[i])
		}
	}
	return out
}

func mailboxOrgUnitPath(jobs []repo.CronJobListingDB) string {
	for i := range jobs {
		if path := jobOrgUnitPath(&jobs[i]); path != "" {
			return path
		}
	}
	return ""
}

func jobOrgUnitPath(job *repo.CronJobListingDB) string {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return ""
	}
	raw, ok := (*job.InputData.Json())["org_unit_path"].(string)
	if !ok {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return google.NormalizeOrgUnitPath(raw)
}

func parseUsersGroupsOrgUnitPath(c echo.Context) string {
	raw := strings.TrimSpace(c.QueryParam("org_unit_path"))
	if raw == "" || strings.EqualFold(raw, "all") || strings.EqualFold(raw, "all_org_units") {
		return ""
	}
	return google.NormalizeOrgUnitPath(raw)
}

func filterUsersGroupsEntitiesByOrgUnitPath(entities []UsersGroupsEntityView, orgUnitPath string) []UsersGroupsEntityView {
	if orgUnitPath == "" {
		return entities
	}
	out := make([]UsersGroupsEntityView, 0, len(entities))
	for i := range entities {
		if google.NormalizeOrgUnitPath(entities[i].OrgUnitPath) == orgUnitPath {
			out = append(out, entities[i])
		}
	}
	return out
}

func uniqueUsersGroupsOrgUnitPaths(entities []UsersGroupsEntityView) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for i := range entities {
		path := strings.TrimSpace(entities[i].OrgUnitPath)
		if path == "" {
			continue
		}
		path = google.NormalizeOrgUnitPath(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func filterUsersGroupsEntitiesByAccountType(entities []UsersGroupsEntityView, accountType string) []UsersGroupsEntityView {
	if accountType == "" {
		return entities
	}
	out := make([]UsersGroupsEntityView, 0, len(entities))
	for i := range entities {
		if entities[i].AccountType == accountType {
			out = append(out, entities[i])
		}
	}
	return out
}

func filterUsersGroupsEntitiesByCredentialStatus(entities []UsersGroupsEntityView, credentialStatus string) []UsersGroupsEntityView {
	if credentialStatus == "" {
		return entities
	}
	out := make([]UsersGroupsEntityView, 0, len(entities))
	for i := range entities {
		if entities[i].CredentialStatus == credentialStatus {
			out = append(out, entities[i])
		}
	}
	return out
}

func paginateUsersGroupsEntities(all []UsersGroupsEntityView, limit, offset int) ([]UsersGroupsEntityView, UsersGroupsPaginationView) {
	total := len(all)
	totalPages := 0
	if limit > 0 && total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	page := 1
	if limit > 0 {
		page = offset/limit + 1
	}
	meta := UsersGroupsPaginationView{
		Limit:      limit,
		Offset:     offset,
		Page:       page,
		TotalPages: totalPages,
		TotalCount: total,
	}
	if total == 0 || offset >= total {
		return []UsersGroupsEntityView{}, meta
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], meta
}

// ---------------------------------------------------------------------------
// Mailbox helpers (GET /users-groups/mailbox/*)
// ---------------------------------------------------------------------------

func parseUsersGroupsMailboxEmail(c echo.Context) (string, error) {
	email := strings.TrimSpace(c.QueryParam("email"))
	if email == "" {
		return "", errUsersGroupsMailboxEmailRequired
	}
	return email, nil
}

type usersGroupsMailboxData struct {
	database *db.PostgresDb
	jobs     []repo.CronJobListingDB
	cred     *repo.GoogleBackupCredentialDB
	policies map[uint]*repo.AutosyncBackupPolicyDB
}

func loadUsersGroupsMailboxJobs(database *db.PostgresDb, userID, email string) ([]repo.CronJobListingDB, map[uint]*repo.AutosyncBackupPolicyDB, error) {
	jobs, err := database.CronJobRepo.ListJobsForUsersGroups(userID, &repo.UsersGroupsJobFilter{
		MailboxEmail: email,
	})
	if err != nil {
		return nil, nil, err
	}
	policies := enrichUsersGroupsJobs(database, jobs)
	return jobs, policies, nil
}

func loadUsersGroupsMailboxContext(c echo.Context) (context.Context, string, string, usersGroupsMailboxData, error) {
	empty := usersGroupsMailboxData{}
	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return ctx, "", "", empty, err
	}
	email, err := parseUsersGroupsMailboxEmail(c)
	if err != nil {
		return ctx, userID, email, empty, err
	}
	jobs, policies, err := loadUsersGroupsMailboxJobs(database, userID, email)
	if err != nil {
		return ctx, userID, email, empty, err
	}
	workspaceJobs := filterUsersGroupsWorkspaceJobs(jobs)
	if len(workspaceJobs) == 0 {
		return ctx, userID, email, empty, errUsersGroupsMailboxNotFound
	}
	return ctx, userID, email, usersGroupsMailboxData{
		database: database,
		jobs:     workspaceJobs,
		cred:     credentialForEmailJobs(jobs, loadCredentialsForJobs(database, jobs)),
		policies: policies,
	}, nil
}

func buildMailboxOverviewServices(services []UsersGroupsEntityServiceView) []UsersGroupsMailboxOverviewService {
	out := make([]UsersGroupsMailboxOverviewService, 0, len(services))
	for i := range services {
		if !services[i].Connected {
			continue
		}
		active := false
		if services[i].Active != nil {
			active = *services[i].Active
		}
		out = append(out, UsersGroupsMailboxOverviewService{
			JobID:        services[i].JobID,
			Method:       services[i].Method,
			Active:       active,
			LastBackupAt: services[i].LastBackupAt,
			NextBackupAt: services[i].NextBackupAt,
		})
	}
	return out
}

func buildMailboxServicesTab(services []UsersGroupsEntityServiceView) []UsersGroupsMailboxServicesItem {
	out := make([]UsersGroupsMailboxServicesItem, 0, len(services))
	for i := range services {
		item := UsersGroupsMailboxServicesItem{
			Method:    services[i].Method,
			Connected: services[i].Connected,
		}
		if services[i].Connected {
			item.JobID = services[i].JobID
		}
		out = append(out, item)
	}
	return out
}

func buildMailboxScheduleRows(services []UsersGroupsEntityServiceView) []UsersGroupsMailboxScheduleItem {
	out := make([]UsersGroupsMailboxScheduleItem, 0, len(services))
	for i := range services {
		if !services[i].Connected {
			continue
		}
		out = append(out, UsersGroupsMailboxScheduleItem{
			JobID:         services[i].JobID,
			Method:        services[i].Method,
			PolicyID:      services[i].PolicyID,
			Interval:      services[i].Interval,
			On:            services[i].On,
			RetentionType: services[i].RetentionType,
		})
	}
	return out
}

func buildMailboxCredentialView(
	cronRepo *repo.CronJobRepository,
	cred *repo.GoogleBackupCredentialDB,
	jobs []repo.CronJobListingDB,
) UsersGroupsMailboxCredentialView {
	needsGoogle, _ := credentialReconnectFlagsFromJobs(cronRepo, cred, jobs)
	view := UsersGroupsMailboxCredentialView{NeedsReconnectGoogleAuth: needsGoogle}
	if cred != nil {
		view.CredentialID = cred.ID
		view.Email = strings.TrimSpace(cred.Email)
	}
	return view
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func usersGroupsAuth(c echo.Context) (context.Context, string, *db.PostgresDb, error) {
	ctx := c.Request().Context()
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return ctx, "", nil, err
	}
	database, ok := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	if !ok || database == nil {
		return ctx, userID, nil, fmt.Errorf("database connection unavailable")
	}
	return ctx, userID, database, nil
}

func usersGroupsUnauthorized(c echo.Context, err error) error {
	return c.JSON(http.StatusUnauthorized, map[string]interface{}{
		"message": "not able to authenticate user",
		"error":   err.Error(),
	})
}

func usersGroupsBadRequest(c echo.Context, message string, err error) error {
	return c.JSON(http.StatusBadRequest, map[string]interface{}{
		"message": message,
		"error":   err.Error(),
	})
}

func usersGroupsInternalError(c echo.Context, ctx context.Context, logMsg string, err error) error {
	logger.Error(ctx, logMsg, logger.ErrorField(err))
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{
		"message": "internal server error",
		"error":   err.Error(),
	})
}

func usersGroupsMailboxNotFound(c echo.Context, email string) error {
	return c.JSON(http.StatusNotFound, map[string]interface{}{
		"message": "mailbox not found",
		"error":   "no backup jobs found for " + email,
	})
}

func handleUsersGroupsMailboxTabError(c echo.Context, ctx context.Context, userID, email, logMsg string, err error) error {
	switch {
	case errors.Is(err, errUsersGroupsMailboxEmailRequired):
		return usersGroupsBadRequest(c, "invalid request", err)
	case errors.Is(err, errUsersGroupsMailboxNotFound):
		return usersGroupsMailboxNotFound(c, email)
	case userID == "":
		return usersGroupsUnauthorized(c, err)
	default:
		return usersGroupsInternalError(c, ctx, logMsg, err)
	}
}

func handleUsersGroupsMailboxTab(c echo.Context, logMsg string, build func(email string, data usersGroupsMailboxData) interface{}) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, email, data, err := loadUsersGroupsMailboxContext(c)
	if err != nil {
		return handleUsersGroupsMailboxTabError(c, ctx, userID, email, logMsg, err)
	}
	return c.JSON(http.StatusOK, build(email, data))
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// HandleAutosyncDashboardAlerts returns counts for Satellite dashboard alert cards.
func HandleAutosyncDashboardAlerts(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	jobs, err := database.CronJobRepo.ListJobsForUsersGroups(userID, nil)
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list jobs for dashboard alerts", err)
	}

	policies := enrichUsersGroupsJobs(database, jobs)
	credByID := loadCredentialsForJobs(database, jobs)

	return c.JSON(http.StatusOK, buildDashboardAlerts(jobs, database.CronJobRepo, credByID, policies))
}

// HandleAutosyncUsersGroupsDomains lists unique domains for the domains dropdown.
func HandleAutosyncUsersGroupsDomains(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	domains, err := database.CredentialRepo.ListUniqueDomainsForUser(userID)
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list unique domains", err)
	}
	if domains == nil {
		domains = []string{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"domains": domains})
}

func uniqueUints(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func usersGroupsBulkActiveFailure(jobID uint, email, method string, err error) map[string]interface{} {
	out := map[string]interface{}{
		"job_id": jobID,
		"error":  err.Error(),
	}
	if email != "" {
		out["email"] = email
	}
	if method != "" {
		out["method"] = method
	}
	return out
}

func usersGroupsBulkActiveSuccess(job *repo.CronJobListingDB) map[string]interface{} {
	return map[string]interface{}{
		"job_id": job.ID,
		"method": strings.TrimSpace(job.Method),
		"email":  jobMailboxEmail(job),
		"active": job.Active,
	}
}

func usersGroupsBulkActiveMessage(successLen, failedLen int) string {
	switch {
	case failedLen == 0:
		return "jobs updated"
	case successLen == 0:
		return "all jobs failed to update"
	default:
		return "some jobs failed to update"
	}
}

func bulkUpdateUsersGroupsJobsActive(
	cronRepo *repo.CronJobRepository,
	userID string,
	jobIDs []uint,
	active bool,
) (success []map[string]interface{}, failed []map[string]interface{}) {
	patch := activeStateUpdateFields(active)
	success = make([]map[string]interface{}, 0, len(jobIDs))
	failed = make([]map[string]interface{}, 0)

	for _, jobID := range jobIDs {
		job, err := cronRepo.GetJobByIDForUser(userID, jobID)
		if err != nil {
			failed = append(failed, usersGroupsBulkActiveFailure(jobID, "", "", err))
			continue
		}
		email := jobMailboxEmail(job)
		method := strings.TrimSpace(job.Method)

		if err := cronRepo.UpdateCronJobByID(jobID, patch); err != nil {
			failed = append(failed, usersGroupsBulkActiveFailure(jobID, email, method, err))
			continue
		}

		updated, err := cronRepo.GetCronJobByID(jobID)
		if err != nil {
			failed = append(failed, usersGroupsBulkActiveFailure(jobID, email, method, err))
			continue
		}
		success = append(success, usersGroupsBulkActiveSuccess(updated))
	}
	return success, failed
}

// HandleUsersGroupsJobsActive sets active true/false on multiple jobs (bulk pause/resume).
func HandleUsersGroupsJobsActive(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	var req UsersGroupsBulkActiveRequest
	if err := c.Bind(&req); err != nil {
		return usersGroupsBadRequest(c, "invalid request body", err)
	}
	if req.Active == nil {
		return usersGroupsBadRequest(c, "invalid request", fmt.Errorf("active is required"))
	}
	jobIDs := uniqueUints(req.JobIDs)
	if len(jobIDs) == 0 {
		return usersGroupsBadRequest(c, "invalid request", fmt.Errorf("job_ids is required"))
	}

	success, failed := bulkUpdateUsersGroupsJobsActive(database.CronJobRepo, userID, jobIDs, *req.Active)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": usersGroupsBulkActiveMessage(len(success), len(failed)),
		"active":  *req.Active,
		"success": success,
		"failed":  failed,
	})
}

// HandleAutosyncUsersGroupsList returns a paginated flat list of mailbox emails with per-service active status.
func HandleAutosyncUsersGroupsList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	method, err := parseUsersGroupsServiceMethod(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid service filter", err)
	}
	accountType, err := parseUsersGroupsAccountType(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid account_type filter", err)
	}
	credentialStatus, err := parseUsersGroupsCredentialStatus(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid credential_status filter", err)
	}
	activeFilter, err := parseUsersGroupsActive(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid active filter", err)
	}
	limit, offset, err := parseUsersGroupsLimitOffset(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid pagination parameters", err)
	}

	jobs, err := database.CronJobRepo.ListJobsForUsersGroups(userID, &repo.UsersGroupsJobFilter{
		Domain:      strings.TrimSpace(c.QueryParam("domain")),
		EmailSearch: strings.TrimSpace(c.QueryParam("search")),
		OrgUnitPath: parseUsersGroupsOrgUnitPath(c),
	})
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list jobs for users-groups", err)
	}

	policies := enrichUsersGroupsJobs(database, jobs)
	credByID := loadCredentialsForJobs(database, jobs)
	allEntities := buildUsersGroupsEntities(jobs, database.CronJobRepo, credByID, policies)
	allEntities = filterUsersGroupsEntitiesByMethod(allEntities, jobs, method)
	allEntities = filterUsersGroupsEntitiesByActive(allEntities, jobs, activeFilter)
	allEntities = filterUsersGroupsEntitiesByAccountType(allEntities, accountType)
	allEntities = filterUsersGroupsEntitiesByCredentialStatus(allEntities, credentialStatus)
	allEntities = filterUsersGroupsEntitiesByOrgUnitPath(allEntities, parseUsersGroupsOrgUnitPath(c))
	orgUnits := uniqueUsersGroupsOrgUnitPaths(allEntities)
	entities, pagination := paginateUsersGroupsEntities(allEntities, limit, offset)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"entities":   entities,
		"org_units":  orgUnits,
		"pagination": pagination,
	})
}

// HandleAutosyncUsersGroupsMailboxOverview returns Overview tab data for one mailbox.
func HandleAutosyncUsersGroupsMailboxOverview(c echo.Context) error {
	return handleUsersGroupsMailboxTab(c, "Failed to load mailbox overview", func(email string, data usersGroupsMailboxData) interface{} {
		services := buildUsersGroupsEntityServices(data.jobs, data.policies)
		return UsersGroupsMailboxOverviewResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Services:                 buildMailboxOverviewServices(services),
		}
	})
}

// HandleAutosyncUsersGroupsMailboxServices returns Services tab data for one mailbox.
func HandleAutosyncUsersGroupsMailboxServices(c echo.Context) error {
	return handleUsersGroupsMailboxTab(c, "Failed to load mailbox services", func(email string, data usersGroupsMailboxData) interface{} {
		services := buildUsersGroupsEntityServices(data.jobs, data.policies)
		return UsersGroupsMailboxServicesResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Services:                 buildMailboxServicesTab(services),
		}
	})
}

// HandleAutosyncUsersGroupsMailboxSchedule returns Schedule tab data for one mailbox.
func HandleAutosyncUsersGroupsMailboxSchedule(c echo.Context) error {
	return handleUsersGroupsMailboxTab(c, "Failed to load mailbox schedule", func(email string, data usersGroupsMailboxData) interface{} {
		services := buildUsersGroupsEntityServices(data.jobs, data.policies)
		return UsersGroupsMailboxScheduleResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Schedules:                buildMailboxScheduleRows(services),
		}
	})
}

// HandleAutosyncUsersGroupsMailboxCredentials returns Credentials tab data for one mailbox.
func HandleAutosyncUsersGroupsMailboxCredentials(c echo.Context) error {
	return handleUsersGroupsMailboxTab(c, "Failed to load mailbox credentials", func(email string, data usersGroupsMailboxData) interface{} {
		return UsersGroupsMailboxCredentialsResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Credential:               buildMailboxCredentialView(data.database.CronJobRepo, data.cred, data.jobs),
		}
	})
}
