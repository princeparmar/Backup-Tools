package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// EnsureEventAttachmentBytes fills file attachment contentBytes on a backed-up Graph event
// (and any exceptionOccurrences on a series master). Failures return the original raw with err.
func EnsureEventAttachmentBytes(ctx context.Context, accessToken, userBase, eventID string, raw []byte) ([]byte, error) {
	out, err := fillEventAttachmentBytes(ctx, accessToken, userBase, eventID, raw)
	if err != nil {
		return raw, err
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(out, &doc) != nil {
		return out, nil
	}
	exRaw, ok := doc["exceptionOccurrences"]
	if !ok || len(exRaw) == 0 || string(exRaw) == "null" {
		return out, nil
	}
	var exceptions []json.RawMessage
	if json.Unmarshal(exRaw, &exceptions) != nil || len(exceptions) == 0 {
		return out, nil
	}
	changed := false
	for i := range exceptions {
		var head struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(exceptions[i], &head) != nil || strings.TrimSpace(head.ID) == "" {
			continue
		}
		filled, fillErr := fillEventAttachmentBytes(ctx, accessToken, userBase, head.ID, exceptions[i])
		if fillErr != nil {
			return raw, fillErr
		}
		if string(filled) != string(exceptions[i]) {
			exceptions[i] = filled
			changed = true
		}
	}
	if !changed {
		return out, nil
	}
	encoded, err := json.Marshal(exceptions)
	if err != nil {
		return out, err
	}
	doc["exceptionOccurrences"] = encoded
	return json.Marshal(doc)
}

func fillEventAttachmentBytes(ctx context.Context, accessToken, userBase, eventID string, raw []byte) ([]byte, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return raw, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw, nil
	}
	atts, err := eventAttachmentRawList(doc)
	if err != nil {
		return nil, err
	}
	hasAttachments := false
	if v, ok := doc["hasAttachments"]; ok {
		_ = json.Unmarshal(v, &hasAttachments)
	}
	listedFromEmpty := false
	if len(atts) == 0 && hasAttachments {
		listed, listErr := listEventAttachments(ctx, accessToken, userBase, eventID)
		if listErr != nil {
			return nil, listErr
		}
		atts = listed
		listedFromEmpty = len(atts) > 0
	}
	if len(atts) == 0 {
		return raw, nil
	}

	filledAny := false
	for i := range atts {
		var meta struct {
			ODataType    string `json:"@odata.type"`
			ID           string `json:"id"`
			ContentBytes string `json:"contentBytes"`
			Name         string `json:"name"`
			ContentType  string `json:"contentType"`
			IsInline     bool   `json:"isInline"`
			ContentID    string `json:"contentId"`
		}
		if err := json.Unmarshal(atts[i], &meta); err != nil {
			continue
		}
		if meta.ODataType != "" && meta.ODataType != "#microsoft.graph.fileAttachment" {
			continue
		}
		if strings.TrimSpace(meta.ContentBytes) != "" {
			continue
		}
		attID := strings.TrimSpace(meta.ID)
		if attID == "" {
			continue
		}
		filled, fetchErr := fetchEventFileAttachmentJSON(ctx, accessToken, userBase, eventID, attID, meta.Name, meta.ContentType, meta.IsInline, meta.ContentID)
		if fetchErr != nil {
			return nil, fmt.Errorf("event attachment %q: %w", meta.Name, fetchErr)
		}
		atts[i] = filled
		filledAny = true
	}
	if !filledAny && !listedFromEmpty {
		return raw, nil
	}
	encoded, err := json.Marshal(atts)
	if err != nil {
		return nil, err
	}
	doc["attachments"] = encoded
	return json.Marshal(doc)
}

