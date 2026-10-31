package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

// validateAccessCheckOwnership ensures the system access-check job belongs to this
// create request (user already enforced by lock): tenant, credential, selection fingerprint.
func validateAccessCheckOwnership(
	st *repo.AccessCheckState,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
	createServices []string,
	users map[string]outlook.DirectoryUser,
) error {
	if st == nil {
		return fmt.Errorf("system job missing access_check state")
	}
	wantTenant := strings.ToLower(strings.TrimSpace(tenantID))
	haveTenant := strings.ToLower(strings.TrimSpace(st.TenantID))
	if wantTenant != "" && haveTenant != "" && wantTenant != haveTenant {
		return fmt.Errorf("system_job_id does not match tenant")
	}
	credID := uint(0)
	if cred != nil {
		credID = cred.ID
	}
	if st.CredentialID != 0 && credID != 0 && st.CredentialID != credID {
		return fmt.Errorf("system_job_id does not match credential")
	}
	storedFP := strings.TrimSpace(st.SelectionFingerprint)
	if storedFP != "" {
		rebuilt := repo.AccessCheckSelectionFingerprint(haveTenant, st.CredentialID, st.Selection)
		if rebuilt != storedFP {
			return fmt.Errorf("system access-check selection is corrupted")
		}
	}
	if len(createServices) == 0 || len(users) == 0 {
		return nil
	}
	createSel := accessCheckSelectionFromCreate(haveTenant, createServices, users)
	if len(createSel) == 0 {
		return nil
	}
	fpCred := st.CredentialID
	if fpCred == 0 {
		fpCred = credID
	}
	fpTenant := haveTenant
	if fpTenant == "" {
		fpTenant = wantTenant
	}
	createFP := repo.AccessCheckSelectionFingerprint(fpTenant, fpCred, createSel)
	if storedFP != "" && createFP != storedFP {
		return fmt.Errorf("system_job_id does not match access-check selection")
	}
	return nil
}

