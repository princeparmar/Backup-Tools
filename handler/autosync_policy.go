package handler

import (
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
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
	gormio "gorm.io/gorm"
)

const reconnectScopeCredential = "credential"

// ConnectedAccountView is shared credential data returned on project update responses.
type ConnectedAccountView struct {
	ProjectID            string `json:"project_id"`
	GoogleEmail          string `json:"google_email"`
	OAuthHolderEmail     string `json:"oauth_holder_email"`
	CredentialID         uint   `json:"credential_id"`
	StorjProjectID       string `json:"storj_project_id,omitempty"`
	AccountType          string `json:"account_type"`
	NeedsGoogleReconnect bool   `json:"needs_google_reconnect"`
	NeedsStorxReconnect  bool   `json:"needs_storx_reconnect"`
	ReconnectScope       string `json:"reconnect_scope"`
}

// AutosyncJobItemView is a slim per-job row in PUT /job/project responses.
type AutosyncJobItemView struct {
	ID              uint            `json:"ID"`
	PolicyID        uint            `json:"policy_id,omitempty"`
	Email           string          `json:"email"`
	Method          string          `json:"method"`
	Interval        string          `json:"interval"`
	On              string          `json:"on"`
	Active          bool            `json:"active"`
	SyncType        string          `json:"sync_type"`
	Status          string          `json:"status"`
	LastRun         *time.Time      `json:"last_run"`
	Message         string          `json:"message"`
	MessageStatus   string          `json:"message_status"`
	TaskMemory      repo.TaskMemory `json:"task_memory"`
	Hidden          bool            `json:"hidden"`
	AutoDeactivated bool            `json:"autodeactivated"`
	FailurePeriods  uint            `json:"failure_periods"`
	NextBackup      *time.Time      `json:"next_backup,omitempty"`
}

// PolicyListRowPolicyView is one policy row on GET /auto-sync/policy.
type PolicyListRowPolicyView struct {
	PolicyID       uint     `json:"policy_id"`
	Name           string   `json:"name"`
	Interval       string   `json:"interval"`
	On             string   `json:"on"`
	RetentionType  string   `json:"retention_type"`
	IsExpired      bool     `json:"is_expired"`
	LinkedJobCount int      `json:"linked_job_count"`
	Services       []string `json:"services"`
}

// PolicyManageSummaryView is the policy block on the manage page.
type PolicyManageSummaryView struct {
	PolicyID         uint   `json:"policy_id"`
	Name             string `json:"name"`
	Interval         string `json:"interval"`
	On               string `json:"on"`
	RetentionType    string `json:"retention_type"`
	IsExpired        bool   `json:"is_expired"`
	AssignmentCount  int    `json:"assignment_count"`
	UniqueEmailCount int    `json:"unique_email_count"`
}

// PolicyLinkedJobView is a slim assignment row linked to a policy.
type PolicyLinkedJobView struct {
	JobID  uint   `json:"job_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Method string `json:"method"`
}

type autosyncPolicyUpdateRequest struct {
	Interval      *string `json:"interval"`
	On            *string `json:"on"`
	RetentionType *string `json:"retention_type"`
}

type autosyncPolicyCreateRequest struct {
	Name          string `json:"name"`
	Interval      string `json:"interval"`
	On            string `json:"on"`
	RetentionType string `json:"retention_type"`
	JobIDs        []uint `json:"job_ids"`
}

type autosyncPolicyMoveRequest struct {
	TargetPolicyID uint   `json:"target_policy_id"`
	JobIDs         []uint `json:"job_ids"`
}

type PolicyScheduleView struct {
	Interval      string     `json:"interval"`
	On            string     `json:"on"`
	RetentionType string     `json:"retention_type"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	IsExpired     bool       `json:"is_expired"`
}

type PolicyMergePreviewPolicyView struct {
	PolicyID       uint     `json:"policy_id"`
	Name           string   `json:"name"`
	LinkedJobCount int      `json:"linked_job_count"`
	Services       []string `json:"services,omitempty"`
}

type PolicyMergePreviewGroup struct {
	Schedule  PolicyScheduleView             `json:"schedule"`
	Policies  []PolicyMergePreviewPolicyView `json:"policies"`
	TotalJobs int                            `json:"total_jobs"`
}

type autosyncPolicyMergeRequest struct {
	PolicyIDs []uint `json:"policy_ids"`
	Name      string `json:"name"`
}

type PolicyAvailableEmailView struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	HereCount  int    `json:"here_count"`
	OtherCount int    `json:"other_count"`
}

type PolicyAvailableServiceView struct {
	JobID             uint   `json:"job_id"`
	Method            string `json:"method"`
	ServiceLabel      string `json:"service_label"`
	Assignment        string `json:"assignment"`
	CurrentPolicyID   uint   `json:"current_policy_id,omitempty"`
	CurrentPolicyName string `json:"current_policy_name,omitempty"`
	CanAdd            bool   `json:"can_add"`
}

