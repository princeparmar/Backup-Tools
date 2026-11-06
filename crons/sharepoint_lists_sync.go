package crons

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

var (
	sharePointSiteListsFn     = outlook.ListSharePointSiteLists
	sharePointListDefFn       = outlook.FetchSharePointListDefinition
	sharePointListItemsPageFn = outlook.ListSharePointListItemsPage
	sharePointListAttsFn      = outlook.ListSharePointItemAttachments
	sharePointListAttDownloadFn = outlook.DownloadSharePointItemAttachment
)

// syncSharePointLists backs up non-library lists (definitions, items, attachments). Preserve-only:
 // Microsoft deletes do not purge vault items. Document libraries are skipped (owned by drive sync).
func syncSharePointLists(ctx context.Context, input ProcessorInput, s sharePointSite, cov *sharePointCoverage) error {
	started := time.Now().UTC().Format(time.RFC3339)
	all, err := sharePointSiteListsFn(ctx, s.token, s.siteID)
	if err != nil {
		if cov != nil {
			cov.setType("lists", func(t *sharePointContentTypeCoverage) {
				t.Status = spStatusPartial
				t.InventoryComplete = false
				t.Note = "list inventory failed; vault list data preserved"
				t.StartedAt = started
				t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			})
			cov.sample("lists", "inventory_failed")
		}
		logger.Warn(ctx, "sharepoint list inventory failed", logger.String("site_id", s.siteID), logger.ErrorField(err))
		return nil // non-fatal for site job; libraries may still succeed
	}
	lists := outlook.SharePointNonLibraryLists(all)
	backed, failed := 0, 0
	for _, list := range lists {
		if err := ctx.Err(); err != nil {
			return err
		}
		if input.HeartBeatFunc != nil {
			if err := input.HeartBeatFunc(); err != nil {
				return err
			}
		}
		if err := syncOneSharePointList(ctx, input, s, list); err != nil {
			if isSharePointRunFatal(err) {
				return err
			}
			failed++
			logger.Warn(ctx, "sharepoint list backup failed", logger.String("list_id", list.ID),
				logger.String("name", list.DisplayName), logger.ErrorField(err))
			if cov != nil {
				cov.sample(list.DisplayName, "list_failed")
			}
			continue
		}
		backed++
	}
	if cov != nil {
		cov.setType("lists", func(t *sharePointContentTypeCoverage) {
			t.Discovered = len(lists)
			t.BackedUp = backed
			t.Failed = failed
			t.InventoryComplete = true
			t.StartedAt = started
			t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			switch {
			case failed == 0 && backed == len(lists):
				t.Status = spStatusComplete
			case backed == 0 && failed > 0:
				t.Status = spStatusFailed
			default:
				t.Status = spStatusPartial
			}
			t.Note = "non-library lists only; document libraries use drive backup"
		})
	}
	return nil
}

func syncOneSharePointList(ctx context.Context, input ProcessorInput, s sharePointSite, list outlook.SharePointSiteList) error {
	label := list.DisplayName
	if strings.TrimSpace(label) == "" {
		label = list.Name
	}
	dir := outlook.SharePointListDir(s.siteKey, list.ID, label)
	store := &satellitePIMStore{
		input: input, storx: s.storx, bucket: satellite.ReserveBucket_OutlookSharePoint,
	}
	def, err := sharePointListDefFn(ctx, s.token, s.siteID, list.ID)
	if err != nil {
		return err
	}
	meta := map[string]any{
		"id": list.ID, "name": list.Name, "displayName": list.DisplayName,
		"webUrl": list.WebURL, "template": list.Template, "kind": list.Kind,
		"site_id": s.siteID, "definition": json.RawMessage(def),
	}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := store.put(ctx, dir+outlook.SharePointListMetaName, metaRaw); err != nil {
		return wrapStorageAbort("outlook_sharepoint", err)
	}

	indexKey := dir + outlook.SharePointListIndexName
	index := outlook.SharePointListItemIndex{V: outlook.SharePointListIndexVersion, Items: map[string]outlook.SharePointListItemIndexEntry{}}
	if tm := input.Job; tm != nil {
		// Load prior index if present in task memory synced set — best-effort via store get.
		if raw, getErr := store.get(ctx, indexKey); getErr == nil {
			_ = json.Unmarshal(raw, &index)
			if index.Items == nil {
				index.Items = map[string]outlook.SharePointListItemIndexEntry{}
			}
		}
	}
	dirty := false
	for next := outlook.SharePointListItemsURL(s.siteID, list.ID); next != ""; {
		if input.HeartBeatFunc != nil {
			if err := input.HeartBeatFunc(); err != nil {
				return err
			}
		}
		items, nextLink, err := sharePointListItemsPageFn(ctx, s.token, next)
		if err != nil {
			return err
		}
		for _, it := range items {
			prev := index.Items[it.ID]
			if prev.ChangeKey != "" && prev.ChangeKey == it.ChangeKey && prev.Key != "" {
				continue
			}
			key := outlook.SharePointListItemKey(dir, it.ID)
			if err := store.put(ctx, key, it.Raw); err != nil {
				if shouldAbortOnItemError(err) {
					return wrapStorageAbort("outlook_sharepoint", err)
				}
				logger.Warn(ctx, "sharepoint list item backup failed", logger.String("key", key), logger.ErrorField(err))
				continue
			}
			if err := backupSharePointItemAttachments(ctx, store, s, list.ID, it.ID, dir); err != nil {
				logger.Warn(ctx, "sharepoint list item attachments incomplete",
					logger.String("item_id", it.ID), logger.ErrorField(err))
			}
			index.Items[it.ID] = outlook.SharePointListItemIndexEntry{Key: key, ChangeKey: it.ChangeKey}
			dirty = true
		}
		next = nextLink
	}
	if dirty || len(index.Items) > 0 {
		index.V = outlook.SharePointListIndexVersion
		raw, err := json.Marshal(index)
		if err != nil {
			return err
		}
		if err := store.put(ctx, indexKey, raw); err != nil {
			return wrapStorageAbort("outlook_sharepoint", err)
		}
	}
	// Persist list delta/checkpoint placeholder in task memory for durable resume.
	if input.Job != nil {
		if input.Job.TaskMemory.SharePointListCursors == nil {
			input.Job.TaskMemory.SharePointListCursors = map[string]repo.SharePointListCursor{}
		}
		input.Job.TaskMemory.SharePointListCursors[list.ID] = repo.SharePointListCursor{
			Name:         list.DisplayName,
			LastSyncAt:   time.Now().UTC().Format(time.RFC3339),
			BaselineDone: true,
		}
	}
	return nil
}

func backupSharePointItemAttachments(ctx context.Context, store pimStore, s sharePointSite, listID, itemID, listDir string) error {
	atts, err := sharePointListAttsFn(ctx, s.token, s.siteID, listID, itemID)
	if err != nil {
		return err
	}
	for _, raw := range atts {
		var head struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID == "" {
			continue
		}
		data, name, err := sharePointListAttDownloadFn(ctx, s.token, s.siteID, listID, itemID, head.ID)
		if err != nil {
			return err
		}
		if name == "" {
			name = head.Name
		}
		key := outlook.SharePointListItemAttachmentKey(listDir, itemID, head.ID, name)
		if err := store.put(ctx, key, data); err != nil {
			return err
		}
	}
	return nil
}
