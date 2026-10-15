package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestSplitMicrosoftUsersGroupsJobs(t *testing.T) {
	jobs := []repo.CronJobListingDB{
		{Method: "outlook"},
		{Method: "outlook_sharepoint"},
		{Method: "outlook_teams"},
	}
	mailbox, org := splitMicrosoftUsersGroupsJobs(jobs)
	if len(mailbox) != 1 || len(org) != 2 {
		t.Fatalf("mailbox=%d org=%d", len(mailbox), len(org))
	}
	sp, teams := splitMicrosoftOrgResourceJobs(org)
	if len(sp) != 1 || len(teams) != 1 {
		t.Fatalf("sp=%d teams=%d", len(sp), len(teams))
	}
}

func TestBuildMicrosoftTeamsResourcesTenantJobRowsAndMerged(t *testing.T) {
	const tenant = "11111111-2222-3333-4444-555555555555"
	known := func(string) []repo.MicrosoftResourceDB {
		return []repo.MicrosoftResourceDB{
			{TenantID: tenant, ResourceType: repo.ResourceTypeTeam, ExternalID: "t1", DisplayName: "Sales"},
			{TenantID: tenant, ResourceType: repo.ResourceTypeTeam, ExternalID: "t2", DisplayName: "Ops"},
		}
	}
	selected := repo.CronJobListingDB{Method: "outlook_teams", Provider: repo.CredentialProviderMicrosoft, TenantID: tenant,
		ResourceType: repo.ResourceTypeTenant, ResourceID: tenant, Active: true,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"team_ids": []interface{}{"t2"}, "team_channel_ids": map[string]interface{}{"t2": []interface{}{"c1"}}})}
	selected.ID = 7
	rows := buildMicrosoftTeamsResources([]repo.CronJobListingDB{selected}, nil, known)
	if len(rows) != 1 || rows[0]["team_id"] != "t2" || rows[0]["name"] != "Ops" || rows[0]["job_id"] != uint(7) {
		t.Fatalf("selected rows: %+v", rows)
	}
	if rows[0]["vault_prefix"] != tenant+"/team/t2" || rows[0]["job_vault"] != tenant+"/tenant/"+tenant {
		t.Fatalf("prefixes: %+v", rows[0])
	}

	all := selected
	all.InputData = database.NewDbJsonFromValue(map[string]interface{}{})
	merged := repo.CronJobListingDB{Method: "outlook_teams", Provider: repo.CredentialProviderMicrosoft, TenantID: tenant,
		ResourceType: repo.ResourceTypeTeam, ResourceID: "t1", Message: repo.TeamsMergedJobMessage,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"team_id": "t1"})}
	rows = buildMicrosoftTeamsResources([]repo.CronJobListingDB{all, merged}, nil, known)
	if len(rows) != 3 || rows[0]["all_teams"] != true || rows[2]["merged"] != true {
		t.Fatalf("all + merged rows: %+v", rows)
	}
}