var policyServiceLabels = map[string]string{
	"gmail":             "Gmail",
	"google_drive":      "Drive",
	"google_calendar":   "Calendar",
	"google_contacts":   "Contacts",
	"google_photos":     "Photos",
	"outlook":           "Outlook",
	"outlook_calendar":  "Outlook Calendar",
	"outlook_contacts":  "Outlook Contacts",
	"outlook_onedrive":  "OneDrive",
	"outlook_sharepoint": "SharePoint",
	"outlook_teams":      "Teams",
	"outlook_groups":     "Groups",
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func jobMailboxEmail(job *repo.CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if job.InputData != nil && job.InputData.Json() != nil {
		if s, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return strings.TrimSpace(job.Name)
}

func credentialReconnectFlags(cred *repo.GoogleBackupCredentialDB) (needsGoogle, needsStorx bool) {
	return credentialReconnectFlagsFromJobs(nil, cred, nil)
}

func credentialReconnectFlagsFromJobs(cronRepo *repo.CronJobRepository, cred *repo.GoogleBackupCredentialDB, jobs []repo.CronJobListingDB) (needsGoogle, needsStorx bool) {
	hasGoogle, hasStorx := false, false
	if cred != nil {
		hasGoogle = strings.TrimSpace(cred.RefreshToken) != ""
		hasStorx = strings.TrimSpace(cred.StorxToken) != ""
	}
	if cronRepo != nil {
		for i := range jobs {
			if !hasGoogle && strings.TrimSpace(cronRepo.ResolvedRefreshToken(&jobs[i])) != "" {
				hasGoogle = true
			}
			if !hasStorx && strings.TrimSpace(cronRepo.ResolvedStorxToken(&jobs[i])) != "" {
				hasStorx = true
			}
		}
	}
	if cred == nil && len(jobs) == 0 {
		return true, true
	}
	return !hasGoogle, !hasStorx
}

func buildConnectedAccountView(cred *repo.GoogleBackupCredentialDB, projectID, googleEmail string) ConnectedAccountView {
	holder := strings.TrimSpace(googleEmail)
	if cred != nil && holder == "" {
		holder = strings.TrimSpace(cred.Email)
	}
	pid := strings.TrimSpace(projectID)
	if cred != nil && pid == "" {
		pid = strings.TrimSpace(cred.StorjProjectID)
	}
	needsGoogle, needsStorx := credentialReconnectFlags(cred)
	v := ConnectedAccountView{
		ProjectID:            pid,
		GoogleEmail:          holder,
		OAuthHolderEmail:     holder,
		NeedsGoogleReconnect: needsGoogle,
		NeedsStorxReconnect:  needsStorx,
		ReconnectScope:       reconnectScopeCredential,
	}
	if cred != nil {
		v.CredentialID = cred.ID
		v.StorjProjectID = strings.TrimSpace(cred.StorjProjectID)
		v.AccountType = strings.TrimSpace(cred.AccountType)
		if v.AccountType == "" {
			v.AccountType = "personal"
		}
	}
	return v
}

func buildPolicyScheduleView(policy *repo.AutosyncBackupPolicyDB) PolicyScheduleView {
	return buildPolicyScheduleViewAt(policy, time.Now().UTC())
}

func buildPolicyScheduleViewAt(policy *repo.AutosyncBackupPolicyDB, now time.Time) PolicyScheduleView {
	if policy == nil {
		return PolicyScheduleView{}
	}
	return PolicyScheduleView{
		Interval:      policy.Interval,
		On:            policy.On,
		RetentionType: policy.RetentionType,
		ExpiresAt:     policy.ExpiresAt,
		IsExpired:     repo.IsPolicyExpired(policy, now),
	}
}

func buildMergePreviewPolicyView(policy *repo.AutosyncBackupPolicyDB, linked []repo.CronJobListingDB) PolicyMergePreviewPolicyView {
	if policy == nil {
		return PolicyMergePreviewPolicyView{}
	}
	return PolicyMergePreviewPolicyView{
		PolicyID:       policy.ID,
		Name:           policy.Name,
		LinkedJobCount: len(linked),
		Services:       sortedUniqueServices(linked),
	}
}

func sortedUniqueServices(jobs []repo.CronJobListingDB) []string {
	seen := make(map[string]struct{})
	for i := range jobs {
		m := strings.TrimSpace(jobs[i].Method)
		if m == "" {
			continue
		}
		seen[m] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func buildPolicyListRowPolicyView(policy *repo.AutosyncBackupPolicyDB, linkedJobs []repo.CronJobListingDB) PolicyListRowPolicyView {
	return buildPolicyListRowPolicyViewAt(policy, linkedJobs, time.Now().UTC())
}

func buildPolicyListRowPolicyViewAt(policy *repo.AutosyncBackupPolicyDB, linkedJobs []repo.CronJobListingDB, now time.Time) PolicyListRowPolicyView {
	return PolicyListRowPolicyView{
		PolicyID:       policy.ID,
		Name:           policy.Name,
		Interval:       policy.Interval,
		On:             policy.On,
		RetentionType:  policy.RetentionType,
		IsExpired:      repo.IsPolicyExpired(policy, now),
		LinkedJobCount: len(linkedJobs),
		Services:       sortedUniqueServices(linkedJobs),
	}
}

func uniqueEmailsFromJobs(jobs []repo.CronJobListingDB) int {
	seen := make(map[string]struct{})
	for i := range jobs {
		email := normalizeEmail(jobMailboxEmail(&jobs[i]))
		if email == "" {
			continue
		}
		seen[email] = struct{}{}
	}
	return len(seen)
}

func buildPolicyManageSummaryView(policy *repo.AutosyncBackupPolicyDB, linkedJobs []repo.CronJobListingDB) PolicyManageSummaryView {
	return buildPolicyManageSummaryViewAt(policy, linkedJobs, time.Now().UTC())
}

func buildPolicyManageSummaryViewAt(policy *repo.AutosyncBackupPolicyDB, linkedJobs []repo.CronJobListingDB, now time.Time) PolicyManageSummaryView {
	return PolicyManageSummaryView{
		PolicyID:         policy.ID,
		Name:             policy.Name,
		Interval:         policy.Interval,
		On:               policy.On,
		RetentionType:    policy.RetentionType,
		IsExpired:        repo.IsPolicyExpired(policy, now),
		AssignmentCount:  len(linkedJobs),
		UniqueEmailCount: uniqueEmailsFromJobs(linkedJobs),
	}
}

func buildPolicyLinkedJobView(job *repo.CronJobListingDB) PolicyLinkedJobView {
	email := jobMailboxEmail(job)
	return PolicyLinkedJobView{
		JobID:  job.ID,
		Name:   displayNameFromMailboxEmail(email),
		Email:  email,
		Method: job.Method,
	}
}

func buildAutosyncJobItemView(job repo.CronJobListingDB) AutosyncJobItemView {
	return AutosyncJobItemView{
		ID:              job.ID,
		PolicyID:        job.PolicyID,
		Email:           jobMailboxEmail(&job),
		Method:          job.Method,
		Interval:        job.Interval,
		On:              job.On,
		Active:          job.Active,
		SyncType:        job.SyncType,
		Status:          job.Status,
		LastRun:         job.LastRun,
		Message:         job.Message,
		MessageStatus:   job.MessageStatus,
		TaskMemory:      job.TaskMemory,
		Hidden:          job.Hidden,
		AutoDeactivated: job.AutoDeactivated,
		FailurePeriods:  job.FailurePeriods,
		NextBackup:      calculateNextBackup(job),
	}
}

func serviceLabelForMethod(method string) string {
	method = strings.TrimSpace(method)
	if label, ok := policyServiceLabels[method]; ok {
		return label
	}
	return method
}

func policyJobMatchesSearch(job *repo.CronJobListingDB, search string) bool {
	search = strings.TrimSpace(strings.ToLower(search))
	if search == "" {
		return true
	}
	email := strings.ToLower(jobMailboxEmail(job))
	display := strings.ToLower(displayNameFromMailboxEmail(email))
	method := strings.ToLower(job.Method)
	label := strings.ToLower(serviceLabelForMethod(job.Method))
	return strings.Contains(email, search) ||
		strings.Contains(display, search) ||
		strings.Contains(method, search) ||
		strings.Contains(label, search)
}

func filterLinkedJobsBySearch(jobs []repo.CronJobListingDB, search string) []repo.CronJobListingDB {
	search = strings.TrimSpace(search)
	if search == "" {
		return jobs
	}
	out := make([]repo.CronJobListingDB, 0, len(jobs))
	for i := range jobs {
		if policyJobMatchesSearch(&jobs[i], search) {
			out = append(out, jobs[i])
		}
	}
	return out
}

func normalizeScheduleIntervalOn(intervalVal, onValue string) (string, string) {
	intervalVal = strings.TrimSpace(strings.ToLower(intervalVal))
	onValue = strings.TrimSpace(onValue)
	switch intervalVal {
	case "nightly", "night", "24h", "1d":
		intervalVal = "daily"
	}
	if intervalVal == "daily" {
		switch strings.ToLower(strings.ReplaceAll(onValue, " ", "")) {
		case "", "12am", "12:00am", "00:00", "midnight":
			onValue = "12am"
		}
	}
	if intervalVal == "monthly" && onValue != "" {
		if day, err := strconv.Atoi(onValue); err == nil {
			onValue = strconv.Itoa(day)
		}
	}
	return intervalVal, onValue
}

func validateScheduleIntervalOn(intervalVal, onValue string) error {
	intervalVal, onValue = normalizeScheduleIntervalOn(intervalVal, onValue)
	if onValue == "" && intervalVal != "3h" && intervalVal != "12h" {
		return fmt.Errorf("on is required for this interval (use empty on for 3h or 12h)")
	}
	if intervalVal == "monthly" {
		day, err := strconv.Atoi(onValue)
		if err != nil {
			return fmt.Errorf("on value must be a valid number for monthly intervals")
		}
		onValue = strconv.Itoa(day)
		if day == 29 || day == 30 || day == 31 {
			return fmt.Errorf("monthly backups cannot be scheduled on the 29th, 30th or 31st day; select a date between 1-28")
		}
	}
	if !validateInterval(intervalVal, onValue) {
		if intervalVal == "12h" && onValue != "" {
			return fmt.Errorf("interval 12h requires empty on; for nightly backup use interval daily with on 12am")
		}
		return fmt.Errorf("on %q is not valid for interval %q", onValue, intervalVal)
	}
	return nil
}

func parseScheduleFromRequest(interval, on *string) (string, string, error) {
	if (interval != nil && on == nil) || (on != nil && interval == nil) {
		return "", "", fmt.Errorf("both interval and on are required together")
	}
	if interval == nil {
		return "", "", nil
	}
	rawOn := ""
	if on != nil {
		rawOn = *on
	}
	sched, err := parseOnboardingSchedule(*interval, rawOn)
	if err != nil {
		return "", "", err
	}
	return sched.Interval, sched.On, nil
}

// policyCtx carries auth + DB + request clock for policy handlers.
type policyCtx struct {
	echo     echo.Context
	userID   string
	database *db.PostgresDb
	now      time.Time
}

func newPolicyCtx(c echo.Context) (policyCtx, error) {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return policyCtx{}, err
	}
	return policyCtx{
		echo:     c,
		userID:   userID,
		database: c.Get(middleware.DbContextKey).(*db.PostgresDb),
		now:      time.Now().UTC(),
	}, nil
}

func (p *policyCtx) loadPolicy(policyID uint) (*repo.AutosyncBackupPolicyDB, error) {
	return loadPolicyForUser(p.database, p.userID, policyID)
}

func policyUnauthorized(c echo.Context, err error) error {
	return c.JSON(http.StatusUnauthorized, map[string]interface{}{
		"message": "Authentication required",
		"error":   err.Error(),
	})
}

func (p *policyCtx) badRequest(detail string) error {
	return p.echo.JSON(http.StatusBadRequest, map[string]interface{}{
		"message": "Invalid Request",
		"error":   detail,
	})
}

func (p *policyCtx) notFound(detail string) error {
	return p.echo.JSON(http.StatusNotFound, map[string]interface{}{
		"message": "Invalid Request",
		"error":   detail,
	})
}

func (p *policyCtx) serverError(message string, err error) error {
	return p.echo.JSON(http.StatusInternalServerError, map[string]interface{}{
		"message": message,
		"error":   err.Error(),
	})
}

func parsePolicyIDParam(c echo.Context) (uint, error) {
	id, err := strconvParseUintParam(c.Param("policy_id"))
	if err != nil {
		return 0, fmt.Errorf("invalid policy_id")
	}
	return id, nil
}

func strconvParseUintParam(raw string) (uint, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return uint(n), nil
}

// jobBatch indexes jobs for O(1) lookups during response building.
type jobBatch struct {
	rows       []repo.CronJobListingDB
	byID       map[uint]repo.CronJobListingDB
	byPolicyID map[uint][]repo.CronJobListingDB
}

func newJobBatch(rows []repo.CronJobListingDB) jobBatch {
	b := jobBatch{
		rows:       rows,
		byID:       make(map[uint]repo.CronJobListingDB, len(rows)),
		byPolicyID: make(map[uint][]repo.CronJobListingDB),
	}
	for i := range rows {
		b.byID[rows[i].ID] = rows[i]
		if rows[i].PolicyID > 0 {
			b.byPolicyID[rows[i].PolicyID] = append(b.byPolicyID[rows[i].PolicyID], rows[i])
		}
	}
	return b
}

func loadJobBatchByPolicyIDs(database *db.PostgresDb, userID string, policyIDs []uint) (jobBatch, error) {
	grouped, err := database.CronJobRepo.ListJobsGroupedByPolicyIDs(userID, policyIDs)
	if err != nil {
		return jobBatch{}, err
	}
	total := 0
	for _, jobs := range grouped {
		total += len(jobs)
	}
	rows := make([]repo.CronJobListingDB, 0, total)
	for _, jobs := range grouped {
		rows = append(rows, jobs...)
	}
	return newJobBatch(rows), nil
}

func loadJobBatchByIDs(database *db.PostgresDb, userID string, jobIDs []uint) (jobBatch, error) {
	rows, err := database.CronJobRepo.GetJobsByIDsForUser(userID, jobIDs)
	if err != nil {
		return jobBatch{}, err
	}
	return newJobBatch(rows), nil
}

func validateJobIDsInBatch(batch jobBatch, jobIDs []uint) error {
	if len(jobIDs) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(jobIDs))
	for _, id := range jobIDs {
		if id == 0 {
			return fmt.Errorf("invalid job id in job_ids")
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("duplicate job id %d in job_ids", id)
		}
		seen[id] = struct{}{}
		if _, ok := batch.byID[id]; !ok {
			return fmt.Errorf("job %d not found for user", id)
		}
	}
	return nil
}

func linkedViewsFromBatch(batch jobBatch, jobIDs []uint) []PolicyLinkedJobView {
	if len(jobIDs) == 0 {
		return nil
	}
	jobs := make([]repo.CronJobListingDB, 0, len(jobIDs))
	for _, id := range jobIDs {
		if job, ok := batch.byID[id]; ok {
			jobs = append(jobs, job)
		}
	}
	return linkedViewsFromJobs(jobs)
}

func linkedViewsFromJobs(jobs []repo.CronJobListingDB) []PolicyLinkedJobView {
	out := make([]PolicyLinkedJobView, len(jobs))
	for i := range jobs {
		out[i] = buildPolicyLinkedJobView(&jobs[i])
	}
	return out
}

func loadPolicyForUser(database *db.PostgresDb, userID string, policyID uint) (*repo.AutosyncBackupPolicyDB, error) {
	policy, err := database.PolicyRepo.GetByID(policyID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(policy.UserID) != strings.TrimSpace(userID) {
		return nil, fmt.Errorf("policy not found for user")
	}
	return policy, nil
}

func listPolicyListRows(policies []repo.AutosyncBackupPolicyDB, batch jobBatch, now time.Time) []PolicyListRowPolicyView {
	out := make([]PolicyListRowPolicyView, 0, len(policies))
	for i := range policies {
		linked := batch.byPolicyID[policies[i].ID]
		out = append(out, buildPolicyListRowPolicyViewAt(&policies[i], linked, now))
	}
	return out
}

func collectMergeGroupPolicyIDs(groups []repo.MergeablePolicyGroupData) []uint {
	seen := make(map[uint]struct{})
	ids := make([]uint, 0)
	for i := range groups {
		for j := range groups[i].Policies {
			id := groups[i].Policies[j].ID
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func buildMergePreviewGroups(groups []repo.MergeablePolicyGroupData, batch jobBatch, now time.Time) []PolicyMergePreviewGroup {
	out := make([]PolicyMergePreviewGroup, 0, len(groups))
	for i := range groups {
		out = append(out, buildMergePreviewGroupAt(groups[i], batch, now))
	}
	return out
}

func buildMergePreviewGroupAt(group repo.MergeablePolicyGroupData, batch jobBatch, now time.Time) PolicyMergePreviewGroup {
	policies := make([]PolicyMergePreviewPolicyView, 0, len(group.Policies))
	for i := range group.Policies {
		p := &group.Policies[i]
		policies = append(policies, buildMergePreviewPolicyView(p, batch.byPolicyID[p.ID]))
	}
	scheduleSource := &group.Policies[0]
	return PolicyMergePreviewGroup{
		Schedule:  buildPolicyScheduleViewAt(scheduleSource, now),
		Policies:  policies,
		TotalJobs: group.TotalJobs,
	}
}

func loadPolicyNameMap(database *db.PostgresDb, policyIDs []uint) (map[uint]string, error) {
	policies, err := database.PolicyRepo.GetByIDs(policyIDs)
	if err != nil {
		return nil, err
	}
	names := make(map[uint]string, len(policies))
	for id, p := range policies {
		if p != nil {
			names[id] = p.Name
		}
	}
	return names, nil
}

func collectDistinctPolicyIDs(jobs []repo.CronJobListingDB) []uint {
	seen := make(map[uint]struct{})
	ids := make([]uint, 0)
	for i := range jobs {
		if jobs[i].PolicyID == 0 {
			continue
		}
		if _, ok := seen[jobs[i].PolicyID]; ok {
			continue
		}
		seen[jobs[i].PolicyID] = struct{}{}
		ids = append(ids, jobs[i].PolicyID)
	}
	return ids
}

type mailboxCounts struct {
	email      string
	hereCount  int
	otherCount int
}

func buildAvailableEmailsView(jobs []repo.CronJobListingDB, targetPolicyID uint, search string) []PolicyAvailableEmailView {
	counts := make(map[string]*mailboxCounts)
	order := make([]string, 0)
	search = normalizeEmail(search)

	for i := range jobs {
		email := jobMailboxEmail(&jobs[i])
		if email == "" {
			continue
		}
		key := normalizeEmail(email)
		entry, ok := counts[key]
		if !ok {
			entry = &mailboxCounts{email: email}
			counts[key] = entry
			order = append(order, key)
		}
		switch {
		case jobs[i].PolicyID == targetPolicyID:
			entry.hereCount++
		case jobs[i].PolicyID > 0:
			entry.otherCount++
		}
	}

	out := make([]PolicyAvailableEmailView, 0, len(order))
	for _, key := range order {
		entry := counts[key]
		display := displayNameFromMailboxEmail(entry.email)
		if search != "" && !strings.Contains(normalizeEmail(entry.email), search) && !strings.Contains(normalizeEmail(display), search) {
			continue
		}
		out = append(out, PolicyAvailableEmailView{
			Name:       display,
			Email:      entry.email,
			HereCount:  entry.hereCount,
			OtherCount: entry.otherCount,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Email < out[j].Email
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func buildAvailableServicesResponse(jobs []repo.CronJobListingDB, targetPolicyID uint, selectedEmail string, policyNames map[uint]string) map[string]interface{} {
	services := make([]PolicyAvailableServiceView, 0)
	displayEmail := strings.TrimSpace(selectedEmail)

	for i := range jobs {
		email := jobMailboxEmail(&jobs[i])
		if !strings.EqualFold(email, selectedEmail) {
			continue
		}
		displayEmail = email
		view := PolicyAvailableServiceView{
			JobID:        jobs[i].ID,
			Method:       jobs[i].Method,
			ServiceLabel: serviceLabelForMethod(jobs[i].Method),
		}
		if jobs[i].PolicyID == targetPolicyID {
			view.Assignment = "here"
			view.CanAdd = false
		} else {
			view.Assignment = "other"
			view.CanAdd = true
			if jobs[i].PolicyID > 0 {
				view.CurrentPolicyID = jobs[i].PolicyID
				view.CurrentPolicyName = policyNames[jobs[i].PolicyID]
			}
		}
		services = append(services, view)
	}

	return map[string]interface{}{
		"message":  "Available services",
		"name":     displayNameFromMailboxEmail(displayEmail),
		"email":    displayEmail,
		"services": services,
	}
}

// HandleAutosyncPolicyList lists all policies for the authenticated user, including those with no linked jobs.
func HandleAutosyncPolicyList(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}

	policies, err := p.database.PolicyRepo.ListByUserID(p.userID)
	if err != nil {
		return p.serverError("Failed to list policies", err)
	}
	if len(policies) == 0 {
		return p.echo.JSON(http.StatusOK, map[string]interface{}{
			"message":  "Backup policies list",
			"policies": []PolicyListRowPolicyView{},
		})
	}

	policyIDs := make([]uint, len(policies))
	for i := range policies {
		policyIDs[i] = policies[i].ID
	}
	batch, err := loadJobBatchByPolicyIDs(p.database, p.userID, policyIDs)
	if err != nil {
		return p.serverError("Failed to list policies", err)
	}

	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Backup policies list",
		"policies": listPolicyListRows(policies, batch, p.now),
	})
}

// HandleAutosyncPolicyByID returns one policy and its linked jobs for the manage page.
func HandleAutosyncPolicyByID(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}
	policyID, err := parsePolicyIDParam(c)
	if err != nil {
		return p.badRequest(err.Error())
	}

	policy, err := p.loadPolicy(policyID)
	if err != nil {
		return p.notFound(err.Error())
	}
	linkedJobs, err := p.database.CronJobRepo.ListJobsByPolicyID(p.userID, policy.ID)
	if err != nil {
		return p.serverError("Failed to load linked jobs", err)
	}

	filtered := filterLinkedJobsBySearch(linkedJobs, p.echo.QueryParam("search"))
	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":     "Backup policy details",
		"policy":      buildPolicyManageSummaryViewAt(policy, linkedJobs, p.now),
		"linked_jobs": linkedViewsFromJobs(filtered),
	})
}

// HandleAutosyncPolicyMergePreview lists mergeable duplicate policy groups.
func HandleAutosyncPolicyMergePreview(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}

	groups, err := p.database.PolicyRepo.ListMergeablePolicyGroups(p.userID)
	if err != nil {
		return p.serverError("Failed to load mergeable policies", err)
	}

	msg := "No duplicate policies to merge"
	summary := map[string]int{
		"mergeable_group_count": 0,
		"policy_count":          0,
		"total_jobs":            0,
	}
	var previewGroups []PolicyMergePreviewGroup
	if len(groups) > 0 {
		msg = "Mergeable duplicate policies"
		batch, berr := loadJobBatchByPolicyIDs(p.database, p.userID, collectMergeGroupPolicyIDs(groups))
		if berr != nil {
			return p.serverError("Failed to build merge preview", berr)
		}
		previewGroups = buildMergePreviewGroups(groups, batch, p.now)
		for i := range groups {
			summary["mergeable_group_count"]++
			summary["policy_count"] += len(groups[i].Policies)
			summary["total_jobs"] += groups[i].TotalJobs
		}
	}

	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message": msg,
		"summary": summary,
		"groups":  previewGroups,
	})
}

// HandleAutosyncPolicyMerge merges one complete duplicate policy group.
func HandleAutosyncPolicyMerge(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}

	var req autosyncPolicyMergeRequest
	if err := p.echo.Bind(&req); err != nil {
		return p.badRequest(err.Error())
	}
	if len(req.PolicyIDs) < 2 {
		return p.echo.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "At least two policy_ids are required",
		})
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return p.badRequest("name is required")
	}

	result, merr := p.database.PolicyRepo.MergeSelectedPolicyGroup(p.userID, req.PolicyIDs, name)
	if merr != nil {
		var incomplete *repo.MergeIncompleteGroupError
		switch {
		case errors.Is(merr, repo.ErrMergePolicyIDsRequired):
			return p.echo.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "At least two policy_ids are required",
			})
		case errors.Is(merr, repo.ErrPolicyNameExists):
			return p.echo.JSON(http.StatusConflict, policyNameConflictResponse(name))
		case strings.Contains(merr.Error(), "policy name is required"):
			return p.badRequest("name is required")
		case errors.Is(merr, repo.ErrMergePolicyNotFound):
			return p.echo.JSON(http.StatusNotFound, map[string]interface{}{
				"message": "Policy not found for user",
				"error":   merr.Error(),
			})
		case errors.Is(merr, repo.ErrMergeMixedGroups):
			return p.badRequest(merr.Error())
		case errors.As(merr, &incomplete):
			return p.echo.JSON(http.StatusBadRequest, map[string]interface{}{
				"message":            "Incomplete merge group; include all policies in this duplicate set",
				"missing_policy_ids": incomplete.MissingPolicyIDs,
			})
		default:
			return p.serverError("Failed to merge duplicate policies", merr)
		}
	}

	newPolicy, perr := p.database.PolicyRepo.GetByID(result.NewPolicyID)
	if perr != nil {
		return p.serverError("Failed to load merged policy", perr)
	}
	linkedJobs, lerr := p.database.CronJobRepo.ListJobsByPolicyID(p.userID, newPolicy.ID)
	if lerr != nil {
		return p.serverError("Failed to load linked jobs", lerr)
	}
	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":            "Policies merged successfully",
		"policy":             buildPolicyListRowPolicyViewAt(newPolicy, linkedJobs, p.now),
		"removed_policy_ids": result.RemovedPolicyIDs,
		"source_policy_ids":  result.SourcePolicyIDs,
		"jobs_moved":         result.JobsMoved,
	})
}

