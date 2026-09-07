package restore

import (
	"context"
	"errors"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/repo"
	storxrefresh "github.com/StorX2-0/Backup-Tools/storx"
)

func runBatchWithStorxRecovery(ctx context.Context, deps *RestoreDeps, proc Processor, rows []repo.SyncedObject) (BatchResult, error) {
	result := RunBatch(ctx, deps, proc, rows)
	if deps == nil || deps.StorxRecovery == nil || !shouldStorxBatchRetry(result) {
		return result, nil
	}

	uplinkRecoveries := 0
	for shouldStorxBatchRetry(result) {
		if deps.Store != nil && deps.Job != nil {
			if err := checkJobContinuable(deps.Store, deps.Job.ID); err != nil {
				return result, err
			}
		}

		uplinkRecoveries++
		if uplinkRecoveries > storxrefresh.MaxUplinkRecoveriesPerRun() {
			return result, fmt.Errorf("%s", firstStorxBatchError(result))
		}

		grant, continueOK, err := deps.StorxRecovery.OnStorxError(ctx, errors.New(firstStorxBatchError(result)))
		if err != nil {
			return result, err
		}
		if !continueOK {
			return result, errors.New(firstStorxBatchError(result))
		}
		deps.AccessGrant = grant
		result = RunBatch(ctx, deps, proc, rows)
	}
	return result, nil
}

func shouldStorxBatchRetry(result BatchResult) bool {
	if result.Processed > 0 || result.Failed == 0 || len(result.FailedKeys) == 0 {
		return false
	}
	for _, fk := range result.FailedKeys {
		if !storxrefresh.IsUplinkError(errors.New(fk.Reason)) {
			return false
		}
	}
	return true
}

func firstStorxBatchError(result BatchResult) string {
	if len(result.FailedKeys) == 0 {
		return "storx uplink error"
	}
	return result.FailedKeys[0].Reason
}
