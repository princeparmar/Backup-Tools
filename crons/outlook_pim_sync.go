package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Graph seam (overridden in tests).
var pimListPageFn = outlook.ListPIMPage

// pimStore is the backup bucket of one calendar or contacts job.
type pimStore interface {
	get(ctx context.Context, key string) ([]byte, error)
	put(ctx context.Context, key string, data []byte) error
}

type satellitePIMStore struct {
	input  ProcessorInput
	storx  string
	bucket string
}

func (s *satellitePIMStore) get(ctx context.Context, key string) ([]byte, error) {
	return satellite.DownloadObject(ctx, s.storx, s.bucket, key)
}

func (s *satellitePIMStore) put(ctx context.Context, key string, data []byte) error {
	return handler.UploadObjectAndSync(ctx, s.input.Database, s.storx, s.bucket, key, data, s.input.Job.UserID, s.input.StorxRecovery)
}

// pimRun backs up Graph events or contacts, one collection (calendar or contact folder) at a time.
type pimRun struct {
	store       pimStore
	accessToken string
	method      string
	// synced holds every backed-up key of the job; an index missing from it starts empty.
	synced    map[string]bool
	heartbeat func() error
	now       func() time.Time

	uploaded, unchanged, removed int
}

// syncCollection backs up every item listed from listURL under dir. Items whose changeKey matches
// the collection index are skipped; items no longer listed are marked removed in the index and
// their backup kept.
func (r *pimRun) syncCollection(ctx context.Context, listURL, dir string) error {
	indexKey := dir + outlook.PIMIndexName
	index, err := r.loadIndex(ctx, indexKey)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	dirty := false
	for next := listURL; next != ""; {
		if err := r.heartbeat(); err != nil {
			return err
		}
		items, nextLink, err := pimListPageFn(ctx, r.accessToken, next)
		if err != nil {
			return err
		}
		for _, it := range items {
			seen[it.ID] = true
			key := outlook.PIMItemKey(dir, it.ID)
			if e, ok := index.Items[it.ID]; ok && e.Key == key && e.ChangeKey == it.ChangeKey && e.RemovedAt == "" && it.ChangeKey != "" {
				r.unchanged++
				continue
			}
			if err := r.store.put(ctx, key, it.Raw); err != nil {
				if shouldAbortOnItemError(err) {
					if dirty {
						r.saveIndex(ctx, indexKey, index)
					}
					return wrapStorageAbort(r.method, err)
				}
				logger.Warn(ctx, "outlook item backup failed", logger.String("method", r.method), logger.String("key", key), logger.ErrorField(err))
				continue
			}
			index.Items[it.ID] = outlook.PIMIndexEntry{Key: key, ChangeKey: it.ChangeKey}
			r.synced[key] = true
			r.uploaded++
			dirty = true
		}
		next = nextLink
	}
	removedAt := r.now().UTC().Format(time.RFC3339)
	for id, e := range index.Items {
		if !seen[id] && e.RemovedAt == "" {
			e.RemovedAt = removedAt
			index.Items[id] = e
			r.removed++
			dirty = true
		}
	}
	if dirty {
		return r.saveIndex(ctx, indexKey, index)
	}
	return nil
}

func (r *pimRun) loadIndex(ctx context.Context, key string) (outlook.PIMIndex, error) {
	index := outlook.PIMIndex{Items: map[string]outlook.PIMIndexEntry{}}
	if !r.synced[key] {
		return index, nil
	}
	raw, err := r.store.get(ctx, key)
	if err != nil {
		return index, fmt.Errorf("read backup index %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, &index); err != nil || index.Items == nil {
		logger.Warn(ctx, "outlook backup index unreadable; backing up the collection again", logger.String("key", key))
		index.Items = map[string]outlook.PIMIndexEntry{}
	}
	return index, nil
}

func (r *pimRun) saveIndex(ctx context.Context, key string, index outlook.PIMIndex) error {
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := r.store.put(ctx, key, raw); err != nil {
		return wrapStorageAbort(r.method, fmt.Errorf("save backup index %s: %w", key, err))
	}
	r.synced[key] = true
	return nil
}

// putJSON writes a small descriptor object (calendar or contact folder).
func (r *pimRun) putJSON(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := r.store.put(ctx, key, raw); err != nil {
		if shouldAbortOnItemError(err) {
			return wrapStorageAbort(r.method, err)
		}
		logger.Warn(ctx, "outlook descriptor backup failed", logger.String("method", r.method), logger.String("key", key), logger.ErrorField(err))
		return nil
	}
	r.synced[key] = true
	return nil
}
