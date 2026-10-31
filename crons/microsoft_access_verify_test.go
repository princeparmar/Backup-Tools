package crons

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func newAccessVerifyTestDB(t *testing.T) *db.PostgresDb {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "access_verify.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(
		&repo.GoogleBackupCredentialDB{},
		&repo.CronJobListingDB{},
		&repo.TaskListingDB{},
		&repo.MicrosoftResourceDB{},
		&repo.MicrosoftAccessVerifyBatchDB{},
	); err != nil {
		t.Fatal(err)
	}
	return &db.PostgresDb{
		DB:                        gdb,
		CronJobRepo:               repo.NewCronJobRepository(gdb),
		CredentialRepo:            repo.NewGoogleBackupCredentialRepository(gdb),
		TaskRepo:                  repo.NewTaskRepository(gdb),
		MicrosoftResourceRepo:     repo.NewMicrosoftResourceRepository(gdb),
		MicrosoftAccessVerifyRepo: repo.NewMicrosoftAccessVerifyBatchRepository(gdb),
	}
}

func TestAccessVerifyEnqueueAndGraceDelete(t *testing.T) {
	store := newAccessVerifyTestDB(t)
	cred, err := store.CredentialRepo.UpsertMicrosoftAccount(repo.MicrosoftAccountUpsert{
		UserID: "u1", ExternalAccountID: "oid-admin", HomeTenantID: "tenant-a", Email: "admin@contoso.com", StorjProjectID: "p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "bob@contoso.com", "outlook", "daily", cred.ID,
		repo.MicrosoftJobIdentity{TenantID: "tenant-a", ResourceType: repo.ResourceTypeUser, ResourceID: "oid-bob"},
		map[string]interface{}{"email": "bob@contoso.com"})
	if err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-15 * 24 * time.Hour).Format(time.RFC3339)
	latest, _ := store.CronJobRepo.GetCronJobByID(job.ID)
	input := map[string]interface{}{
		"email": "bob@contoso.com",
		"access_verify": map[string]interface{}{
			"state":             repo.AccessVerifyStateUnavailable,
			"reason_code":       outlook.AccessReasonUnavailable,
			"message":           "no access",
			"unavailable_since": old,
			"checked_at":        old,
		},
	}
	if err := store.CronJobRepo.UpdateCronJobByID(job.ID, map[string]interface{}{
		"active":         false,
		"message":        "no access",
		"message_status": repo.JobMessageStatusWarning,
		"input_data":     database.NewDbJsonFromValue(input),
	}); err != nil {
		t.Fatal(err)
	}
	_ = latest

	batch, err := store.MicrosoftAccessVerifyRepo.EnqueueBatch("u1", cred.ID, "tenant-a", "p1", []uint{job.ID})
	if err != nil || batch == nil || batch.ID == 0 {
		t.Fatalf("enqueue: %v %+v", err, batch)
	}
	again, err := store.MicrosoftAccessVerifyRepo.EnqueueBatch("u1", cred.ID, "tenant-a", "p1", []uint{job.ID})
	if err != nil || again == nil || again.ID != batch.ID {
		t.Fatalf("idempotent enqueue want %d got %+v (%v)", batch.ID, again, err)
	}

	pending, err := store.CronJobRepo.CreateMicrosoftResourceJob("u1", "cat@contoso.com", "outlook", "daily", cred.ID,
		repo.MicrosoftJobIdentity{TenantID: "tenant-a", ResourceType: repo.ResourceTypeUser, ResourceID: "oid-cat"},
		map[string]interface{}{"email": "cat@contoso.com"})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.CronJobRepo.UpdateCronJobByID(pending.ID, repo.SetJobAccessVerifyPending(pending, batch.ID, "pending"))

	(&AutosyncManager{store: store}).microsoftAccessVerifyGraceDelete(context.Background())
	if got, _ := store.CronJobRepo.GetCronJobByID(job.ID); got != nil {
		t.Fatalf("grace must delete unavailable job older than 14d, still have id=%d", got.ID)
	}
	if got, _ := store.CronJobRepo.GetCronJobByID(pending.ID); got == nil {
		t.Fatal("grace must not delete pending drafts")
	}
}
