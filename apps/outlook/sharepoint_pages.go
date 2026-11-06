package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const sharePointPagesSegment = "pages"

// SharePointPageDir is {siteKey}/pages/.
func SharePointPageDir(siteKey string) string {
	return strings.TrimRight(siteKey, "/") + "/" + sharePointPagesSegment + "/"
}

// SharePointPageObjectKey is {siteKey}/pages/{name}_{pageId}.json so the vault UI can show
 // Home.aspx instead of a bare GUID. pageId stays in the key for uniqueness/restore.
func SharePointPageObjectKey(siteKey, pageID, name, title string) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = strings.TrimSpace(title)
	}
	if label == "" {
		label = "page"
	}
	id := pimKeySegment(pageID)
	if id == "" {
		id = "page"
	}
	return SharePointPageDir(siteKey) + SanitizeOneDrivePathSegment(label) + "_" + id + ".json"
}

// SharePointPageDisplayName returns a human label from a pages vault key (strips trailing _{id}.json).
func SharePointPageDisplayName(objectKey string) string {
	base := objectKey
	if i := strings.LastIndex(objectKey, "/"); i >= 0 {
		base = objectKey[i+1:]
	}
	base = strings.TrimSuffix(base, ".json")
	base = strings.TrimSuffix(base, ".JSON")
	if i := strings.LastIndex(base, "_"); i > 0 {
		return base[:i]
	}
	return base
}

// SharePointSitePage is a modern site page summary for backup.
type SharePointSitePage struct {
	ID          string
	Name        string
	Title       string
	WebURL      string
	Description string
	Raw         json.RawMessage
}

// ListSharePointSitePages lists modern site pages (SitePages library via Graph sitePages API when available).
func ListSharePointSitePages(ctx context.Context, accessToken, siteID string) ([]SharePointSitePage, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return nil, fmt.Errorf("site id is required")
	}
	// Prefer sitePages API; fall back to Site Pages list items.
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/pages/microsoft.graph.sitePage?$select=id,name,title,webUrl,description"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || status == http.StatusBadRequest {
		return listSharePointSitePagesViaList(ctx, accessToken, siteID)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("site pages http %d: %s", status, truncateForErr(body))
	}
	var page struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	out := make([]SharePointSitePage, 0, len(page.Value))
	for _, raw := range page.Value {
		var head struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Title       string `json:"title"`
			WebURL      string `json:"webUrl"`
			Description string `json:"description"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID == "" {
			continue
		}
		out = append(out, SharePointSitePage{
			ID: head.ID, Name: head.Name, Title: head.Title, WebURL: head.WebURL,
			Description: head.Description, Raw: raw,
		})
	}
	return out, nil
}

func listSharePointSitePagesViaList(ctx context.Context, accessToken, siteID string) ([]SharePointSitePage, error) {
	lists, err := ListSharePointSiteLists(ctx, accessToken, siteID)
	if err != nil {
		return nil, err
	}
	var sitePagesID string
	for _, l := range lists {
		if strings.EqualFold(l.Name, "SitePages") || strings.EqualFold(l.DisplayName, "Site Pages") ||
			strings.EqualFold(l.Template, "webPageLibrary") {
			sitePagesID = l.ID
			break
		}
	}
	if sitePagesID == "" {
		return nil, nil
	}
	items, _, err := ListSharePointListItemsPage(ctx, accessToken, SharePointListItemsURL(siteID, sitePagesID))
	if err != nil {
		return nil, err
	}
	out := make([]SharePointSitePage, 0, len(items))
	for _, it := range items {
		out = append(out, SharePointSitePage{ID: it.ID, Raw: it.Raw})
	}
	return out, nil
}

// FetchSharePointSiteConfig loads Graph-restorable site properties (display name, description, webUrl).
 // Full SP admin settings are out of scope; only fields Graph returns on GET /sites/{id}.
func FetchSharePointSiteConfig(ctx context.Context, accessToken, siteID string) ([]byte, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return nil, fmt.Errorf("site id is required")
	}
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) +
		"?$select=id,displayName,name,description,webUrl,isPersonalSite,siteCollection"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("site config http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}

// FetchSharePointSitePage loads one modern page including canvas content when available.
func FetchSharePointSitePage(ctx context.Context, accessToken, siteID, pageID string) ([]byte, error) {
	siteID, pageID = strings.TrimSpace(siteID), strings.TrimSpace(pageID)
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/pages/" + url.PathEscape(pageID) +
		"/microsoft.graph.sitePage?$expand=canvasLayout"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("site page http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}
