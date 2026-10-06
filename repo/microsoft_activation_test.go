package repo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
)

func newActivationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.NewDatabase(gorm.SQLiteConfig(filepath.Join(t.TempDir(), "activation.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Migrate(&GoogleBackupCredentialDB{}, &AutosyncBackupPolicyDB{}); err != nil {
		t.Fatal(err)
	}
	return gdb
}

func TestValidateJobForActivation_microsoftApplicationSkipsRefreshToken(t *testing.T) {
	gdb := newActivationTestDB(t)
	creds := NewGoogleBackupCredentialRepository(gdb)
	jobs := NewCronJobRepository(gdb)

	policy := AutosyncBackupPolicyDB{UserID: "u1", Name: "Daily", Interval: "daily", On: "02:00", RetentionType: RetentionNever}
	if err := gdb.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	cred, err := creds.CreateForUserWithTenant("u1", "admin@contoso.com", "p1", "admin_workspace", "tenant-1", "Contoso", "", "storx-grant")
	if err != nil {
		t.Fatal(err)
	}
	job := &CronJobListingDB{
		Name: "ann@contoso.com", Method: "outlook", PolicyID: policy.ID,
		InputData: database.NewDbJsonFromValue(map[string]interface{}{"credential_id": float64(cred.ID), "email": "ann@contoso.com"}),
	}

	if err := jobs.validateJobForActivation(job); err == nil || !strings.Contains(err.Error(), "refresh_token") {
		t.Fatalf("delegated credential without refresh token must fail activation, got %v", err)
	}

	if err := creds.SetMicrosoftApplicationMode(context.Background(), cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := jobs.validateJobForActivation(job); err != nil {
		t.Fatalf("application credential must activate without refresh token: %v", err)
	}
}
