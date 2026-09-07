package google

import "testing"

func TestNormalizeOrgUnitPath(t *testing.T) {
	if got := NormalizeOrgUnitPath(""); got != "/" {
		t.Fatalf("empty got %q", got)
	}
	if got := NormalizeOrgUnitPath("SAles"); got != "/SAles" {
		t.Fatalf("SAles got %q", got)
	}
}

func TestGroupDomainUsersByOrgUnitIncludesAdmin(t *testing.T) {
	users := []DomainUser{
		{Email: "billing@salestalker.com", OrgUnitPath: "/"},
		{Email: "sales@salestalker.com", OrgUnitPath: "/SAles", IsAdmin: true},
		{Email: "support@salestalker.com", OrgUnitPath: "/SAles"},
	}
	got := GroupDomainUsersByOrgUnit(users)
	if len(got) != 2 {
		t.Fatalf("ou count %d", len(got))
	}
	if got[0].OrgUnitPath != "/" || got[0].UserCount != 1 || got[0].Users[0].Email != "billing@salestalker.com" {
		t.Fatalf("root %+v", got[0])
	}
	if got[1].Name != "SAles" || got[1].UserCount != 2 {
		t.Fatalf("sales ou %+v", got[1])
	}
	if got[1].Users[0].Email != "sales@salestalker.com" || got[1].Users[0].Role != "admin" {
		t.Fatalf("admin %+v", got[1].Users[0])
	}
	if got[1].Users[1].Email != "support@salestalker.com" || got[1].Users[1].Role != "user" {
		t.Fatalf("support %+v", got[1].Users[1])
	}
}
