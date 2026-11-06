package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// ApplyMicrosoftAccessVerifyProbe probes one job and updates its access_verify state.
// Used by the access-verify worker and the admin Re-check API.
//
// Only verification-owned drafts/unavailable jobs are mutated. Already-active production jobs
// keep their existing message/logs and access_verify is not rewritten.
func ApplyMicrosoftAccessVerifyProbe(ctx context.Context, store *db.PostgresDb, token string, job *repo.CronJobListingDB, batchID uint) string {
	if job == nil || store == nil {
		return outlook.AccessProbeError
	}
	// Active jobs that are not access-verify drafts: leave alone (old logs stay).
	if !repo.IsAccessVerifyOwned(job) {
		if job.Active {
			return outlook.AccessProbeAccessible
		}
		return outlook.AccessProbeError
	}

	probe := outlook.ProbeMicrosoftUserServiceAccess(ctx, token, job.ResourceID, job.Method)
	switch probe.Status {
	case outlook.AccessProbeAccessible:
		if m := repo.JobAccessVerify(job); m != nil && m.State == repo.AccessVerifyStateUnavailable {
			_ = store.MicrosoftResourceRepo.RemoveUnavailableService(job.TenantID, job.ResourceType, job.ResourceID, job.Method)
		}
		_ = store.CronJobRepo.UpdateCronJobByID(job.ID, repo.ClearJobAccessVerifyForPromote(job))
		return outlook.AccessProbeAccessible
	case outlook.AccessProbeNoAccess:
		reason := probe.ReasonCode
		if reason == "" {
			reason = outlook.AccessReasonUnavailable
		}
		_ = store.CronJobRepo.UpdateCronJobByID(job.ID, repo.SetJobAccessVerifyUnavailable(job, reason, probe.Message, batchID))
		_ = store.MicrosoftResourceRepo.AddUnavailableService(job.TenantID, job.ResourceType, job.ResourceID, job.Method)
		return outlook.AccessProbeNoAccess
	default:
		msg := probe.Message
		if msg == "" {
			msg = "Checking access in the background."
		}
		_ = store.CronJobRepo.UpdateCronJobByID(job.ID, repo.SetJobAccessVerifyPending(job, batchID, msg))
		return outlook.AccessProbeError
	}
}

// NotifyMicrosoftAccessVerifyOwner sends a Satellite notification after meaningful verify transitions.
func NotifyMicrosoftAccessVerifyOwner(ctx context.Context, userID, tenantID string, promoted, unavailable int) bool {
	if strings.TrimSpace(userID) == "" {
		return false
	}
	priority := "normal"
	title := "Microsoft access check finished"
	body := fmt.Sprintf("Access verification finished for tenant %s: %d ready, %d unavailable.", tenantID, promoted, unavailable)
	if unavailable == 0 {
		body = fmt.Sprintf("Access verification finished for tenant %s: %d services ready for backup.", tenantID, promoted)
	}
	data := map[string]interface{}{
		"event":       "microsoft_access_verify_completed",
		"tenant_id":   tenantID,
		"promoted":    promoted,
		"unavailable": unavailable,
		"type":        "microsoft_access_verify",
	}
	satellite.SendNotificationAsync(ctx, userID, title, body, &priority, data, nil)
	return true
}

type microsoftAccessRecheckRequest struct {
	JobIDs         []uint `json:"job_ids"`
	TenantID       string `json:"tenant_id"`
	AllUnavailable bool   `json:"all_unavailable"`
	IncludePending bool   `json:"include_pending"`
}

