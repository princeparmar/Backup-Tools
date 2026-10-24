package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stubLoginBase(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := microsoftLoginBaseURL
	microsoftLoginBaseURL = srv.URL
	t.Setenv("OUTLOOK_CLIENT_ID", "client")
	t.Setenv("OUTLOOK_CLIENT_SECRET", "secret")
	t.Cleanup(func() {
		microsoftLoginBaseURL = prev
		srv.Close()
	})
}

func TestAuthTokenForTenant_postsToTenantAuthority(t *testing.T) {
	var gotPath string
	stubLoginBase(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tenant-b-token"})
	})
	tok, err := AuthTokenForTenant("rt", "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/tenant-b/oauth2/v2.0/token" || tok.AccessToken != "tenant-b-token" {
		t.Fatalf("path = %q token = %q", gotPath, tok.AccessToken)
	}
}

func TestAuthTokenForTenant_classifiesFailures(t *testing.T) {
	cases := map[string]string{
		"AADSTS50076: MFA required":                     TokenErrorSigninRequired,
		"AADSTS53003: blocked by Conditional Access":    TokenErrorSigninRequired,
		"AADSTS65001: the user has not consented":       TokenErrorConsentRequired,
		"AADSTS90002: Tenant 'x' not found":             TokenErrorTenantNotFound,
		"AADSTS7000222: The provided client secret keys": TokenErrorSecretExpired,
		"something else":                                TokenErrorOther,
	}
	for desc, want := range cases {
		stubLoginBase(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_request", "error_description": desc})
		})
		_, err := AuthTokenForTenant("rt", "t")
		var te *TokenEndpointError
		if !errors.As(err, &te) {
			t.Fatalf("%s: want TokenEndpointError, got %v", desc, err)
		}
		if got := ClassifyTokenError(err); got != want {
			t.Fatalf("%s: class = %q, want %q", desc, got, want)
		}
	}
}

func TestIdentityClaimsFromTokens(t *testing.T) {
	id := fakeJWT(t, map[string]interface{}{"oid": "OID-1", "tid": "TID-1", "preferred_username": "ann@contoso.com"})
	got, err := IdentityClaimsFromTokens(id, "opaque-access-token")
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectID != "oid-1" || got.TenantID != "tid-1" || got.Email != "ann@contoso.com" {
		t.Fatalf("claims = %+v", got)
	}
	if _, err := IdentityClaimsFromTokens("", "opaque"); err == nil {
		t.Fatal("missing oid/tid must fail")
	}
	if IdentityTypeForTenant(MSATenantID) != MicrosoftIdentityPersonal || IdentityTypeForTenant("tid-1") != MicrosoftIdentityWorkSchool {
		t.Fatal("identity type")
	}
}

func TestDiscoverTenants(t *testing.T) {
	prevToken := armTokenFn
	t.Cleanup(func() { armTokenFn = prevToken })

	t.Run("complete", func(t *testing.T) {
		armTokenFn = func(_, authority, scope string) (*TokenResponse, error) {
			if authority != "home" || !strings.Contains(scope, "management.azure.com/user_impersonation") {
				t.Fatalf("authority %q scope %q", authority, scope)
			}
			return &TokenResponse{AccessToken: "arm"}, nil
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"value": []map[string]string{
				{"tenantId": "HOME", "displayName": "Home", "tenantCategory": "Home"},
				{"tenantId": "b", "displayName": "Contoso", "defaultDomain": "contoso.com", "tenantCategory": "ProjectedBy"},
			}})
		}))
		defer srv.Close()
		prev := armBaseURL
		armBaseURL = srv.URL
		defer func() { armBaseURL = prev }()

		res := DiscoverTenants(context.Background(), "rt", "home")
		if res.Status != DiscoveryComplete || len(res.Tenants) != 2 || res.Tenants[0].TenantID != "home" || res.Tenants[1].DefaultDomain != "contoso.com" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("token failure is unavailable, consent is permission_missing", func(t *testing.T) {
		armTokenFn = func(string, string, string) (*TokenResponse, error) {
			return nil, &TokenEndpointError{Body: "AADSTS650052: needs Azure Service Management"}
		}
		if res := DiscoverTenants(context.Background(), "rt", "home"); res.Status != DiscoveryPermissionMissing || len(res.Tenants) != 0 {
			t.Fatalf("result = %+v", res)
		}
		armTokenFn = func(string, string, string) (*TokenResponse, error) {
			return nil, errors.New("network down")
		}
		if res := DiscoverTenants(context.Background(), "rt", "home"); res.Status != DiscoveryUnavailable {
			t.Fatalf("result = %+v", res)
		}
	})
}

