package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

const reconnectScopeCredential = "credential"

// ConnectedAccountView is shared credential data returned on policy list/detail and project update responses.
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

// PolicyDetailView is schedule + job operational fields (legacy per-job shape).
type PolicyDetailView struct {
	PolicyID      uint   `json:"policy_id"`
	CredentialID  uint   `json:"credential_id,omitempty"`
	JobID         uint   `json:"job_id"`
	Interval      string `json:"interval"`
	Email         string `json:"email"`
	Method        string `json:"method"`
	On            string `json:"on"`
	RetentionType string `json:"retention_type"`
	IsExpired     bool   `json:"is_expired"`
	Active        bool   `json:"active"`
	SyncType      string `json:"sync_type"`
}

// PolicyListItemView is one shared policy row (distinct by policy_id).
// Reconnect flags live on account only (not duplicated on the policy object).
type PolicyListItemView = PolicyListRowPolicyView

// PolicyListRowPolicyView is schedule/job-count fields for GET /auto-sync/policy listing rows.
// Reconnect flags live on account only (not duplicated on the policy object).
type PolicyListRowPolicyView struct {
	PolicyID       uint       `json:"policy_id"`
	CredentialID   uint       `json:"credential_id"`
	Interval       string     `json:"interval"`
	On             string     `json:"on"`
	RetentionType  string     `json:"retention_type"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	IsExpired      bool       `json:"is_expired"`
	LinkedJobCount int        `json:"linked_job_count"`
}

// PolicyListRowView is one policy row on GET /auto-sync/policy with connected account context.
type PolicyListRowView struct {
	PolicyListRowPolicyView
	Account ConnectedAccountView `json:"account"`
}

// PolicyLinkedJobView is a slim job row linked to a policy.
type PolicyLinkedJobView struct {
	JobID    uint   `json:"job_id"`
	Email    string `json:"email"`
	Method   string `json:"method"`
	Active   bool   `json:"active"`
	SyncType string `json:"sync_type"`
}

type autosyncPolicyUpdateRequest struct {
	Interval       *string `json:"interval"`
	On             *string `json:"on"`
	RetentionType  *string `json:"retention_type"`
	ApplyAll       *bool   `json:"apply_all"`
	SelectedJobIDs []uint  `json:"selected_job_ids"`
	Active         *bool   `json:"active"`
}

const mergeJobPreviewLimit = 20

type PolicyScheduleView struct {
	Interval      string     `json:"interval"`
	On            string     `json:"on"`
	RetentionType string     `json:"retention_type"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	IsExpired     bool       `json:"is_expired"`
}

type MergePreviewPolicy struct {
	PolicyListItemView
	Role string `json:"role"`
}

type PolicyMergeImpactView struct {
	PolicyKept          uint `json:"policy_kept"`
	PoliciesToRemove    int  `json:"policies_to_remove,omitempty"`
	PoliciesRemoved     int  `json:"policies_removed,omitempty"`
	JobsToRebind        int  `json:"jobs_to_rebind,omitempty"`
	JobsRebound         int  `json:"jobs_rebound,omitempty"`
	TotalJobsAfterMerge int  `json:"total_jobs_after_merge"`
}

type PolicyMergeJobPreview struct {
	JobID    uint   `json:"job_id"`
	Email    string `json:"email"`
	Method   string `json:"method"`
	PolicyID uint   `json:"policy_id"`
}

type PolicyMergePreviewGroup struct {
	Schedule                     PolicyScheduleView      `json:"schedule"`
	Account                      ConnectedAccountView    `json:"account"`
	Impact                       PolicyMergeImpactView   `json:"impact"`
	RecommendedCanonicalPolicyID uint                    `json:"recommended_canonical_policy_id"`
	CanonicalReason              string                  `json:"canonical_reason"`
	Policies                     []MergePreviewPolicy    `json:"policies"`
	PolicyIDs                    []uint                  `json:"policy_ids"`
	LinkedJobsPreview            []PolicyMergeJobPreview `json:"linked_jobs_preview"`
	JobsToRebind                 int                     `json:"jobs_to_rebind"`
	HasMoreJobs                  bool                    `json:"has_more_jobs"`
}

