package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/mstenant"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

func parseMicrosoftUsersGroupsServiceMethod(c echo.Context) (string, error) {
	raw := strings.TrimSpace(c.QueryParam("method"))
	if raw == "" {
		return "", nil
	}
	method := strings.ToLower(raw)
	switch method {
	case "all", "all_services":
		return "", nil
	}
	if _, ok := microsoftWorkspaceServiceMethods[method]; !ok {
		return "", fmt.Errorf("method must be one of: outlook, outlook_calendar, outlook_contacts, outlook_onedrive, outlook_sharepoint, outlook_teams")
	}
	return method, nil
}

// HandleMicrosoftAutosyncUsersGroupsDomains lists domains from Microsoft jobs only.
func HandleMicrosoftAutosyncUsersGroupsDomains(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	jobs, err := database.CronJobRepo.ListJobsForUsersGroups(userID, nil)
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list microsoft jobs for domains", err)
	}
	jobs = filterUsersGroupsMicrosoftJobs(jobs)
	seen := make(map[string]struct{})
	domains := make([]string, 0)
	for i := range jobs {
		email := jobMailboxEmail(&jobs[i])
		if at := strings.LastIndex(email, "@"); at > 0 && at+1 < len(email) {
			d := strings.ToLower(strings.TrimSpace(email[at+1:]))
			if d == "" {
				continue
			}
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			domains = append(domains, d)
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"domains": domains})
}

// HandleMicrosoftUsersGroupsJobsActive bulk-toggles Microsoft jobs (validates each is MS method).
func HandleMicrosoftUsersGroupsJobsActive(c echo.Context) error {
	return HandleUsersGroupsJobsActive(c)
}

// HandleMicrosoftAutosyncUsersGroupsList returns Microsoft mailboxes and organization resources (SharePoint sites).
func HandleMicrosoftAutosyncUsersGroupsList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsAuth(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	method, err := parseMicrosoftUsersGroupsServiceMethod(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid service filter", err)
	}
	accountType, err := parseUsersGroupsAccountType(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid account_type filter", err)
	}
	credentialStatus, err := parseUsersGroupsCredentialStatus(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid credential_status filter", err)
	}
	activeFilter, err := parseUsersGroupsActive(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid active filter", err)
	}
	limit, offset, err := parseUsersGroupsLimitOffset(c)
	if err != nil {
		return usersGroupsBadRequest(c, "invalid pagination parameters", err)
	}

	jobs, err := database.CronJobRepo.ListJobsForUsersGroups(userID, &repo.UsersGroupsJobFilter{
		Domain:      strings.TrimSpace(c.QueryParam("domain")),
		EmailSearch: strings.TrimSpace(c.QueryParam("search")),
	})
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list microsoft jobs for users-groups", err)
	}

	jobs = filterUsersGroupsMicrosoftJobs(jobs)
	selectedTenant, jobs, tenantErr := filterUsersGroupsMicrosoftTenant(c, jobs)
	if tenantErr != nil {
		return c.JSON(http.StatusBadRequest, tenantErr)
	}
	mailboxJobs, orgResourceJobs := splitMicrosoftUsersGroupsJobs(jobs)

	policies := enrichUsersGroupsJobs(database, jobs)
	credByID := loadCredentialsForJobs(database, jobs)
	connectedCred := primaryMicrosoftUsersGroupsCredential(credByID)

	allEntities := buildUsersGroupsEntitiesForFamily(mailboxJobs, database.CronJobRepo, credByID, policies, filterUsersGroupsMicrosoftJobs, microsoftAutosyncServiceMethodsOrder)
	allEntities = filterUsersGroupsEntitiesByMethod(allEntities, mailboxJobs, method)
	allEntities = filterUsersGroupsEntitiesByActive(allEntities, mailboxJobs, activeFilter)
	allEntities = filterUsersGroupsEntitiesByAccountType(allEntities, accountType)
	allEntities = filterUsersGroupsEntitiesByCredentialStatus(allEntities, credentialStatus)
	entities, pagination := paginateUsersGroupsEntities(allEntities, limit, offset)

	sharepointJobs, teamsJobs := splitMicrosoftOrgResourceJobs(orgResourceJobs)
	sharepointSites := buildMicrosoftSharePointSiteResources(sharepointJobs, policies)
	teamsResources := buildMicrosoftTeamsResources(teamsJobs, policies, func(tenantID string) []repo.MicrosoftResourceDB {
		if database.MicrosoftResourceRepo == nil {
			return nil
		}
		rows, err := database.MicrosoftResourceRepo.ListByTenantAndType(tenantID, repo.ResourceTypeTeam)
		if err != nil {
			logger.Warn(ctx, "list tenant teams", logger.ErrorField(err))
		}
		return rows
	})
	if method != "" && method != "outlook_sharepoint" {
		sharepointSites = nil
	}
	if method != "" && method != "outlook_teams" {
		teamsResources = nil
	}

	resp := map[string]interface{}{
		"connected_as": "",
		"account_type": "",
		"tenant_id":    "",
		"mailboxes":    entities,
		"organization_resources": map[string]interface{}{
			"sharepoint_sites": sharepointSites,
			"teams":            teamsResources,
		},
		"pagination": pagination,
	}
	if connectedCred != nil {
		resp["connected_as"] = strings.TrimSpace(connectedCred.Email)
		resp["account_type"] = strings.TrimSpace(connectedCred.AccountType)
	}
	tenantIDs := microsoftUsersGroupsTenantIDs(jobs)
	if selectedTenant != "" {
		resp["tenant_id"] = selectedTenant
	} else if len(tenantIDs) == 1 {
		resp["tenant_id"] = tenantIDs[0]
		selectedTenant = tenantIDs[0]
	}
	if len(tenantIDs) > 1 {
		resp["tenant_ids"] = tenantIDs
	}
	if connectedCred != nil && selectedTenant != "" {
		if state, serr := mstenant.TenantAccessState(database, connectedCred.ID, selectedTenant); serr == nil {
			resp["account_type"] = state.AccountType
			resp["backup_mode"] = state.BackupMode
			resp["connection_state"] = state.ConnectionState
		}
	}
	return c.JSON(http.StatusOK, resp)
}

