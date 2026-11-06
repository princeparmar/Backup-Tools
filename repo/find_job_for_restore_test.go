package repo

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
)

func TestFindJobForRestore_table(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	jobs := NewCronJobRepository(gdb)

	mustCreate := func(name, method string, extra map[string]interface{}) *CronJobListingDB {
		t.Helper()
		job, err := jobs.CreateCronJobForUser("u1", name, method, "daily", extra)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	cal := mustCreate("dhavalder93@gmail.com", "google_calendar", map[string]interface{}{"email": "dhavalder93@gmail.com"})
	mustCreate("dhavalder93@gmail.com", "gmail", map[string]interface{}{"email": "dhavalder93@gmail.com"})
	drive := mustCreate("Ann@X.com", "google_drive", nil)

	cases := []struct {
		name    string
		userID  string
		method  string
		loginID string
		wantOK  bool
		wantID  uint
	}{
		{name: "calendar exact", method: "google_calendar", loginID: "dhavalder93@gmail.com", wantOK: true, wantID: cal.ID},
		{name: "calendar case-insensitive", method: "google_calendar", loginID: "Dhavalder93@Gmail.com", wantOK: true, wantID: cal.ID},
		{name: "calendar UI service name", method: "calendar", loginID: "dhavalder93@gmail.com", wantOK: true, wantID: cal.ID},
		{name: "drive matches resource_id", method: "google_drive", loginID: "ann@x.com", wantOK: true, wantID: drive.ID},
		{name: "drive UI service name", method: "drive", loginID: "ann@x.com", wantOK: true, wantID: drive.ID},
		{name: "gmail exact", method: "gmail", loginID: "dhavalder93@gmail.com", wantOK: true},
		{name: "unknown mailbox", method: "google_calendar", loginID: "missing@gmail.com"},
		{name: "other user", userID: "u2", method: "google_calendar", loginID: "dhavalder93@gmail.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userID := tc.userID
			if userID == "" {
				userID = "u1"
			}
			got, ok, err := jobs.FindJobForRestore(userID, tc.method, tc.loginID)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v job=%+v", ok, tc.wantOK, got)
			}
			if tc.wantID != 0 && (!ok || got.ID != tc.wantID) {
				t.Fatalf("job id=%v want %d", got, tc.wantID)
			}
		})
	}
}

func TestFindJobForRestoreGrant_usesSharedMailboxJob(t *testing.T) {
	gdb := newIdentityTestDB(t)
	migrateIdentityModels(t, gdb)
	jobs := NewCronJobRepository(gdb)
	gmail, err := jobs.CreateCronJobForUser("u1", "dhavalder93@gmail.com", "gmail", "daily", map[string]interface{}{"email": "dhavalder93@gmail.com"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := jobs.FindJobForRestoreGrant("u1", "google_calendar", "dhavalder93@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got == nil || got.ID != gmail.ID {
		t.Fatalf("grant job=%+v want gmail %d", got, gmail.ID)
	}
	if _, ok, err := jobs.FindJobForRestore("u1", "google_calendar", "dhavalder93@gmail.com"); err != nil || ok {
		t.Fatalf("restore-all still needs the calendar job, ok=%v err=%v", ok, err)
	}
}

func TestJobMailbox_table(t *testing.T) {
	email := "a@x.com"
	cases := []struct {
		name string
		job  *CronJobListingDB
		want string
	}{
		{name: "nil", want: ""},
		{
			name: "input email",
			job:  &CronJobListingDB{Name: "n", ResourceID: "r", InputData: database.NewDbJsonFromValue(map[string]interface{}{"email": email})},
			want: email,
		},
		{name: "resource id", job: &CronJobListingDB{Name: "n", ResourceID: email}, want: email},
		{name: "name", job: &CronJobListingDB{Name: email}, want: email},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := JobMailbox(tc.job); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestRestoreJobMethods_table(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "calendar", want: []string{"google_calendar", "calendar"}},
		{in: "google_calendar", want: []string{"google_calendar", "calendar"}},
		{in: "drive", want: []string{"google_drive", "drive"}},
		{in: "contacts", want: []string{"google_contacts", "contacts"}},
		{in: "photos", want: []string{"google_photos", "photos"}},
		{in: "gmail", want: []string{"gmail"}},
	}
	for _, tc := range cases {
		got := restoreJobMethods(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%q methods=%v want %v", tc.in, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("%q methods=%v want %v", tc.in, got, tc.want)
			}
		}
	}
}