// HandleMicrosoftAccessVerifyRecheck re-probes selected or unavailable access-verify jobs.
// Small sets run sync; large sets enqueue a durable batch. Never creates duplicate jobs.
func HandleMicrosoftAccessVerifyRecheck(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	userID, err := satelliteUserIDFromRequest(c)
	if err != nil {
		return err
	}
	database := c.Get(middleware.DbContextKey).(*db.PostgresDb)

	var req microsoftAccessRecheckRequest
	if err := c.Bind(&req); err != nil {
		return jsonError(http.StatusBadRequest, "Invalid Request", err)
	}
	req.TenantID = strings.ToLower(strings.TrimSpace(req.TenantID))
	if req.TenantID == "" {
		req.TenantID = strings.ToLower(strings.TrimSpace(c.Request().Header.Get(headerMicrosoftTenantID)))
	}

	jobIDs := uniqueUints(req.JobIDs)
	var jobs []repo.CronJobListingDB
	if len(jobIDs) > 0 {
		rows, lerr := database.CronJobRepo.ListMicrosoftJobsByIDs(userID, jobIDs)
		if lerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": lerr.Error()})
		}
		jobs = rows
	} else if req.AllUnavailable {
		rows, lerr := database.CronJobRepo.ListMicrosoftAccessVerifyUnavailableJobs(userID, req.TenantID)
		if lerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": lerr.Error()})
		}
		jobs = rows
		if req.IncludePending {
			pending, perr := database.CronJobRepo.ListMicrosoftAccessVerifyPendingJobs(userID, req.TenantID)
			if perr != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": perr.Error()})
			}
			jobs = append(jobs, pending...)
		}
	} else {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "job_ids or all_unavailable is required"})
	}

	filtered := make([]repo.CronJobListingDB, 0, len(jobs))
	seen := map[uint]struct{}{}
	for i := range jobs {
		j := jobs[i]
		if _, ok := seen[j.ID]; ok {
			continue
		}
		seen[j.ID] = struct{}{}
		if !outlook.IsAccessVerifyUserMethod(j.Method) {
			continue
		}
		if req.TenantID != "" && !strings.EqualFold(j.TenantID, req.TenantID) {
			continue
		}
		m := repo.JobAccessVerify(&j)
		if m == nil || (m.State != repo.AccessVerifyStatePending && m.State != repo.AccessVerifyStateUnavailable) {
			continue
		}
		filtered = append(filtered, j)
	}
	if len(filtered) == 0 {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "No jobs needed access re-check",
			"results": []interface{}{},
		})
	}

	credID := repo.JobCredentialID(&filtered[0])
	tenantID := strings.ToLower(strings.TrimSpace(filtered[0].TenantID))
	if req.TenantID != "" {
		tenantID = req.TenantID
	}
	tc, rerr := mstenant.Resolve(ctx, database, mstenant.Request{
		UserID: userID, CredentialID: credID, TenantID: tenantID, RequireApplication: true,
	})
	if rerr != nil {
		return microsoftErrorJSON(c, rerr)
	}
	token := strings.TrimSpace(tc.Token)
	if token == "" {
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": "empty app-only token"})
	}

	ids := make([]uint, 0, len(filtered))
	for i := range filtered {
		ids = append(ids, filtered[i].ID)
	}

	const recheckSyncLimit = 25
	if len(filtered) > recheckSyncLimit {
		for i := range filtered {
			_ = database.CronJobRepo.UpdateCronJobByID(filtered[i].ID, repo.SetJobAccessVerifyPending(&filtered[i], 0, "Re-checking access in the background."))
			ensureSystemAccessCheckTask(database, filtered[i].ID)
		}
		_ = ids
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"verification": map[string]interface{}{
				"status": "queued",
				"mode":   "async",
				"count":  len(filtered),
			},
		})
	}

	results := make([]map[string]interface{}, 0, len(filtered))
	promoted, unavailable, errors := 0, 0, 0
	for i := range filtered {
		status := ApplyMicrosoftAccessVerifyProbe(ctx, database, token, &filtered[i], 0)
		entry := map[string]interface{}{
			"job_id":  filtered[i].ID,
			"email":   filtered[i].Name,
			"method":  filtered[i].Method,
			"status":  status,
		}
		results = append(results, entry)
		switch status {
		case outlook.AccessProbeAccessible:
			promoted++
		case outlook.AccessProbeNoAccess:
			unavailable++
		default:
			errors++
		}
	}
	if promoted > 0 || unavailable > 0 {
		NotifyMicrosoftAccessVerifyOwner(ctx, userID, tenantID, promoted, unavailable)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"verification": map[string]interface{}{
			"status":      "completed",
			"mode":        "sync",
			"promoted":    promoted,
			"unavailable": unavailable,
			"probe_error": errors,
		},
		"results": results,
	})
}
