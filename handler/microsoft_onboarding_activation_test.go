package handler

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestOnboardMicrosoftJobActivation(t *testing.T) {
	const userID = "user-1"
	inactive := false

	cases := []struct {
		name       string
		storxToken string
		active     *bool
		wantActive bool
	}{
		{name: "storx token activates", storxToken: "grant", wantActive: true},
		{name: "own_nodes gate keeps inactive", storxToken: "grant", active: &inactive},
		{name: "no storx token stays inactive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := newMicrosoftTestDB(t)
			if err := database.DB.Migrate(&repo.AutosyncBackupPolicyDB{}); err != nil {
				t.Fatal(err)
			}
			database.PolicyRepo = repo.NewAutosyncBackupPolicyRepository(database.DB)
			policy, err := database.PolicyRepo.CreatePolicy(userID, "Outlook defaults", "daily", "12am", repo.RetentionNever)
			if err != nil {
				t.Fatal(err)
			}

			cred := &repo.GoogleBackupCredentialDB{UserID: userID, Email: "me@contoso.com", Provider: "microsoft", RefreshToken: "1.refresh", StorxToken: tc.storxToken}
			if err := database.DB.DB.Create(cred).Error; err != nil {
				t.Fatal(err)
			}
			req := &GoogleBackupOnboardingRequest{StorxToken: tc.storxToken, Active: tc.active}
			identity := repo.MicrosoftJobIdentity{TenantID: testTenantID, ResourceType: repo.ResourceTypeMailbox, ResourceID: "me@contoso.com"}

			job, fail := onboardMicrosoftJob(nil, database, userID, "outlook", "me@contoso.com", "outlook", "daily", nil,
				identity, onboardingSchedule{Interval: "daily", On: "12am"}, req, cred, true, &onboardingPolicyBatch{allID: policy.ID})
			if fail != nil {
				t.Fatalf("onboard failed: %s", fail.Error)
			}
			if job.Active != tc.wantActive {
				t.Fatalf("result active = %v, want %v", job.Active, tc.wantActive)
			}
			stored, err := database.CronJobRepo.GetCronJobByID(job.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Active != tc.wantActive {
				t.Fatalf("stored active = %v, want %v", stored.Active, tc.wantActive)
			}
		})
	}
}
