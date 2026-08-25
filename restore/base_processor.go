package restore

import (
	"context"
	"sync"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/repo"
	"golang.org/x/sync/errgroup"
)

const restoreJobHeartbeatMinInterval = 10 * time.Second

// touchJobHeartbeat updates last_heart_beat while a batch is in flight (throttled).
func (d *RestoreDeps) touchJobHeartbeat() {
	if d == nil || d.Store == nil || d.Job == nil {
		return
	}
	d.heartbeatMu.Lock()
	defer d.heartbeatMu.Unlock()
	if !d.lastHeartbeat.IsZero() && time.Since(d.lastHeartbeat) < restoreJobHeartbeatMinInterval {
		return
	}
	d.lastHeartbeat = time.Now()
	_ = d.Store.RestoreJobRepo.UpdateHeartBeat(d.Job.ID)
}

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
		if deps != nil && deps.Job != nil {
			logger.Warn(ctx, "Restore item failed",
				logger.Int("job_id", int(deps.Job.ID)),
				logger.String("method", deps.Job.Method),
				logger.String("login_id", deps.Job.LoginID),
				logger.String("object_key", objectKey),
				logger.String("error_code", ErrorCodeFromErr(err)),
				logger.ErrorField(err))
		}
	}

	for _, row := range rows {
		row := row
		if !proc.ShouldRestoreKey(row.ObjectKey) {
			continue
		}
		g.Go(func() error {
			started := time.Now()
			if err := deps.waitRate(gctx); err != nil {
				recordFailure(row.ObjectKey, err)
				return nil
			}
			err := deps.withVault(gctx, func() error {
				return proc.RestoreKey(gctx, deps, row.ObjectKey)
			})
			if err != nil {
				recordFailure(row.ObjectKey, err)
				deps.touchJobHeartbeat()
				return nil
			}
			mu.Lock()
			result.Processed++
			mu.Unlock()
			deps.touchJobHeartbeat()
			if deps.Job != nil {
				logger.Info(ctx, "Restore item completed",
					logger.Int("job_id", int(deps.Job.ID)),
					logger.String("method", deps.Job.Method),
					logger.String("login_id", deps.Job.LoginID),
					logger.String("object_key", row.ObjectKey),
					logger.Int64("duration_ms", time.Since(started).Milliseconds()))
			}
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

