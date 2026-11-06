package repo

import (
	"path/filepath"
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

func TestGetPushedTaskPrefersSystemJobType(t *testing.T) {
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "system_priority.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&CronJobListingDB{}, &TaskListingDB{}); err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskRepository(gdb)

	backup := CronJobListingDB{
		UserID: "u1", Name: "a@x.com", Method: "outlook", SyncType: "daily",
		Provider: CredentialProviderMicrosoft, TenantID: "t1",
		ResourceType: ResourceTypeUser, ResourceID: "oid-a",
		JobType: JobTypeBackup, Active: true,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": 1}),
	}
	if err := gdb.Create(&backup).Error; err != nil {
		t.Fatal(err)
	}
	system := CronJobListingDB{
		UserID: "u1", Name: SystemAccessCheckName, Method: MethodMSAccessVerify, SyncType: SyncTypeSystem,
		Provider: CredentialProviderMicrosoft, TenantID: "t1",
		ResourceType: ResourceTypeSystem, ResourceID: "run-1",
		JobType: JobTypeSystem, Active: false, Hidden: true,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": 1}),
	}
	if err := gdb.Create(&system).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := tasks.CreateTaskForCronJob(backup.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CreateSystemAccessCheckTaskForCronJob(system.ID); err != nil {
		t.Fatal(err)
	}

	got, err := tasks.GetPushedTask()
	if err != nil {
		t.Fatal(err)
	}
	if got.CronJobID != system.ID {
		t.Fatalf("expected system job task first, got cron_job_id=%d want %d", got.CronJobID, system.ID)
	}
}
