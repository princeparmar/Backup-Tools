package outlook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// preferImmutableID keeps message ids stable when a message moves between folders. Every mail
// call (delta pages, fetches, restores) must send it or ids from different calls won't match.
const preferImmutableID = `IdType="ImmutableId"`

// mailMessageReadLimit covers a 150 MB message with base64 attachments.
const mailMessageReadLimit = 210 << 20

// graphMailDo sends a Graph mail request with immutable ids.
func graphMailDo(ctx context.Context, accessToken, method, reqURL string, payload []byte, readLimit int64) ([]byte, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", preferImmutableID)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if readLimit <= 0 {
		readLimit = 32 << 20
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// MailFolder is one backed-up mail folder. Path is the folder's key segment(s), e.g. "Inbox/Clients".
type MailFolder struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	ParentID      string `json:"parent_id,omitempty"`
	WellKnownName string `json:"well_known_name,omitempty"`
	Path          string `json:"path"`
	TotalItems    int    `json:"total_items"`
}

// mailWellKnownFolders maps Graph well-known folder names to the stable key segment used for
// them (independent of the mailbox language). An empty segment means the folder is not backed up.
var mailWellKnownFolders = []struct{ Name, Segment string }{
	{"inbox", "Inbox"},
	{"sentitems", "Sent Items"},
	{"drafts", "Drafts"},
	{"deleteditems", "Deleted Items"},
	{"junkemail", "Junk Email"},
	{"archive", "Archive"},
	{"outbox", "Outbox"},
	{"conversationhistory", "Conversation History"},
	{"scheduled", "Scheduled"},
	{"clutter", "Clutter"},
	{"syncissues", ""},
	{"conflicts", ""},
	{"localfailures", ""},
	{"serverfailures", ""},
}

// MailWellKnownFolderSegment returns the key segment of a well-known folder ("" if unknown or skipped).
func MailWellKnownFolderSegment(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, f := range mailWellKnownFolders {
		if f.Name == name {
			return f.Segment
		}
	}
	return ""
}

// MailWellKnownFolderForSegment is the reverse of MailWellKnownFolderSegment.
func MailWellKnownFolderForSegment(segment string) string {
	for _, f := range mailWellKnownFolders {
		if f.Segment != "" && strings.EqualFold(f.Segment, strings.TrimSpace(segment)) {
			return f.Name
		}
	}
	return ""
}

func mailWellKnownSkipped(name string) bool {
	for _, f := range mailWellKnownFolders {
		if f.Name == name {
			return f.Segment == ""
		}
	}
	return false
}

// SanitizeMailFolderSegment makes a folder display name safe as one object-key segment.
func SanitizeMailFolderSegment(name string) string {
	name = strings.NewReplacer("/", "_", "\\", "_", "\r", " ", "\n", " ", "\t", " ").Replace(name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	return name
}

type graphMailFolderRow struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	ParentFolderID   string `json:"parentFolderId"`
	ChildFolderCount int    `json:"childFolderCount"`
	TotalItemCount   int    `json:"totalItemCount"`
}

const mailFolderSelect = "$top=100&$select=id,displayName,parentFolderId,childFolderCount,totalItemCount"

// mailFolderMaxDepth bounds the folder tree walk.
const mailFolderMaxDepth = 32

