package crons

import (
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/repo"
)

// bindSharePointSiteMemory copies per-site state onto the legacy top-level TaskMemory fields
 // so existing library/list sync code can run unchanged for one site at a time.
func bindSharePointSiteMemory(tm *repo.TaskMemory, siteID string) {
	if tm == nil {
		return
	}
	siteID = strings.TrimSpace(siteID)
	if tm.SharePointSites == nil {
		tm.SharePointSites = map[string]repo.SharePointSiteState{}
	}
	st, ok := tm.SharePointSites[siteID]
	if !ok {
		// Migrate legacy single-site fields once into the nested map.
		if len(tm.SharePointSites) == 0 && (tm.SharePointDeltaLink != nil || len(tm.SharePointLibraries) > 0 || len(tm.SharePointListCursors) > 0) {
			st = repo.SharePointSiteState{
				DeltaLink:    tm.SharePointDeltaLink,
				BaselineDone: tm.SharePointBaselineDone,
				Layout:       tm.SharePointLayout,
				Libraries:    tm.SharePointLibraries,
				ListCursors:  tm.SharePointListCursors,
			}
			tm.SharePointSites[siteID] = st
		}
	}
	tm.SharePointDeltaLink = st.DeltaLink
	tm.SharePointBaselineDone = st.BaselineDone
	tm.SharePointLayout = st.Layout
	tm.SharePointLibraries = st.Libraries
	tm.SharePointListCursors = st.ListCursors
	if tm.SharePointLibraries == nil {
		tm.SharePointLibraries = map[string]repo.SharePointLibraryState{}
	}
	if tm.SharePointListCursors == nil {
		tm.SharePointListCursors = map[string]repo.SharePointListCursor{}
	}
}

// storeSharePointSiteMemory writes the top-level SharePoint fields back under SharePointSites[siteID].
func storeSharePointSiteMemory(tm *repo.TaskMemory, siteID string) {
	if tm == nil {
		return
	}
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return
	}
	if tm.SharePointSites == nil {
		tm.SharePointSites = map[string]repo.SharePointSiteState{}
	}
	tm.SharePointSites[siteID] = repo.SharePointSiteState{
		DeltaLink:    tm.SharePointDeltaLink,
		BaselineDone: tm.SharePointBaselineDone,
		Layout:       tm.SharePointLayout,
		Libraries:    tm.SharePointLibraries,
		ListCursors:  tm.SharePointListCursors,
		LastSyncAt:   time.Now().UTC().Format(time.RFC3339),
	}
}