type autosyncPolicyMergeRequest struct {
	PolicyIDs []uint `json:"policy_ids"`
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
	if policy == nil {
		return PolicyScheduleView{}
	}
	return PolicyScheduleView{
		Interval:      policy.Interval,
		On:            policy.On,
		RetentionType: policy.RetentionType,
		ExpiresAt:     policy.ExpiresAt,
		IsExpired:     repo.IsPolicyExpired(policy, time.Now().UTC()),
	}
}

func buildMergePreviewImpact(group repo.MergeablePolicyGroupData) PolicyMergeImpactView {
	return PolicyMergeImpactView{
		PolicyKept:          group.Canonical.ID,
		PoliciesToRemove:    len(group.Policies) - 1,
		JobsToRebind:        group.JobsOnDuplicates,
		TotalJobsAfterMerge: group.TotalJobsAfter,
	}
}

func buildMergeExecuteImpact(result *repo.MergeExecuteResult) PolicyMergeImpactView {
	if result == nil {
		return PolicyMergeImpactView{}
	}
	return PolicyMergeImpactView{
		PolicyKept:          result.CanonicalPolicyID,
		PoliciesRemoved:     len(result.RemovedPolicyIDs),
		JobsRebound:         result.JobsRebound,
		TotalJobsAfterMerge: result.TotalJobsAfterMerge,
	}
}

