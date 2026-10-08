package crons

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
)

// Seams (overridden in tests).
var (
	msReconcileResolveFn   = mstenant.Resolve
	msReconcileListUsersFn = outlook.ListTenantDirectoryUsers
	msObjectStateFn        = outlook.DirectoryObjectState
)

// Job messages written by the lifecycle check. Jobs paused with msgPausedDeleted are resumed
// when the object is restored.
const (
	msgPausedDeleted = "Paused: the Microsoft object was deleted; backups are kept"
	msgPausedGone    = "Paused: the Microsoft object no longer exists; backups are kept"
)

var scopeCapability = map[string]string{
	repo.ResourceTypeSite:  outlook.CapabilitySharePoint,
	repo.ResourceTypeTeam:  outlook.CapabilityTeamsChannel,
	repo.ResourceTypeGroup: outlook.CapabilityGroups,
}

// reconcileMicrosoftScopes runs every active backup scope: the lifecycle check for its existing
// jobs and, for selection_mode=all, jobs for new users. Failures are recorded on the scope.
func (a *AutosyncManager) reconcileMicrosoftScopes(ctx context.Context) {
	if a.store.MicrosoftScopeRepo == nil {
		return
	}
	scopes, err := a.store.MicrosoftScopeRepo.ListActive()
	if err != nil {
		logger.Warn(ctx, "list microsoft backup scopes", logger.ErrorField(err))
		return
	}
	for _, scope := range scopes {
		created, err := reconcileMicrosoftScope(ctx, a.store, scope)
		msg := ""
		if err != nil {
			msg = err.Error()
			logger.Warn(ctx, "reconcile microsoft backup scope",
				logger.Int("scope_id", int(scope.ID)), logger.String("tenant_id", scope.TenantID), logger.ErrorField(err))
		} else if created > 0 {
			logger.Info(ctx, "reconcile created microsoft jobs",
				logger.Int("scope_id", int(scope.ID)), logger.Int("created", created))
		}
		_ = a.store.MicrosoftScopeRepo.SetReconcileResult(scope.ID, msg)
	}
}

// reconcileMicrosoftScope returns the number of jobs created. A disconnected tenant, missing
// consent or a missing capability fails before anything is read or created.
func reconcileMicrosoftScope(ctx context.Context, store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB) (int, error) {
	tc, err := msReconcileResolveFn(ctx, store, mstenant.Request{
		UserID: scope.UserID, CredentialID: scope.CredentialID, TenantID: scope.TenantID,
		Capability: scopeCapability[scope.ResourceType], RequireApplication: true,
	})
	if err != nil {
		return 0, err
	}
	jobs, err := scopeJobs(store, scope)
	if err != nil {
		return 0, err
	}
	if scope.ResourceType != repo.ResourceTypeUser {
		applyMicrosoftLifecycle(ctx, store, tc, scope.ResourceType, jobs, nil)
		return 0, nil
	}
	users, err := msReconcileListUsersFn(ctx, tc.Token)
	if err != nil {
		return 0, fmt.Errorf("list tenant users: %w", err)
	}
	applyMicrosoftLifecycle(ctx, store, tc, repo.ResourceTypeUser, jobs, users)
	if scope.SelectionMode != repo.MicrosoftScopeAll {
		return 0, nil
	}
	return addMicrosoftScopeUserJobs(store, scope, users, jobs)
}

// scopeJobs returns the scope credential's jobs of the scope's resource type and sync type.
func scopeJobs(store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB) ([]repo.CronJobListingDB, error) {
	rows, err := store.CronJobRepo.ListMicrosoftScopeJobs(scope.UserID, scope.TenantID, scope.ResourceType, scope.SyncType)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, j := range rows {
		if repo.JobCredentialID(&j) == scope.CredentialID {
			out = append(out, j)
		}
	}
	return out, nil
}

// addMicrosoftScopeUserJobs creates, for every enabled user without one, a job per method already
// in the scope. Each new job copies the policy, interval, active state and settings of a sibling
// job (same org unit first).
func addMicrosoftScopeUserJobs(store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB, users []outlook.DirectoryUser, jobs []repo.CronJobListingDB) (int, error) {
	var methods []string
	templates := map[string][]repo.CronJobListingDB{}
	have := map[string]bool{}
	for _, j := range jobs {
		if _, ok := templates[j.Method]; !ok {
			methods = append(methods, j.Method)
		}
		templates[j.Method] = append(templates[j.Method], j)
		have[j.Method+"|"+j.ResourceID] = true
	}
	created := 0
	var firstErr error
	for _, u := range users {
		email := strings.TrimSpace(u.Email())
		if !u.AccountEnabled || strings.TrimSpace(u.ObjectID) == "" || email == "" {
			continue
		}
		upserted := false
		for _, method := range methods {
			if have[method+"|"+u.ObjectID] {
				continue
			}
			if !upserted {
				if _, err := store.MicrosoftResourceRepo.Upsert(repo.MicrosoftResourceDB{
					TenantID: scope.TenantID, ResourceType: repo.ResourceTypeUser, ExternalID: u.ObjectID,
					DisplayName: u.DisplayName, Mail: email, UPN: u.UPN,
				}); err != nil {
					return created, err
				}
				upserted = true
			}
			if err := cloneScopeJob(store, scope, pickScopeTemplate(templates[method], u.OrgUnitPath(), scope.PolicyID), u, email); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("create %s job for %s: %w", method, email, err)
				}
				continue
			}
			have[method+"|"+u.ObjectID] = true
			created++
		}
	}
	return created, firstErr
}

