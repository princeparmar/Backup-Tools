package google

import (
	"context"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

// FallbackObjectKeyDatePath is used when a platform created date is missing or unparseable.
const FallbackObjectKeyDatePath = "1970/01/01"

// ObjectKeyDatePathFromRFC3339 returns yyyy/mm/dd from an RFC3339 timestamp.
// On empty/invalid input it returns FallbackObjectKeyDatePath and logs a warning.
func ObjectKeyDatePathFromRFC3339(created string) string {
	created = strings.TrimSpace(created)
	if created == "" {
		logger.Warn(context.Background(), "object key date missing; using fallback",
			logger.String("fallback", FallbackObjectKeyDatePath),
		)
		return FallbackObjectKeyDatePath
	}
	t, err := time.Parse(time.RFC3339, created)
	if err != nil {
		// Google sometimes returns RFC3339 without timezone offset seconds variants.
		if t2, err2 := time.Parse("2006-01-02T15:04:05.999999999Z07:00", created); err2 == nil {
			t = t2
		} else if t3, err3 := time.Parse("2006-01-02T15:04:05Z07:00", created); err3 == nil {
			t = t3
		} else {
			logger.Warn(context.Background(), "object key date parse failed; using fallback",
				logger.String("created", created),
				logger.String("fallback", FallbackObjectKeyDatePath),
				logger.ErrorField(err),
			)
			return FallbackObjectKeyDatePath
		}
	}
	return t.UTC().Format("2006/01/02")
}

// ObjectKeyDatePathFromUnixMilli returns yyyy/mm/dd from Unix epoch milliseconds (e.g. Gmail internalDate).
func ObjectKeyDatePathFromUnixMilli(ms int64) string {
	if ms <= 0 {
		logger.Warn(context.Background(), "object key date missing; using fallback",
			logger.String("fallback", FallbackObjectKeyDatePath),
		)
		return FallbackObjectKeyDatePath
	}
	return time.UnixMilli(ms).UTC().Format("2006/01/02")
}

// LastPathSegment returns the final path component of key (no trailing slash).
func LastPathSegment(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimSuffix(key, "/")
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}
