package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Capability names (shared contract capabilities keys).
const (
	CapabilityListUsers    = "list_users"
	CapabilityMail         = "mail_backup"
	CapabilityCalendar     = "calendar_backup"
	CapabilityContacts     = "contacts_backup"
	CapabilityOneDrive     = "onedrive_backup"
	CapabilitySharePoint   = "sharepoint_backup"
	CapabilityTeamsChannel = "teams_channel_backup"
	CapabilityGroups       = "groups_backup"
)

// Capability error codes (shared contract capability_errors[*].code).
const (
	CapabilityErrMissingRole    = "missing_role"
	CapabilityErrNotProvisioned = "not_provisioned"
	CapabilityErrForbidden      = "forbidden"
	CapabilityErrTemporary      = "temporary"
)

// Capability status values stored on the tenant row.
const (
	CapabilityStatusProbed    = "probed"
	CapabilityStatusRolesOnly = "roles_only"
)

// CapabilityProbeVersion is bumped when probe semantics change so stale rows can be re-probed.
const CapabilityProbeVersion = 1

// MaxCapabilitySampleUsers bounds per-user workload probes.
const MaxCapabilitySampleUsers = 3

// CapabilityOrder is the stable capability list.
var CapabilityOrder = []string{
	CapabilityListUsers, CapabilityMail, CapabilityCalendar, CapabilityContacts,
	CapabilityOneDrive, CapabilitySharePoint, CapabilityTeamsChannel, CapabilityGroups,
}

// CapabilityRequiredRoles lists the Graph application permissions each capability needs.
var CapabilityRequiredRoles = map[string][]string{
	CapabilityListUsers:    {"User.Read.All"},
	CapabilityMail:         {"Mail.Read"},
	CapabilityCalendar:     {"Calendars.Read"},
	CapabilityContacts:     {"Contacts.Read"},
	CapabilityOneDrive:     {"Files.Read.All"},
	CapabilitySharePoint:   {"Sites.Read.All"},
	CapabilityTeamsChannel: {"Group.Read.All", "Channel.ReadBasic.All", "ChannelMessage.Read.All"},
	CapabilityGroups:       {"Group.Read.All"},
}

// ConsentRequiredRoles decide granted vs insufficient at consent time.
var ConsentRequiredRoles = []string{"User.Read.All", "Mail.Read"}

// roleSupersets lists granted roles that also satisfy a required role.
var roleSupersets = map[string][]string{
	"user.read.all":           {"user.readwrite.all", "directory.read.all", "directory.readwrite.all"},
	"mail.read":               {"mail.readwrite"},
	"calendars.read":          {"calendars.readwrite"},
	"contacts.read":           {"contacts.readwrite"},
	"files.read.all":          {"files.readwrite.all"},
	"sites.read.all":          {"sites.readwrite.all", "sites.fullcontrol.all"},
	"group.read.all":          {"group.readwrite.all", "directory.read.all", "directory.readwrite.all"},
	"channel.readbasic.all":   {"channel.readwrite.all"},
	"channelmessage.read.all": {},
}

// MissingApplicationRoles returns required roles not satisfied by granted (supersets count).
func MissingApplicationRoles(granted []string, required ...string) []string {
	have := make(map[string]struct{}, len(granted))
	for _, g := range granted {
		have[strings.ToLower(strings.TrimSpace(g))] = struct{}{}
	}
	var missing []string
	for _, req := range required {
		key := strings.ToLower(strings.TrimSpace(req))
		if _, ok := have[key]; ok {
			continue
		}
		satisfied := false
		for _, sup := range roleSupersets[key] {
			if _, ok := have[sup]; ok {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, req)
		}
	}
	return missing
}

// CapabilityError explains a false capability.
type CapabilityError struct {
	Code    string `json:"code"`
	Role    string `json:"role,omitempty"`
	Message string `json:"message,omitempty"`
}

// CapabilityResult is the engine output.
type CapabilityResult struct {
	Capabilities map[string]bool
	Errors       map[string]CapabilityError
	// Probed is false when no sample users were available (roles-only evaluation).
	Probed bool
}

// CapabilityInput feeds the engine.
type CapabilityInput struct {
	AccessToken   string // app-only token
	GrantedRoles  []string
	SampleUserIDs []string // licensed, enabled directory users (object id or UPN)
	Previous      map[string]bool
}

type probeOutcome int

