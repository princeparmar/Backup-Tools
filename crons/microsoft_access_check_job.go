package crons

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// runMicrosoftSystemAccessCheckJob probes all selection items on a system access-check job.
// Never creates backup jobs; only updates results and, when drafts are linked, those drafts.
func (a *AutosyncManager) runMicrosoftSystemAccessCheckJob(ctx context.Context, task *repo.TaskListingDB, job *repo.CronJobListingDB) error {
	if a == nil || a.store == nil || job == nil {
		return fmt.Errorf("system access check: missing store or job")
	}
	st := repo.JobAccessCheck(job)
	if st == nil {
		return fmt.Errorf("system access check: missing access_check state")
	}
	if st.Phase == repo.AccessCheckPhaseCompleted && len(st.Results) >= len(st.Selection) {
		if task != nil {
			task.Message = "System: Access check completed"
		}
		return nil
	}

	credID := st.CredentialID
	if credID == 0 {
		credID = repo.JobCredentialID(job)
	}
	tenantID := strings.ToLower(strings.TrimSpace(st.TenantID))
	if tenantID == "" {
		tenantID = strings.ToLower(strings.TrimSpace(job.TenantID))
	}
	if credID == 0 || tenantID == "" {
		return fmt.Errorf("system access check: missing credential or tenant")
	}

	tc, err := mstenant.Resolve(ctx, a.store, mstenant.Request{
		UserID:             job.UserID,
		CredentialID:       credID,
		TenantID:           tenantID,
		RequireApplication: true,
	})
	if err != nil {
		return fmt.Errorf("system access check: resolve token: %w", err)
	}
	token := strings.TrimSpace(tc.Token)
	if token == "" {
		return fmt.Errorf("system access check: empty app-only token")
	}

	doneKeys := map[string]repo.AccessCheckResultItem{}
	for _, r := range st.Results {
		doneKeys[repo.AccessCheckItemKey(r.Email, r.Service)] = r
	}

	promoted, unavailable := 0, 0
	results := make([]repo.AccessCheckResultItem, 0, len(st.Selection))
	// Keep prior results order for already-done items, then continue remaining.
	for _, r := range st.Results {
		results = append(results, r)
	}

	for _, item := range st.Selection {
		key := repo.AccessCheckItemKey(item.Email, item.Service)
		if prev, ok := doneKeys[key]; ok && strings.TrimSpace(prev.Status) != "" && prev.Status != outlook.AccessProbeError {
			continue
		}
		// Retry probe_error / missing.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if task != nil && a.store.TaskRepo != nil {
			_ = a.store.TaskRepo.UpdateHeartBeatForTask(task.ID)
		}

		method := strings.TrimSpace(item.Method)
		if method == "" {
			method = item.Service
		}
		probe := outlook.ProbeMicrosoftUserServiceAccess(ctx, token, item.UserID, method)
		msg := probe.Message
		if msg == "" {
			switch probe.Status {
			case outlook.AccessProbeAccessible:
				msg = "Ready for backup"
			case outlook.AccessProbeNoAccess:
				msg = "Access unavailable"
			default:
				msg = "Access could not be verified"
			}
		}
		row := repo.AccessCheckResultItem{
			Email: item.Email, Service: item.Service, Method: method,
			Status: probe.Status, ReasonCode: probe.ReasonCode, Message: msg,
		}

		// Replace prior probe_error for same key, else append.
		replaced := false
		for i := range results {
			if repo.AccessCheckItemKey(results[i].Email, results[i].Service) == key {
				results[i] = row
				replaced = true
				break
			}
		}
		if !replaced {
			results = append(results, row)
		}
		doneKeys[key] = row

		// Reload latest materialize state (Continue may link drafts mid-run).
		latest, _ := a.store.CronJobRepo.GetCronJobByID(job.ID)
		if latest != nil {
			if lst := repo.JobAccessCheck(latest); lst != nil {
				st.JobsMaterialized = lst.JobsMaterialized
				st.MaterializeMode = lst.MaterializeMode
				st.LinkedByKey = lst.LinkedByKey
				st.LinkedJobIDs = lst.LinkedJobIDs
			}
		}

		if st.JobsMaterialized && st.MaterializeMode == repo.AccessCheckMaterializeDraft && st.LinkedByKey != nil {
			if jobID, ok := st.LinkedByKey[key]; ok && jobID > 0 {
				draft, gerr := a.store.CronJobRepo.GetCronJobByID(jobID)
				if gerr == nil && draft != nil {
					status := handler.ApplyMicrosoftAccessVerifyProbe(ctx, a.store, token, draft, 0)
					row.JobID = jobID
					// Update results entry with job id
					for i := range results {
						if repo.AccessCheckItemKey(results[i].Email, results[i].Service) == key {
							results[i].JobID = jobID
							break
						}
					}
					switch status {
					case outlook.AccessProbeAccessible:
						promoted++
					case outlook.AccessProbeNoAccess:
						unavailable++
					}
				}
			}
		}

		st.Results = results
		st.Summary = repo.AccessCheckSummaryFromResults(results)
		_ = a.store.CronJobRepo.PersistAccessCheckState(job.ID, st, repo.SystemAccessCheckTaskMessage, repo.JobMessageStatusInfo, repo.JobStatusInProgress)
	}

	st.Results = results
	st.Summary = repo.AccessCheckSummaryFromResults(results)
	st.Phase = repo.AccessCheckPhaseCompleted
	_ = a.store.CronJobRepo.PersistAccessCheckState(job.ID, st, "System: Access check completed", repo.JobMessageStatusInfo, repo.JobStatusSuccess)
	if task != nil {
		task.Message = "System: Access check completed"
	}

	if st.JobsMaterialized && (promoted > 0 || unavailable > 0) {
		handler.NotifyMicrosoftAccessVerifyOwner(ctx, job.UserID, tenantID, promoted, unavailable)
	}

	logger.Info(ctx, "system access-check completed",
		logger.Int("job_id", int(job.ID)),
		logger.Int("results", len(results)),
		logger.Int("promoted", promoted),
		logger.Int("unavailable", unavailable),
	)
	return nil
}
