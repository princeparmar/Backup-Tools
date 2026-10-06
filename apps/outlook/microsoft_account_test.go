package outlook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"
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

func TestTenantIDForAccountDetection_JWTPreferred(t *testing.T) {
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

func TestTenantIDForAccountDetection_opaqueToken(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantTID  string
		wantName string
		wantErr  bool
	}{
		{name: "work org", status: 200, body: `{"value":[{"id":"contoso-tid","displayName":"Contoso"}]}`, wantTID: "contoso-tid", wantName: "Contoso"},
		{name: "no org is personal", status: 200, body: `{"value":[]}`, wantTID: MSATenantID},
		{name: "forbidden is personal", status: 403, body: `{}`, wantTID: MSATenantID},
		{name: "not found is personal", status: 404, body: `{}`, wantTID: MSATenantID},
		{name: "unauthorized is error", status: 401, body: `{}`, wantErr: true},
		{name: "server error is error", status: 500, body: `{}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var g capabilityGraph
			g.serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			tid, name, err := tenantIDForAccountDetection(ctx, "opaque-access-token")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got tid=%q", tid)
				}
				return
			}
			if err != nil || tid != tc.wantTID || name != tc.wantName {
				t.Fatalf("got tid=%q name=%q err=%v", tid, name, err)
			}
		})
	}
}
