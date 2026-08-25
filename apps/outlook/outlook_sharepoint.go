package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrSharePointDeltaInvalid means the saved deltaLink can no longer be used; rebaseline required.
var ErrSharePointDeltaInvalid = fmt.Errorf("sharepoint delta cursor invalid; rebaseline required")

// SharePointSiteSummary is a site row for browse/onboarding.
type SharePointSiteSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	WebURL      string `json:"web_url"`
}

// SharePointResolvedSite holds site_id + default document library drive_id.
type SharePointResolvedSite struct {
	SiteID   string
	SiteName string
	SiteURL  string
	DriveID  string
}

// SharePointCronBackupMeta is JSON at {siteKey}/meta/.../{itemId}_{name}.json.
type SharePointCronBackupMeta struct {
	ItemID                string `json:"item_id"`
	Name                  string `json:"name"`
	MimeType              string `json:"mime_type,omitempty"`
	ParentID              string `json:"parent_id,omitempty"`
	ParentPath            string `json:"parent_path,omitempty"`
	WebURL                string `json:"web_url,omitempty"`
	Size                  int64  `json:"size,omitempty"`
	CreatedDateTime       string `json:"created_date_time,omitempty"`
	LastModifiedDateTime  string `json:"last_modified_date_time,omitempty"`
	ETag                  string `json:"etag,omitempty"`
	CTag                  string `json:"ctag,omitempty"`
	SiteID                string `json:"site_id"`
	DriveID               string `json:"drive_id"`
	IsFolder              bool   `json:"is_folder"`
	RemovedFromSharePoint bool   `json:"removed_from_sharepoint,omitempty"`
	DeletedAt             string `json:"deleted_at,omitempty"`
	DataObjectKey         string `json:"data_object_key,omitempty"`
	UpdatedAt             string `json:"updated_at,omitempty"`
}

type graphSiteResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	WebURL      string `json:"webUrl"`
}

type graphSitesSearchResponse struct {
	Value []graphSiteResponse `json:"value"`
}

type graphDriveResponse struct {
	ID string `json:"id"`
}

// DriveRootURLFromDriveID returns the Graph drive resource URL for delta/content calls.
func DriveRootURLFromDriveID(driveID string) string {
	driveID = strings.TrimSpace(driveID)
	return graphBaseURL + "/drives/" + url.PathEscape(driveID)
}

// SharePointInitialDeltaURL is GET /drives/{drive_id}/root/delta for baseline.
func SharePointInitialDeltaURL(driveID string) string {
	return DriveRootURLFromDriveID(driveID) + "/root/delta"
}

// SanitizeSharePointSiteKey makes site_id safe for StorX object key prefixes.
func SanitizeSharePointSiteKey(siteID string) string {
	siteID = strings.TrimSpace(siteID)
	replacer := strings.NewReplacer(",", "_", "/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "#", "_")
	s := replacer.Replace(siteID)
	if s == "" {
		return "site"
	}
	return s
}

// SharePointIDBasedMetaKey is {siteKey}/meta/{yyyy}/{mm}/{dd}/{itemId}_{name}.json
func SharePointIDBasedMetaKey(siteKey, itemID, displayName, createdTime string) string {
	return fmt.Sprintf("%s/meta/%s/%s_%s.json",
		SanitizeSharePointSiteKey(siteKey),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(itemID),
		SanitizeOneDrivePathSegment(displayName),
	)
}

// SharePointIDBasedDataKey is {siteKey}/data/{yyyy}/{mm}/{dd}/{itemId}_{name}
func SharePointIDBasedDataKey(siteKey, itemID, displayName, createdTime string) string {
	return fmt.Sprintf("%s/data/%s/%s_%s",
		SanitizeSharePointSiteKey(siteKey),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(itemID),
		SanitizeOneDrivePathSegment(displayName),
	)
}

// FetchSharePointDeltaPage GETs a Graph drive delta URL (initial, nextLink, or saved deltaLink).
func FetchSharePointDeltaPage(ctx context.Context, accessToken, requestURL string) (*OneDriveDeltaPage, error) {
	page, err := FetchOneDriveDeltaPage(ctx, accessToken, requestURL)
	if err == nil {
		return page, nil
	}
	if err == ErrOneDriveDeltaInvalid {
		return nil, ErrSharePointDeltaInvalid
	}
	return nil, err
}

// OpenSharePointItemContentStream streams file content from /drives/{drive_id}/items/{item_id}/content.
func OpenSharePointItemContentStream(ctx context.Context, accessToken, driveID, itemID string) (io.ReadCloser, int64, error) {
	return OpenOneDriveItemContentStream(ctx, accessToken, DriveRootURLFromDriveID(driveID), itemID)
}

// ListSharePointSites searches sites visible to the signed-in user.
func ListSharePointSites(ctx context.Context, accessToken, search string, top int32) ([]SharePointSiteSummary, error) {
	if top <= 0 {
		top = 50
	}
	q := url.QueryEscape(strings.TrimSpace(search))
	if q == "" {
		q = "*"
	}
	reqURL := fmt.Sprintf("%s/sites?search=%s&$top=%d&$select=id,name,displayName,webUrl", graphBaseURL, q, top)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("sharepoint sites http %d: %s", status, truncateForErr(body))
	}
	var parsed graphSitesSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]SharePointSiteSummary, 0, len(parsed.Value))
	for i := range parsed.Value {
		s := parsed.Value[i]
		out = append(out, SharePointSiteSummary{
			ID:          strings.TrimSpace(s.ID),
			Name:        strings.TrimSpace(s.Name),
			DisplayName: strings.TrimSpace(s.DisplayName),
			WebURL:      strings.TrimSpace(s.WebURL),
		})
	}
	return out, nil
}