func cloneScopeJob(store *db.PostgresDb, scope repo.MicrosoftBackupScopeDB, tmpl repo.CronJobListingDB, u outlook.DirectoryUser, email string) error {
	input := map[string]interface{}{}
	if tmpl.InputData != nil && tmpl.InputData.Json() != nil {
		for k, v := range *tmpl.InputData.Json() {
			input[k] = v
		}
	}
	input["email"] = email
	if _, ok := input["org_unit_path"]; ok {
		input["org_unit_path"] = u.OrgUnitPath()
	}
	job, err := store.CronJobRepo.CreateMicrosoftResourceJob(scope.UserID, email, tmpl.Method, scope.SyncType, scope.CredentialID,
		repo.MicrosoftJobIdentity{TenantID: scope.TenantID, ResourceType: repo.ResourceTypeUser, ResourceID: u.ObjectID}, input)
	if err != nil {
		return err
	}
	return store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"interval":    tmpl.Interval,
		"policy_id":   tmpl.PolicyID,
		"active":      tmpl.Active,
		"storx_token": tmpl.StorxToken,
	})
}

// pickScopeTemplate prefers an active sibling in the same org unit, then an active sibling on the
// scope policy, then any active sibling, then the first.
func pickScopeTemplate(jobs []repo.CronJobListingDB, orgUnit string, policyID uint) repo.CronJobListingDB {
	orgUnitOf := func(j repo.CronJobListingDB) string {
		if j.InputData == nil || j.InputData.Json() == nil {
			return ""
		}
		s, _ := (*j.InputData.Json())["org_unit_path"].(string)
		return s
	}
	for _, match := range []func(repo.CronJobListingDB) bool{
		func(j repo.CronJobListingDB) bool { return j.Active && orgUnitOf(j) != "" && orgUnitOf(j) == orgUnit },
		func(j repo.CronJobListingDB) bool { return j.Active && policyID > 0 && j.PolicyID == policyID },
		func(j repo.CronJobListingDB) bool { return j.Active },
	} {
		for _, j := range jobs {
			if match(j) {
				return j
			}
		}
	}
	return jobs[0]
}

// applyMicrosoftLifecycle updates the resource state of each job's user or group. listed holds the
// live directory users (nil = not listed); objects missing from it, and groups, are read one by one.
// Deleted objects pause their jobs with a warning; a restored object resumes the jobs this check
// paused. Sites and teams are checked by their job runs instead.
func applyMicrosoftLifecycle(ctx context.Context, store *db.PostgresDb, tc *mstenant.Context, resourceType string, jobs []repo.CronJobListingDB, listed []outlook.DirectoryUser) {
	kind := outlook.DirectoryKindUser
	switch resourceType {
	case repo.ResourceTypeUser:
	case repo.ResourceTypeGroup:
		kind = outlook.DirectoryKindGroup
	default:
		return
	}
	live := make(map[string]outlook.DirectoryUser, len(listed))
	for _, u := range listed {
		live[u.ObjectID] = u
	}
	byResource := map[string][]repo.CronJobListingDB{}
	var order []string
	for _, j := range jobs {
		if _, ok := byResource[j.ResourceID]; !ok {
			order = append(order, j.ResourceID)
		}
		byResource[j.ResourceID] = append(byResource[j.ResourceID], j)
	}
	for _, rid := range order {
		state := ""
		if u, ok := live[rid]; ok {
			state = outlook.ObjectStateActive
			if !u.AccountEnabled {
				state = outlook.ObjectStateDisabled
			}
		} else {
			s, err := msObjectStateFn(ctx, tc.Token, kind, rid)
			if err != nil {
				logger.Warn(ctx, "microsoft lifecycle check", logger.String("resource_id", rid), logger.ErrorField(err))
				continue
			}
			state = s
		}
		ApplyMicrosoftObjectState(store, tc.TenantID, resourceType, rid, state, byResource[rid])
	}
}

// ApplyMicrosoftObjectState records an object's lifecycle state and pauses or resumes its jobs.
func ApplyMicrosoftObjectState(store *db.PostgresDb, tenantID, resourceType, resourceID, state string, jobs []repo.CronJobListingDB) {
	switch state {
	case outlook.ObjectStateActive, outlook.ObjectStateDisabled:
		resourceState := repo.MicrosoftResourceActive
		if state == outlook.ObjectStateDisabled {
			resourceState = repo.MicrosoftResourceDisabled
		}
		_ = store.MicrosoftResourceRepo.MarkState(tenantID, resourceType, resourceID, resourceState)
		var resume []uint
		for _, j := range jobs {
			if !j.Active && (j.Message == msgPausedDeleted || j.Message == msgPausedGone) {
				resume = append(resume, j.ID)
			}
		}
		for _, id := range resume {
			_ = store.CronJobRepo.UpdateCronJobByID(id, map[string]interface{}{
				"active": true, "message": "Resumed: the Microsoft object was restored", "message_status": "info",
			})
		}
	case outlook.ObjectStateDeleted, outlook.ObjectStateGone:
		_ = store.MicrosoftResourceRepo.MarkState(tenantID, resourceType, resourceID, repo.MicrosoftResourceDeleted)
		msg := msgPausedDeleted
		if state == outlook.ObjectStateGone {
			msg = msgPausedGone
		}
		var ids []uint
		for _, j := range jobs {
			if j.Active {
				ids = append(ids, j.ID)
			}
		}
		_ = store.CronJobRepo.PauseJobs(ids, msg)
	}
}
