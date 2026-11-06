package outlook

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestIsMSATenant(t *testing.T) {
	if !IsMSATenant(MSATenantID) {
		t.Fatal("MSA tenant id must be recognized")
	}
	if !IsMSATenant("9188040d-6c67-4c5b-b112-36a304b66dad") {
		t.Fatal("documented consumer tenant id must be recognized")
	}
	if IsMSATenant("contoso-tenant-id") {
		t.Fatal("work tenant must not be MSA")
	}
}

func fakeJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestTenantIDFromAccessToken(t *testing.T) {
	token := fakeJWT(t, map[string]interface{}{"tid": MSATenantID})
	tid, err := TenantIDFromAccessToken(token)
	if err != nil || tid != MSATenantID {
		t.Fatalf("tid: got %q err=%v", tid, err)
	}
	if _, err := TenantIDFromAccessToken("not-a-jwt"); err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestTenantIDFromAccessToken_workTenantAndOpaque(t *testing.T) {
	workTenant := "11111111-1111-1111-1111-111111111111"
	token := fakeJWT(t, map[string]interface{}{"tid": workTenant})

	tid, err := TenantIDFromAccessToken(token)
	if err != nil || tid != workTenant {
		t.Fatalf("jwt tid: got %q err=%v", tid, err)
	}
	if _, err := TenantIDFromAccessToken("opaque-token-without-dots"); err == nil {
		t.Fatal("opaque token must not parse as JWT")
	}
}