// HandleAutosyncPolicyDelete removes a policy when it has no linked jobs.
func HandleAutosyncPolicyDelete(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}
	policyID, err := parsePolicyIDParam(c)
	if err != nil {
		return p.badRequest(err.Error())
	}

	linkedCount, derr := p.database.PolicyRepo.DeletePolicyForUser(p.userID, policyID)
	if derr != nil {
		switch {
		case errors.Is(derr, repo.ErrPolicyHasLinkedJobs):
			return p.echo.JSON(http.StatusConflict, map[string]interface{}{
				"message":          "Cannot delete policy with linked jobs",
				"error":            "Move all jobs to another policy before deleting",
				"linked_job_count": linkedCount,
			})
		case errors.Is(derr, gormio.ErrRecordNotFound):
			return p.notFound("policy not found for user")
		default:
			return p.serverError("Failed to delete policy", derr)
		}
	}

	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":   "Policy deleted successfully",
		"policy_id": policyID,
	})
}

// HandleAutosyncPolicyUpdate updates interval and retention for all jobs on a policy.
func HandleAutosyncPolicyUpdate(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}
	policyID, err := parsePolicyIDParam(c)
	if err != nil {
		return p.badRequest(err.Error())
	}

	var req autosyncPolicyUpdateRequest
	if err := p.echo.Bind(&req); err != nil {
		return p.echo.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}
	intervalVal, onValue, schedErr := parseScheduleFromRequest(req.Interval, req.On)
	if schedErr != nil {
		return p.badRequest(schedErr.Error())
	}
	if req.Interval == nil {
		return p.badRequest("interval and on are required")
	}
	retentionType := repo.RetentionNever
	if req.RetentionType != nil {
		retentionType = strings.TrimSpace(*req.RetentionType)
	}

	policy, err := p.loadPolicy(policyID)
	if err != nil {
		return p.notFound(err.Error())
	}
	if err := p.database.PolicyRepo.EnforceExpiredPolicies(); err != nil {
		return p.serverError("Failed to enforce expired policy state", err)
	}
	if err := p.database.PolicyRepo.UpdatePolicy(policy.ID, intervalVal, onValue, retentionType); err != nil {
		return p.serverError("Failed to update policy", err)
	}

	updatedPolicy, err := p.database.PolicyRepo.GetByID(policy.ID)
	if err != nil {
		return p.serverError("Failed to load policy after update", err)
	}
	linkedJobs, err := p.database.CronJobRepo.ListJobsByPolicyID(p.userID, policy.ID)
	if err != nil {
		return p.serverError("Failed to load linked jobs", err)
	}
	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup policy updated successfully",
		"policy":  buildPolicyManageSummaryViewAt(updatedPolicy, linkedJobs, p.now),
	})
}

