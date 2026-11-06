package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SharePointListKind distinguishes backup ownership so the same content is never stored twice.
type SharePointListKind string

const (
	// SharePointListKindDocumentLibrary — file/folder bytes via drive delta only (Documents, Style Library, …).
	SharePointListKindDocumentLibrary SharePointListKind = "document_library"
	// SharePointListKindList — structured list defs/items/attachments (generic lists, etc.).
	SharePointListKindList SharePointListKind = "list"
	// SharePointListKindPages — Site Pages / webPageLibrary owned by the pages collector only.
	SharePointListKindPages SharePointListKind = "pages"
)

// SharePointSiteList is one Graph list on a site with classification for backup routing.
type SharePointSiteList struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName"`
	WebURL      string             `json:"webUrl,omitempty"`
	Template    string             `json:"template,omitempty"`
	Kind        SharePointListKind `json:"kind"`
	DriveID     string             `json:"driveId,omitempty"` // set when Kind is document_library
}

type graphSiteListRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	WebURL      string `json:"webUrl"`
	List        *struct {
		Template string `json:"template"`
	} `json:"list"`
}

// ClassifySharePointListKind maps a Graph list template to backup ownership.
func ClassifySharePointListKind(template string) SharePointListKind {
	switch strings.TrimSpace(template) {
	case "documentLibrary", "mySiteDocumentLibrary":
		return SharePointListKindDocumentLibrary
	case "webPageLibrary":
		// Site Pages — modern pages collector owns these; do not also store via list items.
		return SharePointListKindPages
	default:
		return SharePointListKindList
	}
}

// ClassifySharePointListKindFromList uses template plus well-known names (Site Pages) so
 // mis-labelled templates still avoid list/pages double-store.
func ClassifySharePointListKindFromList(template, name, displayName string) SharePointListKind {
	kind := ClassifySharePointListKind(template)
	if kind == SharePointListKindDocumentLibrary || kind == SharePointListKindPages {
		return kind
	}
	if strings.EqualFold(strings.TrimSpace(name), "SitePages") ||
		strings.EqualFold(strings.TrimSpace(displayName), "Site Pages") {
		return SharePointListKindPages
	}
	return kind
}

// ListSharePointSiteLists pages every list on a site and classifies each entry.
 // Fail-closed: bad nextLink or HTTP errors return err (incomplete inventory).
func ListSharePointSiteLists(ctx context.Context, accessToken, siteID string) ([]SharePointSiteList, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return nil, fmt.Errorf("site id is required")
	}
	raw, err := FetchTeamsAll(ctx, accessToken,
		graphBaseURL+"/sites/"+url.PathEscape(siteID)+"/lists?$select=id,name,displayName,webUrl,list&$expand=drive($select=id)")
	if err != nil {
		return nil, fmt.Errorf("list sharepoint lists: %w", err)
	}
	out := make([]SharePointSiteList, 0, len(raw))
	for _, item := range raw {
		var row struct {
			graphSiteListRow
			Drive *struct {
				ID string `json:"id"`
			} `json:"drive"`
		}
		if err := json.Unmarshal(item, &row); err != nil {
			return nil, fmt.Errorf("list sharepoint lists: decode: %w", err)
		}
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		template := ""
		if row.List != nil {
			template = row.List.Template
		}
		kind := ClassifySharePointListKindFromList(template, row.Name, row.DisplayName)
		entry := SharePointSiteList{
			ID: row.ID, Name: row.Name, DisplayName: row.DisplayName,
			WebURL: row.WebURL, Template: template, Kind: kind,
		}
		if row.Drive != nil {
			entry.DriveID = strings.TrimSpace(row.Drive.ID)
		}
		out = append(out, entry)
	}
	return out, nil
}

// SharePointNonLibraryLists filters to structured lists only (not document libraries, not Site Pages).
func SharePointNonLibraryLists(all []SharePointSiteList) []SharePointSiteList {
	out := make([]SharePointSiteList, 0, len(all))
	for _, l := range all {
		if l.Kind == SharePointListKindList {
			out = append(out, l)
		}
	}
	return out
}

// FetchSharePointListDefinition loads columns and core list metadata for backup.
func FetchSharePointListDefinition(ctx context.Context, accessToken, siteID, listID string) (json.RawMessage, error) {
	siteID, listID = strings.TrimSpace(siteID), strings.TrimSpace(listID)
	if siteID == "" || listID == "" {
		return nil, fmt.Errorf("site id and list id are required")
	}
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/lists/" + url.PathEscape(listID) +
		"?$expand=columns,contentTypes"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("sharepoint list definition http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}
