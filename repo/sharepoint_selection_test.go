package repo

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
)

func TestJobSharePointSitesPrefersSitesArray(t *testing.T) {
	job := &CronJobListingDB{
		Name: "Microsoft SharePoint",
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"site_id":  "legacy",
			"drive_id": "legacy-d",
			"sites": []interface{}{
				map[string]interface{}{"site_id": "a", "drive_id": "da", "site_name": "A"},
				map[string]interface{}{"site_id": "b", "drive_id": "db"},
				map[string]interface{}{"site_id": "a", "drive_id": "dup"}, // dedupe
			},
		}),
	}
	sites := JobSharePointSites(job)
	if len(sites) != 2 || sites[0].SiteID != "a" || sites[0].SiteName != "A" || sites[1].SiteID != "b" {
		t.Fatalf("sites %+v", sites)
	}
}

func TestJobSharePointSitesLegacyFallback(t *testing.T) {
	job := &CronJobListingDB{
		Name: "Contoso",
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"site_id": "s1", "drive_id": "d1", "site_url": "https://s1",
		}),
	}
	sites := JobSharePointSites(job)
	if len(sites) != 1 || sites[0].SiteID != "s1" || sites[0].DriveID != "d1" || sites[0].SiteName != "Contoso" {
		t.Fatalf("legacy %+v", sites)
	}
}

func TestSharePointSelectionAddSiteUnions(t *testing.T) {
	in := map[string]interface{}{}
	in = SharePointSelectionAddSite(in, SharePointSiteSelection{SiteID: "s1", DriveID: "d1", SiteName: "One"})
	in = SharePointSelectionAddSite(in, SharePointSiteSelection{SiteID: "s2", DriveID: "d2"})
	in = SharePointSelectionAddSite(in, SharePointSiteSelection{SiteID: "s1", DriveID: "d1b", SiteName: "One Updated"})
	sites := parseSharePointSitesValue(in["sites"])
	if len(sites) != 2 || sites[0].DriveID != "d1b" || sites[0].SiteName != "One Updated" {
		t.Fatalf("union %+v", sites)
	}
}
