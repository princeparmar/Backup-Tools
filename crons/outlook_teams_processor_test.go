package crons

import "testing"

func TestJobTeamsChannelIDs(t *testing.T) {
	if got := jobTeamsChannelIDs(nil); len(got) != 0 {
		t.Fatalf("nil job: got %v", got)
	}
}

func TestOutlookTeamsProcessorRegistered(t *testing.T) {
	if processorMap["outlook_teams"] == nil {
		t.Fatal("outlook_teams processor must be registered")
	}
}
