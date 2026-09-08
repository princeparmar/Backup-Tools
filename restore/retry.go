package restore

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/repo"
	"google.golang.org/api/googleapi"
)

const (
	googleRetryMaxAttempts       = 5
	googleRetryBackoffCap        = 15 * time.Second
	googleRateLimitMaxAttempts   = 8
	googleRateLimitBackoffCap    = 60 * time.Second
	restoreTaskRetryBackoffCap   = 15 * time.Minute
)

// RetryGoogle runs fn with exponential backoff on retryable Google / network errors.
// Rate-limit (429 / quota) errors get more attempts and a longer backoff cap.
func RetryGoogle(ctx context.Context, fn func() error) error {
	var lastErr error
	maxAttempts := googleRetryMaxAttempts
	for attempt := 0; attempt < maxAttempts; attempt++ {
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
		if isGoogleRateLimitError(lastErr) {
			maxAttempts = googleRateLimitMaxAttempts
		}
		if attempt == maxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(googleRetryDelay(attempt, isGoogleRateLimitError(lastErr))):
		}
	}
	return lastErr
}

func googleRetryDelay(attempt int, rateLimited bool) time.Duration {
	capDelay := googleRetryBackoffCap
	if rateLimited {
		capDelay = googleRateLimitBackoffCap
	}
	base := time.Second << attempt
	if base > capDelay {
		base = capDelay
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
	if isGoogleRateLimitError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if isGoogleServerError(msg) || strings.Contains(msg, "timeout") {
		return true
	}
	if strings.Contains(msg, "403") {
		return isQuotaOrRateLimitMessage(msg)
	}
	return isQuotaOrRateLimitMessage(msg)
}

func isGoogleRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr != nil && apiErr.Code == 429 {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") {
		return true
	}
	if strings.Contains(msg, "403") && isQuotaOrRateLimitMessage(msg) {
		return true
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
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		switch apiErr.Code {
		case 429:
			return "429"
		case 403:
			if isQuotaOrRateLimitMessage(strings.ToLower(apiErr.Message)) || isQuotaOrRateLimitMessage(strings.ToLower(apiErr.Error())) {
				return "403_quota"
			}
			return "403"
		case 404:
			return "404"
		case 401:
			return "401"
		}
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