// ListMailFolders walks the visible mail folder tree (hidden and search folders excluded) and
// skips Exchange system folders (Sync Issues and its children). Inbox comes first.
func ListMailFolders(ctx context.Context, accessToken, userBaseURL string) ([]MailFolder, error) {
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	if userBaseURL == "" || strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("user base and access token are required")
	}
	wellKnown, err := resolveWellKnownMailFolders(ctx, accessToken, userBaseURL)
	if err != nil {
		return nil, err
	}
	var out []MailFolder
	var walk func(listURL, parentPath string, depth int) error
	walk = func(listURL, parentPath string, depth int) error {
		for next := listURL; next != ""; {
			body, status, err := graphMailDo(ctx, accessToken, http.MethodGet, next, nil, 0)
			if err != nil {
				return err
			}
			if status < 200 || status >= 300 {
				return fmt.Errorf("list mail folders http %d: %s", status, truncateForErr(body))
			}
			var page struct {
				Value    []graphMailFolderRow `json:"value"`
				NextLink string               `json:"@odata.nextLink"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return fmt.Errorf("decode mail folders: %w", err)
			}
			for _, row := range page.Value {
				id := strings.TrimSpace(row.ID)
				if id == "" {
					continue
				}
				name := wellKnown[id]
				if mailWellKnownSkipped(name) {
					continue
				}
				segment := MailWellKnownFolderSegment(name)
				if segment == "" || parentPath != "" {
					segment = SanitizeMailFolderSegment(row.DisplayName)
				}
				folderPath := segment
				if parentPath != "" {
					folderPath = parentPath + "/" + segment
				}
				out = append(out, MailFolder{
					ID: id, DisplayName: strings.TrimSpace(row.DisplayName), ParentID: strings.TrimSpace(row.ParentFolderID),
					WellKnownName: name, Path: folderPath, TotalItems: row.TotalItemCount,
				})
				if row.ChildFolderCount > 0 && depth < mailFolderMaxDepth {
					childURL := fmt.Sprintf("%s/mailFolders/%s/childFolders?%s", userBaseURL, urlPathEscape(id), mailFolderSelect)
					if err := walk(childURL, folderPath, depth+1); err != nil {
						return err
					}
				}
			}
			next = strings.TrimSpace(page.NextLink)
		}
		return nil
	}
	if err := walk(userBaseURL+"/mailFolders?"+mailFolderSelect, "", 0); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		ii, ji := out[i].WellKnownName == "inbox", out[j].WellKnownName == "inbox"
		if ii != ji {
			return ii
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// resolveWellKnownMailFolders returns folder id -> well-known name in one $batch call. Folders a
// mailbox doesn't have (archive, scheduled, ...) are left out.
func resolveWellKnownMailFolders(ctx context.Context, accessToken, userBaseURL string) (map[string]string, error) {
	relBase := strings.TrimPrefix(userBaseURL, graphBaseURL)
	type batchReq struct {
		ID      string            `json:"id"`
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	reqs := make([]batchReq, 0, len(mailWellKnownFolders))
	for _, f := range mailWellKnownFolders {
		reqs = append(reqs, batchReq{
			ID: f.Name, Method: http.MethodGet, URL: relBase + "/mailFolders/" + f.Name + "?$select=id",
			Headers: map[string]string{"Prefer": preferImmutableID},
		})
	}
	payload, err := json.Marshal(map[string]interface{}{"requests": reqs})
	if err != nil {
		return nil, err
	}
	body, status, err := graphMailDo(ctx, accessToken, http.MethodPost, graphBaseURL+"/$batch", payload, 0)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("resolve well-known mail folders http %d: %s", status, truncateForErr(body))
	}
	var parsed struct {
		Responses []struct {
			ID     string `json:"id"`
			Status int    `json:"status"`
			Body   struct {
				ID string `json:"id"`
			} `json:"body"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode well-known mail folders: %w", err)
	}
	out := make(map[string]string, len(parsed.Responses))
	for _, r := range parsed.Responses {
		if r.Status >= 200 && r.Status < 300 && strings.TrimSpace(r.Body.ID) != "" {
			out[strings.TrimSpace(r.Body.ID)] = r.ID
		}
	}
	return out, nil
}

// TranslateMailIDsToImmutable maps legacy (rest) message ids to immutable ids, 1000 per call.
// Ids Graph can't translate are left out of the result.
func TranslateMailIDsToImmutable(ctx context.Context, accessToken, userBaseURL string, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	userBaseURL = strings.TrimRight(strings.TrimSpace(userBaseURL), "/")
	for start := 0; start < len(ids); start += 1000 {
		end := start + 1000
		if end > len(ids) {
			end = len(ids)
		}
		payload, err := json.Marshal(map[string]interface{}{
			"inputIds": ids[start:end], "sourceIdType": "restId", "targetIdType": "restImmutableEntryId",
		})
		if err != nil {
			return out, err
		}
		body, status, err := graphMailDo(ctx, accessToken, http.MethodPost, userBaseURL+"/translateExchangeIds", payload, 0)
		if err != nil {
			return out, err
		}
		if status < 200 || status >= 300 {
			return out, fmt.Errorf("translate message ids http %d: %s", status, truncateForErr(body))
		}
		var parsed struct {
			Value []struct {
				SourceID string `json:"sourceId"`
				TargetID string `json:"targetId"`
			} `json:"value"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return out, fmt.Errorf("decode translated ids: %w", err)
		}
		for _, v := range parsed.Value {
			if s, t := strings.TrimSpace(v.SourceID), strings.TrimSpace(v.TargetID); s != "" && t != "" {
				out[s] = t
			}
		}
	}
	return out, nil
}