const (
	probeOK probeOutcome = iota
	probeForbidden
	probeNotProvisioned
	probeTemporary
)

// EvaluateCapabilities runs the two-step capability check: application roles first (no Graph call
// for a missing role), then Graph probes against up to MaxCapabilitySampleUsers sample users.
// Temporary failures keep the previous value.
func EvaluateCapabilities(ctx context.Context, in CapabilityInput) CapabilityResult {
	res := CapabilityResult{
		Capabilities: map[string]bool{},
		Errors:       map[string]CapabilityError{},
	}
	samples := in.SampleUserIDs
	if len(samples) > MaxCapabilitySampleUsers {
		samples = samples[:MaxCapabilitySampleUsers]
	}
	res.Probed = len(samples) > 0

	for _, capName := range CapabilityOrder {
		if missing := MissingApplicationRoles(in.GrantedRoles, CapabilityRequiredRoles[capName]...); len(missing) > 0 {
			res.Capabilities[capName] = false
			res.Errors[capName] = CapabilityError{Code: CapabilityErrMissingRole, Role: missing[0]}
			continue
		}

		var outcome probeOutcome
		var msg string
		switch capName {
		case CapabilityListUsers:
			outcome, msg = probeTenantPath(ctx, in.AccessToken, "/users?$top=1&$select=id")
		case CapabilitySharePoint:
			outcome, msg = probeTenantPath(ctx, in.AccessToken, "/sites/root?$select=id")
		case CapabilityTeamsChannel:
			outcome, msg = probeTeamsChannel(ctx, in.AccessToken)
		case CapabilityGroups:
			outcome, msg = probeGroups(ctx, in.AccessToken)
		default:
			if !res.Probed {
				// Roles present but no directory users to probe yet; re-run after directory sync.
				res.Capabilities[capName] = true
				continue
			}
			outcome, msg = probeAcrossUsers(ctx, in.AccessToken, samples, userWorkloadPath(capName))
		}

		switch outcome {
		case probeOK:
			res.Capabilities[capName] = true
		case probeTemporary:
			res.Capabilities[capName] = in.Previous[capName]
			res.Errors[capName] = CapabilityError{Code: CapabilityErrTemporary, Message: msg}
		case probeForbidden:
			res.Capabilities[capName] = false
			res.Errors[capName] = CapabilityError{Code: CapabilityErrForbidden, Message: msg}
		case probeNotProvisioned:
			res.Capabilities[capName] = false
			res.Errors[capName] = CapabilityError{Code: CapabilityErrNotProvisioned, Message: msg}
		}
	}
	return res
}

func userWorkloadPath(capName string) func(user string) string {
	return func(user string) string {
		u := "/users/" + url.PathEscape(user)
		switch capName {
		case CapabilityMail:
			return u + "/messages?$top=1&$select=id"
		case CapabilityCalendar:
			return u + "/calendars?$top=1&$select=id"
		case CapabilityContacts:
			return u + "/contacts?$top=1&$select=id"
		case CapabilityOneDrive:
			return u + "/drive?$select=id"
		}
		return u
	}
}

// probeAcrossUsers succeeds when any sample user succeeds; not_provisioned (no license) on one user
// is not a failure if another succeeds.
func probeAcrossUsers(ctx context.Context, token string, users []string, pathFor func(string) string) (probeOutcome, string) {
	sawTemporary, sawForbidden := false, false
	lastMsg := ""
	for _, user := range users {
		outcome, msg := probeTenantPath(ctx, token, pathFor(user))
		switch outcome {
		case probeOK:
			return probeOK, ""
		case probeTemporary:
			sawTemporary = true
		case probeForbidden:
			sawForbidden = true
		}
		if msg != "" {
			lastMsg = msg
		}
	}
	switch {
	case sawTemporary:
		return probeTemporary, lastMsg
	case sawForbidden:
		return probeForbidden, lastMsg
	default:
		return probeNotProvisioned, lastMsg
	}
}

func probeTenantPath(ctx context.Context, token, path string) (probeOutcome, string) {
	body, status, err := graphDoJSON(ctx, token, http.MethodGet, graphBaseURL+path, nil)
	return classifyProbe(status, body, err)
}