func validateMergePolicyIDSet(submitted, fullGroup []uint) []uint {
	submittedSet := make(map[uint]struct{}, len(submitted))
	for _, id := range submitted {
		submittedSet[id] = struct{}{}
	}
	missing := make([]uint, 0)
	for _, id := range fullGroup {
		if _, ok := submittedSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func capMergeJobPreview(jobs []PolicyMergeJobPreview, limit int) ([]PolicyMergeJobPreview, bool) {
	if limit <= 0 || len(jobs) <= limit {
		return jobs, false
	}
	return jobs[:limit], true
}

func accountViewFromCredential(cred *repo.GoogleBackupCredentialDB) ConnectedAccountView {
	account := buildConnectedAccountView(cred, "", "")
	if cred != nil {
		account.GoogleEmail = strings.TrimSpace(cred.Email)
		account.OAuthHolderEmail = strings.TrimSpace(cred.Email)
	}
	return account
}

func buildConnectedAccountViewForPolicy(database *db.PostgresDb, cred *repo.GoogleBackupCredentialDB, projectID, googleEmail string, linkedJobs []repo.CronJobListingDB) ConnectedAccountView {
	account := buildConnectedAccountView(cred, projectID, googleEmail)
	if database != nil && database.CronJobRepo != nil {
		needsGoogle, needsStorx := credentialReconnectFlagsFromJobs(database.CronJobRepo, cred, linkedJobs)
		account.NeedsGoogleReconnect = needsGoogle
		account.NeedsStorxReconnect = needsStorx
	}
	return account
}

func buildPolicyListRowPolicyView(policy *repo.AutosyncBackupPolicyDB, linkedJobCount int) PolicyListRowPolicyView {
	now := time.Now().UTC()
	return PolicyListRowPolicyView{
		PolicyID:       policy.ID,
		CredentialID:   policy.CredentialID,
		Interval:       policy.Interval,
		On:             policy.On,
		RetentionType:  policy.RetentionType,
		ExpiresAt:      policy.ExpiresAt,
		IsExpired:      repo.IsPolicyExpired(policy, now),
		LinkedJobCount: linkedJobCount,
	}
}

func buildPolicyListItemView(policy *repo.AutosyncBackupPolicyDB, linkedJobCount int) PolicyListItemView {
	return buildPolicyListRowPolicyView(policy, linkedJobCount)
}

func loadCredentialForPolicy(database *db.PostgresDb, policy *repo.AutosyncBackupPolicyDB) (*repo.GoogleBackupCredentialDB, error) {
	if database == nil || policy == nil || policy.CredentialID == 0 {
		return nil, fmt.Errorf("credential not found for policy")
	}
	return database.CredentialRepo.GetByID(policy.CredentialID)
}

func buildPolicyLinkedJobView(job *repo.CronJobListingDB) PolicyLinkedJobView {
	return PolicyLinkedJobView{
		JobID:    job.ID,
		Email:    jobMailboxEmail(job),
		Method:   job.Method,
		Active:   job.Active,
		SyncType: job.SyncType,
	}
}

func buildPolicyDetailView(database *db.PostgresDb, policy *repo.AutosyncBackupPolicyDB, job *repo.CronJobListingDB, omitCredentialID bool) PolicyDetailView {
	database.PolicyRepo.EnrichJobFromPolicy(job)
	v := PolicyDetailView{
		PolicyID:      policy.ID,
		JobID:         job.ID,
		Interval:      policy.Interval,
		On:            policy.On,
		RetentionType: policy.RetentionType,
		IsExpired:     repo.IsPolicyExpired(policy, time.Now().UTC()),
		Email:         jobMailboxEmail(job),
		Method:        job.Method,
		Active:        job.Active,
		SyncType:      job.SyncType,
	}
	if !omitCredentialID {
		v.CredentialID = policy.CredentialID
	}
	return v
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

func validateSelectedJobsForUser(database *db.PostgresDb, userID string, jobIDs []uint) error {
	if len(jobIDs) == 0 {
		return fmt.Errorf("selected_job_ids is required")
	}
	seen := make(map[uint]struct{}, len(jobIDs))
	for _, id := range jobIDs {
		if id == 0 {
			return fmt.Errorf("invalid job id in selected_job_ids")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if _, err := database.CronJobRepo.GetJobByIDForUser(userID, id); err != nil {
			return fmt.Errorf("job %d not found for user", id)
		}
	}
	return nil
}

func resolvePolicyCredential(database *db.PostgresDb, userID, credentialIDStr, projectID, googleEmail string) (*repo.GoogleBackupCredentialDB, string, string, error) {
	if credentialIDStr != "" {
		cid, parseErr := strconv.ParseUint(credentialIDStr, 10, 64)
		if parseErr != nil || cid == 0 {
			return nil, "", "", fmt.Errorf("credential_id must be a positive integer")
		}
		cred, err := database.CredentialRepo.GetByID(uint(cid))
		if err != nil {
			return nil, "", "", fmt.Errorf("credential not found")
		}
		return cred, cred.StorjProjectID, cred.Email, nil
	}
	if projectID == "" || googleEmail == "" {
		return nil, "", "", fmt.Errorf("credential_id or project_id + google_email is required")
	}
	cred, err := resolveUserCredentialByProjectAndEmail(database, userID, projectID, googleEmail)
	if err != nil {
		return nil, "", "", err
	}
	return cred, cred.StorjProjectID, cred.Email, nil
}

func policyAccountFilterProvided(credentialIDStr, projectID, googleEmail string) bool {
	return strings.TrimSpace(credentialIDStr) != "" ||
		strings.TrimSpace(projectID) != "" ||
		strings.TrimSpace(googleEmail) != ""
}

func listPoliciesForUser(database *db.PostgresDb, userID string, policies []repo.AutosyncBackupPolicyDB) ([]PolicyListRowView, error) {
	policyItems := make([]PolicyListRowView, 0, len(policies))
	credCache := make(map[uint]*repo.GoogleBackupCredentialDB)
	for i := range policies {
		linked, lerr := database.CronJobRepo.ListJobsByPolicyID(userID, policies[i].ID)
		if lerr != nil {
			continue
		}
		if len(linked) == 0 {
			continue
		}
		var cred *repo.GoogleBackupCredentialDB
		if policies[i].CredentialID > 0 {
			if cached, ok := credCache[policies[i].CredentialID]; ok {
				cred = cached
			} else {
				cred, _ = database.CredentialRepo.GetByID(policies[i].CredentialID)
				credCache[policies[i].CredentialID] = cred
			}
		}
		account := accountViewFromCredential(cred)
		if database != nil && database.CronJobRepo != nil {
			needsGoogle, needsStorx := credentialReconnectFlagsFromJobs(database.CronJobRepo, cred, linked)
			account.NeedsGoogleReconnect = needsGoogle
			account.NeedsStorxReconnect = needsStorx
		}
		policyItems = append(policyItems, PolicyListRowView{
			PolicyListRowPolicyView: buildPolicyListRowPolicyView(&policies[i], len(linked)),
			Account:                 account,
		})
	}
	return policyItems, nil
}

func loadPolicyForUser(database *db.PostgresDb, userID string, policyID uint) (*repo.AutosyncBackupPolicyDB, error) {
	policy, err := database.PolicyRepo.GetByID(policyID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(policy.UserID) != strings.TrimSpace(userID) {
		return nil, fmt.Errorf("policy not found for user")
	}
	linked, err := database.CronJobRepo.ListJobsByPolicyID(userID, policy.ID)
	if err != nil {
		return nil, err
	}
	if len(linked) == 0 {
		return nil, fmt.Errorf("no jobs linked to policy")
	}
	return policy, nil
}

func validateSelectedJobsOnPolicy(database *db.PostgresDb, userID string, policyID uint, jobIDs []uint) error {
	if err := validateSelectedJobsForUser(database, userID, jobIDs); err != nil {
		return err
	}
	for _, id := range jobIDs {
		job, err := database.CronJobRepo.GetJobByIDForUser(userID, id)
		if err != nil {
			return fmt.Errorf("job %d not found for user", id)
		}
		if job.PolicyID != policyID {
			return fmt.Errorf("job %d is not linked to policy %d", id, policyID)
		}
	}
	return nil
}

// HandleAutosyncPolicyList lists all distinct shared policies for the authenticated user.
// Optional query credential_id or project_id+google_email filters to one connected account.
func HandleAutosyncPolicyList(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	credentialIDStr := strings.TrimSpace(c.QueryParam("credential_id"))
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	googleEmail := strings.TrimSpace(c.QueryParam("google_email"))

	var policies []repo.AutosyncBackupPolicyDB
	resp := map[string]interface{}{
		"message":  "Backup policies list",
		"policies": []PolicyListRowView{},
		"failed":   []interface{}{},
	}

	if policyAccountFilterProvided(credentialIDStr, projectID, googleEmail) {
		cred, pid, email, cerr := resolvePolicyCredential(database, userID, credentialIDStr, projectID, googleEmail)
		if cerr != nil {
			var he *echo.HTTPError
			if errors.As(cerr, &he) {
				return c.JSON(he.Code, he.Message)
			}
			status := http.StatusBadRequest
			if strings.Contains(cerr.Error(), "not found") {
				status = http.StatusNotFound
			}
			return c.JSON(status, map[string]interface{}{
				"message": "Invalid Request",
				"error":   cerr.Error(),
			})
		}
		policies, err = database.PolicyRepo.ListByUserAndCredentialID(userID, cred.ID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Failed to list policies", "error": err.Error()})
		}
		allLinked := make([]repo.CronJobListingDB, 0)
		for i := range policies {
			linked, lerr := database.CronJobRepo.ListJobsByPolicyID(userID, policies[i].ID)
			if lerr != nil {
				continue
			}
			allLinked = append(allLinked, linked...)
		}
		resp["account"] = buildConnectedAccountViewForPolicy(database, cred, pid, email, allLinked)
	} else {
		policies, err = database.PolicyRepo.ListByUserID(userID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Failed to list policies", "error": err.Error()})
		}
	}

	policyItems, err := listPoliciesForUser(database, userID, policies)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"message": "Failed to list policies", "error": err.Error()})
	}
	resp["policies"] = policyItems

	return c.JSON(http.StatusOK, resp)
}

// HandleAutosyncPolicyByID returns one policy by id.
func HandleAutosyncPolicyByID(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}
	policyID, err := strconv.Atoi(c.Param("policy_id"))
	if err != nil || policyID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "invalid policy_id",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	policy, err := loadPolicyForUser(database, userID, uint(policyID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}
	linkedJobs, err := database.CronJobRepo.ListJobsByPolicyID(userID, policy.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load linked jobs",
			"error":   err.Error(),
		})
	}
	linkedViews := make([]PolicyLinkedJobView, 0, len(linkedJobs))
	for i := range linkedJobs {
		linkedViews = append(linkedViews, buildPolicyLinkedJobView(&linkedJobs[i]))
	}

	cred, _ := loadCredentialForPolicy(database, policy)
	resp := map[string]interface{}{
		"message":     "Backup policy details",
		"policy":      buildPolicyListItemView(policy, len(linkedJobs)),
		"linked_jobs": linkedViews,
		"failed":      []interface{}{},
	}
	if cred != nil {
		resp["account"] = buildConnectedAccountViewForPolicy(database, cred, cred.StorjProjectID, cred.Email, linkedJobs)
	}

	return c.JSON(http.StatusOK, resp)
}

