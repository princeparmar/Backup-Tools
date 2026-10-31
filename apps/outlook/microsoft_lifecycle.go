package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Directory object lifecycle states (DirectoryObjectState).
const (
	ObjectStateActive   = "active"
	ObjectStateDisabled = "disabled"
	// ObjectStateDeleted is soft-deleted: in deletedItems and restorable for 30 days.
	ObjectStateDeleted = "deleted"
	// ObjectStateGone is neither live nor in deletedItems (hard-deleted).
	ObjectStateGone = "gone"
)

// Directory object kinds for DirectoryObjectState.
const (
	DirectoryKindUser  = "users"
	DirectoryKindGroup = "groups"
)

// DirectoryObjectState reads a user or group by object ID (app-only token). A 404 is followed by a
// /directory/deletedItems lookup so a soft-deleted object is told apart from a hard-deleted one.
// Any other failure is returned as an error and must not change the stored state.
func DirectoryObjectState(ctx context.Context, appToken, kind, objectID string) (string, error) {
	objectID = strings.TrimSpace(objectID)
	if objectID == "" {
		return "", fmt.Errorf("object id is required")
	}
	if kind != DirectoryKindUser && kind != DirectoryKindGroup {
		return "", fmt.Errorf("unsupported directory kind %q", kind)
	}
	sel := "id"
	if kind == DirectoryKindUser {
		sel = "id,accountEnabled"
	}
	body, status, err := graphDoJSON(ctx, appToken, http.MethodGet,
		graphBaseURL+"/"+kind+"/"+url.PathEscape(objectID)+"?$select="+sel, nil)
	if err != nil {
		return "", err
	}
	switch {
	case status >= 200 && status < 300:
		var row struct {
			AccountEnabled *bool `json:"accountEnabled"`
		}
		if err := json.Unmarshal(body, &row); err != nil {
			return "", err
		}
		if row.AccountEnabled != nil && !*row.AccountEnabled {
			return ObjectStateDisabled, nil
		}
		return ObjectStateActive, nil
	case status != http.StatusNotFound:
		return "", fmt.Errorf("graph %s/%s: HTTP %d: %s", kind, objectID, status, truncateForErr(body))
	}

	body, status, err = graphDoJSON(ctx, appToken, http.MethodGet,
		graphBaseURL+"/directory/deletedItems/"+url.PathEscape(objectID)+"?$select=id", nil)
	if err != nil {
		return "", err
	}
	switch {
	case status >= 200 && status < 300:
		return ObjectStateDeleted, nil
	case status == http.StatusNotFound:
		return ObjectStateGone, nil
	}
	return "", fmt.Errorf("graph deletedItems/%s: HTTP %d: %s", objectID, status, truncateForErr(body))
}

// resourceMissingCodes are Graph error codes meaning the user/group, or its mailbox or OneDrive,
// does not exist (as opposed to one missing item inside it).
var resourceMissingCodes = []string{
	"Request_ResourceNotFound",
	"ErrorInvalidUser",
	"ErrorNonExistentMailbox",
	"MailboxNotEnabledForRESTAPI",
	"mysite not found",
}

// IsResourceMissingError reports whether a Graph failure means the backed-up user/group or its
// service is missing. DirectoryObjectState tells a deleted object from a missing service.
func IsResourceMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, code := range resourceMissingCodes {
		if strings.Contains(msg, strings.ToLower(code)) {
			return true
		}
	}
	return false
}
