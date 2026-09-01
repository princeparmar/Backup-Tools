package handler

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/StorX2-0/Backup-Tools/repo"
)

var emailLocalDelimiterRE = regexp.MustCompile(`[._\-]+`)

const corporateDefaultPolicyName = "Corporate defaults"

// defaultPolicyName returns the auto-default policy name for a credential.
func defaultPolicyName(cred *repo.GoogleBackupCredentialDB) string {
	if cred == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(cred.AccountType)) {
	case "admin_workspace":
		return corporateDefaultPolicyName
	default:
		return strings.TrimSpace(cred.Email)
	}
}

// onboardingPolicyBaseName uses UI policy_name when present; otherwise the credential default
// (corporate: "Corporate defaults", personal: mailbox email).
func onboardingPolicyBaseName(cred *repo.GoogleBackupCredentialDB, req *GoogleBackupOnboardingRequest) string {
	if req != nil {
		if name := strings.TrimSpace(req.PolicyName); name != "" {
			return name
		}
	}
	return defaultPolicyName(cred)
}

// defaultOrgUnitPolicyName names a corporate policy created per Organizational Unit.
func defaultOrgUnitPolicyName(orgUnitPath string) string {
	name := strings.TrimSpace(displayNameFromOrgUnitPath(orgUnitPath))
	if name == "" || name == "/" {
		return "OU Root defaults"
	}
	return fmt.Sprintf("OU %s defaults", name)
}

func displayNameFromOrgUnitPath(orgUnitPath string) string {
	path := strings.TrimSpace(orgUnitPath)
	if path == "" || path == "/" {
		return "/"
	}
	path = strings.TrimSuffix(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		seg := strings.TrimSpace(path[i+1:])
		if seg != "" {
			return seg
		}
	}
	return path
}

// displayNameFromMailboxEmail humanizes a mailbox email for UI display.
func displayNameFromMailboxEmail(email string) string {
	local := strings.TrimSpace(nameFromMailboxEmail(email))
	if local == "" {
		return email
	}
	rawParts := emailLocalDelimiterRE.Split(local, -1)
	words := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		for _, chunk := range splitAlphaNumChunks(p) {
			words = append(words, titleCaseWord(chunk))
		}
	}
	if len(words) == 0 {
		return titleCaseWord(local)
	}
	return strings.Join(words, " ")
}

func splitAlphaNumChunks(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var buf strings.Builder
	var prevKind runeKind
	for _, r := range s {
		kind := runeKindOf(r)
		if buf.Len() > 0 && kind != prevKind && prevKind != kindUnknown {
			out = append(out, buf.String())
			buf.Reset()
		}
		buf.WriteRune(r)
		prevKind = kind
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}

type runeKind int

const (
	kindUnknown runeKind = iota
	kindLetter
	kindDigit
	kindOther
)

func runeKindOf(r rune) runeKind {
	switch {
	case unicode.IsLetter(r):
		return kindLetter
	case unicode.IsDigit(r):
		return kindDigit
	default:
		return kindOther
	}
}

func titleCaseWord(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(strings.ToLower(s))
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func isPersonalCredential(cred *repo.GoogleBackupCredentialDB) bool {
	if cred == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(cred.AccountType)) {
	case "admin_workspace", "employee_workspace":
		return false
	default:
		return true
	}
}

// isFirstOnboardingConnection decides whether onboarding may auto-create a default policy.
// Personal: only the user's very first policy (no existing policies).
// Corporate: first jobs for this credential (auto "Corporate defaults").
func isFirstOnboardingConnection(cred *repo.GoogleBackupCredentialDB, credentialHasJobs, userHasPolicies bool) bool {
	if isPersonalCredential(cred) {
		return !userHasPolicies
	}
	return !credentialHasJobs
}

func policyNameConflictResponse(name string) map[string]interface{} {
	return map[string]interface{}{
		"message": "Policy name already exists",
		"error":   fmt.Sprintf("a policy named %q already exists for this user", name),
	}
}
