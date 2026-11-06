package crons

import (
	"context"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

var (
	sharePointPagesFn      = outlook.ListSharePointSitePages
	sharePointPageGetFn    = outlook.FetchSharePointSitePage
	sharePointSiteConfigFn = outlook.FetchSharePointSiteConfig
)

// syncSharePointPages backs up modern site pages (full JSON when Graph allows) plus a
 // Graph-restorable site config snapshot. Preserve-only.
func syncSharePointPages(ctx context.Context, input ProcessorInput, s sharePointSite, cov *sharePointCoverage) {
	started := time.Now().UTC().Format(time.RFC3339)
	store := &satellitePIMStore{input: input, storx: s.storx, bucket: satellite.ReserveBucket_OutlookSharePoint}
	if cfg, err := sharePointSiteConfigFn(ctx, s.token, s.siteID); err != nil {
		logger.Warn(ctx, "sharepoint site config snapshot failed", logger.ErrorField(err))
	} else if err := store.put(ctx, s.siteKey+"/_site_config.json", cfg); err != nil {
		logger.Warn(ctx, "sharepoint site config store failed", logger.ErrorField(err))
	}

	pages, err := sharePointPagesFn(ctx, s.token, s.siteID)
	if err != nil {
		logger.Warn(ctx, "sharepoint pages inventory failed", logger.ErrorField(err))
		if cov != nil {
			cov.setType("pages", func(t *sharePointContentTypeCoverage) {
				t.Status = spStatusPartial
				t.InventoryComplete = false
				t.Note = "pages inventory failed; vault pages preserved"
				t.StartedAt = started
				t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			})
		}
		return
	}
	backed, failed := 0, 0
	for _, p := range pages {
		if input.HeartBeatFunc != nil {
			if err := input.HeartBeatFunc(); err != nil {
				return
			}
		}
		raw := p.Raw
		if full, getErr := sharePointPageGetFn(ctx, s.token, s.siteID, p.ID); getErr == nil {
			raw = full
		}
		key := outlook.SharePointPageObjectKey(s.siteKey, p.ID, p.Name, p.Title)
		if err := store.put(ctx, key, raw); err != nil {
			failed++
			logger.Warn(ctx, "sharepoint page backup failed", logger.String("page_id", p.ID), logger.ErrorField(err))
			continue
		}
		backed++
	}
	if cov != nil {
		cov.setType("pages", func(t *sharePointContentTypeCoverage) {
			t.Discovered = len(pages)
			t.BackedUp = backed
			t.Failed = failed
			t.InventoryComplete = true
			t.StartedAt = started
			t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			switch {
			case failed == 0 && backed == len(pages):
				t.Status = spStatusComplete
			case backed == 0 && len(pages) > 0:
				t.Status = spStatusFailed
			case len(pages) == 0:
				t.Status = spStatusComplete
				t.Note = "no site pages discovered"
			default:
				t.Status = spStatusPartial
			}
		})
	}
}
