package crons

import "testing"

func TestOutlookGroupsProcessorRegistered(t *testing.T) {
	if processorMap["outlook_groups"] == nil {
		t.Fatal("outlook_groups processor must be registered")
	}
}
