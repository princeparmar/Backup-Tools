package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	// MailboxFallbackEstimateBytes is used when Graph cannot report a mailbox's item count.
	MailboxFallbackEstimateBytes int64 = 2 * 1024 * 1024 * 1024
	// OneDriveFallbackEstimateBytes is used when Graph cannot report a drive's quota usage.
	OneDriveFallbackEstimateBytes int64 = 5 * 1024 * 1024 * 1024
	// averageMailMessageBytes approximates one message with typical attachments.
	averageMailMessageBytes int64 = 75 * 1024
)

// EstimateMailboxBytes estimates a mailbox's backup size from the item counts of its
// top-level mail folders (subfolders are not walked, so this undercounts deep hierarchies).
// userBaseURL is /me or /users/{id} (see UserBaseURL).
func EstimateMailboxBytes(ctx context.Context, accessToken, userBaseURL string) (int64, error) {
	reqURL := strings.TrimRight(strings.TrimSpace(userBaseURL), "/") + "/mailFolders?$select=totalItemCount&$top=250"
	type folderRow struct {
		TotalItemCount int64 `json:"totalItemCount"`
	}
	rows, err := graphListAll[folderRow](ctx, accessToken, reqURL, 0)
	if err != nil {
		return 0, fmt.Errorf("estimate mailbox: %w", err)
	}
	var items int64
	for _, r := range rows {
		if r.TotalItemCount > 0 {
			items += r.TotalItemCount
		}
	}
	return items * averageMailMessageBytes, nil
}

// EstimateOneDriveBytes returns the used bytes of the user's OneDrive. A user without a
// provisioned drive reports 0.
func EstimateOneDriveBytes(ctx context.Context, accessToken, userBaseURL string) (int64, error) {
	reqURL := strings.TrimRight(strings.TrimSpace(userBaseURL), "/") + "/drive?$select=quota"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("estimate onedrive: %w", err)
	}
	if status == http.StatusNotFound {
		return 0, nil
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("estimate onedrive http %d: %s", status, truncateForErr(body))
	}
	var parsed struct {
		Quota struct {
			Used int64 `json:"used"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("parse onedrive quota: %w", err)
	}
	if parsed.Quota.Used < 0 {
		return 0, nil
	}
	return parsed.Quota.Used, nil
}
