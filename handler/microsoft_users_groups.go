package handler

import (
	"fmt"
	"net/http"
	"strings"

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
		return "", fmt.Errorf("method must be one of: outlook, outlook_calendar, outlook_contacts, outlook_onedrive, outlook_sharepoint, outlook_teams, outlook_groups")
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
		OrgUnitPath: parseUsersGroupsOrgUnitPath(c),
	})
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list microsoft jobs for users-groups", err)
	}

	jobs = filterUsersGroupsMicrosoftJobs(jobs)
	mailboxJobs, orgResourceJobs := splitMicrosoftUsersGroupsJobs(jobs)

	policies := enrichUsersGroupsJobs(database, jobs)
	credByID := loadCredentialsForJobs(database, jobs)
	connectedCred := primaryMicrosoftUsersGroupsCredential(credByID)

	allEntities := buildUsersGroupsEntitiesForFamily(mailboxJobs, database.CronJobRepo, credByID, policies, filterUsersGroupsMicrosoftJobs, microsoftAutosyncServiceMethodsOrder)
	allEntities = filterUsersGroupsEntitiesByMethod(allEntities, mailboxJobs, method)
	allEntities = filterUsersGroupsEntitiesByActive(allEntities, mailboxJobs, activeFilter)
	allEntities = filterUsersGroupsEntitiesByAccountType(allEntities, accountType)
	allEntities = filterUsersGroupsEntitiesByCredentialStatus(allEntities, credentialStatus)
	allEntities = filterUsersGroupsEntitiesByOrgUnitPath(allEntities, parseUsersGroupsOrgUnitPath(c))
	orgUnits := uniqueUsersGroupsOrgUnitPaths(allEntities)
	entities, pagination := paginateUsersGroupsEntities(allEntities, limit, offset)

	sharepointJobs, teamsJobs, groupsJobs := splitMicrosoftOrgResourceJobs(orgResourceJobs)
	sharepointSites := buildMicrosoftSharePointSiteResources(sharepointJobs, policies)
	teamsResources := buildMicrosoftTeamsResources(teamsJobs, policies)
	groupsResources := buildMicrosoftGroupsResources(groupsJobs, policies)
	if method != "" && method != "outlook_sharepoint" {
		sharepointSites = nil
	}
	if method != "" && method != "outlook_teams" {
		teamsResources = nil
	}
	if method != "" && method != "outlook_groups" {
		groupsResources = nil
	}

	resp := map[string]interface{}{
		"connected_as": "",
		"account_type": "",
		"tenant_id":    "",
		"mailboxes":    entities,
		"organization_resources": map[string]interface{}{
			"sharepoint_sites": sharepointSites,
			"teams":            teamsResources,
			"groups":           groupsResources,
		},
		"org_units":  orgUnits,
		"pagination": pagination,
	}
	if connectedCred != nil {
		resp["connected_as"] = strings.TrimSpace(connectedCred.Email)
		resp["account_type"] = strings.TrimSpace(connectedCred.AccountType)
		resp["tenant_id"] = strings.TrimSpace(connectedCred.TenantID)
	}
	return c.JSON(http.StatusOK, resp)
}

func splitMicrosoftUsersGroupsJobs(jobs []repo.CronJobListingDB) (mailboxJobs, orgResourceJobs []repo.CronJobListingDB) {
	for i := range jobs {
		switch strings.TrimSpace(jobs[i].Method) {
		case "outlook_sharepoint", "outlook_teams", "outlook_groups":
			orgResourceJobs = append(orgResourceJobs, jobs[i])
		default:
			mailboxJobs = append(mailboxJobs, jobs[i])
		}
	}
	return mailboxJobs, orgResourceJobs
}

func splitMicrosoftOrgResourceJobs(jobs []repo.CronJobListingDB) (sharepointJobs, teamsJobs, groupsJobs []repo.CronJobListingDB) {
	for i := range jobs {
		switch strings.TrimSpace(jobs[i].Method) {
		case "outlook_sharepoint":
			sharepointJobs = append(sharepointJobs, jobs[i])
		case "outlook_teams":
			teamsJobs = append(teamsJobs, jobs[i])
		case "outlook_groups":
			groupsJobs = append(groupsJobs, jobs[i])
		}
	}
	return sharepointJobs, teamsJobs, groupsJobs
}

func primaryMicrosoftUsersGroupsCredential(credByID map[uint]*repo.GoogleBackupCredentialDB) *repo.GoogleBackupCredentialDB {
	for _, cred := range credByID {
		if cred != nil && strings.TrimSpace(cred.Email) != "" {
			return cred
		}
	}
	return nil
}

func buildMicrosoftSharePointSiteResources(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		job := &jobs[i]
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
		out = append(out, entry)
	}
	return out
}

func buildMicrosoftTeamsResources(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		job := &jobs[i]
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
		out = append(out, entry)
	}
	return out
}

func buildMicrosoftGroupsResources(jobs []repo.CronJobListingDB, policies map[uint]*repo.AutosyncBackupPolicyDB) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(jobs))
	for i := range jobs {
		job := &jobs[i]
		groupName := strings.TrimSpace(job.Name)
		groupID, groupMail := "", ""
		if job.InputData != nil && job.InputData.Json() != nil {
			raw := *job.InputData.Json()
			if v, ok := raw["group_id"].(string); ok {
				groupID = strings.TrimSpace(v)
			}
			if v, ok := raw["group_mail"].(string); ok {
				groupMail = strings.TrimSpace(v)
			}
			if v, ok := raw["group_name"].(string); ok && strings.TrimSpace(v) != "" {
				groupName = strings.TrimSpace(v)
			}
		}
		entry := map[string]interface{}{
			"name":       groupName,
			"group_id":   groupID,
			"group_mail": groupMail,
			"job_id":     job.ID,
			"method":     job.Method,
			"active":     job.Active,
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