// HandleAutosyncPolicyCreate creates a named policy and optionally rebinds jobs.
func HandleAutosyncPolicyCreate(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}

	var req autosyncPolicyCreateRequest
	if err := p.echo.Bind(&req); err != nil {
		return p.badRequest(err.Error())
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return p.badRequest("name is required")
	}
	intervalVal, onValue := normalizeScheduleIntervalOn(req.Interval, req.On)
	if err := validateScheduleIntervalOn(intervalVal, onValue); err != nil {
		return p.badRequest(err.Error())
	}
	retentionType := strings.TrimSpace(req.RetentionType)
	if retentionType == "" {
		retentionType = repo.RetentionNever
	}

	jobBatch, err := loadJobBatchByIDs(p.database, p.userID, req.JobIDs)
	if err != nil {
		return p.serverError("Failed to validate jobs", err)
	}
	if err := validateJobIDsInBatch(jobBatch, req.JobIDs); err != nil {
		return p.badRequest(err.Error())
	}

	policy, cerr := p.database.PolicyRepo.CreatePolicy(p.userID, name, intervalVal, onValue, retentionType)
	if cerr != nil {
		if errors.Is(cerr, repo.ErrPolicyNameExists) {
			return p.echo.JSON(http.StatusConflict, policyNameConflictResponse(name))
		}
		return p.serverError("Failed to create policy", cerr)
	}

	var movedJobs []PolicyLinkedJobView
	if len(req.JobIDs) > 0 {
		if err := p.database.PolicyRepo.RebindJobsToPolicy(req.JobIDs, policy.ID); err != nil {
			return p.serverError("Failed to assign jobs to policy", err)
		}
		movedJobs = linkedViewsFromBatch(jobBatch, req.JobIDs)
	}

	linkedJobs := jobBatch.byPolicyID[policy.ID]
	if len(req.JobIDs) > 0 {
		linkedJobs = make([]repo.CronJobListingDB, 0, len(req.JobIDs))
		for _, id := range req.JobIDs {
			if job, ok := jobBatch.byID[id]; ok {
				job.PolicyID = policy.ID
				linkedJobs = append(linkedJobs, job)
			}
		}
	}
	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":    "Policy created successfully",
		"policy":     buildPolicyManageSummaryViewAt(policy, linkedJobs, p.now),
		"moved_jobs": movedJobs,
	})
}