// HandleAutosyncPolicyByJobID returns the policy for a job.
func HandleAutosyncPolicyByJobID(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}
	jobID, err := strconv.Atoi(c.Param("job_id"))
	if err != nil || jobID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "invalid job_id",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	job, err := database.CronJobRepo.GetJobByIDForUser(userID, uint(jobID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}
	if job.PolicyID == 0 {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "no policy found for job",
		})
	}
	policy, err := database.PolicyRepo.GetByID(job.PolicyID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "no policy found for job",
		})
	}
	if strings.TrimSpace(policy.UserID) != strings.TrimSpace(userID) {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "policy not found for user",
		})
	}

	linkedJobs, lerr := database.CronJobRepo.ListJobsByPolicyID(userID, policy.ID)
	if lerr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load linked jobs",
			"error":   lerr.Error(),
		})
	}

	cred, _ := loadCredentialForPolicy(database, policy)
	resp := map[string]interface{}{
		"message":     "Backup policy for job",
		"policy":      buildPolicyListItemView(policy, len(linkedJobs)),
		"linked_jobs": []PolicyLinkedJobView{buildPolicyLinkedJobView(job)},
		"failed":      []interface{}{},
	}
	if cred != nil {
		resp["account"] = buildConnectedAccountViewForPolicy(database, cred, cred.StorjProjectID, cred.Email, linkedJobs)
	}

	return c.JSON(http.StatusOK, resp)
}

