package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

// HandleMicrosoftAccessVerifyStart creates (or resumes) one system access-check job + task.
// Does not create backup jobs — probes only until Continue/Initialize materializes.
func HandleMicrosoftAccessVerifyStart(c echo.Context) error {
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
	if err := req.validateForAccessPreview(userID); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	if !req.applicationMode() {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "access check start requires backup_mode=organization",
		})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	services := normalizeOnboardingServices(req.Services)
	tc, _, err := resolveMicrosoftOnboardingTenant(c, ctx, database, userID, &req, true)
	if err != nil {
		return microsoftErrorJSON(c, err)
	}
	appCtx, aerr := authorizeMicrosoftApplicationOnboarding(ctx, tc, &req, services)
	if aerr != nil {
		return orgAccessErrorJSON(c, aerr)
	}

	emails := dedupeEmailsPreservingOrder(appCtx.Emails)
	var selection []repo.AccessCheckSelectionItem
	for _, svc := range services {
		method, ok := microsoftOnboardingServiceToMethod[svc]
		if !ok || !outlook.IsAccessVerifyUserMethod(method) {
			continue
		}
		for _, email := range emails {
			u := appCtx.Users[strings.ToLower(email)]
			if strings.TrimSpace(u.ObjectID) == "" {
				continue
			}
			selection = append(selection, repo.AccessCheckSelectionItem{
				Email:   email,
				UserID:  u.ObjectID,
				Service: svc,
				Method:  method,
			})
		}
	}

	credID := uint(0)
	if tc.Credential != nil {
		credID = tc.Credential.ID
	}
	fingerprint := repo.AccessCheckSelectionFingerprint(tc.TenantID, credID, selection)

	if existing, ferr := database.CronJobRepo.FindOpenAccessCheckJob(userID, tc.TenantID, credID, fingerprint); ferr == nil && existing != nil {
		ensureSystemAccessCheckTask(database, existing.ID)
		payload := accessCheckStatusPayload(existing)
		payload["resumed"] = true
		return c.JSON(http.StatusOK, payload)
	}

	job, cerr := database.CronJobRepo.CreateSystemAccessCheckJob(userID, tc.TenantID, credID, selection, fingerprint)
	if cerr != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": cerr.Error()})
	}
	ensureSystemAccessCheckTask(database, job.ID)
	payload := accessCheckStatusPayload(job)
	payload["resumed"] = false
	return c.JSON(http.StatusOK, payload)
}

// HandleMicrosoftAccessVerifyStatus returns progress for a system access-check job.
func HandleMicrosoftAccessVerifyStatus(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}
	idStr := strings.TrimSpace(c.Param("id"))
	jobID64, perr := strconv.ParseUint(idStr, 10, 64)
	if perr != nil || jobID64 == 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "invalid system_job_id"})
	}

	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)
	job, gerr := database.CronJobRepo.GetCronJobByID(uint(jobID64))
	if gerr != nil || job == nil || job.UserID != userID || !repo.IsSystemJob(job) {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "system access-check job not found"})
	}
	return c.JSON(http.StatusOK, accessCheckStatusPayload(job))
}

func accessCheckStatusPayload(job *repo.CronJobListingDB) map[string]interface{} {
	st := repo.JobAccessCheck(job)
	results := []repo.AccessCheckResultItem{}
	summary := map[string]int{"accessible": 0, "no_access": 0, "probe_error": 0, "total": 0}
	phase := repo.AccessCheckPhaseProbing
	done := false
	jobsMaterialized := false
	materializeMode := ""
	startedAt := ""
	userCount := 0
	if st != nil {
		if st.Results != nil {
			results = st.Results
		}
		if st.Summary != nil {
			summary = st.Summary
		} else {
			summary = repo.AccessCheckSummaryFromResults(results)
		}
		phase = st.Phase
		done = phase == repo.AccessCheckPhaseCompleted
		jobsMaterialized = st.JobsMaterialized
		materializeMode = st.MaterializeMode
		startedAt = st.StartedAt
		seen := map[string]struct{}{}
		for _, s := range st.Selection {
			k := strings.ToLower(strings.TrimSpace(s.Email))
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
		}
		userCount = len(seen)
	}
	msg := ""
	if done {
		msg = accessCheckDoneMessage(summary)
	} else if len(results) > 0 {
		msg = "Access verification in progress."
	} else {
		msg = "Access verification started."
	}
	return map[string]interface{}{
		"success":           true,
		"system_job_id":     job.ID,
		"phase":             phase,
		"done":              done,
		"jobs_materialized": jobsMaterialized,
		"materialize_mode":  materializeMode,
		"results":           results,
		"summary":           summary,
		"user_count":        userCount,
		"started_at":        startedAt,
		"message":           msg,
		"tenant_id":         job.TenantID,
	}
}

func accessCheckDoneMessage(summary map[string]int) string {
	if summary["no_access"] > 0 && summary["accessible"] == 0 && summary["probe_error"] == 0 {
		return "None of the selected mailbox services are accessible. You can continue; unavailable jobs stay inactive until fixed and re-checked."
	}
	if summary["no_access"] > 0 {
		return "Some services are inaccessible. Review the results, then continue to create backup jobs."
	}
	if summary["probe_error"] > 0 {
		return "Some services could not be verified. Review the results, then continue."
	}
	return "Access looks good. Continue to create backup jobs."
}