func classifyProbe(status int, body []byte, err error) (probeOutcome, string) {
	if err != nil {
		return probeTemporary, err.Error()
	}
	switch {
	case status >= 200 && status < 300:
		return probeOK, ""
	case status == http.StatusTooManyRequests || status >= 500:
		return probeTemporary, fmt.Sprintf("HTTP %d", status)
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		if isNotProvisionedBody(body) {
			return probeNotProvisioned, graphErrorCode(body)
		}
		return probeForbidden, graphErrorCode(body)
	case status == http.StatusNotFound || status == http.StatusBadRequest:
		return probeNotProvisioned, graphErrorCode(body)
	default:
		return probeForbidden, fmt.Sprintf("HTTP %d: %s", status, graphErrorCode(body))
	}
}

// isNotProvisionedBody detects "no license / no mailbox / no OneDrive" responses Graph returns as 4xx.
func isNotProvisionedBody(body []byte) bool {
	code := strings.ToLower(graphErrorCode(body))
	for _, marker := range []string{"mailboxnotenabledforrestapi", "mailboxnotsupportedforrestapi", "resourcenotfound", "mysitenotfound", "notfound"} {
		if strings.Contains(code, marker) {
			return true
		}
	}
	return false
}

func graphErrorCode(body []byte) string {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	if parsed.Error.Code != "" && parsed.Error.Message != "" {
		return parsed.Error.Code + ": " + parsed.Error.Message
	}
	return parsed.Error.Code + parsed.Error.Message
}

// probeTeamsChannel lists Teams-provisioned groups, then reads one channel's messages.
// No team / channel / sample data is not_provisioned (distinct from missing_role).
func probeTeamsChannel(ctx context.Context, token string) (probeOutcome, string) {
	teamsURL := graphBaseURL + "/groups?$filter=" + url.QueryEscape("resourceProvisioningOptions/Any(x:x eq 'Team')") + "&$select=id&$top=5"
	body, status, err := graphDoJSON(ctx, token, http.MethodGet, teamsURL, nil)
	if outcome, msg := classifyProbe(status, body, err); outcome != probeOK {
		return outcome, msg
	}
	var teams struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	_ = json.Unmarshal(body, &teams)
	if len(teams.Value) == 0 {
		return probeNotProvisioned, "no Teams in tenant"
	}

	sawTemporary, sawForbidden := false, false
	lastMsg := ""
	for _, team := range teams.Value {
		chBody, chStatus, chErr := graphDoJSON(ctx, token, http.MethodGet,
			graphBaseURL+"/teams/"+url.PathEscape(team.ID)+"/channels?$select=id", nil)
		outcome, msg := classifyProbe(chStatus, chBody, chErr)
		if outcome != probeOK {
			switch outcome {
			case probeTemporary:
				sawTemporary = true
			case probeForbidden:
				sawForbidden = true
			}
			lastMsg = msg
			continue
		}
		var channels struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		_ = json.Unmarshal(chBody, &channels)
		if len(channels.Value) == 0 {
			continue
		}
		msgURL := graphBaseURL + "/teams/" + url.PathEscape(team.ID) + "/channels/" + url.PathEscape(channels.Value[0].ID) + "/messages?$top=1"
		mBody, mStatus, mErr := graphDoJSON(ctx, token, http.MethodGet, msgURL, nil)
		outcome, msg = classifyProbe(mStatus, mBody, mErr)
		switch outcome {
		case probeOK:
			return probeOK, ""
		case probeTemporary:
			sawTemporary = true
		case probeForbidden:
			sawForbidden = true
		}
		lastMsg = msg
	}
	switch {
	case sawTemporary:
		return probeTemporary, lastMsg
	case sawForbidden:
		return probeForbidden, lastMsg
	default:
		if lastMsg == "" {
			lastMsg = "no Teams channels with data"
		}
		return probeNotProvisioned, lastMsg
	}
}

func probeGroups(ctx context.Context, token string) (probeOutcome, string) {
	groupsURL := graphBaseURL + "/groups?$filter=" + url.QueryEscape("groupTypes/any(c:c eq 'Unified')") + "&$select=id&$top=1"
	body, status, err := graphDoJSON(ctx, token, http.MethodGet, groupsURL, nil)
	if outcome, msg := classifyProbe(status, body, err); outcome != probeOK {
		return outcome, msg
	}
	var parsed struct {
		Value []json.RawMessage `json:"value"`
	}
	_ = json.Unmarshal(body, &parsed)
	if len(parsed.Value) == 0 {
		return probeNotProvisioned, "no Microsoft 365 groups in tenant"
	}
	return probeOK, ""
}
