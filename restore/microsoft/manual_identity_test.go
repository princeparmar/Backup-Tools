package microsoft

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

func TestManualRestoreIdentityIssue(t *testing.T) {
	work := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	other := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	cases := []struct {
		name         string
		tokenTenant  string
		headerTenant string
		keyTenants   []string
		wantMsg      string
		wantCode     int
	}{
		{
			name:        "personal account vs Teams work key",
			tokenTenant: outlook.MSATenantID,
			keyTenants:  []string{work},
			wantMsg:     ManualRestorePersonalAccount,
			wantCode:    http.StatusForbidden,
		},
		{
			name:         "personal account vs selected work tenant",
			tokenTenant:  outlook.MSATenantID,
			headerTenant: work,
			keyTenants:   []string{work},
			wantMsg:      ManualRestorePersonalAccount,
			wantCode:     http.StatusForbidden,
		},
		{
			name:        "other tenant user vs SharePoint work key",
			tokenTenant: other,
			keyTenants:  []string{work},
			wantMsg:     ManualRestoreOtherOrganization,
			wantCode:    http.StatusForbidden,
		},
		{
			name:         "signed-in tenant is not the selected tenant",
			tokenTenant:  other,
			headerTenant: work,
			keyTenants:   []string{work},
			wantMsg:      ManualRestoreWrongSelectedOrg,
			wantCode:     http.StatusForbidden,
		},
		{
			name:         "same-tenant other employee is allowed",
			tokenTenant:  work,
			headerTenant: work,
			keyTenants:   []string{work, work},
		},
		{
			name:        "personal account restoring personal-tenant keys is allowed",
			tokenTenant: outlook.MSATenantID,
			keyTenants:  []string{outlook.MSATenantID},
		},
		{
			name:     "missing token tenant",
			wantMsg:  "cannot determine the access token's tenant; send MICROSOFT_TENANT_ID",
			wantCode: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMsg, gotCode := ManualRestoreIdentityIssue(tc.tokenTenant, tc.headerTenant, tc.keyTenants)
			if gotMsg != tc.wantMsg || gotCode != tc.wantCode {
				t.Fatalf("got %d %q, want %d %q", gotCode, gotMsg, tc.wantCode, tc.wantMsg)
			}
		})
	}
}

func TestWrapWorkspaceWriteError(t *testing.T) {
	cases := []struct {
		name string
		kind string
		err  error
		want string
	}{
		{
			name: "Teams Graph 403 from another employee",
			kind: "teams",
			err:  &outlook.TeamsGraphError{Status: http.StatusForbidden, Code: "Forbidden"},
			want: ManualRestoreTeamsNoAccess,
		},
		{
			name: "Teams Graph 404 from a missing chat",
			kind: "teams",
			err:  &outlook.TeamsGraphError{Status: http.StatusNotFound, Code: "NotFound"},
			want: ManualRestoreTeamsNoAccess,
		},
		{
			name: "SharePoint upload 403 from another employee",
			kind: "sharepoint",
			err:  errors.New("onedrive upload session http 403: Access denied"),
			want: ManualRestoreSharePointNoAccess,
		},
		{
			name: "SharePoint list create 404",
			kind: "sharepoint",
			err:  errors.New("create list item http 404: item not found"),
			want: ManualRestoreSharePointNoAccess,
		},
		{
			name: "download failure stays as-is",
			kind: "teams",
			err:  errors.New("download backup object: not found"),
			want: "download backup object: not found",
		},
		{
			name: "nil error stays nil",
			kind: "sharepoint",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WrapWorkspaceWriteError(tc.kind, tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("got %v", got)
				}
				return
			}
			if got == nil || !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
			if tc.want != tc.err.Error() && !errors.Is(got, tc.err) && !strings.Contains(got.Error(), tc.err.Error()) {
				t.Fatalf("wrapped error should keep the Graph detail: %v", got)
			}
		})
	}
}
