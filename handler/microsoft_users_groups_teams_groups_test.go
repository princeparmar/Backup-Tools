package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestSplitMicrosoftUsersGroupsJobs(t *testing.T) {
	jobs := []repo.CronJobListingDB{
		{Method: "outlook"},
		{Method: "outlook_sharepoint"},
		{Method: "outlook_teams"},
		{Method: "outlook_groups"},
	}
	mailbox, org := splitMicrosoftUsersGroupsJobs(jobs)
	if len(mailbox) != 1 || len(org) != 3 {
		t.Fatalf("mailbox=%d org=%d", len(mailbox), len(org))
	}
	sp, teams, groups := splitMicrosoftOrgResourceJobs(org)
	if len(sp) != 1 || len(teams) != 1 || len(groups) != 1 {
		t.Fatalf("sp=%d teams=%d groups=%d", len(sp), len(teams), len(groups))
	}
}
