package crons

import (
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestGoogleMediaJobMailbox_table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		job  *repo.CronJobListingDB
		want string
	}{
		{name: "nil job", job: nil, want: ""},
		{
			name: "email in input_data",
			job: &repo.CronJobListingDB{
				Name:      "fallback@contoso.com",
				InputData: database.NewDbJsonFromValue(map[string]interface{}{"email": "emp@contoso.com"}),
			},
			want: "emp@contoso.com",
		},
		{
			name: "falls back to job name",
			job: &repo.CronJobListingDB{
				Name:      "emp@contoso.com",
				InputData: database.NewDbJsonFromValue(map[string]interface{}{}),
			},
			want: "emp@contoso.com",
		},
		{
			name: "blank email uses name",
			job: &repo.CronJobListingDB{
				Name:      "emp@contoso.com",
				InputData: database.NewDbJsonFromValue(map[string]interface{}{"email": "  "}),
			},
			want: "emp@contoso.com",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := googleMediaJobMailbox(tt.job); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
