package repo

import (
	"strings"
)

// SharePointMergedJobMessage is set on old per-site SharePoint jobs paused after the tenant job
 // imported them.
const SharePointMergedJobMessage = "Merged into the SharePoint tenant job; backups are kept"

// SharePointSiteSelection is one site backed up by the combined outlook_sharepoint tenant job.
type SharePointSiteSelection struct {
	SiteID    string `json:"site_id"`
	DriveID   string `json:"drive_id"`
	SiteName  string `json:"site_name,omitempty"`
	SiteURL   string `json:"site_url,omitempty"`
}

// JobSharePointSites returns the sites selected on an outlook_sharepoint job.
 // Prefers input_data.sites[]; falls back to legacy single site_id + drive_id.
func JobSharePointSites(job *CronJobListingDB) []SharePointSiteSelection {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return nil
	}
	raw := *job.InputData.Json()
	if sites := parseSharePointSitesValue(raw["sites"]); len(sites) > 0 {
		return sites
	}
	siteID := strings.TrimSpace(JobSharePointSiteID(job))
	driveID := strings.TrimSpace(JobSharePointDriveID(job))
	if siteID == "" || driveID == "" {
		return nil
	}
	name, url := "", ""
	if v, ok := raw["site_name"].(string); ok {
		name = strings.TrimSpace(v)
	}
	if v, ok := raw["site_url"].(string); ok {
		url = strings.TrimSpace(v)
	}
	if name == "" {
		name = strings.TrimSpace(job.Name)
	}
	return []SharePointSiteSelection{{
		SiteID: siteID, DriveID: driveID, SiteName: name, SiteURL: url,
	}}
}

func parseSharePointSitesValue(v interface{}) []SharePointSiteSelection {
	var rows []interface{}
	switch t := v.(type) {
	case []SharePointSiteSelection:
		out := make([]SharePointSiteSelection, 0, len(t))
		for _, s := range t {
			if sid := strings.TrimSpace(s.SiteID); sid != "" && strings.TrimSpace(s.DriveID) != "" {
				s.SiteID, s.DriveID = sid, strings.TrimSpace(s.DriveID)
				s.SiteName, s.SiteURL = strings.TrimSpace(s.SiteName), strings.TrimSpace(s.SiteURL)
				out = append(out, s)
			}
		}
		return out
	case []interface{}:
		rows = t
	default:
		return nil
	}
	out := make([]SharePointSiteSelection, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		m, ok := row.(map[string]interface{})
		if !ok {
			continue
		}
		siteID := strings.TrimSpace(asString(m["site_id"]))
		driveID := strings.TrimSpace(asString(m["drive_id"]))
		if siteID == "" || driveID == "" || seen[siteID] {
			continue
		}
		seen[siteID] = true
		out = append(out, SharePointSiteSelection{
			SiteID: siteID, DriveID: driveID,
			SiteName: strings.TrimSpace(asString(m["site_name"])),
			SiteURL:  strings.TrimSpace(asString(m["site_url"])),
		})
	}
	return out
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

// SharePointSelectionAddSite unions a site into a SharePoint tenant job's input_data.sites.
func SharePointSelectionAddSite(in map[string]interface{}, site SharePointSiteSelection) map[string]interface{} {
	if in == nil {
		in = map[string]interface{}{}
	}
	site.SiteID = strings.TrimSpace(site.SiteID)
	site.DriveID = strings.TrimSpace(site.DriveID)
	if site.SiteID == "" || site.DriveID == "" {
		return in
	}
	cur := parseSharePointSitesValue(in["sites"])
	for i := range cur {
		if cur[i].SiteID == site.SiteID {
			cur[i].DriveID = site.DriveID
			if strings.TrimSpace(site.SiteName) != "" {
				cur[i].SiteName = strings.TrimSpace(site.SiteName)
			}
			if strings.TrimSpace(site.SiteURL) != "" {
				cur[i].SiteURL = strings.TrimSpace(site.SiteURL)
			}
			in["sites"] = cur
			return in
		}
	}
	cur = append(cur, SharePointSiteSelection{
		SiteID: site.SiteID, DriveID: site.DriveID,
		SiteName: strings.TrimSpace(site.SiteName),
		SiteURL:  strings.TrimSpace(site.SiteURL),
	})
	in["sites"] = cur
	return in
}
