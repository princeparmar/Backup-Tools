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
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// ConnectedAccountView is shared credential data returned once on project/policy list responses.
type ConnectedAccountView struct {
	ProjectID      string `json:"project_id"`
	GoogleEmail    string `json:"google_email"`
	CredentialID   uint   `json:"credential_id"`
	StorjProjectID string `json:"storj_project_id,omitempty"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	StorxToken     string `json:"storx_token,omitempty"`
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
type PolicyListItemView struct {
	PolicyID       uint       `json:"policy_id"`
	CredentialID   uint       `json:"credential_id"`
	Interval       string     `json:"interval"`
	On             string     `json:"on"`
	RetentionType  string     `json:"retention_type"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	IsExpired      bool       `json:"is_expired"`
	LinkedJobCount int        `json:"linked_job_count"`
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

type autosyncPolicyMergeRequest struct {
	DryRun bool `json:"dry_run"`
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

func maskConnectedAccountView(v *ConnectedAccountView) {
	if v == nil {
		return
	}
	if v.RefreshToken != "" {
		v.RefreshToken = utils.MaskString(v.RefreshToken)
	}
	if v.StorxToken != "" {
		v.StorxToken = utils.MaskString(v.StorxToken)
	}
}

func buildConnectedAccountView(cred *repo.GoogleBackupCredentialDB, projectID, googleEmail string) ConnectedAccountView {
	v := ConnectedAccountView{
		ProjectID:    strings.TrimSpace(projectID),
		GoogleEmail:  strings.TrimSpace(googleEmail),
		CredentialID: cred.ID,
	}
	if cred != nil {
		v.StorjProjectID = strings.TrimSpace(cred.StorjProjectID)
		v.RefreshToken = strings.TrimSpace(cred.RefreshToken)
		v.StorxToken = strings.TrimSpace(cred.StorxToken)
	}
	maskConnectedAccountView(&v)
	return v
}

func buildPolicyListItemView(policy *repo.AutosyncBackupPolicyDB, linkedJobCount int) PolicyListItemView {
	now := time.Now().UTC()
	return PolicyListItemView{
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

func listPoliciesForUser(database *db.PostgresDb, userID string, policies []repo.AutosyncBackupPolicyDB) ([]PolicyListItemView, error) {
	policyItems := make([]PolicyListItemView, 0, len(policies))
	for i := range policies {
		linked, lerr := database.CronJobRepo.ListJobsByPolicyID(userID, policies[i].ID)
		if lerr != nil {
			continue
		}
		if len(linked) == 0 {
			continue
		}
		policyItems = append(policyItems, buildPolicyListItemView(&policies[i], len(linked)))
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
		"policies": []PolicyListItemView{},
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
		resp["account"] = buildConnectedAccountView(cred, pid, email)
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":     "Backup policy details",
		"policy":      buildPolicyListItemView(policy, len(linkedJobs)),
		"linked_jobs": linkedViews,
		"failed":      []interface{}{},
	})
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":     "Backup policy for job",
		"policy":      buildPolicyListItemView(policy, 1),
		"linked_jobs": []PolicyLinkedJobView{buildPolicyLinkedJobView(job)},
		"failed":      []interface{}{},
	})
}

// HandleAutosyncPolicyMerge merges duplicate policy rows per user+credential+schedule fingerprint.
func HandleAutosyncPolicyMerge(c echo.Context) error {
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Authentication required",
			"error":   err.Error(),
		})
	}

	var req autosyncPolicyMergeRequest
	_ = c.Bind(&req)

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	result, merr := database.PolicyRepo.MergeDuplicatePolicies(userID, req.DryRun)
	if merr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to merge duplicate policies",
			"error":   merr.Error(),
		})
	}

	msg := "Duplicate policies merged"
	if req.DryRun {
		msg = "Duplicate policy merge preview (dry run)"
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": msg,
		"merge":   result,
		"dry_run": req.DryRun,
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
				msg = "Another active policy already has this schedule; change retention or schedule, or POST /auto-sync/policy/merge"
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
				msg = "An active policy with this schedule already exists; use apply_all true to update it for all mailboxes, or change retention/schedule, or POST /auto-sync/policy/merge"
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
