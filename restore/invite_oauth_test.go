package restore

import (
	"context"
	"strings"
	"testing"
)

func TestInviteOAuthReadinessDoesNotAskForDelegation(t *testing.T) {
	out := &ReadinessResult{Service: "gmail", LoginID: "support@salestalker.com"}
	got, err := evaluateInviteOAuthReadiness(context.Background(), out, "gmail", out.LoginID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready {
		t.Fatal("empty Google sign-in should not be ready")
	}
	if got.Reason != ReadinessReasonInviteOAuthRequired {
		t.Fatalf("reason = %q", got.Reason)
	}
	if strings.Contains(strings.ToLower(got.Message), "authorize the gmail restore scope") {
		t.Fatalf("message = %q", got.Message)
	}
	if strings.Contains(got.Message, "Admin Console") {
		t.Fatalf("message = %q", got.Message)
	}
}