// ResolveSharePointSite resolves site_id (and default drive_id) from site_id or site_url input.
func ResolveSharePointSite(ctx context.Context, accessToken, siteID, siteURL string) (*SharePointResolvedSite, error) {
	siteID = strings.TrimSpace(siteID)
	siteURL = strings.TrimSpace(siteURL)
	if siteID == "" && siteURL == "" {
		return nil, fmt.Errorf("site_id or site_url is required")
	}

	var site graphSiteResponse
	if siteID != "" {
		reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID)
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("sharepoint site http %d: %s", status, truncateForErr(body))
		}
		if err := json.Unmarshal(body, &site); err != nil {
			return nil, err
		}
	} else {
		host, path, err := parseSharePointSiteURL(siteURL)
		if err != nil {
			return nil, err
		}
		reqURL := fmt.Sprintf("%s/sites/%s:%s", graphBaseURL, url.PathEscape(host), url.PathEscape(path))
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("sharepoint site by path http %d: %s", status, truncateForErr(body))
		}
		if err := json.Unmarshal(body, &site); err != nil {
			return nil, err
		}
	}

	resolvedSiteID := strings.TrimSpace(site.ID)
	if resolvedSiteID == "" {
		return nil, fmt.Errorf("sharepoint site id missing from graph response")
	}

	driveID, err := fetchSiteDefaultDriveID(ctx, accessToken, resolvedSiteID)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(site.DisplayName)
	if name == "" {
		name = strings.TrimSpace(site.Name)
	}
	web := strings.TrimSpace(site.WebURL)
	if web == "" {
		web = siteURL
	}

	return &SharePointResolvedSite{
		SiteID:   resolvedSiteID,
		SiteName: name,
		SiteURL:  web,
		DriveID:  driveID,
	}, nil
}

func fetchSiteDefaultDriveID(ctx context.Context, accessToken, siteID string) (string, error) {
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/drive?$select=id"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("sharepoint default drive http %d: %s", status, truncateForErr(body))
	}
	var drive graphDriveResponse
	if err := json.Unmarshal(body, &drive); err != nil {
		return "", err
	}
	driveID := strings.TrimSpace(drive.ID)
	if driveID == "" {
		return "", fmt.Errorf("sharepoint default drive id missing")
	}
	return driveID, nil
}

func parseSharePointSiteURL(raw string) (host, path string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("invalid site_url: %w", err)
	}
	host = strings.TrimSpace(u.Host)
	path = strings.TrimSpace(u.Path)
	if host == "" {
		return "", "", fmt.Errorf("site_url host is required")
	}
	if path == "" {
		path = "/"
	}
	return host, path, nil
}

// ListSharePointFlatFilesPage lists non-folder files at drive root (browse).
func ListSharePointFlatFilesPage(ctx context.Context, accessToken, driveID string, skip, top int32) ([]OneDriveItem, error) {
	return ListOneDriveFlatFilesPage(ctx, accessToken, DriveRootURLFromDriveID(driveID), skip, top)
}
