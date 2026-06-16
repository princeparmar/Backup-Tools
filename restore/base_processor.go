package restore

import (
	"context"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/repo"
	"golang.org/x/sync/errgroup"
)

// RunBatch restores a page of synced objects with centralized backpressure.
func RunBatch(ctx context.Context, deps *RestoreDeps, proc Processor, rows []repo.SyncedObject) BatchResult {
	result := BatchResult{}
	if len(rows) == 0 {
		return result
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(deps.Config.MaxConcurrency)

	var mu sync.Mutex
	recordFailure := func(objectKey string, err error) {
		mu.Lock()
		defer mu.Unlock()
		result.Failed++
		result.FailedKeys = append(result.FailedKeys, FailedKey{
			ObjectKey: objectKey,
			Reason:    err.Error(),
			ErrorCode: ErrorCodeFromErr(err),
		})
	}

	for _, row := range rows {
		row := row
		if !proc.ShouldRestoreKey(row.ObjectKey) {
			continue
		}
		g.Go(func() error {
			if err := deps.waitRate(gctx); err != nil {
				recordFailure(row.ObjectKey, err)
				return nil
			}
			err := deps.withVault(gctx, func() error {
				return proc.RestoreKey(gctx, deps, row.ObjectKey)
			})
			if err != nil {
				recordFailure(row.ObjectKey, err)
				return nil
			}
			mu.Lock()
			result.Processed++
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		recordFailure("", err)
	}
	result.LastObjectID = rows[len(rows)-1].ID
	return result
}

func (d *RestoreDeps) waitRate(ctx context.Context) error {
	if d.googleLimiter == nil {
		return nil
	}
	return d.googleLimiter.Wait(ctx)
}

func (d *RestoreDeps) withVault(ctx context.Context, fn func() error) error {
	if d.vaultSem == nil {
		return fn()
	}
	select {
	case d.vaultSem <- struct{}{}:
		defer func() { <-d.vaultSem }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ShouldSkipObjectKey filters placeholders and empty keys (shared by processors and tests).
func ShouldSkipObjectKey(objectKey string) bool {
	key := strings.TrimSpace(objectKey)
	return key == "" || strings.Contains(key, "/.file_placeholder")
}