// filterUsersGroupsMicrosoftTenant keeps the jobs of the selected tenant when MICROSOFT_TENANT_ID
// or tenant_id is sent. With no selection every tenant is returned, so a personal account added
// beside an organization still appears on the mailbox list. Each row carries its own tenant_id.
func filterUsersGroupsMicrosoftTenant(c echo.Context, jobs []repo.CronJobListingDB) (string, []repo.CronJobListingDB, map[string]interface{}) {
	selected := strings.ToLower(strings.TrimSpace(c.Request().Header.Get(headerMicrosoftTenantID)))
	if selected == "" {
		selected = strings.ToLower(strings.TrimSpace(c.QueryParam("tenant_id")))
	}
	if selected == "" {
		return "", jobs, nil
	}
	out := make([]repo.CronJobListingDB, 0, len(jobs))
	for i := range jobs {
		if strings.EqualFold(strings.TrimSpace(jobs[i].TenantID), selected) {
			out = append(out, jobs[i])
		}
	}
	return selected, out, nil
}

// microsoftUsersGroupsTenantIDs returns the distinct tenant ids on jobs, sorted.
func microsoftUsersGroupsTenantIDs(jobs []repo.CronJobListingDB) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for i := range jobs {
		tid := strings.ToLower(strings.TrimSpace(jobs[i].TenantID))
		if tid == "" || seen[tid] {
			continue
		}
		seen[tid] = true
		out = append(out, tid)
	}
	sort.Strings(out)
	return out
}

// addMicrosoftResourceIdentity adds the job's tenant, resource identity and vault folder to an
// organization resource row.
func addMicrosoftResourceIdentity(entry map[string]interface{}, job *repo.CronJobListingDB) {
	tenantID, resourceType, resourceID, vaultPrefix := microsoftJobStorageIdentity(job)
	entry["tenant_id"] = tenantID
	entry["resource_type"] = resourceType
	entry["resource_id"] = resourceID
	entry["vault_prefix"] = vaultPrefix
}

func splitMicrosoftUsersGroupsJobs(jobs []repo.CronJobListingDB) (mailboxJobs, orgResourceJobs []repo.CronJobListingDB) {
	for i := range jobs {
		switch strings.TrimSpace(jobs[i].Method) {
		case "outlook_sharepoint", "outlook_teams":
			orgResourceJobs = append(orgResourceJobs, jobs[i])
		default:
			mailboxJobs = append(mailboxJobs, jobs[i])
		}
	}
	return mailboxJobs, orgResourceJobs
}

