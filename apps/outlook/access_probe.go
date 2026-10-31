package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Per-user access probe outcomes for onboarding / access-verify.
const (
	AccessProbeAccessible = "accessible"
	AccessProbeNoAccess   = "no_access"
	AccessProbeError      = "probe_error"
)

// Stable reason codes for access-verify UI and job markers.
const (
	AccessReasonOK              = ""
	AccessReasonUnavailable     = "ACCESS_UNAVAILABLE"
	AccessReasonProbeFailed     = "ACCESS_PROBE_ERROR"
	AccessReasonPending         = "ACCESS_VERIFY_PENDING"
	AccessReasonMissingResource = "ACCESS_RESOURCE_MISSING"
	AccessReasonDenied          = "ACCESS_DENIED"
)

// UserServiceAccessProbe is the result of ProbeMicrosoftUserServiceAccess.
type UserServiceAccessProbe struct {
	Status     string    `json:"status"`
	ReasonCode string    `json:"reason_code,omitempty"`
	Message    string    `json:"message,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	GraphCode  string    `json:"graph_code,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

// ProbeMicrosoftUserServiceAccess runs a lightweight Graph read for one user and one backup method.
// method is the cron method: outlook, outlook_calendar, outlook_contacts, outlook_onedrive.
func ProbeMicrosoftUserServiceAccess(ctx context.Context, token, userObjectID, method string) UserServiceAccessProbe {
	now := time.Now().UTC()
	out := UserServiceAccessProbe{CheckedAt: now}
	token = strings.TrimSpace(token)
	userObjectID = strings.TrimSpace(userObjectID)
	method = strings.TrimSpace(method)
	if token == "" || userObjectID == "" {
		out.Status = AccessProbeError
		out.ReasonCode = AccessReasonProbeFailed
		out.Message = "Access could not be verified."
		return out
	}
	if !IsAccessVerifyUserMethod(method) {
		out.Status = AccessProbeError
		out.ReasonCode = AccessReasonProbeFailed
		out.Message = "Access could not be verified for this service."
		return out
	}
	path := userWorkloadPath(capabilityNameForMethod(method))(userObjectID)
	body, status, err := graphDoJSON(ctx, token, http.MethodGet, graphBaseURL+path, nil)
	out.HTTPStatus = status
	code, msg := parseGraphError(body)
	out.GraphCode = code

	if err != nil {
		out.Status = AccessProbeError
		out.ReasonCode = AccessReasonProbeFailed
		out.Message = "Access could not be verified. Backup will retry later."
		return out
	}

	outcome, _ := classifyProbe(status, body, nil)
	switch outcome {
	case probeOK:
		out.Status = AccessProbeAccessible
		return out
	case probeTemporary:
		out.Status = AccessProbeError
		out.ReasonCode = AccessReasonProbeFailed
		out.Message = "Access could not be verified. Backup will retry later."
		return out
	case probeNotProvisioned:
		out.Status = AccessProbeNoAccess
		out.ReasonCode = AccessReasonMissingResource
		out.Message = customerNoAccessMessage(method, code, msg)
		return out
	case probeForbidden:
		if isNotProvisionedBody(body) || isPerUserAccessDenied(code) {
			out.Status = AccessProbeNoAccess
			if isPerUserAccessDenied(code) {
				out.ReasonCode = AccessReasonDenied
			} else {
				out.ReasonCode = AccessReasonMissingResource
			}
			out.Message = customerNoAccessMessage(method, code, msg)
			return out
		}
		// Tenant-wide / inconclusive permission problems must not mark confirmed no-access.
		out.Status = AccessProbeError
		out.ReasonCode = AccessReasonProbeFailed
		out.Message = "Access could not be verified. Backup will retry later."
		return out
	default:
		out.Status = AccessProbeError
		out.ReasonCode = AccessReasonProbeFailed
		out.Message = "Access could not be verified. Backup will retry later."
		return out
	}
}

func capabilityNameForMethod(method string) string {
	switch method {
	case "outlook":
		return CapabilityMail
	case "outlook_calendar":
		return CapabilityCalendar
	case "outlook_contacts":
		return CapabilityContacts
	case "outlook_onedrive":
		return CapabilityOneDrive
	default:
		return ""
	}
}

// IsAccessVerifyUserMethod reports methods covered by per-user access verification.
func IsAccessVerifyUserMethod(method string) bool {
	switch strings.TrimSpace(method) {
	case "outlook", "outlook_calendar", "outlook_contacts", "outlook_onedrive":
		return true
	default:
		return false
	}
}

func isPerUserAccessDenied(graphCode string) bool {
	c := strings.ToLower(strings.TrimSpace(graphCode))
	for _, marker := range []string{"accessdenied", "erroraccessdenied", "authorization_requestdenied", "forbidden"} {
		if strings.Contains(c, marker) {
			return true
		}
	}
	return false
}

func customerNoAccessMessage(method, graphCode, graphMessage string) string {
	if strings.TrimSpace(graphMessage) != "" {
		return strings.TrimSpace(graphMessage)
	}
	if graphCode != "" {
		return fmt.Sprintf("Microsoft did not allow access (%s).", graphCode)
	}
	switch method {
	case "outlook":
		return "Mail is not available for this account."
	case "outlook_calendar":
		return "Calendar is not available for this account."
	case "outlook_contacts":
		return "Contacts are not available for this account."
	case "outlook_onedrive":
		return "OneDrive is not available for this account."
	default:
		return "This service is not available for this account."
	}
}

func parseGraphError(body []byte) (code, message string) {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return "", ""
	}
	return strings.TrimSpace(parsed.Error.Code), strings.TrimSpace(parsed.Error.Message)
}
