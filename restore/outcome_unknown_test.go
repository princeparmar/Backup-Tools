package restore

import (
	"errors"
	"fmt"
	"testing"
)

type codedTestError struct{ code string }

func (e *codedTestError) Error() string            { return "coded " + e.code }
func (e *codedTestError) RestoreErrorCode() string { return e.code }

func TestErrorCodeFromErrUsesItemCode(t *testing.T) {
	if got := ErrorCodeFromErr(fmt.Errorf("wrapped: %w", &codedTestError{code: ErrorCodeOutcomeUnknown})); got != ErrorCodeOutcomeUnknown {
		t.Fatalf("got %q", got)
	}
	if got := ErrorCodeFromErr(&codedTestError{code: "skipped"}); got != "skipped" {
		t.Fatalf("got %q", got)
	}
	if got := ErrorCodeFromErr(&codedTestError{}); got != "error" {
		t.Fatalf("an empty item code falls back to the error text: %q", got)
	}
	if got := ErrorCodeFromErr(errors.New("Error 401: invalid_token")); got != "401" {
		t.Fatalf("got %q", got)
	}
}

func TestBatchWithUnknownOutcomeIsNotRetried(t *testing.T) {
	failedOnly := BatchResult{Failed: 2, FailedKeys: []FailedKey{
		{ObjectKey: "a", ErrorCode: "403"}, {ObjectKey: "b", ErrorCode: "error"},
	}}
	if !batchRetryable(failedOnly) {
		t.Fatal("definite failures keep the batch retry")
	}
	unknown := BatchResult{Failed: 2, FailedKeys: []FailedKey{
		{ObjectKey: "a", ErrorCode: "403"}, {ObjectKey: "b", ErrorCode: ErrorCodeOutcomeUnknown},
	}}
	if batchRetryable(unknown) {
		t.Fatal("a batch with a write of unknown outcome is not retried")
	}
}