func splitMicrosoftOrgResourceJobs(jobs []repo.CronJobListingDB) (sharepointJobs, teamsJobs []repo.CronJobListingDB) {
	for i := range jobs {
		switch strings.TrimSpace(jobs[i].Method) {
		case "outlook_sharepoint":
			sharepointJobs = append(sharepointJobs, jobs[i])
		case "outlook_teams":
			teamsJobs = append(teamsJobs, jobs[i])
		}
	}
	return sharepointJobs, teamsJobs
}

func primaryMicrosoftUsersGroupsCredential(credByID map[uint]*repo.GoogleBackupCredentialDB) *repo.GoogleBackupCredentialDB {
	for _, cred := range credByID {
		if cred != nil && strings.TrimSpace(cred.Email) != "" {
			return cred
		}
	}
	return nil
}

// buildMicrosoftSharePointSiteResources lists one row per site. A tenant SharePoint job expands to
 // a row per selected site (all share the same job_id); old per-site jobs paused by the merge are
 // marked merged. Vault prefixes stay per-site so the browser can open each site.
func buildMicrosoftSharePointSiteResources(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		job := &jobs[i]
		if job.ResourceType == repo.ResourceTypeTenant {
			out = append(out, buildMicrosoftTenantSharePointRows(job, policies)...)
			continue
		}
		out = append(out, buildMicrosoftLegacySharePointSiteRow(job, policies))
	}
	return out
}

func buildMicrosoftTenantSharePointRows(job *repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) []map[string]interface{} {
	sites := repo.JobSharePointSites(job)
	tenantID := strings.ToLower(strings.TrimSpace(job.TenantID))
	out := make([]map[string]interface{}, 0, len(sites))
	for _, sel := range sites {
		siteID := strings.TrimSpace(sel.SiteID)
		if siteID == "" {
			continue
		}
		siteName := strings.TrimSpace(sel.SiteName)
		if siteName == "" {
			siteName = strings.TrimSpace(job.Name)
		}
		vaultPrefix := outlook.ResourceKeyPrefix(tenantID, repo.ResourceTypeSite, siteID)
		entry := map[string]interface{}{
			"name":       siteName,
			"site_id":    siteID,
			"site_url":   strings.TrimSpace(sel.SiteURL),
			"job_id":     job.ID,
			"method":     job.Method,
			"active":     job.Active,
			"tenant_job": true,
			"tenant_id":  tenantID,
			"resource_type": repo.ResourceTypeSite,
			"resource_id":   siteID,
			"vault_prefix":  vaultPrefix,
			"job_vault":     outlook.ResourceKeyPrefix(tenantID, repo.ResourceTypeTenant, tenantID),
		}
		if job.PolicyID > 0 {
			if p, ok := policies[job.PolicyID]; ok && p != nil {
				entry["policy_id"] = p.ID
				entry["interval"] = p.Interval
			}
		}
		libs := sharePointLibrariesForSite(job, siteID)
		if rows := sharePointLibraryRowsFromMap(libs, vaultPrefix); len(rows) > 0 {
			entry["libraries"] = rows
		}
		out = append(out, entry)
	}
	return out
}

func buildMicrosoftLegacySharePointSiteRow(job *repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) map[string]interface{} {
	siteName := strings.TrimSpace(job.Name)
	siteID, siteURL := "", ""
	if job.InputData != nil && job.InputData.Json() != nil {
		raw := *job.InputData.Json()
		if v, ok := raw["site_id"].(string); ok {
			siteID = strings.TrimSpace(v)
		}
		if v, ok := raw["site_url"].(string); ok {
			siteURL = strings.TrimSpace(v)
		}
		if v, ok := raw["site_name"].(string); ok && strings.TrimSpace(v) != "" {
			siteName = strings.TrimSpace(v)
		}
	}
	if siteID == "" {
		siteID = strings.TrimSpace(job.ResourceID)
	}
	entry := map[string]interface{}{
		"name":     siteName,
		"site_id":  siteID,
		"site_url": siteURL,
		"job_id":   job.ID,
		"method":   job.Method,
		"active":   job.Active,
	}
	if job.PolicyID > 0 {
		if p, ok := policies[job.PolicyID]; ok && p != nil {
			entry["policy_id"] = p.ID
			entry["interval"] = p.Interval
		}
	}
	addMicrosoftResourceIdentity(entry, job)
	if !job.Active && strings.TrimSpace(job.Message) == repo.SharePointMergedJobMessage {
		entry["merged"] = true
	}
	libs := sharePointLibrariesForSite(job, siteID)
	if rows := sharePointLibraryRowsFromMap(libs, entry["vault_prefix"]); len(rows) > 0 {
		entry["libraries"] = rows
	}
	return entry
}