// HandleAutosyncPolicyMove rebinds jobs to an existing policy.
func HandleAutosyncPolicyMove(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}

	var req autosyncPolicyMoveRequest
	if err := p.echo.Bind(&req); err != nil {
		return p.badRequest(err.Error())
	}
	if req.TargetPolicyID == 0 {
		return p.badRequest("target_policy_id is required")
	}
	if len(req.JobIDs) == 0 {
		return p.badRequest("job_ids is required")
	}
	if _, err := p.loadPolicy(req.TargetPolicyID); err != nil {
		return p.notFound("target policy not found for user")
	}

	jobBatch, err := loadJobBatchByIDs(p.database, p.userID, req.JobIDs)
	if err != nil {
		return p.serverError("Failed to validate jobs", err)
	}
	if err := validateJobIDsInBatch(jobBatch, req.JobIDs); err != nil {
		return p.badRequest(err.Error())
	}
	if err := p.database.PolicyRepo.RebindJobsToPolicy(req.JobIDs, req.TargetPolicyID); err != nil {
		return p.serverError("Failed to move assignments", err)
	}

	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":          "Assignments moved successfully",
		"target_policy_id": req.TargetPolicyID,
		"moved_jobs":       linkedViewsFromBatch(jobBatch, req.JobIDs),
	})
}

