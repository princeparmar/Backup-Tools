package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestIsMSATenant(t *testing.T) {
	if !IsMSATenant(MSATenantID) {
		t.Fatal("MSA tenant id must be recognized")
	}
	if IsMSATenant("contoso-tenant-id") {
		t.Fatal("work tenant must not be MSA")
	}
}

func TestCanPerformOrgBackup(t *testing.T) {
	if CanPerformOrgBackup(nil) {
		t.Fatal("empty roles must not qualify")
	}
	if CanPerformOrgBackup([]string{"random-role"}) {
		t.Fatal("unknown role must not qualify")
	}
	if !CanPerformOrgBackup([]string{OrgBackupAdminRoleTemplateIDs[2]}) {
		t.Fatal("SharePoint Administrator must qualify")
	}
	if !CanPerformOrgBackup([]string{"other", OrgBackupAdminRoleTemplateIDs[0]}) {
		t.Fatal("Global Administrator must qualify")
	}
}

func TestTenantIDFromAccessToken(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"tid": MSATenantID})
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	tid, err := TenantIDFromAccessToken(token)
	if err != nil || tid != MSATenantID {
		t.Fatalf("tid: got %q err=%v", tid, err)
	}
	if _, err := TenantIDFromAccessToken("not-a-jwt"); err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestTenantIDForAccountDetection_JWTPreferred(t *testing.T) {
	workTenant := "11111111-1111-1111-1111-111111111111"
	payload, _ := json.Marshal(map[string]string{"tid": workTenant})
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	tid, err := TenantIDFromAccessToken(token)
	if err != nil || tid != workTenant {
		t.Fatalf("jwt tid: got %q err=%v", tid, err)
	}
	if _, err := TenantIDFromAccessToken("opaque-token-without-dots"); err == nil {
		t.Fatal("opaque token must not parse as JWT")
	}
}

func TestTenantIDForAccountDetection_opaqueFallsBackToMSA(t *testing.T) {
	tid, name, err := tenantIDForAccountDetection(context.Background(), "opaque-consumer-access-token")
	if err != nil {
		t.Fatalf("opaque token should fall back to MSA: %v", err)
	}
	if tid != MSATenantID || name != "" {
		t.Fatalf("got tid=%q name=%q", tid, name)
	}
}

func TestDirectoryUsersToEntities(t *testing.T) {
	entities := DirectoryUsersToEntities([]DomainUser{
		{ID: "1", Mail: "a@contoso.com", DisplayName: "A", AccountEnabled: true},
		{ID: "2", UserPrincipalName: "b@contoso.com"},
	})
	if len(entities) != 2 || entities[1].Email != "b@contoso.com" {
		t.Fatalf("unexpected entities: %+v", entities)
	}
}
