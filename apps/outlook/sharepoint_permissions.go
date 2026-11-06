package outlook

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// FetchSharePointSitePermissions returns caller-visible site permissions JSON.
 // Never treat this as a complete ACL inventory.
func FetchSharePointSitePermissions(ctx context.Context, accessToken, siteID string) ([]byte, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return nil, fmt.Errorf("site id is required")
	}
	reqURL := graphBaseURL + "/sites/" + url.PathEscape(siteID) + "/permissions"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("site permissions http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}

// FetchSharePointDriveRootPermissions returns caller-visible permissions on a drive root.
func FetchSharePointDriveRootPermissions(ctx context.Context, accessToken, driveID string) ([]byte, error) {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return nil, fmt.Errorf("drive id is required")
	}
	reqURL := DriveRootURLFromDriveID(driveID) + "/root/permissions"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("drive permissions http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}

// FetchDriveItemVersions lists version metadata for a drive item (no content download).
func FetchDriveItemVersions(ctx context.Context, accessToken, driveID, itemID string) ([]byte, error) {
	driveID, itemID = strings.TrimSpace(driveID), strings.TrimSpace(itemID)
	if driveID == "" || itemID == "" {
		return nil, fmt.Errorf("drive id and item id are required")
	}
	reqURL := DriveRootURLFromDriveID(driveID) + "/items/" + url.PathEscape(itemID) + "/versions"
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("item versions http %d: %s", status, truncateForErr(body))
	}
	return body, nil
}

// MapSharePointPermissionPrincipal resolves a Graph permission identity for restore.
 // Returns ok=false when the principal cannot be mapped safely (deleted user, missing group,
 // guest without mapping, display-name-only). Never widen ACL as a fallback.
 //
 // When directory is non-nil, a directory hit is required (fail closed). Empty mapped value
 // means intentionally blocked. When directory is nil, same-tenant id/UPN/email passthrough
 // is allowed; displayName alone is never enough.
func MapSharePointPermissionPrincipal(identity map[string]any, directory map[string]string) (principalID string, ok bool) {
	if identity == nil {
		return "", false
	}
	var passthrough string
	for _, key := range []string{"id", "userPrincipalName", "email", "displayName"} {
		v, _ := identity[key].(string)
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if directory != nil {
			if mapped, found := directory[strings.ToLower(v)]; found {
				if mapped == "" {
					return "", false
				}
				return mapped, true
			}
			continue
		}
		if key != "displayName" && passthrough == "" {
			passthrough = v
		}
	}
	if directory != nil {
		return "", false
	}
	if passthrough != "" {
		return passthrough, true
	}
	return "", false
}
