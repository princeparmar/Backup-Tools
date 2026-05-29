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

// PolicyDetailView is schedule + job operational fields for policy GET/PUT responses.
type PolicyDetailView struct {
	PolicyID     uint   `json:"policy_id"`
	CredentialID uint   `json:"credential_id,omitempty"`
	JobID        uint   `json:"job_id"`
	Interval     string `json:"interval"`
	Email        string `json:"email"`
	Method       string `json:"method"`
	On           string `json:"on"`
	Active       bool   `json:"active"`
	SyncType     string `json:"sync_type"`
}

type autosyncPolicyUpdateRequest struct {
	Interval *string `json:"interval"`
	On       *string `json:"on"`
	Active   *bool   `json:"active"`
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

func buildPolicyDetailView(database *db.PostgresDb, policy *repo.AutosyncBackupPolicyDB, job *repo.CronJobListingDB, omitCredentialID bool) PolicyDetailView {
	database.PolicyRepo.EnrichJobFromPolicy(job)
	v := PolicyDetailView{
		PolicyID: policy.ID,
		JobID:    policy.JobID,
		Interval: policy.Interval,
		On:       policy.On,
		Email:    jobMailboxEmail(job),
		Method:   job.Method,
		Active:   job.Active,
		SyncType: job.SyncType,
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

func loadPolicyForUser(database *db.PostgresDb, userID string, policyID uint) (*repo.AutosyncBackupPolicyDB, *repo.CronJobListingDB, error) {
	policy, err := database.PolicyRepo.GetByID(policyID)
	if err != nil {
		return nil, nil, err
	}
	job, err := database.CronJobRepo.GetJobByIDForUser(userID, policy.JobID)
	if err != nil {
		return nil, nil, err
	}
	if policy.CredentialID != 0 && repo.JobCredentialID(job) != policy.CredentialID {
		return nil, nil, fmt.Errorf("policy credential mismatch")
	}
	return policy, job, nil
}

// HandleAutosyncPolicyList lists policies for a connected account.
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

	var cred *repo.GoogleBackupCredentialDB
	if credentialIDStr != "" {
		cid, parseErr := strconv.ParseUint(credentialIDStr, 10, 64)
		if parseErr != nil || cid == 0 {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "credential_id must be a positive integer",
			})
		}
		cred, err = database.CredentialRepo.GetByID(uint(cid))
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "credential not found",
			})
		}
	} else {
		if projectID == "" || googleEmail == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"message": "Invalid Request",
				"error":   "credential_id or project_id + google_email is required",
			})
		}
		cred, err = resolveUserCredentialByProjectAndEmail(database, userID, projectID, googleEmail)
		if err != nil {
			var he *echo.HTTPError
			if errors.As(err, &he) {
				return c.JSON(he.Code, he.Message)
			}
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"message": "internal server error",
				"error":   err.Error(),
			})
		}
		projectID = cred.StorjProjectID
		googleEmail = cred.Email
	}

	policies, err := database.PolicyRepo.ListByCredentialID(cred.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to list policies",
			"error":   err.Error(),
		})
	}

	account := buildConnectedAccountView(cred, projectID, googleEmail)
	success := make([]PolicyDetailView, 0, len(policies))
	for i := range policies {
		job, jobErr := database.CronJobRepo.GetJobByIDForUser(userID, policies[i].JobID)
		if jobErr != nil {
			continue
		}
		success = append(success, buildPolicyDetailView(database, &policies[i], job, true))
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup policies list",
		"account": account,
		"success": success,
		"failed":  []interface{}{},
	})
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
	policy, job, err := loadPolicyForUser(database, userID, uint(policyID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup policy details",
		"success": []PolicyDetailView{buildPolicyDetailView(database, policy, job, false)},
		"failed":  []interface{}{},
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
	policy, err := database.PolicyRepo.GetByJobID(uint(jobID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   "no policy found for job",
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup policy for job",
		"success": []PolicyDetailView{buildPolicyDetailView(database, policy, job, false)},
		"failed":  []interface{}{},
	})
}

// HandleAutosyncPolicyUpdate updates interval+on on one policy row.
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

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	policy, _, err := loadPolicyForUser(database, userID, uint(policyID))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"message": "Invalid Request",
			"error":   err.Error(),
		})
	}

	if err := database.PolicyRepo.UpdateSchedule(policy.ID, intervalVal, onValue); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to update policy",
			"error":   err.Error(),
		})
	}

	updatedPolicy, err := database.PolicyRepo.GetByID(policy.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load policy after update",
			"error":   err.Error(),
		})
	}
	updatedJob, err := database.CronJobRepo.GetJobByIDForUser(userID, policy.JobID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"message": "Failed to load job after update",
			"error":   err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup policy updated successfully",
		"success": []PolicyDetailView{buildPolicyDetailView(database, updatedPolicy, updatedJob, false)},
		"failed":  []interface{}{},
	})
}