func accessCheckSelectionFromCreate(
	_ string,
	services []string,
	users map[string]outlook.DirectoryUser,
) []repo.AccessCheckSelectionItem {
	var selection []repo.AccessCheckSelectionItem
	for _, svc := range services {
		method := microsoftOnboardingServiceToMethod[svc]
		if !outlook.IsAccessVerifyUserMethod(method) {
			continue
		}
		for _, u := range users {
			email := strings.TrimSpace(u.Mail)
			if email == "" {
				email = strings.TrimSpace(u.UPN)
			}
			if email == "" || strings.TrimSpace(u.ObjectID) == "" {
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
	return selection
}

type accessCheckMaterializeResult struct {
	Mode        string
	Jobs        []onboardingJobResult
	Failed      []onboardingFailedResult
	Skipped     []onboardingSkippedResult
	Unverified  []onboardingUnverifiedResult
	AlreadyDone bool
}

// materializeAccessCheckJobs creates backup jobs from a system access-check job under row lock.
// If phase=completed → final states from results; else → all drafts linked for worker updates.
// Idempotent when jobs_materialized is already true.
// createServices is the create request's service list; used to verify selection fingerprint ownership.
func materializeAccessCheckJobs(
	c echo.Context,
	ctx context.Context,
	database *db.PostgresDb,
	userID string,
	systemJobID uint,
	syncType string,
	schedule onboardingSchedule,
	gReq *GoogleBackupOnboardingRequest,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	users map[string]outlook.DirectoryUser,
	createServices []string,
) (*accessCheckMaterializeResult, error) {
	_ = ctx
	if database == nil || systemJobID == 0 {
		return nil, fmt.Errorf("system_job_id is required")
	}

	tx := database.CronJobRepo.BeginTx()
	if tx.Error != nil {
		return nil, tx.Error
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	sysJob, err := database.CronJobRepo.LockSystemAccessCheckJob(tx, systemJobID, userID)
	if err != nil {
		return nil, fmt.Errorf("lock system access-check job: %w", err)
	}
	st := repo.JobAccessCheck(sysJob)
	if st == nil {
		return nil, fmt.Errorf("system job missing access_check state")
	}
	if err := validateAccessCheckOwnership(st, cred, tenantID, createServices, users); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tenantID) == "" {
		tenantID = st.TenantID
	}

	out := &accessCheckMaterializeResult{}

	if st.JobsMaterialized {
		out.AlreadyDone = true
		out.Mode = st.MaterializeMode
		_ = tx.Rollback()
		committed = true
		// Reload linked jobs for response outside the lock.
		for _, id := range st.LinkedJobIDs {
			if j, _ := database.CronJobRepo.GetCronJobByID(id); j != nil {
				svc := serviceNameFromMethod(j.Method)
				out.Jobs = append(out.Jobs, onboardingJobResult{
					Service: svc, Email: j.Name, JobID: j.ID, Active: j.Active, Existing: true,
				})
			}
		}
		return out, nil
	}

	finalMode := st.Phase == repo.AccessCheckPhaseCompleted
	if finalMode {
		out.Mode = repo.AccessCheckMaterializeFinal
	} else {
		out.Mode = repo.AccessCheckMaterializeDraft
	}

	// Commit the lock early only after we set the flag? Better: create jobs outside long lock
	// but set flag under lock first with empty linked, then fill — race with worker is OK if
	// LinkedByKey is written before commit.
	//
	// Strategy: build jobs using the normal DB (not tx) AFTER marking intent... 
	// Safer: create jobs with main DB while holding lock is bad for long ops.
	// Use: set jobs_materialized under lock with mode, commit, then create jobs, then update links.
	// Worker may probe between — without LinkedByKey it only writes results; after links it updates.
	//
	// Race with second Continue: flag set under lock first.

	st.JobsMaterialized = true
	st.MaterializeMode = out.Mode
	if st.LinkedByKey == nil {
		st.LinkedByKey = map[string]uint{}
	}
	patch := repo.SetJobAccessCheck(sysJob, st)
	if err := tx.Model(&repo.CronJobListingDB{}).Where("id = ?", sysJob.ID).Updates(patch).Error; err != nil {
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	committed = true

	// Create backup jobs (idempotent via unique identity).
	resultByKey := map[string]repo.AccessCheckResultItem{}
	for _, r := range st.Results {
		resultByKey[repo.AccessCheckItemKey(r.Email, r.Service)] = r
	}

	linkedIDs := make([]uint, 0, len(st.Selection))
	linkedByKey := map[string]uint{}

	for _, item := range st.Selection {
		svc := item.Service
		method := item.Method
		if method == "" {
			method = microsoftOnboardingServiceToMethod[svc]
		}
		u, ok := users[strings.ToLower(item.Email)]
		if !ok || strings.TrimSpace(u.ObjectID) == "" {
			u = outlook.DirectoryUser{ObjectID: item.UserID, Mail: item.Email, UPN: item.Email}
		}
		if strings.TrimSpace(u.ObjectID) == "" {
			out.Failed = append(out.Failed, onboardingFailedResult{Service: svc, Email: item.Email, Error: "user object ID is unknown"})
			continue
		}

		asDraft := !finalMode
		if finalMode {
			if r, ok := resultByKey[repo.AccessCheckItemKey(item.Email, svc)]; ok {
				switch r.Status {
				case outlook.AccessProbeAccessible:
					asDraft = false
				default:
					asDraft = true
				}
			} else {
				asDraft = true
			}
		}

		job, fail := createOneMicrosoftUserJob(c, database, userID, svc, item.Email, method, syncType,
			schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, u, asDraft)
		if fail != nil {
			out.Failed = append(out.Failed, *fail)
			continue
		}
		if job == nil {
			continue
		}
		out.Jobs = append(out.Jobs, *job)
		linkedIDs = append(linkedIDs, job.JobID)
		key := repo.AccessCheckItemKey(item.Email, svc)
		linkedByKey[key] = job.JobID

		latest, _ := database.CronJobRepo.GetCronJobByID(job.JobID)
		if latest == nil {
			continue
		}
		r, has := resultByKey[key]
		if finalMode {
			if !has {
				out.Unverified = append(out.Unverified, onboardingUnverifiedResult{
					Service: svc, Email: item.Email, JobID: job.JobID,
					Message: "Access could not be verified",
				})
				continue
			}
			switch r.Status {
			case outlook.AccessProbeAccessible:
				if repo.IsAccessVerifyOwned(latest) {
					_ = database.CronJobRepo.UpdateCronJobByID(job.JobID, repo.ClearJobAccessVerifyForPromote(latest))
					job.Active = true
				}
			case outlook.AccessProbeNoAccess:
				reason := r.ReasonCode
				if reason == "" {
					reason = outlook.AccessReasonUnavailable
				}
				_ = database.CronJobRepo.UpdateCronJobByID(job.JobID, repo.SetJobAccessVerifyUnavailable(latest, reason, r.Message, 0))
				_ = database.MicrosoftResourceRepo.AddUnavailableService(tenantID, repo.ResourceTypeUser, u.ObjectID, method)
				out.Skipped = append(out.Skipped, onboardingSkippedResult{
					Service: svc, Email: item.Email, ReasonCode: reason, Error: r.Message,
				})
				job.Active = false
			default:
				_ = database.CronJobRepo.UpdateCronJobByID(job.JobID, repo.SetJobAccessVerifyPending(latest, 0, r.Message))
				out.Unverified = append(out.Unverified, onboardingUnverifiedResult{
					Service: svc, Email: item.Email, JobID: job.JobID, Message: r.Message,
				})
				job.Active = false
			}
			continue
		}
		// Draft mode: apply any results already known so Continue-while-running stays current.
		if has {
			switch r.Status {
			case outlook.AccessProbeAccessible:
				_ = database.CronJobRepo.UpdateCronJobByID(job.JobID, repo.ClearJobAccessVerifyForPromote(latest))
				job.Active = true
			case outlook.AccessProbeNoAccess:
				reason := r.ReasonCode
				if reason == "" {
					reason = outlook.AccessReasonUnavailable
				}
				_ = database.CronJobRepo.UpdateCronJobByID(job.JobID, repo.SetJobAccessVerifyUnavailable(latest, reason, r.Message, 0))
				_ = database.MicrosoftResourceRepo.AddUnavailableService(tenantID, repo.ResourceTypeUser, u.ObjectID, method)
				out.Skipped = append(out.Skipped, onboardingSkippedResult{
					Service: svc, Email: item.Email, ReasonCode: reason, Error: r.Message,
				})
				job.Active = false
			default:
				out.Unverified = append(out.Unverified, onboardingUnverifiedResult{
					Service: svc, Email: item.Email, JobID: job.JobID, Message: r.Message,
				})
			}
		} else {
			out.Unverified = append(out.Unverified, onboardingUnverifiedResult{
				Service: svc, Email: item.Email, JobID: job.JobID,
				Message: "Checking access in the background.",
			})
		}
	}

	// Persist links on system job.
	sysLatest, _ := database.CronJobRepo.GetCronJobByID(systemJobID)
	if sysLatest != nil {
		st2 := repo.JobAccessCheck(sysLatest)
		if st2 == nil {
			st2 = st
		}
		st2.JobsMaterialized = true
		st2.MaterializeMode = out.Mode
		st2.LinkedJobIDs = linkedIDs
		st2.LinkedByKey = linkedByKey
		// Stamp job ids onto results where present.
		for i := range st2.Results {
			k := repo.AccessCheckItemKey(st2.Results[i].Email, st2.Results[i].Service)
			if id, ok := linkedByKey[k]; ok {
				st2.Results[i].JobID = id
			}
		}
		_ = database.CronJobRepo.PersistAccessCheckState(systemJobID, st2, "", "", "")
	}

	return out, nil
}

func serviceNameFromMethod(method string) string {
	switch strings.TrimSpace(method) {
	case "outlook":
		return "outlook"
	case "outlook_calendar":
		return "calendar"
	case "outlook_contacts":
		return "contacts"
	case "outlook_onedrive":
		return "onedrive"
	case "outlook_sharepoint":
		return "sharepoint"
	case "outlook_teams":
		return "teams"
	default:
		return method
	}
}
