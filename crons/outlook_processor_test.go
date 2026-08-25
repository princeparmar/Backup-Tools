package crons

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestOutlookProcessorRegistered(t *testing.T) {
	p, ok := processorMap["outlook"]
	if !ok {
		t.Fatal("outlook missing from processorMap")
	}
	if p == nil {
		t.Fatal("outlook processor is nil")
	}
	if !repo.IsMicrosoftAutosyncMethod("outlook") {
		t.Fatal("outlook must be a microsoft autosync method")
	}
	found := false
	for _, m := range repo.MicrosoftAutosyncMethods {
		if m == "outlook" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MicrosoftAutosyncMethods missing outlook")
	}
}