// HandleAutosyncPolicyOptions returns policy_id and name for move pickers.
func HandleAutosyncPolicyOptions(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}
	options, err := p.database.PolicyRepo.ListPolicyOptions(p.userID)
	if err != nil {
		return p.serverError("Failed to list policy options", err)
	}
	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Policy options",
		"policies": options,
	})
}

// HandleAutosyncPolicyAvailableAssignments supports the Add Email & Services modal.
func HandleAutosyncPolicyAvailableAssignments(c echo.Context) error {
	p, err := newPolicyCtx(c)
	if err != nil {
		return policyUnauthorized(c, err)
	}

	targetPolicyID, err := strconvParseUintParam(strings.TrimSpace(p.echo.QueryParam("policy_id")))
	if err != nil {
		return p.badRequest("policy_id is required and must be a positive integer")
	}
	if _, err := loadPolicyForUser(p.database, p.userID, targetPolicyID); err != nil {
		return p.notFound("policy not found for user")
	}

	allJobs, err := p.database.CronJobRepo.ListAllAutosyncJobsForUser(p.userID)
	if err != nil {
		return p.serverError("Failed to load jobs", err)
	}
	allJobs = repo.FilterJobsByMethods(allJobs, repo.IsSharedCredentialAutosyncMethod)

	selectedEmail := strings.TrimSpace(p.echo.QueryParam("email"))
	if selectedEmail != "" {
		policyNames, nerr := loadPolicyNameMap(p.database, collectDistinctPolicyIDs(allJobs))
		if nerr != nil {
			return p.serverError("Failed to load policy names", nerr)
		}
		return p.echo.JSON(http.StatusOK, buildAvailableServicesResponse(allJobs, targetPolicyID, selectedEmail, policyNames))
	}

	return p.echo.JSON(http.StatusOK, map[string]interface{}{
		"message": "Available assignments",
		"emails":  buildAvailableEmailsView(allJobs, targetPolicyID, p.echo.QueryParam("search")),
	})
}