// sharePointLibrariesForSite returns library state for one site. Tenant jobs keep it under
 // TaskMemory.SharePointSites[siteID]; legacy jobs use the top-level SharePointLibraries map.
func sharePointLibrariesForSite(job *repo.CronJobListingDB, siteID string) map[string]repo.SharePointLibraryState {
	if job == nil {
		return nil
	}
	siteID = strings.TrimSpace(siteID)
	if st, ok := job.TaskMemory.SharePointSites[siteID]; ok && len(st.Libraries) > 0 {
		return st.Libraries
	}
	return job.TaskMemory.SharePointLibraries
}

// sharePointLibraryRows lists the document libraries a site job has backed up, default library
 // first, each with its vault prefix. Jobs from before library tracking have none.
func sharePointLibraryRows(job *repo.CronJobListingDB, sitePrefix interface{}) []map[string]interface{} {
	return sharePointLibraryRowsFromMap(job.TaskMemory.SharePointLibraries, sitePrefix)
}

func sharePointLibraryRowsFromMap(libs map[string]repo.SharePointLibraryState, sitePrefix interface{}) []map[string]interface{} {
	if len(libs) == 0 {
		return nil
	}
	site, _ := sitePrefix.(string)
	ids := make([]string, 0, len(libs))
	for id := range libs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := libs[ids[i]], libs[ids[j]]
		if a.Default != b.Default {
			return a.Default
		}
		if !strings.EqualFold(a.Name, b.Name) {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return ids[i] < ids[j]
	})
	out := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		st := libs[id]
		prefix := site
		if !st.Default && site != "" {
			prefix = outlook.SharePointLibraryPrefix(site, id)
		}
		out = append(out, map[string]interface{}{
			"drive_id":     id,
			"name":         st.Name,
			"web_url":      st.WebURL,
			"default":      st.Default,
			"removed":      st.RemovedAt != "",
			"removed_at":   st.RemovedAt,
			"last_sync_at": st.LastSyncAt,
			"failed":       st.LastError != "",
			"vault_prefix": prefix,
		})
	}
	return out
}

// buildMicrosoftTeamsResources lists one row per team. A tenant Teams job gives a row per selected
// team (every known team of the tenant when the selection is "all"), each pointing to the team's
// vault prefix; knownTeams returns the tenant's team rows (names live there, not in the job).
// Old per-team jobs paused by the merge are marked merged.
func buildMicrosoftTeamsResources(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB,
	knownTeams func(tenantID string) []repo.MicrosoftResourceDB) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		job := &jobs[i]
		if job.ResourceType == repo.ResourceTypeTenant {
			out = append(out, buildMicrosoftTenantTeamsRows(job, policies, knownTeams)...)
			continue
		}
		teamName := strings.TrimSpace(job.Name)
		teamID, teamWebURL := "", ""
		if job.InputData != nil && job.InputData.Json() != nil {
			raw := *job.InputData.Json()
			if v, ok := raw["team_id"].(string); ok {
				teamID = strings.TrimSpace(v)
			}
			if v, ok := raw["team_web_url"].(string); ok {
				teamWebURL = strings.TrimSpace(v)
			}
			if v, ok := raw["team_name"].(string); ok && strings.TrimSpace(v) != "" {
				teamName = strings.TrimSpace(v)
			}
		}
		entry := map[string]interface{}{
			"name":         teamName,
			"team_id":      teamID,
			"team_web_url": teamWebURL,
			"job_id":       job.ID,
			"method":       job.Method,
			"active":       job.Active,
		}
		if job.PolicyID > 0 {
			if p, ok := policies[job.PolicyID]; ok && p != nil {
				entry["policy_id"] = p.ID
				entry["interval"] = p.Interval
			}
		}
		addMicrosoftResourceIdentity(entry, job)
		if !job.Active && strings.TrimSpace(job.Message) == repo.TeamsMergedJobMessage {
			entry["merged"] = true
		}
		out = append(out, entry)
	}
	return out
}

