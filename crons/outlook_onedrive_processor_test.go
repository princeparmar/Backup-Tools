package crons

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

// Wiring checks that replace live Graph E2E in CI (manual E2E still required against a tenant).
func TestOutlookOneDriveProcessorRegistered(t *testing.T) {
	p, ok := processorMap["outlook_onedrive"]
	if !ok || p == nil {
		t.Fatal("outlook_onedrive missing from processorMap")
	}
	if !repo.IsMicrosoftAutosyncMethod("outlook_onedrive") {
		t.Fatal("outlook_onedrive must be a microsoft autosync method")
	}
	found := false
	for _, m := range repo.MicrosoftAutosyncMethods {
		if m == "outlook_onedrive" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MicrosoftAutosyncMethods missing outlook_onedrive")
	}
}
