package google

import (
	"context"
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/quota"
)

func TestClampContacts(t *testing.T) {
	if got := clampContacts(100); got != ContactsMinEstimateBytes {
		t.Fatalf("small clamp: got %d want %d", got, ContactsMinEstimateBytes)
	}
	big := int64(ContactsMaxEstimateBytes + 1)
	if got := clampContacts(big); got != ContactsMaxEstimateBytes {
		t.Fatalf("big clamp: got %d want %d", got, ContactsMaxEstimateBytes)
	}
	mid := int64(10 * 1024 * 1024)
	if got := clampContacts(mid); got != mid {
		t.Fatalf("mid: got %d want %d", got, mid)
	}
}

func TestRequiredBytesNoMultiplier(t *testing.T) {
	est := int64(1000)
	req := quota.RequiredBytes(est)
	if req != est {
		t.Fatalf("RequiredBytes(%d)=%d want %d (no safety multiplier)", est, req, est)
	}
	if quota.SafetyFactor != 1.0 {
		t.Fatalf("SafetyFactor=%v want 1.0", quota.SafetyFactor)
	}
}

func TestEstimateCalendarNilService(t *testing.T) {
	n, err := EstimateCalendarBytes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != CalendarMinEstimateBytes {
		t.Fatalf("got %d want %d", n, CalendarMinEstimateBytes)
	}
}
