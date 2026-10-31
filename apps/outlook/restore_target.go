package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// RestoreWriteRoles are the application roles an app-only restore needs per Microsoft method.
var RestoreWriteRoles = map[string][]string{
	"outlook":            {"Mail.ReadWrite"},
	"outlook_calendar":   {"Calendars.ReadWrite"},
	"outlook_contacts":   {"Contacts.ReadWrite"},
	"outlook_onedrive":   {"Files.ReadWrite.All"},
	"outlook_sharepoint": {"Sites.ReadWrite.All"},
}

// MissingRestoreWriteRoles returns the application roles accessToken lacks to restore method.
func MissingRestoreWriteRoles(accessToken, method string) []string {
	return MissingApplicationRoles(TokenRoles(accessToken), RestoreWriteRoles[method]...)
}

// TenantOrganization is the display name and verified domains of a tenant.
type TenantOrganization struct {
	Name          string
	DefaultDomain string
	Domains       []string
}

// GetTenantOrganization reads /organization (User.Read.All is enough for app-only tokens).
func GetTenantOrganization(ctx context.Context, accessToken string) (TenantOrganization, error) {
	var page struct {
		Value []struct {
			DisplayName     string `json:"displayName"`
			VerifiedDomains []struct {
				Name      string `json:"name"`
				IsDefault bool   `json:"isDefault"`
			} `json:"verifiedDomains"`
		} `json:"value"`
	}
	if err := graphGetInto(ctx, accessToken, graphBaseURL+"/organization?$select=displayName,verifiedDomains", &page); err != nil {
		return TenantOrganization{}, fmt.Errorf("read organization: %w", err)
	}
	if len(page.Value) == 0 {
		return TenantOrganization{}, errors.New("read organization: empty response")
	}
	org := TenantOrganization{Name: strings.TrimSpace(page.Value[0].DisplayName)}
	for _, d := range page.Value[0].VerifiedDomains {
		name := strings.ToLower(strings.TrimSpace(d.Name))
		if name == "" {
			continue
		}
		org.Domains = append(org.Domains, name)
		if d.IsDefault {
			org.DefaultDomain = name
		}
	}
	if org.DefaultDomain == "" && len(org.Domains) > 0 {
		org.DefaultDomain = org.Domains[0]
	}
	return org, nil
}

// ErrDirectoryUserNotFound: no user in the tenant has the email as UPN or primary mail.
var ErrDirectoryUserNotFound = errors.New("user not found in the tenant directory")

// FindDirectoryUser finds the tenant user whose UPN or mail is email.
func FindDirectoryUser(ctx context.Context, accessToken, email string) (DirectoryUser, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return DirectoryUser{}, ErrDirectoryUserNotFound
	}
	q := strings.ReplaceAll(email, "'", "''")
	reqURL := graphBaseURL + "/users?$select=id,userPrincipalName,mail,displayName,accountEnabled&$top=2&$filter=" +
		url.QueryEscape("userPrincipalName eq '"+q+"' or mail eq '"+q+"'")
	var page struct {
		Value []graphDirectoryUserRow `json:"value"`
	}
	if err := graphGetInto(ctx, accessToken, reqURL, &page); err != nil {
		return DirectoryUser{}, fmt.Errorf("find tenant user: %w", err)
	}
	for _, r := range page.Value {
		if id := strings.TrimSpace(r.ID); id != "" {
			return DirectoryUser{
				ObjectID:       id,
				UPN:            strings.TrimSpace(r.UserPrincipalName),
				Mail:           strings.TrimSpace(r.Mail),
				DisplayName:    strings.TrimSpace(r.DisplayName),
				AccountEnabled: r.AccountEnabled == nil || *r.AccountEnabled,
			}, nil
		}
	}
	return DirectoryUser{}, ErrDirectoryUserNotFound
}

// ErrRestoreTargetUnavailable: the target user has no mailbox (mail, calendar, contacts) or no
// OneDrive for the restored service.
var ErrRestoreTargetUnavailable = errors.New("the destination user has no mailbox or OneDrive for this service")

// CheckRestoreTarget confirms the user at userBase has the workload method restores into.
func CheckRestoreTarget(ctx context.Context, accessToken, userBase, method string) error {
	base := strings.TrimRight(userBase, "/")
	var probe string
	switch method {
	case "outlook":
		probe = base + "/mailFolders/inbox?$select=id"
	case "outlook_calendar":
		probe = base + "/calendar?$select=id"
	case "outlook_contacts":
		probe = base + "/contactFolders?$top=1&$select=id"
	case "outlook_onedrive":
		probe = base + "/drive?$select=id"
	default:
		return nil
	}
	body, status, err := graphDoJSON(ctx, accessToken, http.MethodGet, probe, nil)
	switch outcome, msg := classifyProbe(status, body, err); outcome {
	case probeOK:
		return nil
	case probeNotProvisioned:
		return fmt.Errorf("%w (%s)", ErrRestoreTargetUnavailable, msg)
	default:
		return fmt.Errorf("check destination user: %s", msg)
	}
}

// EnsureContactFolder returns the id of the user's contact folder named name, creating it when
// missing. parentFolderID scopes the match and create under that parent; empty creates a top-level
// folder. When parentFolderID is empty, an existing folder of the same name anywhere in the tree
// is reused (migration-friendly).
func EnsureContactFolder(ctx context.Context, accessToken, userBase, name, parentFolderID string) (string, error) {
	name = strings.TrimSpace(name)
	parentFolderID = strings.TrimSpace(parentFolderID)
	if name == "" {
		return "", fmt.Errorf("contact folder name is required")
	}
	folders, err := ListPIMContactFolders(ctx, accessToken, userBase)
	if err != nil {
		return "", err
	}
	var anyMatch string
	for _, f := range folders {
		if strings.TrimSpace(f.DisplayName) != name {
			continue
		}
		if parentFolderID != "" && strings.TrimSpace(f.ParentFolderID) == parentFolderID {
			return f.ID, nil
		}
		if parentFolderID == "" && anyMatch == "" {
			anyMatch = f.ID
		}
	}
	if parentFolderID == "" && anyMatch != "" {
		return anyMatch, nil
	}
	body, err := json.Marshal(map[string]string{"displayName": name})
	if err != nil {
		return "", err
	}
	createURL := strings.TrimRight(userBase, "/") + "/contactFolders"
	if parentFolderID != "" {
		createURL = strings.TrimRight(userBase, "/") + "/contactFolders/" + urlPathEscape(parentFolderID) + "/childFolders"
	}
	resp, status, err := graphDoJSONWrite(ctx, accessToken, http.MethodPost, createURL, body)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if status < 200 || status >= 300 || json.Unmarshal(resp, &created) != nil || created.ID == "" {
		return "", fmt.Errorf("create contact folder http %d: %s", status, truncateForErr(resp))
	}
	return created.ID, nil
}
