package handler

import (
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

type onboardingSkippedResult struct {
	Service    string `json:"service,omitempty"`
	Email      string `json:"email,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	Error      string `json:"error"`
}

type onboardingUnverifiedResult struct {
	Service string `json:"service,omitempty"`
	Email   string `json:"email,omitempty"`
	JobID   uint   `json:"job_id,omitempty"`
	Message string `json:"message"`
}

type microsoftUserJobCreateResult struct {
	Jobs    []onboardingJobResult
	Failed  []onboardingFailedResult
	Skipped []onboardingSkippedResult
}

func createOneMicrosoftUserJob(
	c echo.Context, database *db.PostgresDb, userID, svc, targetEmail, method, syncType string,
	schedule onboardingSchedule, gReq *GoogleBackupOnboardingRequest, cred *repo.GoogleBackupCredentialDB,
	tenantID string, isFirstConnection bool, policyBatch *onboardingPolicyBatch,
	u outlook.DirectoryUser, asDraft bool,
) (*onboardingJobResult, *onboardingFailedResult) {
	if _, err := database.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
		TenantID: tenantID, ResourceType: repo.ResourceTypeUser, ExternalID: u.ObjectID,
		DisplayName: u.DisplayName, Mail: targetEmail, UPN: u.UPN,
	}); err != nil {
		return nil, &onboardingFailedResult{Service: svc, Email: targetEmail, Error: err.Error()}
	}
	identity := repo.MicrosoftJobIdentity{TenantID: tenantID, ResourceType: repo.ResourceTypeUser, ResourceID: u.ObjectID}
	job, fail := onboardMicrosoftJob(c, database, userID, svc, targetEmail, method, syncType, nil,
		identity, schedule, gReq, cred, isFirstConnection, policyBatch)
	if fail != nil || job == nil {
		return job, fail
	}
	// Draft marker only on newly created jobs. Existing/active jobs keep their message and
	// access_verify untouched (no rewrite of production logs).
	if asDraft && !job.Existing {
		latest, _ := database.CronJobRepo.GetCronJobByID(job.JobID)
		if latest != nil {
			patch := repo.SetJobAccessVerifyPending(latest, 0, "Checking access in the background.")
			_ = database.CronJobRepo.UpdateCronJobByID(job.JobID, patch)
			job.Active = false
		}
	}
	return job, nil
}

func ensureSystemAccessCheckTask(database *db.PostgresDb, jobID uint) {
	if database == nil || database.TaskRepo == nil || jobID == 0 {
		return
	}
	_, _ = database.TaskRepo.CreateSystemAccessCheckTaskForCronJob(jobID)
}

// createMicrosoftUserJobsPlain creates mailbox jobs without access-verify drafts (delegated / no system job).
func createMicrosoftUserJobsPlain(
	c echo.Context,
	userID, method, svc, syncType string,
	schedule onboardingSchedule,
	gReq *GoogleBackupOnboardingRequest,
	cred *repo.GoogleBackupCredentialDB,
	tenantID string,
	isFirstConnection bool,
	policyBatch *onboardingPolicyBatch,
	emails []string,
	users map[string]outlook.DirectoryUser,
	database *db.PostgresDb,
) microsoftUserJobCreateResult {
	emails = dedupeEmailsPreservingOrder(emails)
	out := microsoftUserJobCreateResult{}
	for _, targetEmail := range emails {
		u, ok := users[strings.ToLower(targetEmail)]
		if !ok || strings.TrimSpace(u.ObjectID) == "" {
			out.Failed = append(out.Failed, onboardingFailedResult{Service: svc, Email: targetEmail, Error: "user object ID is unknown in the tenant directory"})
			continue
		}
		job, fail := createOneMicrosoftUserJob(c, database, userID, svc, targetEmail, method, syncType,
			schedule, gReq, cred, tenantID, isFirstConnection, policyBatch, u, false)
		if fail != nil {
			out.Failed = append(out.Failed, *fail)
			continue
		}
		if job != nil {
			out.Jobs = append(out.Jobs, *job)
		}
	}
	return out
}
