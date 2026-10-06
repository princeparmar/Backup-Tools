package outlook

import (
	"strings"
	"testing"
)

// Application (app-only) jobs must never build /me URLs: an app-only token has no signed-in user.
func TestApplicationClientNeverBuildsMeURLs(t *testing.T) {
	if _, err := NewOutlookClientForUser("app-token", ""); err == nil {
		t.Fatal("application client without a target user must fail")
	}

	client, err := NewOutlookClientForUser("app-token", "ann@contoso.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, mailbox := range []string{"", "ann@contoso.com", "bob@contoso.com"} {
		base, err := client.MailUserBaseURL(mailbox)
		if err != nil {
			t.Fatal(err)
		}
		drive, err := client.OneDriveDriveRootURL(mailbox)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range []string{base, drive} {
			if strings.Contains(u, "/me") || !strings.Contains(u, "/users/") {
				t.Fatalf("application URL for mailbox %q uses /me: %s", mailbox, u)
			}
		}
	}

	if _, err := UserBaseURL("", "", "", true); err == nil {
		t.Fatal("application URL without a mailbox must fail")
	}
	if u, _ := UserBaseURL("ann@contoso.com", "ann@contoso.com", "", true); strings.Contains(u, "/me") {
		t.Fatalf("application access must use /users even for the consenting admin: %s", u)
	}
	if u, _ := UserBaseURL("ann@contoso.com", "ann@contoso.com", "", false); !strings.HasSuffix(u, "/me") {
		t.Fatalf("delegated self mailbox should use /me: %s", u)
	}
}