func serveRoleGraph(t *testing.T, assignments interface{}, assignmentsStatus int, memberOf []directoryRoleRow, memberOfStatus int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/roleManagement/directory/roleAssignments"):
			if assignmentsStatus != 0 {
				w.WriteHeader(assignmentsStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"value": assignments})
		case strings.Contains(r.URL.Path, "/me/memberOf/"):
			if memberOfStatus != 0 {
				w.WriteHeader(memberOfStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"value": memberOf})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	prev := graphBaseURL
	graphBaseURL = srv.URL
	t.Cleanup(func() {
		graphBaseURL = prev
		srv.Close()
	})
}

func TestReadTenantRoles_fallbackOrderAndScope(t *testing.T) {
	ctx := context.Background()

	t.Run("role assignments are authoritative and keep scope", func(t *testing.T) {
		serveRoleGraph(t, []map[string]interface{}{
			{"directoryScopeId": "/administrativeUnits/au-1", "roleDefinition": map[string]string{"displayName": "User Administrator", "templateId": "fe930be7-5e62-47db-91fc-433a67967a1a"}},
		}, 0, nil, 0)
		res := ReadTenantRoles(ctx, "at", "", "oid")
		if res.Status != RoleStatusKnown || res.Source != "role_assignments" || len(res.Roles) != 1 || res.Roles[0].Scope != "/administrativeUnits/au-1" {
			t.Fatalf("result = %+v", res)
		}
		if res.IsTenantWideAdmin() {
			t.Fatal("an administrative-unit scoped role must not count as tenant-wide admin")
		}
	})

	t.Run("memberOf fallback is tenant-wide", func(t *testing.T) {
		serveRoleGraph(t, nil, http.StatusForbidden, []directoryRoleRow{{RoleTemplateID: globalAdminTemplateID, DisplayName: "Global Administrator"}}, 0)
		res := ReadTenantRoles(ctx, "at", "", "oid")
		if res.Status != RoleStatusKnown || res.Source != "member_of" || !res.IsTenantWideAdmin() {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("wids alone stays unknown and never admin", func(t *testing.T) {
		serveRoleGraph(t, nil, http.StatusForbidden, nil, http.StatusForbidden)
		access := fakeJWT(t, map[string]interface{}{"wids": []string{globalAdminTemplateID}})
		res := ReadTenantRoles(ctx, access, "", "oid")
		if res.Status != RoleStatusUnknown || len(res.Roles) != 1 || res.IsTenantWideAdmin() {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("nothing readable is unknown", func(t *testing.T) {
		serveRoleGraph(t, nil, http.StatusForbidden, nil, http.StatusForbidden)
		res := ReadTenantRoles(ctx, "opaque", "", "oid")
		if res.Status != RoleStatusUnknown || len(res.Roles) != 0 {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestServicePrincipalForApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sp-1"})
	}))
	prev := graphBaseURL
	graphBaseURL = srv.URL
	t.Cleanup(func() {
		graphBaseURL = prev
		srv.Close()
	})
	if id, found, err := ServicePrincipalForApp(context.Background(), "app", "client"); err != nil || !found || id != "sp-1" {
		t.Fatalf("id=%q found=%v err=%v", id, found, err)
	}
	if _, found, err := ServicePrincipalForApp(context.Background(), "app", "missing"); err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
}

func TestClientSecretExpiryWarning(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	t.Setenv("OUTLOOK_CLIENT_SECRET_EXPIRES_AT", "")
	if w := ClientSecretExpiryWarning(now); w != "" {
		t.Fatalf("unset: %q", w)
	}
	t.Setenv("OUTLOOK_CLIENT_SECRET_EXPIRES_AT", "2026-10-20")
	if w := ClientSecretExpiryWarning(now); !strings.Contains(w, "14 days") {
		t.Fatalf("soon: %q", w)
	}
	t.Setenv("OUTLOOK_CLIENT_SECRET_EXPIRES_AT", "2027-06-01")
	if w := ClientSecretExpiryWarning(now); w != "" {
		t.Fatalf("far: %q", w)
	}
	t.Setenv("OUTLOOK_CLIENT_SECRET_EXPIRES_AT", "2026-10-01T00:00:00Z")
	if w := ClientSecretExpiryWarning(now); !strings.Contains(w, "expired") {
		t.Fatalf("expired: %q", w)
	}
}
