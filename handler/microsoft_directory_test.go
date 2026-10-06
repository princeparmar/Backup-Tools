package handler

import (
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
)

func TestFilterMicrosoftDirectoryUsers(t *testing.T) {
	created := time.Now()
	users := []outlook.DirectoryUser{
		{ObjectID: "u2", DisplayName: "Bob", UPN: "bob@contoso.com", Department: "Sales", AccountEnabled: true, HasLicense: true},
		{ObjectID: "u1", DisplayName: "Ann", Mail: "ann@contoso.com", Department: "sales", AccountEnabled: true, CreatedAt: &created},
		{ObjectID: "u3", DisplayName: "Cy", Mail: "cy@contoso.com", AccountEnabled: false},
	}
	got := filterMicrosoftDirectoryUsers(users, microsoftDirectoryFilter{Department: "SALES", EnabledOnly: true})
	if len(got) != 2 || got[0].ObjectID != "u1" || got[1].ObjectID != "u2" {
		t.Fatalf("department filter/sort = %+v", got)
	}
	if got := filterMicrosoftDirectoryUsers(users, microsoftDirectoryFilter{OrgUnitPath: "Sales/"}); len(got) != 2 {
		t.Fatalf("org unit filter = %+v", got)
	}
	if got := filterMicrosoftDirectoryUsers(users, microsoftDirectoryFilter{Search: "CY@"}); len(got) != 1 || got[0].ObjectID != "u3" {
		t.Fatalf("search filter = %+v", got)
	}
	units := microsoftOrgUnitViews(users)
	if len(units) != 2 || units[0]["org_unit_path"] != "/Sales" || units[0]["user_count"] != 1 {
		t.Fatalf("org units = %+v", units)
	}
	if s := microsoftCapabilitySampleUsers(users, 5); len(s) != 1 || s[0] != "u2" {
		t.Fatalf("samples = %v", s)
	}
	views := microsoftDirectoryUserViews(users, map[string][]string{"u1": {"Global Administrator"}})
	if views[0]["is_admin"] != false || views[1]["is_admin"] != true {
		t.Fatalf("is_admin = %v / %v", views[0]["is_admin"], views[1]["is_admin"])
	}
	if r, _ := views[1]["roles"].([]string); len(r) != 1 || r[0] != "Global Administrator" {
		t.Fatalf("roles = %v", views[1]["roles"])
	}
}
