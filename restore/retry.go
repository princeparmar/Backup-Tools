package restore

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	googleRetryMaxAttempts     = 5
	googleRetryBackoffCap      = 15 * time.Second
	restoreTaskRetryBackoffCap = 15 * time.Minute
)

// RetryGoogle runs fn with exponential backoff on retryable Google / network errors.
func RetryGoogle(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < googleRetryMaxAttempts; attempt++ {
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
		if attempt == googleRetryMaxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(googleRetryDelay(attempt)):
		}
	}
	return lastErr
}

func googleRetryDelay(attempt int) time.Duration {
	base := time.Second << attempt
	if base > googleRetryBackoffCap {
		base = googleRetryBackoffCap
	}
	return jitterDuration(base)
}

// jitterDuration returns a random delay in [0, max] to reduce synchronized retry storms.
func jitterDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}

func isRetryableGoogleError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") {
		return true
	}
	if isGoogleServerError(msg) || strings.Contains(msg, "timeout") {
		return true
	}
	if strings.Contains(msg, "403") {
		return isQuotaOrRateLimitMessage(msg)
	}
	return isQuotaOrRateLimitMessage(msg)
}

func isGoogleServerError(msg string) bool {
	return strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504")
}

func isQuotaOrRateLimitMessage(msg string) bool {
	return strings.Contains(msg, "quota") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "ratelimit") ||
		strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "userratelimitexceeded") ||
		strings.Contains(msg, "ratelimitexceeded")
}

func ErrorCodeFromErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "429"):
		return "429"
	case strings.Contains(msg, "403") && isQuotaOrRateLimitMessage(msg):
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
	if backoff > restoreTaskRetryBackoffCap {
		backoff = restoreTaskRetryBackoffCap
	}
	return time.Now().Add(jitterDuration(backoff))
}
