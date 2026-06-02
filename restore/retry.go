package restore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/repo"
)

// RetryGoogle runs fn with exponential backoff on retryable Google / network errors.
func RetryGoogle(ctx context.Context, fn func() error) error {
	var lastErr error
	backoff := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !isRetryableGoogleError(lastErr) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	return lastErr
}

func isRetryableGoogleError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "quota") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "timeout") ||
		errors.Is(err, context.DeadlineExceeded)
}

func ErrorCodeFromErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "429"):
		return "429"
	case strings.Contains(msg, "403") && strings.Contains(msg, "quota"):
		return "403_quota"
	case strings.Contains(msg, "403"):
		return "403"
	case strings.Contains(msg, "404"):
		return "404"
	case strings.Contains(msg, "401"):
		return "401"
	default:
		return "error"
	}
}

// IsRetryTaskDue reports whether a retrying task is eligible for reclaim (next_attempt_at <= now).
func IsRetryTaskDue(status string, nextAttemptAt *time.Time, now time.Time) bool {
	if status != repo.RestoreTaskStatusRetrying || nextAttemptAt == nil {
		return false
	}
	return !nextAttemptAt.After(now)
}

// NextRetryTime returns the next attempt time for a task-level retry.
func NextRetryTime(retryCount uint) time.Time {
	backoff := time.Second << retryCount
	if backoff > 15*time.Minute {
		backoff = 15 * time.Minute
	}
	return time.Now().Add(backoff)
}
