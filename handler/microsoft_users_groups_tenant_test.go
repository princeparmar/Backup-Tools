package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/labstack/echo/v4"
)

const (
	ugTenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	ugTenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func ugContext(target string, header map[string]string) echo.Context {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return echo.New().NewContext(req, httptest.NewRecorder())
}

func ugJob(id uint, tenant, rid string) repo.CronJobListingDB {
	j := repo.CronJobListingDB{Provider: repo.CredentialProviderMicrosoft, TenantID: tenant, ResourceType: repo.ResourceTypeUser, ResourceID: rid, Method: "outlook"}
	j.ID = id
	return j
}

func TestFilterUsersGroupsMicrosoftTenant(t *testing.T) {
	jobs := []repo.CronJobListingDB{ugJob(1, ugTenantA, "oid-1"), ugJob(2, ugTenantB, "oid-2"), ugJob(3, ugTenantA, "oid-3")}

	if _, _, errBody := filterUsersGroupsMicrosoftTenant(ugContext("/", nil), jobs); errBody == nil || errBody["code"] != "tenant_id_required" {
		t.Fatalf("several tenants without a selection: %v", errBody)
	}

	tid, got, errBody := filterUsersGroupsMicrosoftTenant(ugContext("/?tenant_id="+ugTenantA, nil), jobs)
	if errBody != nil || tid != ugTenantA || len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("query tenant: %s %v %v", tid, got, errBody)
	}

	tid, got, _ = filterUsersGroupsMicrosoftTenant(ugContext("/?tenant_id="+ugTenantA, map[string]string{"MICROSOFT_TENANT_ID": ugTenantB}), jobs)
	if tid != ugTenantB || len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("header wins over query: %s %v", tid, got)
	}

	tid, got, errBody = filterUsersGroupsMicrosoftTenant(ugContext("/", nil), jobs[:1])
	if errBody != nil || tid != ugTenantA || len(got) != 1 {
		t.Fatalf("single tenant needs no selection: %s %v %v", tid, got, errBody)
	}
}

func TestMicrosoftJobStorageIdentity(t *testing.T) {
	job := ugJob(1, ugTenantA, "oid-1")
	tid, typ, rid, prefix := microsoftJobStorageIdentity(&job)
	if tid != ugTenantA || typ != repo.ResourceTypeUser || rid != "oid-1" || prefix != ugTenantA+"/user/oid-1" {
		t.Fatalf("identity = %q %q %q %q", tid, typ, rid, prefix)
	}
	google := repo.CronJobListingDB{Provider: repo.CredentialProviderGoogle, Name: "ann@gmail.com", Method: "gmail"}
	if tid, _, _, prefix := microsoftJobStorageIdentity(&google); tid != "" || prefix != "" {
		t.Fatal("google jobs have no tenant prefix")
	}
}