func eventAttachmentRawList(doc map[string]json.RawMessage) ([]json.RawMessage, error) {
	raw, ok := doc["attachments"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var atts []json.RawMessage
	if err := json.Unmarshal(raw, &atts); err != nil {
		return nil, fmt.Errorf("decode event attachments: %w", err)
	}
	return atts, nil
}

func listEventAttachments(ctx context.Context, accessToken, userBase, eventID string) ([]json.RawMessage, error) {
	reqURL := strings.TrimRight(userBase, "/") + "/events/" + urlPathEscape(eventID) + "/attachments?$top=100"
	var out []json.RawMessage
	for reqURL != "" {
		body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("list event attachments http %d: %s", status, truncateForErr(body))
		}
		var page struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode event attachment list: %w", err)
		}
		out = append(out, page.Value...)
		reqURL = strings.TrimSpace(page.NextLink)
	}
	return out, nil
}

func fetchEventFileAttachmentJSON(ctx context.Context, accessToken, userBase, eventID, attachmentID, name, contentType string, isInline bool, contentID string) (json.RawMessage, error) {
	metaURL := strings.TrimRight(userBase, "/") + "/events/" + urlPathEscape(eventID) + "/attachments/" + urlPathEscape(attachmentID)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	if status >= 200 && status < 300 {
		var meta struct {
			ContentBytes string `json:"contentBytes"`
		}
		if json.Unmarshal(body, &meta) == nil && strings.TrimSpace(meta.ContentBytes) != "" {
			return body, nil
		}
	}
	raw, status, err := graphExchange(ctx, accessToken, http.MethodGet, metaURL+"/$value", nil, nil, 32<<20)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("event attachment $value http %d: %s", status, truncateForErr(raw))
	}
	payload := map[string]interface{}{
		"@odata.type":  "#microsoft.graph.fileAttachment",
		"id":           attachmentID,
		"name":         name,
		"contentBytes": base64.StdEncoding.EncodeToString(raw),
		"isInline":     isInline,
	}
	if contentType != "" {
		payload["contentType"] = contentType
	}
	if contentID != "" {
		payload["contentId"] = contentID
	}
	return json.Marshal(payload)
}

// RestoreEventFileAttachments posts file attachments from a backed-up event onto createdEventID.
// Non-file attachments are skipped (warn-only). Empty or missing attachments are a no-op.
func RestoreEventFileAttachments(ctx context.Context, accessToken, userBase, createdEventID string, raw []byte) error {
	createdEventID = strings.TrimSpace(createdEventID)
	if createdEventID == "" {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	atts, err := eventAttachmentRawList(doc)
	if err != nil || len(atts) == 0 {
		return err
	}
	var failed []string
	for _, att := range atts {
		var meta struct {
			ODataType    string `json:"@odata.type"`
			Name         string `json:"name"`
			ContentBytes string `json:"contentBytes"`
			ContentType  string `json:"contentType"`
			IsInline     bool   `json:"isInline"`
			ContentID    string `json:"contentId"`
		}
		if json.Unmarshal(att, &meta) != nil {
			continue
		}
		if meta.ODataType != "" && meta.ODataType != "#microsoft.graph.fileAttachment" {
			continue
		}
		if strings.TrimSpace(meta.ContentBytes) == "" {
			continue
		}
		payload := map[string]interface{}{
			"@odata.type":  "#microsoft.graph.fileAttachment",
			"name":         meta.Name,
			"contentBytes": meta.ContentBytes,
			"isInline":     meta.IsInline,
		}
		if meta.ContentType != "" {
			payload["contentType"] = meta.ContentType
		}
		if meta.ContentID != "" {
			payload["contentId"] = meta.ContentID
		}
		body, err := json.Marshal(payload)
		if err != nil {
			failed = append(failed, meta.Name)
			continue
		}
		resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost,
			strings.TrimRight(userBase, "/")+"/events/"+urlPathEscape(createdEventID)+"/attachments", body)
		if err != nil || status < 200 || status >= 300 {
			failed = append(failed, meta.Name)
			_ = resp
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d event attachment(s) not restored: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
