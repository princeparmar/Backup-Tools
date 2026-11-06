package repo

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
)

func TestHasLinkedJobsForCredentialIgnoresSystemJobs(t *testing.T) {
	db := newIdentityTestDB(t)
	migrateIdentityModels(t, db)
	jobs := NewCronJobRepository(db)

	credID := uint(42)
	sys := CronJobListingDB{
		UserID: "u1", Name: SystemAccessCheckName, Method: MethodMSAccessVerify, SyncType: SyncTypeSystem,
		Provider: CredentialProviderMicrosoft, JobType: JobTypeSystem, Hidden: true,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(credID)}),
	}
	if err := db.Create(&sys).Error; err != nil {
		t.Fatal(err)
	}
	has, err := jobs.HasLinkedJobsForCredential("u1", credID)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("system access-check job must not count as a linked backup job")
	}

	backup := CronJobListingDB{
		UserID: "u1", Name: "user@contoso.com", Method: "outlook", SyncType: "daily",
		Provider: CredentialProviderMicrosoft, JobType: JobTypeBackup,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(credID)}),
	}
	if err := db.Create(&backup).Error; err != nil {
		t.Fatal(err)
	}
	has, err = jobs.HasLinkedJobsForCredential("u1", credID)
	if err != nil || !has {
		t.Fatalf("backup job should count: has=%v err=%v", has, err)
	}
}
