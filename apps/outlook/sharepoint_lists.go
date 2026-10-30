package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	SharePointListMetaName  = "_list.json"
	SharePointListIndexName = "_index.json"
	sharePointListsSegment  = "lists"
)

// SharePointListDir is {siteKey}/lists/{listId}/ (legacy) or {siteKey}/lists/{name}_{listId}/
// when displayName is set so the vault UI can show human list names.
func SharePointListDir(siteKey, listID string, displayName ...string) string {
	seg := pimKeySegment(listID)
	if len(displayName) > 0 {
		if label := strings.TrimSpace(displayName[0]); label != "" {
			seg = SanitizeOneDrivePathSegment(label) + "_" + seg
		}
	}
	return strings.TrimRight(siteKey, "/") + "/" + sharePointListsSegment + "/" + seg + "/"
}

// SharePointListDisplayName returns a human label from a lists/... vault segment or object key.
func SharePointListDisplayName(segmentOrKey string) string {
	base := segmentOrKey
	if i := strings.LastIndex(segmentOrKey, "/"); i >= 0 {
		// Prefer the lists/{segment}/ part when given a full key.
		parts := strings.Split(strings.Trim(segmentOrKey, "/"), "/")
		for j := 0; j+1 < len(parts); j++ {
			if parts[j] == sharePointListsSegment {
				base = parts[j+1]
				break
			}
		}
		if base == segmentOrKey {
			base = segmentOrKey[i+1:]
		}
	}
	base = strings.TrimSuffix(base, ".json")
	if i := strings.LastIndex(base, "_"); i > 0 {
		return base[:i]
	}
	return base
}

// SharePointListItemKey is {listDir}{itemId}.json.
func SharePointListItemKey(listDir, itemID string) string {
	return listDir + pimKeySegment(itemID) + ".json"
}

// SharePointListItemAttachmentKey stores attachment bytes beside the item.
func SharePointListItemAttachmentKey(listDir, itemID, attachmentID, name string) string {
	return listDir + "attachments/" + pimKeySegment(itemID) + "/" + pimKeySegment(attachmentID) + "_" + SanitizeOneDrivePathSegment(name)
}

// SharePointListItem is one list item from Graph (full fields JSON in Raw).
type SharePointListItem struct {
	ID        string
	ChangeKey string
	Raw       json.RawMessage
}

// SharePointListItemIndex tracks backed-up items for one list (preserve-only; no purge).
type SharePointListItemIndex struct {
	V     int                                     `json:"v,omitempty"`
	Items map[string]SharePointListItemIndexEntry `json:"items"`
}

type SharePointListItemIndexEntry struct {
	Key       string `json:"key"`
	ChangeKey string `json:"change_key,omitempty"`
}

const SharePointListIndexVersion = 1

// SharePointListItemsURL pages items of a list.
func SharePointListItemsURL(siteID, listID string) string {
	return graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/lists/" + url.PathEscape(listID) +
		"/items?$expand=fields&$select=id,eTag&$top=100"
}

// ListSharePointListItemsPage GETs one page of list items.
func ListSharePointListItemsPage(ctx context.Context, accessToken, pageURL string) ([]SharePointListItem, string, error) {
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("sharepoint list items http %d: %s", status, truncateForErr(body))
	}
	var page struct {
		Value    []json.RawMessage `json:"value"`
		NextLink string            `json:"@odata.nextLink"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("decode list items: %w", err)
	}
	items := make([]SharePointListItem, 0, len(page.Value))
	for _, raw := range page.Value {
		var head struct {
			ID   string `json:"id"`
			ETag string `json:"eTag"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID == "" {
			continue
		}
		items = append(items, SharePointListItem{ID: head.ID, ChangeKey: head.ETag, Raw: raw})
	}
	return items, page.NextLink, nil
}

// ListSharePointItemAttachments lists file attachments on a list item.
func ListSharePointItemAttachments(ctx context.Context, accessToken, siteID, listID, itemID string) ([]json.RawMessage, error) {
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/lists/" + url.PathEscape(listID) +
		"/items/" + url.PathEscape(itemID) + "/attachments"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("list item attachments http %d: %s", status, truncateForErr(body))
	}
	var page struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return page.Value, nil
}

