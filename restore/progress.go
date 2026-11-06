package restore

// adjustedRestoreTotal lowers the job total for inventory-only keys, but never
// below work already counted. Shrinking below processed+failed made live UI
// show 100% while status stayed running.
func adjustedRestoreTotal(total, skipped, processed, failed uint) uint {
	if skipped > 0 && total >= skipped {
		total -= skipped
	}
	return liftRestoreTotal(total, processed, failed)
}

// liftRestoreTotal raises total when processed+failed already exceeded it
// (mid-batch bumps can outrun a shrunken inventory total).
func liftRestoreTotal(total, processed, failed uint) uint {
	done := processed + failed
	if total < done {
		return done
	}
	return total
}

// recoveredRestoreCursor keeps the furthest page cursor from leftover tasks
// so a reclaim does not restart Drive from id 0 after stacked batches.
func recoveredRestoreCursor(jobCursor, maxTaskEnd uint) uint {
	if maxTaskEnd > jobCursor {
		return maxTaskEnd
	}
	return jobCursor
}