// onboardingPolicyBatch caches policy ids created during one onboarding request.
// allID is used for policy_scope=all; byOU is used for policy_scope=org_unit.
type onboardingPolicyBatch struct {
	allID uint
	byOU  map[string]uint
}

// resolveOnboardingPolicyID resolves or creates the policy for onboarding job assignment.
func resolveOnboardingPolicyID(
	database *db.PostgresDb,
	userID string,
	cred *repo.GoogleBackupCredentialDB,
	req *GoogleBackupOnboardingRequest,
	email string,
	schedule onboardingSchedule,
	isFirstConnection bool,
	batch *onboardingPolicyBatch,
) (uint, error) {
	if batch == nil {
		batch = &onboardingPolicyBatch{}
	}
	if req != nil && req.isOrgUnitPolicyScope() {
		return resolveOnboardingOrgUnitPolicyID(database, userID, req, email, schedule, batch)
	}

	if batch.allID > 0 {
		return batch.allID, nil
	}

	if req.PolicyID != nil && *req.PolicyID > 0 {
		policy, err := loadPolicyForUser(database, userID, *req.PolicyID)
		if err != nil {
			return 0, fmt.Errorf("policy not found for user")
		}
		batch.allID = policy.ID
		return policy.ID, nil
	}

	if isFirstConnection {
		baseName := onboardingPolicyBaseName(cred, req)
		if strings.TrimSpace(baseName) == "" {
			return 0, fmt.Errorf("policy name is required")
		}
		var name string
		if req != nil && strings.TrimSpace(req.PolicyName) != "" {
			name = strings.TrimSpace(req.PolicyName)
		} else {
			unique, err := database.PolicyRepo.EnsureUniquePolicyName(userID, baseName)
			if err != nil {
				return 0, err
			}
			name = unique
		}
		policy, err := database.PolicyRepo.CreatePolicy(userID, name, schedule.Interval, schedule.On, repo.RetentionNever)
		if err != nil {
			if errors.Is(err, repo.ErrPolicyNameExists) {
				return 0, repo.ErrPolicyNameExists
			}
			return 0, err
		}
		batch.allID = policy.ID
		return policy.ID, nil
	}

	policyName := strings.TrimSpace(req.PolicyName)
	if policyName == "" {
		return 0, fmt.Errorf("policy_id or policy_name is required for subsequent connections")
	}
	created, err := database.PolicyRepo.CreatePolicy(userID, policyName, schedule.Interval, schedule.On, repo.RetentionNever)
	if err != nil {
		if errors.Is(err, repo.ErrPolicyNameExists) {
			return 0, repo.ErrPolicyNameExists
		}
		return 0, err
	}
	batch.allID = created.ID
	return created.ID, nil
}

