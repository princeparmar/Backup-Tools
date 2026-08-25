package register_test

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/restore"
	_ "github.com/StorX2-0/Backup-Tools/restore/register"
)

func TestRegistry_processorsRegistered(t *testing.T) {
	t.Parallel()
	methods := []string{
		"outlook", "outlook_calendar", "outlook_contacts", "outlook_onedrive",
		"outlook_sharepoint", "outlook_teams", "outlook_groups",
		"gmail", "google_drive",
	}
	for _, method := range methods {
		method := method
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			if _, err := restore.ProcessorForMethod(method); err != nil {
				t.Fatalf("ProcessorForMethod(%q): %v", method, err)
			}
		})
	}
}