func buildPolicyMergePreviewGroup(database *db.PostgresDb, group repo.MergeablePolicyGroupData) (PolicyMergePreviewGroup, error) {
	cred, _ := loadCredentialForPolicy(database, &group.Canonical)
	linkedJobs, _ := database.CronJobRepo.ListJobsByPolicyID(group.Canonical.UserID, group.Canonical.ID)
	var projectID, googleEmail string
	if cred != nil {
		projectID = cred.StorjProjectID
		googleEmail = cred.Email
	}
	account := buildConnectedAccountViewForPolicy(database, cred, projectID, googleEmail, linkedJobs)

	policies := make([]MergePreviewPolicy, 0, len(group.Policies))
	for i := range group.Policies {
		role := "duplicate"
		if group.Policies[i].ID == group.Canonical.ID {
			role = "canonical"
		}
		policies = append(policies, MergePreviewPolicy{
			PolicyListItemView: buildPolicyListItemView(&group.Policies[i], group.JobCounts[group.Policies[i].ID]),
			Role:               role,
		})
	}

	jobPreviews := make([]PolicyMergeJobPreview, 0, group.JobsOnDuplicates)
	for _, duplicateID := range group.DuplicatePolicyIDs {
		jobs, err := database.CronJobRepo.ListJobsByPolicyID(group.Canonical.UserID, duplicateID)
		if err != nil {
			return PolicyMergePreviewGroup{}, err
		}
		for j := range jobs {
			jobPreviews = append(jobPreviews, PolicyMergeJobPreview{
				JobID:    jobs[j].ID,
				Email:    jobMailboxEmail(&jobs[j]),
				Method:   jobs[j].Method,
				PolicyID: duplicateID,
			})
		}
	}
	previewJobs, hasMore := capMergeJobPreview(jobPreviews, mergeJobPreviewLimit)

	return PolicyMergePreviewGroup{
		Schedule:                     buildPolicyScheduleView(&group.Canonical),
		Account:                      account,
		Impact:                       buildMergePreviewImpact(group),
		RecommendedCanonicalPolicyID: group.Canonical.ID,
		CanonicalReason:              group.CanonicalReason,
		Policies:                     policies,
		PolicyIDs:                    group.PolicyIDs,
		LinkedJobsPreview:            previewJobs,
		JobsToRebind:                 group.JobsOnDuplicates,
		HasMoreJobs:                  hasMore,
	}, nil
}

