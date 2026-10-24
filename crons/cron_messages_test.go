package crons

import (
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestBackupSuccessMessage(t *testing.T) {
	if BackupSuccessMessage(false) != cronJobBackupSuccess {
		t.Fatalf("got %q", BackupSuccessMessage(false))
	}
	if BackupSuccessMessage(true) != cronJobBackupSuccess {
		t.Fatalf("on-demand should use same success copy")
	}
}

func TestDriveStorageSkipMessages(t *testing.T) {
	if !strings.Contains(cronJobDriveStoragePartial, "some files skipped") {
		t.Fatal(cronJobDriveStoragePartial)
	}
	if !strings.Contains(cronJobDriveStorageNone, "not enough CyberLS storage") {
		t.Fatal(cronJobDriveStorageNone)
	}
	if ProcessorLeftWarningOutcome(&repo.CronJobListingDB{
		Message:       cronJobDriveStoragePartial,
		MessageStatus: repo.JobMessageStatusWarning,
	}) != true {
		t.Fatal("expected warning outcome")
	}
}
