package crons

import (
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	sharePointCoverageInputKey  = "sharepoint_coverage"
	sharePointCoverageSampleCap = 10

	spRunInProgress = "in_progress"
	spRunComplete   = "complete"
	spRunPartial    = "partial"
	spRunFailed     = "failed"

	spStatusComplete        = "complete"
	spStatusPartial         = "partial"
	spStatusFailed          = "failed"
	spStatusUnsupported     = "unsupported"
	spStatusDiscoveredOnly  = "discovered_only"
)

// sharePointCoverage is durable site-run stats on job input_data (crash-safe: written after
 // each library and finalized on every exit path). Vault objects are never purged by this
 // coverage path.
type sharePointCoverage struct {
	SiteID    string `json:"site_id"`
	SiteKey   string `json:"site_key"`
	StartedAt string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	RunStatus string `json:"run_status"`

	ListComplete  bool   `json:"list_complete"`
	ListingError  string `json:"listing_error,omitempty"`
	LibrariesTotal  int `json:"libraries_total"`
	LibrariesSynced int `json:"libraries_synced"`
	LibrariesFailed int `json:"libraries_failed"`

	Libraries []sharePointLibraryCoverage     `json:"libraries,omitempty"`
	Types     []sharePointContentTypeCoverage `json:"content_types,omitempty"`
	Samples   []sharePointCoverageSample      `json:"samples,omitempty"`
}

