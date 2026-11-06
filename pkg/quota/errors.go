package quota

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// FailureCodeStorageQuota is persisted on cron jobs when storage pre-check or mid-run limit fails.
	FailureCodeStorageQuota = "STORAGE_QUOTA"
	// FailureCodeBandwidthQuota is persisted when download/restore bandwidth is insufficient.
	FailureCodeBandwidthQuota = "BANDWIDTH_QUOTA"

	// SafetyFactor is kept for API compatibility; required bytes equal the estimate (no inflation).
	SafetyFactor = 1.0
)

// ErrStorageQuota is returned when a backup must not start (or mid-run storage limit).
type ErrStorageQuota struct {
	EstimateBytes  int64
	RequiredBytes  int64
	RemainingBytes int64
	UsedBytes      int64
	LimitBytes     int64
	Method         string
	MidRun         bool
}

func (e *ErrStorageQuota) Error() string {
	if e == nil {
		return "storage quota exceeded"
	}
	if e.MidRun {
		return fmt.Sprintf("storage limit exceeded (method=%s used=%d limit=%d)", e.Method, e.UsedBytes, e.LimitBytes)
	}
	return fmt.Sprintf(
		"STORAGE_QUOTA: need %d bytes (estimate %d), remaining %d (used %d / limit %d) method=%s",
		e.RequiredBytes, e.EstimateBytes, e.RemainingBytes, e.UsedBytes, e.LimitBytes, e.Method,
	)
}

// ErrBandwidthQuota is returned when a restore/export download must not start.
type ErrBandwidthQuota struct {
	EstimateBytes  int64
	RemainingBytes int64
	UsedBytes      int64
	LimitBytes     int64
}

func (e *ErrBandwidthQuota) Error() string {
	if e == nil {
		return "bandwidth quota exceeded"
	}
	return fmt.Sprintf(
		"BANDWIDTH_QUOTA: need %d bytes, remaining %d (used %d / limit %d)",
		e.EstimateBytes, e.RemainingBytes, e.UsedBytes, e.LimitBytes,
	)
}

// IsStorageQuota reports STORAGE_QUOTA / storage limit errors (pre-check or uplink).
func IsStorageQuota(err error) bool {
	if err == nil {
		return false
	}
	var sq *ErrStorageQuota
	if errors.As(err, &sq) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "storage limit exceeded") ||
		strings.Contains(msg, "storage_quota") ||
		strings.HasPrefix(msg, "storage_quota:")
}

// IsBandwidthQuota reports BANDWIDTH_QUOTA / egress limit errors.
func IsBandwidthQuota(err error) bool {
	if err == nil {
		return false
	}
	var bq *ErrBandwidthQuota
	if errors.As(err, &bq) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "bandwidth_quota") ||
		strings.Contains(msg, "exceeded usage limit") ||
		(strings.Contains(msg, "bandwidth") && strings.Contains(msg, "limit"))
}

// RequiredBytes returns the estimate as-is (no safety multiplier).
func RequiredBytes(estimate int64) int64 {
	if estimate <= 0 {
		return 0
	}
	return estimate
}

// FileFitsStorage reports whether fileBytes fits in remaining storage.
// fileBytes<=0 (unknown size, e.g. some Google Docs) always fits — caller may upload and rely on Layer-2.
func FileFitsStorage(fileBytes, remainingBytes int64) bool {
	if fileBytes <= 0 {
		return true
	}
	return RequiredBytes(fileBytes) <= remainingBytes
}
