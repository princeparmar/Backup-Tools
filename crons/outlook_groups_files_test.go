package crons

import (
	"context"
	"errors"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

const groupSiteID = "contoso.sharepoint.com,site-guid,web-guid"

func TestGroupFilesCoveredBySharePoint(t *testing.T) {
	store := newReconcileTestDB(t)
	_, cred := seedScope(t, store, repo.MicrosoftScopeSelected)
	groupJob, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "Sales", "outlook_groups", "daily", cred.ID,
		repo.MicrosoftJobIdentity{TenantID: reconcileTenant, ResourceType: repo.ResourceTypeGroup, ResourceID: "group-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := ProcessorInput{Job: groupJob, Database: store}

	prev := msGroupRootSiteFn
	t.Cleanup(func() { msGroupRootSiteFn = prev })
	msGroupRootSiteFn = func(context.Context, string, string) (string, error) { return groupSiteID, nil }

	if groupFilesCoveredBySharePoint(context.Background(), input, "tok", "group-1") {
		t.Fatal("no SharePoint job: the groups job must keep the files")
	}

	siteJob, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "Sales site", "outlook_sharepoint", "daily", cred.ID,
		repo.MicrosoftJobIdentity{TenantID: reconcileTenant, ResourceType: repo.ResourceTypeSite, ResourceID: groupSiteID}, map[string]interface{}{"site_id": groupSiteID, "drive_id": "drive-1"})
	if err != nil {
		t.Fatal(err)
	}
	if groupFilesCoveredBySharePoint(context.Background(), input, "tok", "group-1") {
		t.Fatal("inactive SharePoint job must not take the files")
	}
	if err := store.CronJobRepo.UpdateCronJobByID(siteJob.ID, map[string]interface{}{
		"interval": "daily", "policy_id": uint(9), "active": true, "storx_token": "storx-grant",
	}); err != nil {
		t.Fatal(err)
	}
	if !groupFilesCoveredBySharePoint(context.Background(), input, "tok", "group-1") {
		t.Fatal("active SharePoint job for the group's site owns the files")
	}

	msGroupRootSiteFn = func(context.Context, string, string) (string, error) { return "", errors.New("graph down") }
	if groupFilesCoveredBySharePoint(context.Background(), input, "tok", "group-1") {
		t.Fatal("lookup failure must keep the files in the groups job")
	}
}
