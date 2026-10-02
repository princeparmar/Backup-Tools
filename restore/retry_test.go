package restore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

func TestIsRetryableGoogleError_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "429 string", err: errors.New("googleapi: Error 429: Too many requests"), want: true},
		{name: "googleapi 429", err: &googleapi.Error{Code: 429, Message: "Too many requests"}, want: true},
		{name: "403 quota", err: errors.New("googleapi: Error 403: User Rate Limit Exceeded"), want: true},
		{name: "googleapi 403 quota", err: &googleapi.Error{Code: 403, Message: "Rate Limit Exceeded"}, want: true},
		{name: "403 non-quota", err: errors.New("googleapi: Error 403: Forbidden"), want: false},
		{name: "googleapi 403 non-quota", err: &googleapi.Error{Code: 403, Message: "Forbidden"}, want: false},
		{name: "500", err: errors.New("googleapi: Error 500: Internal"), want: true},
		{name: "timeout", err: errors.New("context deadline exceeded: timeout"), want: true},
		{name: "generic", err: errors.New("invalid argument"), want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableGoogleError(tt.err); got != tt.want {
				t.Fatalf("got %v want %v for %v", got, tt.want, tt.err)
			}
		})
	}
}

func TestIsGoogleRateLimitError_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "429 string", err: fmt.Errorf("status 429"), want: true},
		{name: "googleapi 429", err: &googleapi.Error{Code: 429, Message: "rateLimitExceeded"}, want: true},
		{name: "403 quota message", err: errors.New("Error 403: quotaExceeded"), want: true},
		{name: "503 not rate limit", err: errors.New("Error 503: Service Unavailable"), want: false},
		{name: "googleapi 500", err: &googleapi.Error{Code: 500, Message: "Internal"}, want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isGoogleRateLimitError(tt.err); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestErrorCodeFromErr_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "googleapi 429", err: &googleapi.Error{Code: 429, Message: "Too many requests"}, want: "429"},
		{name: "googleapi 403 quota", err: &googleapi.Error{Code: 403, Message: "User Rate Limit Exceeded"}, want: "403_quota"},
		{name: "googleapi 403", err: &googleapi.Error{Code: 403, Message: "Forbidden"}, want: "403"},
		{name: "string 429", err: errors.New("Error 429: rateLimitExceeded"), want: "429"},
		{name: "string 401", err: errors.New("Error 401: invalid_token"), want: "401"},
		{name: "unknown", err: errors.New("boom"), want: "error"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ErrorCodeFromErr(tt.err); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestGoogleRetryDelay_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		attempt     int
		rateLimited bool
		maxCap      time.Duration
	}{
		{name: "normal attempt 0", attempt: 0, rateLimited: false, maxCap: googleRetryBackoffCap},
		{name: "normal high attempt capped", attempt: 10, rateLimited: false, maxCap: googleRetryBackoffCap},
		{name: "rate limit attempt 0", attempt: 0, rateLimited: true, maxCap: googleRateLimitBackoffCap},
		{name: "rate limit high attempt capped", attempt: 10, rateLimited: true, maxCap: googleRateLimitBackoffCap},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := googleRetryDelay(tt.attempt, tt.rateLimited)
			if d < 0 || d > tt.maxCap {
				t.Fatalf("delay %v outside [0, %v]", d, tt.maxCap)
			}
		})
	}
}

func TestServiceConfigThrottle_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method           string
		batchSize        int
		maxConcurrency   int
		vaultConcurrency int
		rateLimitPerSec  float64
	}{
		{method: "gmail", batchSize: 25, maxConcurrency: 2, vaultConcurrency: 2, rateLimitPerSec: 2},
		{method: "google_drive", batchSize: 25, maxConcurrency: 3, vaultConcurrency: 3, rateLimitPerSec: 5},
		{method: "google_photos", batchSize: 10, maxConcurrency: 3, vaultConcurrency: 3, rateLimitPerSec: 5},
		{method: "google_calendar", batchSize: 50, maxConcurrency: 5, vaultConcurrency: 5, rateLimitPerSec: 8},
		{method: "google_contacts", batchSize: 50, maxConcurrency: 5, vaultConcurrency: 5, rateLimitPerSec: 8},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			cfg, ok := ConfigForMethod(tt.method)
			if !ok {
				t.Fatal("missing config")
			}
			if cfg.BatchSize != tt.batchSize ||
				cfg.MaxConcurrency != tt.maxConcurrency ||
				cfg.VaultConcurrency != tt.vaultConcurrency ||
				cfg.RateLimitPerSec != tt.rateLimitPerSec {
				t.Fatalf("got %+v", cfg)
			}
		})
	}
}
