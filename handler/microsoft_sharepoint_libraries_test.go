package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
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
