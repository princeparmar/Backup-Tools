package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DirectoryUser is one Entra user read live from Graph (app-only token). Directory users are not
// stored; like Google Workspace domain users they are listed on demand.
type DirectoryUser struct {
	ObjectID       string
	UPN            string
	Mail           string
	DisplayName    string
	Department     string
	JobTitle       string
	OfficeLocation string
	AccountEnabled bool
	HasLicense     bool
	CreatedAt      *time.Time
}

// Email returns mail, falling back to UPN.
func (u DirectoryUser) Email() string {
	if m := strings.TrimSpace(u.Mail); m != "" {
		return m
	}
	return strings.TrimSpace(u.UPN)
}

// OrgUnitPath maps the user's department to a StorX org unit ("/" + department, else "/").
func (u DirectoryUser) OrgUnitPath() string {
	department := strings.Trim(strings.TrimSpace(u.Department), "/")
	if department == "" {
		return "/"
	}
	return "/" + department
}

type graphDirectoryUserRow struct {
	ID                string            `json:"id"`
	UserPrincipalName string            `json:"userPrincipalName"`
	Mail              string            `json:"mail"`
	DisplayName       string            `json:"displayName"`
	Department        string            `json:"department"`
	JobTitle          string            `json:"jobTitle"`
	OfficeLocation    string            `json:"officeLocation"`
	AccountEnabled    *bool             `json:"accountEnabled"`
	AssignedLicenses  []json.RawMessage `json:"assignedLicenses"`
	CreatedDateTime   *time.Time        `json:"createdDateTime"`
}

// ListTenantDirectoryUsers lists every user in the tenant (app-only token, User.Read.All).
func ListTenantDirectoryUsers(ctx context.Context, accessToken string) ([]DirectoryUser, error) {
	reqURL := graphBaseURL + "/users?$select=id,userPrincipalName,mail,displayName,department,jobTitle," +
		"officeLocation,accountEnabled,assignedLicenses,createdDateTime&$top=999"
	rows, err := graphListAll[graphDirectoryUserRow](ctx, accessToken, reqURL, 0)
	if err != nil {
		return nil, fmt.Errorf("list tenant users: %w", err)
	}
	out := make([]DirectoryUser, 0, len(rows))
	for _, r := range rows {
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		out = append(out, DirectoryUser{
			ObjectID:       id,
			UPN:            strings.TrimSpace(r.UserPrincipalName),
			Mail:           strings.TrimSpace(r.Mail),
			DisplayName:    strings.TrimSpace(r.DisplayName),
			Department:     strings.TrimSpace(r.Department),
			JobTitle:       strings.TrimSpace(r.JobTitle),
			OfficeLocation: strings.TrimSpace(r.OfficeLocation),
			AccountEnabled: r.AccountEnabled == nil || *r.AccountEnabled,
			HasLicense:     len(r.AssignedLicenses) > 0,
			CreatedAt:      r.CreatedDateTime,
		})
	}
	return out, nil
}

// graphListAll follows @odata.nextLink and decodes every page's value array.
func graphListAll[T any](ctx context.Context, accessToken, startURL string, limit int) ([]T, error) {
	var out []T
	next := startURL
	for next != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, next, nil)
		if err != nil {
			return out, err
		}
		if status < 200 || status >= 300 {
			return out, fmt.Errorf("graph list http %d: %s", status, truncateForErr(body))
		}
		var page struct {
			Value    []T    `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return out, err
		}
		out = append(out, page.Value...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		next = page.NextLink
	}
	return out, nil
}

// ListTenantTeams lists every Teams-provisioned group in the tenant (app-only token).
// limit <= 0 returns all.
func ListTenantTeams(ctx context.Context, accessToken string, limit int) ([]TeamSummary, error) {
	reqURL := graphBaseURL + "/groups?$filter=" + url.QueryEscape("resourceProvisioningOptions/Any(x:x eq 'Team')") +
		"&$select=id,displayName,description&$top=999"
	rows, err := graphListAll[graphGroupRow](ctx, accessToken, reqURL, limit)
	if err != nil {
		return nil, fmt.Errorf("list tenant teams: %w", err)
	}
	out := make([]TeamSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, TeamSummary{
			ID:          strings.TrimSpace(r.ID),
			DisplayName: strings.TrimSpace(r.DisplayName),
			Description: strings.TrimSpace(r.Description),
		})
	}
	return out, nil
}

// ListTenantGroups lists every Microsoft 365 (Unified) group in the tenant (app-only token).
func ListTenantGroups(ctx context.Context, accessToken string, limit int) ([]GroupSummary, error) {
	reqURL := graphBaseURL + "/groups?$filter=" + url.QueryEscape("groupTypes/any(c:c eq 'Unified')") +
		"&$select=id,displayName,mail,description&$top=999"
	rows, err := graphListAll[graphGroupRow](ctx, accessToken, reqURL, limit)
	if err != nil {
		return nil, fmt.Errorf("list tenant groups: %w", err)
	}
	out := make([]GroupSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupSummary{
			ID:          strings.TrimSpace(r.ID),
			DisplayName: strings.TrimSpace(r.DisplayName),
			Mail:        strings.TrimSpace(r.Mail),
			Description: strings.TrimSpace(r.Description),
		})
	}
	return out, nil
}

type graphSiteRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	WebURL      string `json:"webUrl"`
}

// systemSharePointSitePaths are hidden/system site collections returned by getAllSites that are not
// user content sites.
var systemSharePointSitePaths = []string{"/contentstorage/", "/search/", "/portals/", "/sites/appcatalog/"}

// isSystemSharePointSite reports getAllSites rows that are not user sites: unnamed system sites,
// personal OneDrive sites (backed up as onedrive) and known system paths.
func isSystemSharePointSite(r graphSiteRow) bool {
	if strings.TrimSpace(r.DisplayName) == "" {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(r.WebURL))
	if err != nil {
		return false
	}
	if strings.HasSuffix(strings.ToLower(u.Host), "-my.sharepoint.com") {
		return true
	}
	path := strings.TrimRight(strings.ToLower(u.Path), "/") + "/"
	for _, p := range systemSharePointSitePaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// ListTenantSharePointSites lists tenant sites (app-only token). With a search term it uses site
// search; otherwise getAllSites, falling back to search=* when getAllSites is unavailable.
func ListTenantSharePointSites(ctx context.Context, accessToken, search string, limit int) ([]SharePointSiteSummary, error) {
	const sel = "$select=id,name,displayName,webUrl"
	var rows []graphSiteRow
	var err error
	if s := strings.TrimSpace(search); s != "" {
		rows, err = graphListAll[graphSiteRow](ctx, accessToken, graphBaseURL+"/sites?search="+url.QueryEscape(s)+"&"+sel, limit)
	} else {
		rows, err = graphListAll[graphSiteRow](ctx, accessToken, graphBaseURL+"/sites/getAllSites?"+sel, limit)
		if err != nil {
			rows, err = graphListAll[graphSiteRow](ctx, accessToken, graphBaseURL+"/sites?search=*&"+sel, limit)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("list tenant sharepoint sites: %w", err)
	}
	out := make([]SharePointSiteSummary, 0, len(rows))
	for _, r := range rows {
		if isSystemSharePointSite(r) {
			continue
		}
		out = append(out, SharePointSiteSummary{
			ID:          strings.TrimSpace(r.ID),
			Name:        strings.TrimSpace(r.Name),
			DisplayName: strings.TrimSpace(r.DisplayName),
			WebURL:      strings.TrimSpace(r.WebURL),
		})
	}
	return out, nil
}
