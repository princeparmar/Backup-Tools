package microsoft

import (
	"context"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/restore"
)

func TestMicrosoftInviteOtherMailboxNeedsOwnerAppNotInviteeLogin(t *testing.T) {
	// Outlook mail / Calendar / Contacts / OneDrive under admin@ → sales@:
	// invitee Entra login only hits "Need admin approval". Use owner app consent.
	cases := []struct {
		name                 string
		loginID              string
		holder               string
		wantOtherMailbox     bool
		wantInviteeLoginOK   bool // false = do not open invitee MSAL for this case
	}{
		{
			name:               "sales under admin — owner app consent path",
			loginID:            "sales@space2let.com",
			holder:             "admin@space2let.onmicrosoft.com",
			wantOtherMailbox:   true,
			wantInviteeLoginOK: false,
		},
		{
			name:               "same mailbox — invitee login ok if owner not ready",
			loginID:            "sales@space2let.com",
			holder:             "sales@space2let.com",
			wantOtherMailbox:   false,
			wantInviteeLoginOK: true,
		},
		{
			name:               "calendar child under admin",
			loginID:            "user@contoso.com",
			holder:             "admin@contoso.com",
			wantOtherMailbox:   true,
			wantInviteeLoginOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cred := &repo.GoogleBackupCredentialDB{Email: tc.holder}
			other := isOtherMailboxRestore(tc.loginID, cred)
			if other != tc.wantOtherMailbox {
				t.Fatalf("otherMailbox = %v, want %v", other, tc.wantOtherMailbox)
			}
			// Invitee MSAL is only a fallback when NOT other-mailbox.
			if got := !other; got != tc.wantInviteeLoginOK {
				t.Fatalf("inviteeLoginFallback = %v, want %v", got, tc.wantInviteeLoginOK)
			}
		})
	}
}

func TestEvaluateInviteMicrosoftReadiness_preservesCredentialID(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		token      string
		credID     uint
		wantReady  bool
		wantReason string
	}{
		{
			name:       "outlook missing token keeps source credential",
			method:     "outlook",
			credID:     41,
			wantReady:  false,
			wantReason: restore.ReadinessReasonInviteOAuthRequired,
		},
		{
			name:       "onedrive missing token keeps source credential",
			method:     "outlook_onedrive",
			credID:     42,
			wantReady:  false,
			wantReason: restore.ReadinessReasonInviteOAuthRequired,
		},
		{
			name:       "calendar missing token keeps source credential",
			method:     "outlook_calendar",
			credID:     43,
			wantReady:  false,
			wantReason: restore.ReadinessReasonInviteOAuthRequired,
		},
		{
			name:       "contacts missing token keeps source credential",
			method:     "outlook_contacts",
			credID:     44,
			wantReady:  false,
			wantReason: restore.ReadinessReasonInviteOAuthRequired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := &restore.ReadinessResult{
				Service:      tc.method,
				LoginID:      "mailbox@contoso.com",
				CredentialID: tc.credID,
			}
			got, err := evaluateInviteMicrosoftReadiness(context.Background(), out, tc.method, tc.method, out.LoginID, tc.token)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.CredentialID != tc.credID {
				t.Fatalf("CredentialID = %d, want %d (source backup credential must stay on invite jobs)", got.CredentialID, tc.credID)
			}
			if got.Ready != tc.wantReady {
				t.Fatalf("Ready = %v, want %v", got.Ready, tc.wantReady)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestMicrosoftRestoreScopesForInviteServices(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{
		{method: "outlook", want: "Mail.ReadWrite"},
		{method: "outlook_calendar", want: "Calendars.ReadWrite"},
		{method: "outlook_contacts", want: "Contacts.ReadWrite"},
		{method: "outlook_onedrive", want: "Files.ReadWrite.All"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			got := microsoftRestoreScopesForMethod(tc.method)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("scopes(%q) = %v, want [%s]", tc.method, got, tc.want)
			}
		})
	}
}
