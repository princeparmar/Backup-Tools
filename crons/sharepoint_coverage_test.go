package crons

import (
	"strings"
	"testing"
)

func TestSharePointCoveragePermissionsVersionsDoNotDemoteSite(t *testing.T) {
	cov := newSharePointCoverage("site", "key")
	cov.ListComplete = true
	cov.RunStatus = spRunComplete
	cov.setType("document_libraries", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusComplete
		ct.InventoryComplete = true
	})
	cov.setType("lists", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusComplete
		ct.InventoryComplete = true
	})
	cov.setType("pages", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusComplete
		ct.InventoryComplete = true
	})
	cov.setType("permissions", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusPartial
		ct.InventoryComplete = false
	})
	cov.setType("versions", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusDiscoveredOnly
		ct.InventoryComplete = false
	})
	cov.applyWorkspaceGaps()
	if cov.RunStatus != spRunComplete {
		t.Fatalf("expected permissions/versions not to demote site, got %s", cov.RunStatus)
	}
	if msg := sharePointCoverageWarning(cov); msg != "" {
		t.Fatalf("expected no job warning for expected gaps, got %q", msg)
	}
}

func TestSharePointCoverageListsPartialDemotesSite(t *testing.T) {
	cov := newSharePointCoverage("site", "key")
	cov.ListComplete = true
	cov.RunStatus = spRunComplete
	cov.setType("document_libraries", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusComplete
	})
	cov.setType("lists", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusPartial
		ct.Failed = 1
	})
	cov.setType("pages", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusComplete
	})
	cov.setType("permissions", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusPartial
	})
	cov.applyWorkspaceGaps()
	if cov.RunStatus != spRunPartial {
		t.Fatalf("lists partial must demote site, got %s", cov.RunStatus)
	}
	msg := sharePointCoverageWarning(cov)
	if !strings.Contains(msg, "lists partial") {
		t.Fatalf("warning should name lists gap, got %q", msg)
	}
}

func TestSharePointCoverageUnsupportedP4DoesNotAloneFail(t *testing.T) {
	cov := newSharePointCoverage("site", "key")
	cov.RunStatus = spRunComplete
	for _, typ := range []string{"document_libraries", "lists", "pages"} {
		typ := typ
		cov.setType(typ, func(ct *sharePointContentTypeCoverage) {
			ct.Status = spStatusComplete
			ct.InventoryComplete = true
		})
	}
	cov.setType("permissions", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusPartial
		ct.InventoryComplete = false
	})
	cov.setType("versions", func(ct *sharePointContentTypeCoverage) {
		ct.Status = spStatusDiscoveredOnly
	})
	cov.applyWorkspaceGaps()
	if cov.RunStatus != spRunComplete {
		t.Fatalf("all required complete should stay complete, got %s", cov.RunStatus)
	}
}