// HandleAutosyncPolicyMergePreview lists all mergeable duplicate policy groups for the authenticated user.
func HandleAutosyncPolicyMergePreview(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	groups, gerr := database.PolicyRepo.ListMergeablePolicyGroups(userID)
	if gerr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load mergeable policies",
			"error":   gerr.Error(),
		})
	}

	previewGroups := make([]PolicyMergePreviewGroup, 0, len(groups))
	summary := map[string]int{
		"mergeable_group_count":  0,
		"duplicate_policy_count": 0,
		"jobs_that_would_move":   0,
	}
	msg := "No duplicate policies to merge"
	if len(groups) > 0 {
		msg = "Mergeable duplicate policies"
	}
	for i := range groups {
		previewGroup, perr := buildPolicyMergePreviewGroup(database, groups[i])
		if perr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "Failed to build merge preview",
				"error":   perr.Error(),
			})
		}
		previewGroups = append(previewGroups, previewGroup)
		summary["mergeable_group_count"]++
		summary["duplicate_policy_count"] += len(groups[i].Policies) - 1
		summary["jobs_that_would_move"] += groups[i].JobsOnDuplicates
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": msg,
		"summary": summary,
		"groups":  previewGroups,
		"failed":  []interface{}{},
	})
}

// HandleAutosyncPolicyMerge merges one complete duplicate policy group selected by policy_ids.
func HandleAutosyncPolicyMerge(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	var req autosyncPolicyMergeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}
	if len(req.PolicyIDs) < 2 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "At least two policy_ids are required",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	result, merr := database.PolicyRepo.MergeSelectedPolicyGroup(userID, req.PolicyIDs)
	if merr != nil {
		var incomplete *repo.MergeIncompleteGroupError
		switch {
		case errors.Is(merr, repo.ErrMergePolicyIDsRequired):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "At least two policy_ids are required",
			})
		case errors.Is(merr, repo.ErrMergePolicyNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"message": "Policy not found for user",
				"error":   merr.Error(),
			})
		case errors.Is(merr, repo.ErrMergeMixedGroups):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "All policy_ids must belong to the same schedule group",
				"error":   merr.Error(),
			})
		case errors.As(merr, &incomplete):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message":            "Incomplete merge group; include all policies in this duplicate set",
				"missing_policy_ids": incomplete.MissingPolicyIDs,
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "Failed to merge duplicate policies",
				"error":   merr.Error(),
			})
		}
	}

	canonicalPolicy, perr := database.PolicyRepo.GetByID(result.CanonicalPolicyID)
	if perr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load merged policy",
			"error":   perr.Error(),
		})
	}
	linkedJobs, lerr := database.CronJobRepo.ListJobsByPolicyID(userID, canonicalPolicy.ID)
	if lerr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load linked jobs",
			"error":   lerr.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Policies merged successfully",
		"merge": map[string]interface{}{
			"schedule":            buildPolicyScheduleView(canonicalPolicy),
			"canonical_policy_id": result.CanonicalPolicyID,
			"canonical_reason":    result.CanonicalReason,
			"removed_policy_ids":  result.RemovedPolicyIDs,
			"policy_ids":          result.PolicyIDs,
			"jobs_rebound":        result.JobsRebound,
			"impact":              buildMergeExecuteImpact(result),
			"policy":              buildPolicyListItemView(canonicalPolicy, len(linkedJobs)),
		},
		"failed": []interface{}{},
	})
}

