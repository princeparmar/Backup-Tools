package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SharePointVaultGet loads one vault object (wired by restore callers).
type SharePointVaultGet func(ctx context.Context, key string) ([]byte, error)

// SharePointCrossRefIssue is one unlinkable relationship found after restore.
type SharePointCrossRefIssue struct {
	Kind    string `json:"kind"` // lookup | page_link | permission
	Source  string `json:"source"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message"`
}

// RestoreSharePointSitePage creates a modern site page from backed-up Graph JSON when the API allows.
// Returns created page id. Failures are returned; callers must not treat discovery as restore success.
func RestoreSharePointSitePage(ctx context.Context, accessToken, siteID string, raw []byte) (string, error) {
	siteID = GraphSharePointSiteID(siteID)
	if siteID == "" {
		return "", fmt.Errorf("site id is required")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse site page: %w", err)
	}
	// Prefer name/title from backup; strip read-only ids.
	name, _ := doc["name"].(string)
	title, _ := doc["title"].(string)
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSpace(title)
	}
	if name == "" {
		return "", fmt.Errorf("site page missing name/title")
	}
	payload := map[string]any{
		"@odata.type": "#microsoft.graph.sitePage",
		"name":        name,
		"title":       cmpString(title, name),
		"pageLayout":  "article",
	}
	if layout, ok := doc["canvasLayout"]; ok {
		payload["canvasLayout"] = layout
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	createURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/pages"
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, createURL, body)
	if err != nil {
		return "", err
	}
	if status == http.StatusConflict {
		payload["name"] = uniqueSharePointPageName(name)
		if retry, mErr := json.Marshal(payload); mErr == nil {
			resp, status, err = graphDoJSONWrite(ctx, accessToken, http.MethodPost, createURL, retry)
			if err != nil {
				return "", err
			}
		}
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("create site page http %d: %s", status, truncateForErr(resp))
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(resp, &created)
	if strings.TrimSpace(created.ID) == "" {
		return "", fmt.Errorf("create site page: empty id")
	}
	return created.ID, nil
}

func cmpString(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func uniqueSharePointPageName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "restored.aspx"
	}
	if strings.HasSuffix(strings.ToLower(name), ".aspx") {
		return name[:len(name)-5] + "-restored.aspx"
	}
	return name + "-restored"
}

// ValidateSharePointListLookups reports lookup fields whose target ids are not in knownItemIDs.
func ValidateSharePointListLookups(itemRaw []byte, knownItemIDs map[string]bool) []SharePointCrossRefIssue {
	var issues []SharePointCrossRefIssue
	var doc map[string]json.RawMessage
	if json.Unmarshal(itemRaw, &doc) != nil {
		return issues
	}
	fieldsRaw, ok := doc["fields"]
	if !ok {
		return issues
	}
	var fields map[string]any
	if json.Unmarshal(fieldsRaw, &fields) != nil {
		return issues
	}
	for k, v := range fields {
		if !strings.HasSuffix(k, "LookupId") && !strings.Contains(strings.ToLower(k), "lookupid") {
			continue
		}
		id := fmt.Sprint(v)
		if id == "" || id == "<nil>" {
			continue
		}
		if knownItemIDs != nil && knownItemIDs[id] {
			continue
		}
		issues = append(issues, SharePointCrossRefIssue{
			Kind:    "lookup",
			Source:  k,
			Target:  id,
			Message: "lookup target not found after restore",
		})
	}
	return issues
}

// RestoreSharePointListItemFromVault restores one list-item JSON using sibling _list.json for ids.
// destinationSiteID, when set, is the migration site. Empty writes back to the site in _list.json.
func RestoreSharePointListItemFromVault(ctx context.Context, accessToken, destinationSiteID, itemKey string, get SharePointVaultGet) (string, error) {
	if get == nil {
		return "", fmt.Errorf("vault get is required")
	}
	metaKey, ok := SharePointListMetaKeyFromItemKey(itemKey)
	if !ok {
		return "", fmt.Errorf("not a sharepoint list item key: %s", itemKey)
	}
	metaRaw, err := get(ctx, metaKey)
	if err != nil {
		return "", fmt.Errorf("read list meta: %w", err)
	}
	var meta struct {
		ID     string `json:"id"`
		SiteID string `json:"site_id"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return "", fmt.Errorf("parse list meta: %w", err)
	}
	siteID := sharePointListWriteSite(destinationSiteID, meta.SiteID)
	if siteID == "" {
		siteID = GraphSharePointSiteIDFromKey(itemKey)
	}
	if strings.TrimSpace(meta.ID) == "" || siteID == "" {
		return "", fmt.Errorf("list meta missing id/site_id")
	}
	itemRaw, err := get(ctx, itemKey)
	if err != nil {
		return "", fmt.Errorf("read list item: %w", err)
	}
	return RestoreSharePointListItem(ctx, accessToken, siteID, meta.ID, itemRaw)
}

// sharePointListWriteSite is the Graph site a list item restores into.
func sharePointListWriteSite(destinationSiteID, metaSiteID string) string {
	if siteID := GraphSharePointSiteID(destinationSiteID); siteID != "" {
		return siteID
	}
	return GraphSharePointSiteID(metaSiteID)
}

// RestoreSharePointPageFromVault restores one pages/*.json object into siteID.
// When siteID is empty, loads {siteKey}/_site_config.json for the Graph site id.
func RestoreSharePointPageFromVault(ctx context.Context, accessToken, siteID, pageKey string, get SharePointVaultGet) (string, error) {
	if get == nil {
		return "", fmt.Errorf("vault get is required")
	}
	if !IsSharePointPageKey(pageKey) {
		return "", fmt.Errorf("not a sharepoint page key: %s", pageKey)
	}
	siteID = GraphSharePointSiteID(siteID)
	if siteID == "" {
		if fromKey := GraphSharePointSiteIDFromKey(pageKey); fromKey != "" {
			siteID = fromKey
		}
	}
	if siteID == "" {
		// pageKey = {siteKey}/pages/{id}.json
		parts := strings.Split(pageKey, "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == sharePointPagesSegment {
				siteKey := strings.Join(parts[:i], "/")
				cfgRaw, err := get(ctx, strings.TrimRight(siteKey, "/")+"/_site_config.json")
				if err != nil {
					return "", fmt.Errorf("site id missing and _site_config.json unreadable: %w", err)
				}
				var cfg struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(cfgRaw, &cfg)
				siteID = GraphSharePointSiteID(cfg.ID)
				break
			}
		}
	}
	if siteID == "" {
		return "", fmt.Errorf("site id is required")
	}
	raw, err := get(ctx, pageKey)
	if err != nil {
		return "", err
	}
	return RestoreSharePointSitePage(ctx, accessToken, siteID, raw)
}

// RestoreSharePointPermissionACE grants one permission when the principal maps safely.
// Returns error without granting broader access when mapping fails.
func RestoreSharePointPermissionACE(ctx context.Context, accessToken, resourceURL string, identity map[string]any, roles []string, directory map[string]string) error {
	principalID, ok := MapSharePointPermissionPrincipal(identity, directory)
	if !ok {
		return fmt.Errorf("permission principal unmapped; refusing broader ACL fallback")
	}
	if len(roles) == 0 {
		roles = []string{"read"}
	}
	payload, err := json.Marshal(map[string]any{
		"roles": roles,
		"grantedToIdentities": []map[string]any{
			{"user": map[string]any{"id": principalID}},
		},
	})
	if err != nil {
		return err
	}
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, strings.TrimRight(resourceURL, "/")+"/permissions", payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("grant permission http %d: %s", status, truncateForErr(resp))
	}
	return nil
}
