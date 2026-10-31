package repo

import (
	"testing"
)

func TestAccessCheckSelectionFingerprintStable(t *testing.T) {
	a := []AccessCheckSelectionItem{
		{Email: "B@x.com", UserID: "2", Service: "outlook", Method: "outlook"},
		{Email: "a@x.com", UserID: "1", Service: "onedrive", Method: "outlook_onedrive"},
	}
	b := []AccessCheckSelectionItem{
		{Email: "a@x.com", UserID: "1", Service: "onedrive", Method: "outlook_onedrive"},
		{Email: "b@x.com", UserID: "2", Service: "outlook", Method: "outlook"},
	}
	fa := AccessCheckSelectionFingerprint("tenant-1", 9, a)
	fb := AccessCheckSelectionFingerprint("TENANT-1", 9, b)
	if fa == "" || fa != fb {
		t.Fatalf("fingerprint mismatch: %q vs %q", fa, fb)
	}
	fc := AccessCheckSelectionFingerprint("tenant-1", 10, a)
	if fa == fc {
		t.Fatalf("different credential should change fingerprint")
	}
}

func TestAccessCheckItemKey(t *testing.T) {
	if AccessCheckItemKey("Ann@X.com", "Outlook") != "ann@x.com|outlook" {
		t.Fatalf("unexpected key")
	}
}

func TestAccessCheckSummaryFromResults(t *testing.T) {
	s := AccessCheckSummaryFromResults([]AccessCheckResultItem{
		{Status: "accessible"},
		{Status: "no_access"},
		{Status: "probe_error"},
		{Status: "accessible"},
	})
	if s["accessible"] != 2 || s["no_access"] != 1 || s["probe_error"] != 1 || s["total"] != 4 {
		t.Fatalf("bad summary: %+v", s)
	}
}

func TestIsSystemJob(t *testing.T) {
	if IsSystemJob(nil) {
		t.Fatal("nil")
	}
	if IsSystemJob(&CronJobListingDB{JobType: JobTypeBackup}) {
		t.Fatal("backup")
	}
	if !IsSystemJob(&CronJobListingDB{JobType: JobTypeSystem}) {
		t.Fatal("system")
	}
	if EffectiveJobType(&CronJobListingDB{}) != JobTypeBackup {
		t.Fatal("empty defaults to backup")
	}
}
