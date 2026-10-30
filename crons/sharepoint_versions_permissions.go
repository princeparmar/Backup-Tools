package crons

import (
	"context"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

var (
	sharePointSitePermsFn  = outlook.FetchSharePointSitePermissions
	sharePointDrivePermsFn = outlook.FetchSharePointDriveRootPermissions
)

// syncSharePointVersionsAndPermissions stores caller-visible permission snapshots.
 // inventory_complete is always false for permissions. Versions are marked discovered_only
 // until full version content restore is proven (P2 gate).
func syncSharePointVersionsAndPermissions(ctx context.Context, input ProcessorInput, s sharePointSite, cov *sharePointCoverage) {
	started := time.Now().UTC().Format(time.RFC3339)
	store := &satellitePIMStore{input: input, storx: s.storx, bucket: satellite.ReserveBucket_OutlookSharePoint}

	permBacked, permFailed := 0, 0
	if body, err := sharePointSitePermsFn(ctx, s.token, s.siteID); err != nil {
		permFailed++
		logger.Warn(ctx, "sharepoint site permissions not fetched", logger.ErrorField(err))
	} else {
		key := s.siteKey + "/_permissions/site.json"
		if err := store.put(ctx, key, body); err != nil {
			permFailed++
		} else {
			permBacked++
		}
	}

	if input.Job != nil {
		for driveID := range input.Job.TaskMemory.SharePointLibraries {
			body, err := sharePointDrivePermsFn(ctx, s.token, driveID)
			if err != nil {
				permFailed++
				continue
			}
			key := outlook.SharePointLibraryPrefix(s.siteKey, driveID) + "_permissions.json"
			if err := store.put(ctx, key, body); err != nil {
				permFailed++
				continue
			}
			permBacked++
		}
	}

	if cov != nil {
		cov.setType("permissions", func(t *sharePointContentTypeCoverage) {
			t.Discovered = permBacked + permFailed
			t.BackedUp = permBacked
			t.Failed = permFailed
			t.InventoryComplete = false
			t.StartedAt = started
			t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			t.Status = spStatusPartial
			t.Note = "caller-visible permissions only; not a complete ACL inventory"
		})
		cov.setType("versions", func(t *sharePointContentTypeCoverage) {
			t.Status = spStatusDiscoveredOnly
			t.InventoryComplete = false
			t.StartedAt = started
			t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			t.Note = "version content restore not yet complete; see Phase D / P2"
		})
	}
}
