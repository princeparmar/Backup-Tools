package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrOutlookContactsDeltaInvalid means the saved contacts deltaLink can no longer be used.
var ErrOutlookContactsDeltaInvalid = fmt.Errorf("outlook contacts delta cursor invalid; rebaseline required")

// OutlookContactsLayoutPhotos embeds contact photos in each backed-up contact JSON.
// Prefer OutlookContactsLayoutFull for the current vault shape (photos + anniversary).
const OutlookContactsLayoutPhotos = 1

// ContactsDeltaURL is the initial Graph contacts delta URL for a folder ("" = default Contacts).
// No $select: changed rows must carry the full contact JSON the vault restores.
func ContactsDeltaURL(userBase, folderID string) string {
	base := strings.TrimRight(strings.TrimSpace(userBase), "/")
	folderID = strings.TrimSpace(folderID)
	if folderID == "" {
		return base + "/contacts/delta"
	}
	return base + "/contactFolders/" + url.PathEscape(folderID) + "/contacts/delta"
}

// ContactsDeltaPage is one page of a Graph contacts delta response.
type ContactsDeltaPage struct {
	Items     []PIMItem
	NextLink  string
	DeltaLink string
}

type graphContactsDeltaResponse struct {
	Value     []json.RawMessage `json:"value"`
	NextLink  string            `json:"@odata.nextLink"`
	DeltaLink string            `json:"@odata.deltaLink"`
}

// FetchContactsDeltaPage GETs an absolute Graph contacts delta URL.
func FetchContactsDeltaPage(ctx context.Context, accessToken, requestURL string) (*ContactsDeltaPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return nil, fmt.Errorf("contacts delta url is required")
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusGone || isDeltaResyncRequired(body) {
		return nil, ErrOutlookContactsDeltaInvalid
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("contacts delta http %d: %s", status, truncateForErr(body))
	}
	var parsed graphContactsDeltaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode contacts delta: %w", err)
	}
	out := &ContactsDeltaPage{
		NextLink:  strings.TrimSpace(parsed.NextLink),
		DeltaLink: strings.TrimSpace(parsed.DeltaLink),
		Items:     make([]PIMItem, 0, len(parsed.Value)),
	}
	for _, raw := range parsed.Value {
		var head struct {
			ID      string `json:"id"`
			ChangeKey string `json:"changeKey"`
			Removed *struct {
				Reason string `json:"reason"`
			} `json:"@removed"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID == "" {
			continue
		}
		out.Items = append(out.Items, PIMItem{
			ID: head.ID, ChangeKey: head.ChangeKey, Raw: raw, Removed: head.Removed != nil,
		})
	}
	return out, nil
}
