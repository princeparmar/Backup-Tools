package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestValidateAccessCheckOwnership(t *testing.T) {
	sel := []repo.AccessCheckSelectionItem{{
		Email: "a@contoso.com", UserID: "oid-a", Service: "onedrive", Method: "outlook_onedrive",
	}}
	fp := repo.AccessCheckSelectionFingerprint("tenant-a", 9, sel)
	st := &repo.AccessCheckState{
		TenantID:             "tenant-a",
		CredentialID:         9,
		Selection:            sel,
		SelectionFingerprint: fp,
	}
	cred := &repo.GoogleBackupCredentialDB{}
	cred.ID = 9
	users := map[string]outlook.DirectoryUser{
		"a@contoso.com": {ObjectID: "oid-a", Mail: "a@contoso.com"},
	}

	if err := validateAccessCheckOwnership(st, cred, "tenant-a", []string{"onedrive"}, users); err != nil {
		t.Fatalf("want ok: %v", err)
	}
	if err := validateAccessCheckOwnership(st, cred, "other-tenant", []string{"onedrive"}, users); err == nil {
		t.Fatal("want tenant mismatch")
	}
	badCred := &repo.GoogleBackupCredentialDB{}
	badCred.ID = 99
	if err := validateAccessCheckOwnership(st, badCred, "tenant-a", []string{"onedrive"}, users); err == nil {
		t.Fatal("want credential mismatch")
	}
	extra := map[string]outlook.DirectoryUser{
		"a@contoso.com": {ObjectID: "oid-a", Mail: "a@contoso.com"},
		"b@contoso.com": {ObjectID: "oid-b", Mail: "b@contoso.com"},
	}
	if err := validateAccessCheckOwnership(st, cred, "tenant-a", []string{"onedrive"}, extra); err == nil {
		t.Fatal("want selection mismatch")
	}
}
