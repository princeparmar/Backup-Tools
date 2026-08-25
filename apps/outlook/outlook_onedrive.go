package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	graphBaseURL              = "https://graph.microsoft.com/v1.0"
	fallbackObjectKeyDatePath = "1970/01/01"
	maxGraphRetries           = 5
)

// ErrOneDriveDeltaInvalid means the saved deltaLink can no longer be used; rebaseline required.
var ErrOneDriveDeltaInvalid = fmt.Errorf("onedrive delta cursor invalid; rebaseline required")

// OneDriveItem is a file (or deleted stub) from Graph drive delta / list.
type OneDriveItem struct {
	ID                   string
	Name                 string
	Size                 int64
	CreatedDateTime      string
	LastModifiedDateTime string
	WebURL               string
	MimeType             string
	ParentID             string
	ParentPath           string // optional path from parentReference.path when present; never fetched recursively
	IsFolder             bool
	IsDeleted            bool
	ETag                 string
	CTag                 string
}

// OneDriveCronBackupMeta is JSON stored at {email}/meta/.../{itemId}_{name}.json.
type OneDriveCronBackupMeta struct {
	ItemID               string `json:"item_id"`
	Name                 string `json:"name"`
	MimeType             string `json:"mime_type,omitempty"`
	ParentID             string `json:"parent_id,omitempty"`
	ParentPath           string `json:"parent_path,omitempty"`
	WebURL               string `json:"web_url,omitempty"`
	Size                 int64  `json:"size,omitempty"`
	CreatedDateTime      string `json:"created_date_time,omitempty"`
	LastModifiedDateTime string `json:"last_modified_date_time,omitempty"`
	ETag                 string `json:"etag,omitempty"`
	CTag                 string `json:"ctag,omitempty"`
	IsFolder             bool   `json:"is_folder"`
	RemovedFromOneDrive  bool   `json:"removed_from_onedrive,omitempty"`
	DeletedAt            string `json:"deleted_at,omitempty"`
	DataObjectKey        string `json:"data_object_key,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

// OneDriveDeltaPage is one page of a Graph delta response.
type OneDriveDeltaPage struct {
	Items     []OneDriveItem
	NextLink  string
	DeltaLink string
}

type graphDeltaResponse struct {
	Value    []graphDriveItem `json:"value"`
	NextLink string           `json:"@odata.nextLink"`
	DeltaLink string          `json:"@odata.deltaLink"`
}

type graphDriveItem struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Size                 int64  `json:"size"`
	CreatedDateTime      string `json:"createdDateTime"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
	WebURL               string `json:"webUrl"`
	ETag                 string `json:"eTag"`
	CTag                 string `json:"cTag"`
	Folder               *struct {
		ChildCount int32 `json:"childCount"`
	} `json:"folder"`
	File *struct {
		MimeType string `json:"mimeType"`
	} `json:"file"`
	ParentReference *struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"parentReference"`
	Deleted *struct {
		State string `json:"state"`
	} `json:"deleted"`
}

// OneDriveDriveRootURL returns the Graph drive root for mailbox.
// Uses /me/drive when mailbox matches the signed-in user; otherwise /users/{mailbox}/drive.
func (client *OutlookClient) OneDriveDriveRootURL(mailbox string) (string, error) {
	mailbox = strings.TrimSpace(mailbox)
	user, err := client.GetCurrentUser()
	if err != nil {
		return "", err
	}
	meMail := strings.ToLower(strings.TrimSpace(user.Mail))
	meUPN := strings.ToLower(strings.TrimSpace(user.UserPrincipalName))
	mb := strings.ToLower(mailbox)
	if mailbox == "" || mb == meMail || mb == meUPN {
		return graphBaseURL + "/me/drive", nil
	}
	return graphBaseURL + "/users/" + urlPathEscape(mailbox) + "/drive", nil
}

func urlPathEscape(s string) string {
	// Graph user id / UPN in path: encode reserved chars but keep @.
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "%20")
}

// OneDriveInitialDeltaURL is GET {drive}/root/delta for first baseline.
func OneDriveInitialDeltaURL(driveRootURL string) string {
	return strings.TrimRight(strings.TrimSpace(driveRootURL), "/") + "/root/delta"
}

// FetchOneDriveDeltaPage GETs an absolute Graph URL (initial delta, nextLink, or saved deltaLink).
func FetchOneDriveDeltaPage(ctx context.Context, accessToken, requestURL string) (*OneDriveDeltaPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestURL = strings.TrimSpace(requestURL)
	accessToken = strings.TrimSpace(accessToken)
	if requestURL == "" {
		return nil, fmt.Errorf("onedrive delta url is required")
	}
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}

	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusGone || isDeltaResyncRequired(body) {
		return nil, ErrOneDriveDeltaInvalid
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("onedrive delta http %d: %s", status, truncateForErr(body))
	}

	var parsed graphDeltaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode onedrive delta: %w", err)
	}
	out := &OneDriveDeltaPage{
		NextLink:  strings.TrimSpace(parsed.NextLink),
		DeltaLink: strings.TrimSpace(parsed.DeltaLink),
		Items:     make([]OneDriveItem, 0, len(parsed.Value)),
	}
	for i := range parsed.Value {
		out.Items = append(out.Items, mapGraphDriveItem(parsed.Value[i]))
	}
	return out, nil
}

func mapGraphDriveItem(it graphDriveItem) OneDriveItem {
	item := OneDriveItem{
		ID:                   strings.TrimSpace(it.ID),
		Name:                 strings.TrimSpace(it.Name),
		Size:                 it.Size,
		CreatedDateTime:      strings.TrimSpace(it.CreatedDateTime),
		LastModifiedDateTime: strings.TrimSpace(it.LastModifiedDateTime),
		WebURL:               strings.TrimSpace(it.WebURL),
		ETag:                 strings.TrimSpace(it.ETag),
		CTag:                 strings.TrimSpace(it.CTag),
		IsFolder:             it.Folder != nil,
		IsDeleted:            it.Deleted != nil,
	}
	if it.File != nil {
		item.MimeType = strings.TrimSpace(it.File.MimeType)
	}
	if it.ParentReference != nil {
		item.ParentID = strings.TrimSpace(it.ParentReference.ID)
		item.ParentPath = strings.TrimSpace(it.ParentReference.Path)
	}
	return item
}

func isDeltaResyncRequired(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "resyncrequired") ||
		strings.Contains(s, "resync required") ||
		strings.Contains(s, "token is no longer valid") ||
		strings.Contains(s, "deltatoken") && strings.Contains(s, "invalid")
}

// OpenOneDriveItemContentStream opens a streaming GET for item content.
// Caller must Close the body. Prefer this over buffering large files.
func OpenOneDriveItemContentStream(ctx context.Context, accessToken, driveRootURL, itemID string) (io.ReadCloser, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	itemID = strings.TrimSpace(itemID)
	driveRootURL = strings.TrimRight(strings.TrimSpace(driveRootURL), "/")
	if itemID == "" || driveRootURL == "" {
		return nil, 0, fmt.Errorf("drive root and item id are required")
	}
	url := driveRootURL + "/items/" + itemID + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))

	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("onedrive content http %d: %s", resp.StatusCode, truncateForErr(b))
	}
	return resp.Body, resp.ContentLength, nil
}

// ListOneDriveFlatFilesPage lists non-folder children under drive root (browse).
// Uses /root/children with $skip/$top; not delta. Folders are filtered out.
func ListOneDriveFlatFilesPage(ctx context.Context, accessToken, driveRootURL string, skip, top int32) ([]OneDriveItem, error) {
	if top <= 0 {
		top = 50
	}
	if skip < 0 {
		skip = 0
	}
	url := fmt.Sprintf("%s/root/children?$top=%d&$skip=%d&$select=id,name,size,createdDateTime,lastModifiedDateTime,webUrl,file,folder,parentReference",
		strings.TrimRight(strings.TrimSpace(driveRootURL), "/"), top, skip)
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("onedrive list http %d: %s", status, truncateForErr(body))
	}
	var parsed graphDeltaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]OneDriveItem, 0, len(parsed.Value))
	for i := range parsed.Value {
		it := mapGraphDriveItem(parsed.Value[i])
		if it.IsFolder || it.IsDeleted || it.ID == "" {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

func graphDoJSON(ctx context.Context, accessToken, method, url string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := graphHTTPDoWithRetry(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

func graphHTTPDoWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 0} // streaming downloads; per-request ctx still applies
	var lastErr error
	for attempt := 0; attempt < maxGraphRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Clone request for retries (body is nil for our GETs).
		r := req.Clone(ctx)
		resp, err := client.Do(r)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 400 * time.Millisecond)
			continue
		}
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			wait := retryAfterDuration(resp.Header.Get("Retry-After"), attempt)
			_ = resp.Body.Close()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		default:
			return resp, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("onedrive graph request exhausted retries")
}

func retryAfterDuration(header string, attempt int) time.Duration {
	header = strings.TrimSpace(header)
	if header != "" {
		if sec, err := strconv.Atoi(header); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
		if t, err := http.ParseTime(header); err == nil {
			d := time.Until(t)
			if d > 0 {
				return d
			}
		}
	}
	return time.Duration(attempt+1) * 500 * time.Millisecond
}

func truncateForErr(b []byte) string {
	s := string(b)
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// SanitizeOneDrivePathSegment makes names safe for StorX object keys.
func SanitizeOneDrivePathSegment(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "?", "_", "*", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	)
	s = replacer.Replace(s)
	if s == "" {
		return "untitled"
	}
	return s
}

func objectKeyDatePath(created string) string {
	created = strings.TrimSpace(created)
	if created == "" {
		return fallbackObjectKeyDatePath
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		return t.UTC().Format("2006/01/02")
	}
	if t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", created); err == nil {
		return t.UTC().Format("2006/01/02")
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", created); err == nil {
		return t.UTC().Format("2006/01/02")
	}
	return fallbackObjectKeyDatePath
}

// OneDriveIDBasedMetaKey is {email}/meta/{yyyy}/{mm}/{dd}/{itemId}_{name}.json
func OneDriveIDBasedMetaKey(email, itemID, displayName, createdTime string) string {
	return fmt.Sprintf("%s/meta/%s/%s_%s.json",
		strings.TrimSpace(email),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(itemID),
		SanitizeOneDrivePathSegment(displayName),
	)
}

// OneDriveIDBasedDataKey is {email}/data/{yyyy}/{mm}/{dd}/{itemId}_{name}
func OneDriveIDBasedDataKey(email, itemID, displayName, createdTime string) string {
	return fmt.Sprintf("%s/data/%s/%s_%s",
		strings.TrimSpace(email),
		objectKeyDatePath(createdTime),
		strings.TrimSpace(itemID),
		SanitizeOneDrivePathSegment(displayName),
	)
}