func resolveOnboardingOrgUnitPolicyID(
	database *db.PostgresDb,
	userID string,
	req *GoogleBackupOnboardingRequest,
	email string,
	fallback onboardingSchedule,
	batch *onboardingPolicyBatch,
) (uint, error) {
	path := orgUnitPathForEmail(req, email)
	if path == "" {
		path = "/"
	}
	path = google.NormalizeOrgUnitPath(path)
	if batch.byOU == nil {
		batch.byOU = make(map[string]uint)
	}
	if id, ok := batch.byOU[path]; ok && id > 0 {
		return id, nil
	}

	sched, baseName, err := orgUnitOnboardingSchedule(req, path, fallback)
	if err != nil {
		return 0, err
	}
	name, err := database.PolicyRepo.EnsureUniquePolicyName(userID, baseName)
	if err != nil {
		return 0, err
	}
	created, err := database.PolicyRepo.CreatePolicy(userID, name, sched.Interval, sched.On, repo.RetentionNever)
	if err != nil {
		if errors.Is(err, repo.ErrPolicyNameExists) {
			return 0, repo.ErrPolicyNameExists
		}
		return 0, err
	}
	batch.byOU[path] = created.ID
	return created.ID, nil
}

func lookupOrgUnitScheduleInput(req *GoogleBackupOnboardingRequest, path string) (OrgUnitScheduleInput, bool) {
	if req == nil || len(req.OrgUnitSchedules) == 0 {
		return OrgUnitScheduleInput{}, false
	}
	path = google.NormalizeOrgUnitPath(path)
	if input, ok := req.OrgUnitSchedules[path]; ok {
		return input, true
	}
	for k, input := range req.OrgUnitSchedules {
		if google.NormalizeOrgUnitPath(k) == path {
			return input, true
		}
	}
	return OrgUnitScheduleInput{}, false
}

func orgUnitOnboardingSchedule(req *GoogleBackupOnboardingRequest, path string, fallback onboardingSchedule) (onboardingSchedule, string, error) {
	path = google.NormalizeOrgUnitPath(path)
	baseName := defaultOrgUnitPolicyName(path)
	if input, ok := lookupOrgUnitScheduleInput(req, path); ok {
		if name := strings.TrimSpace(input.PolicyName); name != "" {
			baseName = name
		}
		interval := strings.TrimSpace(input.Interval)
		on := strings.TrimSpace(input.On)
		if interval == "" {
			interval = fallback.Interval
			on = fallback.On
			if interval == "" && req != nil {
				interval = req.Interval
				on = req.On
			}
		}
		sched, err := parseOnboardingSchedule(interval, on)
		if err != nil {
			return onboardingSchedule{}, "", err
		}
		return sched, baseName, nil
	}
	if fallback.Interval != "" {
		return fallback, baseName, nil
	}
	if req != nil && strings.TrimSpace(req.Interval) != "" {
		sched, err := parseOnboardingSchedule(req.Interval, req.On)
		if err != nil {
			return onboardingSchedule{}, "", err
		}
		return sched, baseName, nil
	}
	return onboardingSchedule{}, "", fmt.Errorf("org_unit_schedules missing entry for %s", path)
}

func onboardingPoliciesFromBatch(database *db.PostgresDb, batch *onboardingPolicyBatch) []onboardingPolicyCreated {
	if batch == nil {
		return nil
	}
	out := make([]onboardingPolicyCreated, 0)
	seen := make(map[uint]struct{})
	add := func(id uint, orgUnitPath string) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		entry := onboardingPolicyCreated{PolicyID: id, OrgUnitPath: orgUnitPath}
		if database != nil {
			if policy, err := database.PolicyRepo.GetByID(id); err == nil && policy != nil {
				entry.Name = policy.Name
				entry.Interval = policy.Interval
				entry.On = policy.On
			}
		}
		out = append(out, entry)
	}
	if len(batch.byOU) > 0 {
		paths := make([]string, 0, len(batch.byOU))
		for path := range batch.byOU {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			add(batch.byOU[path], path)
		}
		return out
	}
	add(batch.allID, "")
	return out
}
