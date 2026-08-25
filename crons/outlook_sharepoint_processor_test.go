package crons

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestOutlookSharePointProcessorRegistered(t *testing.T) {
	p, ok := processorMap["outlook_sharepoint"]
	if !ok || p == nil {
		t.Fatal("outlook_sharepoint missing from processorMap")
	}
	if !repo.IsMicrosoftAutosyncMethod("outlook_sharepoint") {
		t.Fatal("outlook_sharepoint must be a microsoft autosync method")
	}
	found := false
	for _, m := range repo.MicrosoftAutosyncMethods {
		if m == "outlook_sharepoint" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MicrosoftAutosyncMethods missing outlook_sharepoint")
	}
}