type sharePointLibraryCoverage struct {
	DriveID            string `json:"drive_id"`
	Name               string `json:"name,omitempty"`
	Default            bool   `json:"default,omitempty"`
	Status             string `json:"status"`
	LastSyncAt         string `json:"last_sync_at,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	BaselineDone       bool   `json:"baseline_done,omitempty"`
	InventoryComplete  bool   `json:"inventory_complete"`
}

type sharePointContentTypeCoverage struct {
	Type               string `json:"type"`
	Discovered         int    `json:"discovered"`
	BackedUp           int    `json:"backed_up"`
	Failed             int    `json:"failed"`
	Status             string `json:"status"`
	InventoryComplete  bool   `json:"inventory_complete"`
	StartedAt          string `json:"started_at,omitempty"`
	FinishedAt         string `json:"finished_at,omitempty"`
	Note               string `json:"note,omitempty"`
}

type sharePointCoverageSample struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func (c *sharePointCoverage) sample(name, reason string) {
	if c == nil || len(c.Samples) >= sharePointCoverageSampleCap {
		return
	}
	c.Samples = append(c.Samples, sharePointCoverageSample{
		Name:   strings.TrimSpace(name),
		Reason: strings.TrimSpace(reason),
	})
}

func newSharePointCoverage(siteID, siteKey string) *sharePointCoverage {
	now := time.Now().UTC().Format(time.RFC3339)
	return &sharePointCoverage{
		SiteID:    siteID,
		SiteKey:   siteKey,
		StartedAt: now,
		RunStatus: spRunInProgress,
		Types: []sharePointContentTypeCoverage{
			{Type: "document_libraries", Status: spStatusPartial, InventoryComplete: false},
			{Type: "lists", Status: spStatusUnsupported, Note: "non-library lists; see P1"},
			{Type: "versions", Status: spStatusUnsupported, Note: "see P2"},
			{Type: "permissions", Status: spStatusUnsupported, Note: "partial Graph visibility; see P2"},
			{Type: "pages", Status: spStatusUnsupported, Note: "see P3"},
			{Type: "recycle_bin", Status: spStatusUnsupported, Note: "see P4"},
			{Type: "onenote_packages", Status: spStatusUnsupported, Note: "see P4"},
			{Type: "workflows_apps", Status: spStatusUnsupported, Note: "see P4"},
		},
	}
}

func (c *sharePointCoverage) setType(typeName string, update func(*sharePointContentTypeCoverage)) {
	if c == nil {
		return
	}
	for i := range c.Types {
		if c.Types[i].Type == typeName {
			update(&c.Types[i])
			return
		}
	}
	row := sharePointContentTypeCoverage{Type: typeName}
	update(&row)
	c.Types = append(c.Types, row)
}

func (c *sharePointCoverage) finalize(res sharePointSiteResult) {
	if c == nil {
		return
	}
	c.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	c.LibrariesSynced = res.synced
	c.LibrariesFailed = len(res.failed)
	switch {
	case res.synced == 0 && (len(res.failed) > 0 || res.listErr != nil):
		c.RunStatus = spRunFailed
	case len(res.failed) > 0 || res.listErr != nil || !c.ListComplete:
		c.RunStatus = spRunPartial
	default:
		c.RunStatus = spRunComplete
	}
	libStatus := spStatusPartial
	switch {
	case res.synced == 0 && (len(res.failed) > 0 || res.listErr != nil):
		libStatus = spStatusFailed
	case len(res.failed) == 0 && res.listErr == nil && c.ListComplete:
		libStatus = spStatusComplete
	}
	c.setType("document_libraries", func(t *sharePointContentTypeCoverage) {
		t.Discovered = c.LibrariesTotal
		t.BackedUp = c.LibrariesSynced
		t.Failed = c.LibrariesFailed
		t.InventoryComplete = c.ListComplete
		t.StartedAt = c.StartedAt
		t.FinishedAt = c.FinishedAt
		t.Status = libStatus
		if res.listErr != nil {
			t.Note = "library inventory incomplete; vault libraries preserved"
		}
	})
	c.applyWorkspaceGaps()
}

// applyWorkspaceGaps demotes site run_status to partial when a required content type
 // still has real gaps (libraries / lists / pages).
 //
 // Permissions stay type-status partial forever (caller-visible only) and versions stay
 // discovered_only until content restore is proven — those rows must NOT by themselves
 // mark the whole site partial or spam job warnings on every successful run.
 // Unsupported P4 rows also do not demote the site.
func (c *sharePointCoverage) applyWorkspaceGaps() {
	if c == nil || c.RunStatus == spRunFailed {
		return
	}
	for _, t := range c.Types {
		switch t.Type {
		case "document_libraries", "lists", "pages":
			// required workspace types
		case "permissions", "versions", "recycle_bin", "onenote_packages", "workflows_apps":
			continue
		default:
			continue
		}
		switch t.Status {
		case spStatusComplete, spStatusUnsupported, "":
			continue
		default:
			c.RunStatus = spRunPartial
			return
		}
	}
}

// sharePointCoverageWarning describes real required-type gaps for the job message.
 // Empty when there is nothing user-actionable beyond expected permissions/versions limits.
func sharePointCoverageWarning(cov *sharePointCoverage) string {
	if cov == nil || cov.RunStatus != spRunPartial {
		return ""
	}
	var parts []string
	for _, t := range cov.Types {
		switch t.Type {
		case "document_libraries", "lists", "pages":
		default:
			continue
		}
		switch t.Status {
		case spStatusPartial, spStatusFailed:
			label := t.Type
			switch t.Type {
			case "document_libraries":
				label = "document libraries"
			}
			parts = append(parts, label+" "+t.Status)
		}
	}
	if !cov.ListComplete {
		parts = append(parts, "library inventory incomplete")
	}
	if len(parts) == 0 {
		return ""
	}
	return sharePointWarningPrefix + ": " + strings.Join(parts, "; ") + "; see sharepoint_coverage"
}

func persistSharePointCoverage(input ProcessorInput, cov *sharePointCoverage) {
	if cov == nil || input.Job == nil || input.Database == nil || input.Database.CronJobRepo == nil {
		return
	}
	job := input.Job
	data := map[string]interface{}{}
	if job.InputData != nil && job.InputData.Json() != nil {
		for k, v := range *job.InputData.Json() {
			data[k] = v
		}
	}
	data[sharePointCoverageInputKey] = *cov
	_ = input.Database.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"input_data": database.NewDbJsonFromValue(data),
	})
	// Keep in-memory job copy aligned for subsequent checkpoints in the same run.
	job.InputData = database.NewDbJsonFromValue(data)
}

// persistSharePointSiteCoverage stores one site's coverage under
 // input_data.sharepoint_coverage.sites[siteID] on the combined tenant job.
func persistSharePointSiteCoverage(input ProcessorInput, siteID string, cov *sharePointCoverage) {
	if cov == nil || input.Job == nil || input.Database == nil || input.Database.CronJobRepo == nil {
		return
	}
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		persistSharePointCoverage(input, cov)
		return
	}
	job := input.Job
	data := map[string]interface{}{}
	if job.InputData != nil && job.InputData.Json() != nil {
		for k, v := range *job.InputData.Json() {
			data[k] = v
		}
	}
	root, _ := data[sharePointCoverageInputKey].(map[string]interface{})
	if root == nil {
		// May already be a typed struct from a prior write — rebuild.
		root = map[string]interface{}{}
		if prev, ok := data[sharePointCoverageInputKey].(sharePointCoverage); ok {
			root["started_at"] = prev.StartedAt
			root["run_status"] = prev.RunStatus
		}
	}
	sites, _ := root["sites"].(map[string]interface{})
	if sites == nil {
		sites = map[string]interface{}{}
	}
	sites[siteID] = *cov
	root["sites"] = sites
	if root["started_at"] == nil || root["started_at"] == "" {
		root["started_at"] = cov.StartedAt
	}
	root["run_status"] = spRunInProgress
	data[sharePointCoverageInputKey] = root
	_ = input.Database.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"input_data": database.NewDbJsonFromValue(data),
	})
	job.InputData = database.NewDbJsonFromValue(data)
}

func persistSharePointTenantCoverageRollup(input ProcessorInput, synced, failed, total int) {
	if input.Job == nil || input.Database == nil || input.Database.CronJobRepo == nil {
		return
	}
	job := input.Job
	data := map[string]interface{}{}
	if job.InputData != nil && job.InputData.Json() != nil {
		for k, v := range *job.InputData.Json() {
			data[k] = v
		}
	}
	root, _ := data[sharePointCoverageInputKey].(map[string]interface{})
	if root == nil {
		root = map[string]interface{}{}
	}
	root["finished_at"] = time.Now().UTC().Format(time.RFC3339)
	root["sites_total"] = total
	root["sites_synced"] = synced
	root["sites_failed"] = failed
	switch {
	case synced == 0 && failed > 0:
		root["run_status"] = spRunFailed
	case failed > 0 || synced < total:
		root["run_status"] = spRunPartial
	default:
		root["run_status"] = spRunComplete
	}
	data[sharePointCoverageInputKey] = root
	_ = input.Database.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"input_data": database.NewDbJsonFromValue(data),
	})
	job.InputData = database.NewDbJsonFromValue(data)
}

// mergeKnownLibrariesIntoCoverage keeps previously known libraries visible when the current
 // listing is incomplete (preserve-all / honest coverage).
func mergeKnownLibrariesIntoCoverage(cov *sharePointCoverage, known map[string]repo.SharePointLibraryState, defaultID string, listedIDs map[string]bool, listComplete bool) {
	if cov == nil || known == nil {
		return
	}
	have := map[string]bool{}
	for _, l := range cov.Libraries {
		have[l.DriveID] = true
	}
	for id, st := range known {
		if have[id] {
			continue
		}
		if listComplete && listedIDs[id] {
			continue
		}
		status := spStatusPartial
		if !listComplete {
			status = spStatusPartial
		}
		cov.Libraries = append(cov.Libraries, sharePointLibraryCoverage{
			DriveID:           id,
			Name:              st.Name,
			Default:           id == defaultID || st.Default,
			Status:            status,
			LastSyncAt:        st.LastSyncAt,
			LastError:         st.LastError,
			BaselineDone:      st.BaselineDone,
			InventoryComplete: listComplete && listedIDs[id],
		})
	}
}
