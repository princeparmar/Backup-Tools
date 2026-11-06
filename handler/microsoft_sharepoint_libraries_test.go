package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func siteJob(tm repo.TaskMemory) repo.CronJobListingDB {
	return repo.CronJobListingDB{
		Method: "outlook_sharepoint", Name: "Contoso", TenantID: testTenantID,
		ResourceType: repo.ResourceTypeSite, ResourceID: "contoso.sharepoint.com,s1,w1", TaskMemory: tm,
	}
}

func TestSharePointSiteRowsListLibraries(t *testing.T) {
	jobs := []repo.CronJobListingDB{siteJob(repo.TaskMemory{SharePointLibraries: map[string]repo.SharePointLibraryState{
		"b!legal": {Name: "Legal", RemovedAt: "2026-01-02T00:00:00Z"},
		"b!docs":  {Name: "Documents", Default: true, LastSyncAt: "2026-01-03T00:00:00Z"},
		"b!hr":    {Name: "HR", LastError: "graph http 403"},
	}})}
	rows := buildMicrosoftSharePointSiteResources(jobs, nil)
	site := rows[0]["vault_prefix"].(string)
	if site != outlook.ResourceKeyPrefix(testTenantID, repo.ResourceTypeSite, "contoso.sharepoint.com,s1,w1") {
		t.Fatalf("site prefix %q", site)
	}
	libs, ok := rows[0]["libraries"].([]map[string]interface{})
	if !ok || len(libs) != 3 {
		t.Fatalf("libraries %#v", rows[0]["libraries"])
	}
	if libs[0]["name"] != "Documents" || libs[0]["default"] != true || libs[0]["vault_prefix"] != site {
		t.Fatalf("default library row %v", libs[0])
	}
	if libs[1]["name"] != "HR" || libs[1]["failed"] != true || libs[1]["vault_prefix"] != outlook.SharePointLibraryPrefix(site, "b!hr") {
		t.Fatalf("hr row %v", libs[1])
	}
	if libs[2]["name"] != "Legal" || libs[2]["removed"] != true {
		t.Fatalf("legal row %v", libs[2])
	}
	for _, l := range libs {
		if _, leaked := l["delta_link"]; leaked {
			t.Fatal("library rows must not expose cursors")
		}
	}
}

func TestSharePointSiteRowsLegacyJobHasNoLibraries(t *testing.T) {
	rows := buildMicrosoftSharePointSiteResources([]repo.CronJobListingDB{siteJob(repo.TaskMemory{})}, nil)
	if _, ok := rows[0]["libraries"]; ok {
		t.Fatalf("legacy job row %v", rows[0])
	}
	if rows[0]["vault_prefix"] == "" {
		t.Fatal("legacy job lost its vault prefix")
	}
}

func TestSharePointSiteRowsTenantJobExpandsSites(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	job := repo.CronJobListingDB{
		Method: "outlook_sharepoint", Name: "Microsoft SharePoint", TenantID: tenant,
		ResourceType: repo.ResourceTypeTenant, ResourceID: tenant, Active: true,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"sites": []interface{}{
				map[string]interface{}{"site_id": "site-a", "drive_id": "d-a", "site_name": "Alpha", "site_url": "https://a"},
				map[string]interface{}{"site_id": "site-b", "drive_id": "d-b", "site_name": "Beta"},
			},
		}),
		TaskMemory: repo.TaskMemory{
			SharePointSites: map[string]repo.SharePointSiteState{
				"site-a": {Libraries: map[string]repo.SharePointLibraryState{
					"d-a": {Name: "Documents", Default: true},
				}},
			},
		},
	}
	job.ID = 42
	merged := repo.CronJobListingDB{
		Method: "outlook_sharepoint", Name: "Old Site", TenantID: tenant,
		ResourceType: repo.ResourceTypeSite, ResourceID: "site-a", Active: false,
		Message: repo.SharePointMergedJobMessage,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"site_id": "site-a", "drive_id": "d-a", "site_name": "Alpha",
		}),
	}
	rows := buildMicrosoftSharePointSiteResources([]repo.CronJobListingDB{job, merged}, nil)
	if len(rows) != 3 {
		t.Fatalf("want 2 tenant sites + 1 merged, got %d: %+v", len(rows), rows)
	}
	if rows[0]["tenant_job"] != true || rows[0]["job_id"] != uint(42) || rows[0]["site_id"] != "site-a" {
		t.Fatalf("first site %+v", rows[0])
	}
	if rows[0]["vault_prefix"] != outlook.ResourceKeyPrefix(tenant, repo.ResourceTypeSite, "site-a") {
		t.Fatalf("vault prefix %+v", rows[0]["vault_prefix"])
	}
	libs, _ := rows[0]["libraries"].([]map[string]interface{})
	if len(libs) != 1 || libs[0]["name"] != "Documents" {
		t.Fatalf("libraries %+v", rows[0]["libraries"])
	}
	if rows[1]["site_id"] != "site-b" || rows[1]["job_id"] != uint(42) {
		t.Fatalf("second site %+v", rows[1])
	}
	if rows[2]["merged"] != true {
		t.Fatalf("merged row %+v", rows[2])
	}
}

func TestSaveTenantScopesMarksAllSites(t *testing.T) {
	store := newMicrosoftTestDB(t)
	cred := &repo.GoogleBackupCredentialDB{StorjProjectID: "p1"}
	cred.ID = 7
	saveMicrosoftTenantScopes(store, "u1", cred, testTenantID, "daily", []string{"sharepoint", "outlook"},
		map[string]bool{repo.ResourceTypeSite: true}, 3)
	scopes, err := store.MicrosoftScopeRepo.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, s := range scopes {
		modes[s.ResourceType] = s.SelectionMode
	}
	if modes[repo.ResourceTypeSite] != repo.MicrosoftScopeAll || modes[repo.ResourceTypeUser] == repo.MicrosoftScopeAll {
		t.Fatalf("scope modes %v", modes)
	}

	// Naming sites later never turns the all scope back into a selection.
	saveMicrosoftTenantScopes(store, "u1", cred, testTenantID, "daily", []string{"sharepoint"}, nil, 3)
	scopes, _ = store.MicrosoftScopeRepo.ListActive()
	for _, s := range scopes {
		if s.ResourceType == repo.ResourceTypeSite && s.SelectionMode != repo.MicrosoftScopeAll {
			t.Fatalf("site scope became %q", s.SelectionMode)
		}
	}
}
