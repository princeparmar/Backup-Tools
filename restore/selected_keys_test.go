package restore

import (
	"encoding/base64"
	"strconv"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestNormalizeSelectedRestoreKeys(t *testing.T) {
	login := "user@gmail.com"
	msPrefix := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid-ann/"
	encoded := base64.StdEncoding.EncodeToString([]byte(login + "/contacts/a.json"))
	cases := []struct {
		name    string
		prefix  string
		keys    []string
		want    []string
		wantErr string
	}{
		{name: "empty means restore all", prefix: login + "/", keys: nil, want: nil},
		{
			name:   "keeps mailbox keys and drops dups",
			prefix: login + "/",
			keys: []string{
				login + "/contacts/a.json",
				"  " + login + "/contacts/a.json",
				login + "/contacts/b.json",
			},
			want: []string{login + "/contacts/a.json", login + "/contacts/b.json"},
		},
		{
			name:   "drops other mailbox and placeholders",
			prefix: login + "/",
			keys: []string{
				"other@x.com/contacts/a.json",
				login + "/.file_placeholder",
				login + "/contacts/keep.json",
			},
			want: []string{login + "/contacts/keep.json"},
		},
		{
			name:   "decodes legacy base64 vault key",
			prefix: login + "/",
			keys:   []string{encoded},
			want:   []string{login + "/contacts/a.json"},
		},
		{
			name:    "all keys wrong mailbox",
			prefix:  login + "/",
			keys:    []string{"other@x.com/contacts/a.json"},
			wantErr: "no selected keys belong to this mailbox",
		},
		{
			name:    "too many keys",
			prefix:  login + "/",
			keys:    tooManySelectedKeys(login),
			wantErr: "at most 200 selected keys per restore job",
		},
		{
			name:   "microsoft resource prefix keeps matching keys",
			prefix: msPrefix,
			keys: []string{
				msPrefix + "Inbox/msg1.json",
				"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/user/other/Inbox/x.json",
				msPrefix + "Inbox/msg2.json",
			},
			want: []string{msPrefix + "Inbox/msg1.json", msPrefix + "Inbox/msg2.json"},
		},
		{
			name:    "microsoft email login must not reject resource keys — use resource prefix",
			prefix:  "admin@space2let.onmicrosoft.com/",
			keys:    []string{msPrefix + "Inbox/msg1.json"},
			wantErr: "no selected keys belong to this mailbox",
		},
		{
			name:    "microsoft wrong resource dropped",
			prefix:  msPrefix,
			keys:    []string{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/user/other/Inbox/x.json"},
			wantErr: "no selected keys belong to this mailbox",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSelectedRestoreKeys(tc.prefix, tc.keys)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error=%v want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %#v want %#v", got, tc.want)
				}
			}
		})
	}
}

func TestSelectedRestoreKeyPrefix(t *testing.T) {
	cases := []struct {
		name    string
		prep    *ReadinessResult
		cronJob *repo.CronJobListingDB
		want    string
	}{
		{
			name: "google falls back to email",
			prep: &ReadinessResult{Service: "gmail", LoginID: "Ann@Gmail.com"},
			want: "ann@gmail.com/",
		},
		{
			name: "microsoft uses resource prefix from cron job",
			prep: &ReadinessResult{Service: "outlook", LoginID: "admin@space2let.onmicrosoft.com"},
			cronJob: &repo.CronJobListingDB{
				TenantID:     "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA",
				ResourceType: repo.ResourceTypeUser,
				ResourceID:   "oid-ann",
			},
			want: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid-ann/",
		},
		{
			name: "nil prep",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectedRestoreKeyPrefix(tc.prep, tc.cronJob)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSelectedKeyPageAndRange(t *testing.T) {
	keys := []string{"u/a", "u/b", "u/c", "u/d"}
	cases := []struct {
		name    string
		after   uint
		limit   int
		start   uint
		end     uint
		usePage bool
		want    []string
	}{
		{name: "first page of two", after: 0, limit: 2, usePage: true, want: []string{"u/a", "u/b"}},
		{name: "second page", after: 2, limit: 2, usePage: true, want: []string{"u/c", "u/d"}},
		{name: "drained page", after: 4, limit: 2, usePage: true, want: nil},
		{name: "retry range first batch", start: 0, end: 2, want: []string{"u/a", "u/b"}},
		{name: "retry range clipped", start: 3, end: 10, want: []string{"u/d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rows []repo.SyncedObject
			if tc.usePage {
				rows = SelectedKeyPage(keys, tc.after, tc.limit)
			} else {
				rows = SelectedKeyRange(keys, tc.start, tc.end)
			}
			if len(rows) != len(tc.want) {
				t.Fatalf("got %d rows want %d", len(rows), len(tc.want))
			}
			for i, row := range rows {
				if row.ObjectKey != tc.want[i] {
					t.Fatalf("row %d key %q want %q", i, row.ObjectKey, tc.want[i])
				}
				if row.ID != uint(indexOfKey(keys, row.ObjectKey)+1) {
					t.Fatalf("row %d id %d", i, row.ID)
				}
			}
		})
	}
}

func TestSelectedKeysDrained(t *testing.T) {
	keys := []string{"u/a", "u/b"}
	job := &repo.RestoreJobListingDB{CursorID: 2, SelectedKeys: selectedKeysValue(keys)}
	if !selectedKeysDrained(job) {
		t.Fatal("expected drained at cursor 2")
	}
	job.CursorID = 1
	if selectedKeysDrained(job) {
		t.Fatal("cursor 1 is not drained")
	}
	if selectedKeysDrained(&repo.RestoreJobListingDB{}) {
		t.Fatal("restore-all job is not selected-drained")
	}
}

func tooManySelectedKeys(login string) []string {
	out := make([]string, maxSelectedRestoreKeys+1)
	for i := range out {
		out[i] = login + "/contacts/" + strconv.Itoa(i) + ".json"
	}
	return out
}

func indexOfKey(keys []string, key string) int {
	for i, k := range keys {
		if k == key {
			return i
		}
	}
	return -1
}