// DownloadSharePointItemAttachment fetches attachment content bytes.
func DownloadSharePointItemAttachment(ctx context.Context, accessToken, siteID, listID, itemID, attachmentID string) ([]byte, string, error) {
	metaURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/lists/" + url.PathEscape(listID) +
		"/items/" + url.PathEscape(itemID) + "/attachments/" + url.PathEscape(attachmentID)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fmt.Errorf("attachment meta http %d: %s", status, truncateForErr(body))
	}
	var meta struct {
		Name         string `json:"name"`
		ContentType  string `json:"contentType"`
		ContentBytes string `json:"contentBytes"`
	}
	_ = json.Unmarshal(body, &meta)
	if strings.TrimSpace(meta.ContentBytes) != "" {
		raw, err := base64.StdEncoding.DecodeString(meta.ContentBytes)
		if err != nil {
			return nil, meta.Name, fmt.Errorf("decode attachment: %w", err)
		}
		return raw, meta.Name, nil
	}
	raw, status, err := graphExchange(ctx, accessToken, http.MethodGet, metaURL+"/$value", nil, nil, 32<<20)
	if err != nil {
		return nil, meta.Name, err
	}
	if status < 200 || status >= 300 {
		return nil, meta.Name, fmt.Errorf("attachment $value http %d: %s", status, truncateForErr(raw))
	}
	return raw, meta.Name, nil
}

// RestoreSharePointListItem creates a list item from backed-up fields JSON.
func RestoreSharePointListItem(ctx context.Context, accessToken, siteID, listID string, raw []byte) (string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse list item: %w", err)
	}
	fieldsRaw, ok := doc["fields"]
	if !ok {
		return "", fmt.Errorf("list item missing fields")
	}
	var fields map[string]any
	if err := json.Unmarshal(fieldsRaw, &fields); err != nil {
		return "", fmt.Errorf("parse list item fields: %w", err)
	}
	stripSharePointListCreateFields(fields)
	payload, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return "", err
	}
	siteID = GraphSharePointSiteID(siteID)
	createURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/lists/" + url.PathEscape(listID) + "/items"
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, createURL, payload)
	if err != nil {
		return "", err
	}
	if status == http.StatusBadRequest {
		stripSharePointListLookupFields(fields)
		if retry, mErr := json.Marshal(map[string]any{"fields": fields}); mErr == nil {
			resp, status, err = graphDoJSONWrite(ctx, accessToken, http.MethodPost, createURL, retry)
			if err != nil {
				return "", err
			}
		}
	}
	if status == http.StatusConflict {
		return "", nil
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("create list item http %d: %s", status, truncateForErr(resp))
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(resp, &created)
	return strings.TrimSpace(created.ID), nil
}

func stripSharePointListCreateFields(fields map[string]any) {
	for _, k := range []string{"id", "@odata.etag", "@odata.type", "ContentType", "ContentTypeId",
		"Created", "AuthorLookupId", "EditorLookupId", "Modified", "Edit", "LinkTitle", "LinkTitleNoMenu",
		"_UIVersionString", "ItemChildCount", "FolderChildCount", "Attachments"} {
		delete(fields, k)
	}
}

func stripSharePointListLookupFields(fields map[string]any) {
	for k := range fields {
		lk := strings.ToLower(k)
		if strings.HasSuffix(lk, "lookupid") || strings.Contains(lk, "lookupid") {
			delete(fields, k)
		}
	}
}

// IsSharePointListItemKey reports vault keys for list item JSON (not meta/index/attachments).
func IsSharePointListItemKey(key string) bool {
	base := path.Base(key)
	if !strings.HasSuffix(strings.ToLower(base), ".json") || strings.HasPrefix(base, "_") {
		return false
	}
	return strings.Contains(key, "/"+sharePointListsSegment+"/") && !strings.Contains(key, "/attachments/")
}

// SharePointListMetaKeyFromItemKey returns {listDir}_list.json for a list item vault key.
func SharePointListMetaKeyFromItemKey(itemKey string) (string, bool) {
	if !IsSharePointListItemKey(itemKey) {
		return "", false
	}
	dir := path.Dir(itemKey)
	if dir == "." || dir == "/" {
		return "", false
	}
	return strings.TrimRight(dir, "/") + "/" + SharePointListMetaName, true
}

// IsSharePointPageKey reports vault keys under {site}/pages/*.json.
func IsSharePointPageKey(key string) bool {
	base := path.Base(key)
	if !strings.HasSuffix(strings.ToLower(base), ".json") {
		return false
	}
	return strings.Contains(key, "/"+sharePointPagesSegment+"/")
}
