package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/labstack/echo/v4"
)

// UsersGroupsServiceView is one service row under an email entity.
type UsersGroupsServiceView struct {
	Method string `json:"method"`
	Active bool   `json:"active"`
}

const (
	autosyncPolicyListLink  = "/auto-sync/policy"
	usersGroupsDefaultLimit = 10
)

// UsersGroupsPaginationView is pagination metadata for GET /auto-sync/users-groups.
type UsersGroupsPaginationView struct {
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
}

// UsersGroupsEntityView is one mailbox row on GET /auto-sync/users-groups.
type UsersGroupsEntityView struct {
	Name     string                   `json:"name"`
	Email    string                   `json:"email"`
	Services []UsersGroupsServiceView `json:"services"`
}

var workspaceServiceMethods = func() map[string]struct{} {
	set := make(map[string]struct{}, len(autosyncServiceMethodsOrder))
	for _, method := range autosyncServiceMethodsOrder {
		set[method] = struct{}{}
	}
	return set
}()

func nameFromMailboxEmail(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.LastIndex(email, "@"); at > 0 {
		return email[:at]
	}
	return email
}

func buildUsersGroupsServices(jobs []repo.CronJobListingDB) []UsersGroupsServiceView {
	activeByMethod := make(map[string]bool)
	for i := range jobs {
		method := strings.TrimSpace(jobs[i].Method)
		if jobs[i].Active {
			activeByMethod[method] = true
			continue
		}
		if _, ok := activeByMethod[method]; !ok {
			activeByMethod[method] = false
		}
	}
	out := make([]UsersGroupsServiceView, 0, len(activeByMethod))
	for _, method := range autosyncServiceMethodsOrder {
		active, ok := activeByMethod[method]
		if !ok {
			continue
		}
		out = append(out, UsersGroupsServiceView{Method: method, Active: active})
	}
	return out
}

func buildUsersGroupsEntities(jobs []repo.CronJobListingDB) []UsersGroupsEntityView {
	byEmail := make(map[string][]repo.CronJobListingDB)
	for i := range jobs {
		if _, ok := workspaceServiceMethods[strings.TrimSpace(jobs[i].Method)]; !ok {
			continue
		}
		email := jobMailboxEmail(&jobs[i])
		if email == "" {
			continue
		}
		byEmail[email] = append(byEmail[email], jobs[i])
	}

	emails := make([]string, 0, len(byEmail))
	for email := range byEmail {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	out := make([]UsersGroupsEntityView, 0, len(emails))
	for _, email := range emails {
		emailJobs := byEmail[email]
		out = append(out, UsersGroupsEntityView{
			Name:     nameFromMailboxEmail(email),
			Email:    email,
			Services: buildUsersGroupsServices(emailJobs),
		})
	}
	return out
}

func parseUsersGroupsServiceMethod(c echo.Context) (string, error) {
	raw := strings.TrimSpace(c.QueryParam("method"))
	if raw == "" {
		return "", nil
	}
	method := strings.ToLower(raw)
	switch method {
	case "all", "all_services":
		return "", nil
	}
	if _, ok := workspaceServiceMethods[method]; !ok {
		return "", fmt.Errorf("method must be one of: gmail, google_drive, google_photos, google_contacts, google_calendar")
	}
	return method, nil
}

func filterUsersGroupsEntitiesByMethod(entities []UsersGroupsEntityView, jobs []repo.CronJobListingDB, method string) []UsersGroupsEntityView {
	if method == "" {
		return entities
	}
	emailsWithMethod := make(map[string]struct{})
	for i := range jobs {
		if strings.TrimSpace(jobs[i].Method) != method {
			continue
		}
		if email := jobMailboxEmail(&jobs[i]); email != "" {
			emailsWithMethod[email] = struct{}{}
		}
	}
	out := make([]UsersGroupsEntityView, 0)
	for i := range entities {
		if _, ok := emailsWithMethod[entities[i].Email]; ok {
			out = append(out, entities[i])
		}
	}
	return out
}

func parseUsersGroupsLimitOffset(c echo.Context) (limit, offset int, err error) {
	limit = usersGroupsDefaultLimit
	offset = 0
	if l := strings.TrimSpace(c.QueryParam("limit")); l != "" {
		limit, err = strconv.Atoi(l)
		if err != nil || limit < 1 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
	}
	if o := strings.TrimSpace(c.QueryParam("offset")); o != "" {
		offset, err = strconv.Atoi(o)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

func paginateUsersGroupsEntities(all []UsersGroupsEntityView, limit, offset int) ([]UsersGroupsEntityView, UsersGroupsPaginationView) {
	total := len(all)
	totalPages := 0
	if limit > 0 && total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	page := 1
	if limit > 0 {
		page = offset/limit + 1
	}
	meta := UsersGroupsPaginationView{
		Limit:      limit,
		Offset:     offset,
		Page:       page,
		TotalPages: totalPages,
		TotalCount: total,
	}
	if total == 0 || offset >= total {
		return []UsersGroupsEntityView{}, meta
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], meta
}

func usersGroupsUnauthorized(c echo.Context, err error) error {
	return c.JSON(http.StatusUnauthorized, map[string]interface{}{
		"message": "not able to authenticate user",
		"error":   err.Error(),
	})
}

func usersGroupsInternalError(c echo.Context, ctx context.Context, logMsg string, err error) error {
	logger.Error(ctx, logMsg, logger.ErrorField(err))
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{
		"message": "internal server error",
		"error":   err.Error(),
	})
}

func usersGroupsUserAndDB(c echo.Context) (context.Context, string, *db.PostgresDb, error) {
	ctx := c.Request().Context()
	userID, err := satellite.GetUserdetails(c)
	if err != nil {
		return ctx, "", nil, err
	}
	return ctx, userID, c.Get(middleware.DbContextKey).(*db.PostgresDb), nil
}

// HandleAutosyncUsersGroupsDomains lists unique domains for the domains dropdown.
func HandleAutosyncUsersGroupsDomains(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsUserAndDB(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	domains, err := database.CredentialRepo.ListUniqueDomainsForUser(userID)
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list unique domains", err)
	}
	if domains == nil {
		domains = []string{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"domains": domains})
}

// HandleAutosyncUsersGroupsList returns a paginated flat list of mailbox emails with per-service active status.
func HandleAutosyncUsersGroupsList(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	ctx, userID, database, err := usersGroupsUserAndDB(c)
	if err != nil {
		return usersGroupsUnauthorized(c, err)
	}

	method, err := parseUsersGroupsServiceMethod(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "invalid service filter",
			"error":   err.Error(),
		})
	}

	limit, offset, err := parseUsersGroupsLimitOffset(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "invalid pagination parameters",
			"error":   err.Error(),
		})
	}

	jobs, err := database.CronJobRepo.ListJobsForUsersGroups(userID, &repo.UsersGroupsJobFilter{
		Domain:      strings.TrimSpace(c.QueryParam("domain")),
		EmailSearch: strings.TrimSpace(c.QueryParam("search")),
	})
	if err != nil {
		return usersGroupsInternalError(c, ctx, "Failed to list jobs for users-groups", err)
	}

	entities, pagination := paginateUsersGroupsEntities(
		filterUsersGroupsEntitiesByMethod(buildUsersGroupsEntities(jobs), jobs, method),
		limit,
		offset,
	)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"policy_link": autosyncPolicyListLink,
		"entities":    entities,
		"pagination":  pagination,
	})
}