func buildMicrosoftTenantTeamsRows(job *repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB,
	knownTeams func(tenantID string) []repo.MicrosoftResourceDB) []map[string]interface{} {
	tenantID, _, _, _ := microsoftJobStorageIdentity(job)
	var in map[string]interface{}
	if job.InputData != nil && job.InputData.Json() != nil {
		in = *job.InputData.Json()
	}
	known := map[string]repo.MicrosoftResourceDB{}
	var all []string
	if knownTeams != nil {
		for _, t := range knownTeams(tenantID) {
			known[t.ExternalID] = t
			if t.State == "" || t.State == "active" {
				all = append(all, t.ExternalID)
			}
		}
	}
	teamIDs := repo.InputStringList(in["team_ids"])
	allTeams := len(teamIDs) == 0
	if allTeams {
		teamIDs = all
	}
	perTeam, _ := in["team_channel_ids"].(map[string]interface{})
	out := make([]map[string]interface{}, 0, len(teamIDs))
	for _, teamID := range teamIDs {
		t := known[teamID]
		webURL := ""
		if t.Metadata != nil && t.Metadata.Json() != nil {
			if v, ok := (*t.Metadata.Json())["web_url"].(string); ok {
				webURL = strings.TrimSpace(v)
			}
		}
		entry := map[string]interface{}{
			"name":          strings.TrimSpace(t.DisplayName),
			"team_id":       teamID,
			"team_web_url":  webURL,
			"job_id":        job.ID,
			"method":        job.Method,
			"active":        job.Active,
			"tenant_job":    true,
			"all_teams":     allTeams,
			"tenant_id":     tenantID,
			"resource_type": repo.ResourceTypeTeam,
			"resource_id":   teamID,
			"vault_prefix":  outlook.ResourceKeyPrefix(tenantID, repo.ResourceTypeTeam, teamID),
			"job_vault":     outlook.ResourceKeyPrefix(tenantID, repo.ResourceTypeTenant, tenantID),
		}
		if ch := repo.InputStringList(perTeam[teamID]); len(ch) > 0 {
			entry["channel_ids"] = ch
		}
		if job.PolicyID > 0 {
			if p, ok := policies[job.PolicyID]; ok && p != nil {
				entry["policy_id"] = p.ID
				entry["interval"] = p.Interval
			}
		}
		out = append(out, entry)
	}
	return out
}

func HandleMicrosoftAutosyncUsersGroupsMailboxOverview(c echo.Context) error {
	return handleMicrosoftUsersGroupsMailboxTab(c, "Failed to load mailbox overview", func(email string, data usersGroupsMailboxData) interface{} {
		services := buildUsersGroupsEntityServicesForOrder(data.jobs, data.policies, microsoftAutosyncServiceMethodsOrder)
		return UsersGroupsMailboxOverviewResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Services:                 buildMailboxOverviewServices(services),
		}
	})
}

func HandleMicrosoftAutosyncUsersGroupsMailboxServices(c echo.Context) error {
	return handleMicrosoftUsersGroupsMailboxTab(c, "Failed to load mailbox services", func(email string, data usersGroupsMailboxData) interface{} {
		services := buildUsersGroupsEntityServicesForOrder(data.jobs, data.policies, microsoftAutosyncServiceMethodsOrder)
		return UsersGroupsMailboxServicesResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Services:                 buildMailboxServicesTab(services),
		}
	})
}

func HandleMicrosoftAutosyncUsersGroupsMailboxSchedule(c echo.Context) error {
	return handleMicrosoftUsersGroupsMailboxTab(c, "Failed to load mailbox schedule", func(email string, data usersGroupsMailboxData) interface{} {
		services := buildUsersGroupsEntityServicesForOrder(data.jobs, data.policies, microsoftAutosyncServiceMethodsOrder)
		return UsersGroupsMailboxScheduleResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Schedules:                buildMailboxScheduleRows(services),
		}
	})
}

func HandleMicrosoftAutosyncUsersGroupsMailboxCredentials(c echo.Context) error {
	return handleMicrosoftUsersGroupsMailboxTab(c, "Failed to load mailbox credentials", func(email string, data usersGroupsMailboxData) interface{} {
		return UsersGroupsMailboxCredentialsResponse{
			UsersGroupsMailboxHeader: usersGroupsMailboxHeader(email, data.cred),
			Credential:               buildMailboxCredentialView(data.database.CronJobRepo, data.cred, data.jobs),
		}
	})
}

func handleMicrosoftUsersGroupsMailboxTab(c echo.Context, logMsg string, build func(email string, data usersGroupsMailboxData) interface{}) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, email, data, err := loadUsersGroupsMailboxContextForFamily(c, filterUsersGroupsMicrosoftJobs)
	if err != nil {
		return handleUsersGroupsMailboxTabError(c, ctx, userID, email, logMsg, err)
	}
	return c.JSON(http.StatusOK, build(email, data))
}
