package crons

import "testing"

func TestOutlookGroupsProcessorRegistered(t *testing.T) {
	t.Skip("Groups backup is hidden for now; processor registration is commented out in jobs.go")
	if processorMap["outlook_groups"] == nil {
		t.Fatal("outlook_groups processor must be registered")
	}
}