// HandleAutosyncPolicyUpdate updates shared policy schedule/retention (apply_all or selective rebind).
func HandleAutosyncPolicyUpdate(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}
	policyID, err := strconv.Atoi(c.Param("policy_id"))
	if err != nil || policyID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "invalid policy_id",
		})
	}

	var req autosyncPolicyUpdateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid request body",
			"error":   err.Error(),
		})
	}
	if req.Active != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "active updates use PUT /auto-sync/job/:job_id",
		})
	}
	intervalVal, onValue, schedErr := parseScheduleFromRequest(req.Interval, req.On)
	if schedErr != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   schedErr.Error(),
		})
	}
	if req.Interval == nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "interval and on are required",
		})
	}
	retentionType := repo.RetentionNever
	if req.RetentionType != nil {
		retentionType = strings.TrimSpace(*req.RetentionType)
	}
	applyAll := true
	if req.ApplyAll != nil {
		applyAll = *req.ApplyAll
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	policy, err := loadPolicyForUser(database, userID, uint(policyID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	affectedJobIDs := make([]uint, 0)

	if err := database.PolicyRepo.EnforceExpiredPolicies(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to enforce expired policy state",
			"error":   err.Error(),
		})
	}

	if applyAll {
		// apply_all=true: always update the policy row from the URL; never create or rebind to another policy.
		linkedJobs, lerr := database.CronJobRepo.ListJobsByPolicyID(userID, policy.ID)
		if lerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "Failed to load linked jobs",
				"error":   lerr.Error(),
			})
		}
		for i := range linkedJobs {
			affectedJobIDs = append(affectedJobIDs, linkedJobs[i].ID)
		}
		if err := database.PolicyRepo.UpdatePolicy(policy.ID, intervalVal, onValue, retentionType); err != nil {
			status := http.StatusInternalServerError
			msg := "Failed to update policy"
			if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
				status = http.StatusConflict
				msg = "Another active policy already has this schedule; change retention or schedule, or use GET /auto-sync/policy/merge/preview then POST /auto-sync/policy/merge"
			}
			return c.JSON(status, map[string]interface{}{
				"message": msg,
				"error":   err.Error(),
			})
		}
	} else {
		// apply_all=false: always a new policy row + fresh expires_at for selected jobs only (do not join another active batch).
		linkedJobs, jerr := database.CronJobRepo.ListJobsByPolicyID(userID, policy.ID)
		if jerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "Failed to load linked jobs",
				"error":   jerr.Error(),
			})
		}
		if len(req.SelectedJobIDs) == 0 {
			choices := make([]map[string]interface{}, 0, len(linkedJobs))
			for i := range linkedJobs {
				choices = append(choices, map[string]interface{}{
					"job_id": linkedJobs[i].ID,
					"email":  jobMailboxEmail(&linkedJobs[i]),
					"method": linkedJobs[i].Method,
				})
			}
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message":     "selected_job_ids required when apply_all is false",
				"linked_jobs": choices,
			})
		}
		if err := validateSelectedJobsOnPolicy(database, userID, policy.ID, req.SelectedJobIDs); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   err.Error(),
			})
		}
		affectedJobIDs = append(affectedJobIDs, req.SelectedJobIDs...)
		newPolicy, ferr := database.PolicyRepo.CreateNewPolicy(userID, policy.CredentialID, intervalVal, onValue, retentionType)
		if ferr != nil {
			status := http.StatusInternalServerError
			msg := "Failed to create policy for selected jobs"
			if strings.Contains(strings.ToLower(ferr.Error()), "duplicate key") {
				status = http.StatusConflict
				msg = "An active policy with this schedule already exists; use apply_all true to update it for all mailboxes, or change retention/schedule, or use GET /auto-sync/policy/merge/preview then POST /auto-sync/policy/merge"
			}
			return c.JSON(status, map[string]interface{}{
				"message": msg,
				"error":   ferr.Error(),
			})
		}
		if err := database.PolicyRepo.RebindJobsToPolicy(req.SelectedJobIDs, newPolicy.ID); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "Failed to rebind selected jobs",
				"error":   err.Error(),
			})
		}
		policy = newPolicy
	}

	updatedPolicy, err := database.PolicyRepo.GetByID(policy.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load policy after update",
			"error":   err.Error(),
		})
	}

	affectedViews := make([]PolicyLinkedJobView, 0, len(affectedJobIDs))
	for _, jobID := range affectedJobIDs {
		job, jerr := database.CronJobRepo.GetJobByIDForUser(userID, jobID)
		if jerr != nil {
			continue
		}
		affectedViews = append(affectedViews, buildPolicyLinkedJobView(job))
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "Backup policy updated successfully",
		"apply_all":     applyAll,
		"policy":        buildPolicyListItemView(updatedPolicy, len(affectedViews)),
		"affected_jobs": affectedViews,
		"failed":        []interface{}{},
	})
}
