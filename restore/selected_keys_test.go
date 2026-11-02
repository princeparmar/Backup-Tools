package restore

import (
	"encoding/base64"
	"strconv"
	"testing"

	"github.com/StorX2-0/Backup-Tools/repo"
)

func TestNormalizeSelectedRestoreKeys(t *testing.T) {
	login := "user@gmail.com"
	encoded := base64.StdEncoding.EncodeToString([]byte(login + "/contacts/a.json"))
	cases := []struct {
		name    string
		login   string
		keys    []string
		want    []string
		wantErr string
	}{
		{name: "empty means restore all", login: login, keys: nil, want: nil},
		{
			name:  "keeps mailbox keys and drops dups",
			login: login,
			keys: []string{
				login + "/contacts/a.json",
				"  " + login + "/contacts/a.json",
				login + "/contacts/b.json",
			},
			want: []string{login + "/contacts/a.json", login + "/contacts/b.json"},
		},
		{
			name:  "drops other mailbox and placeholders",
			login: login,
			keys: []string{
				"other@x.com/contacts/a.json",
				login + "/.file_placeholder",
				login + "/contacts/keep.json",
			},
			want: []string{login + "/contacts/keep.json"},
		},
		{
			name:  "decodes legacy base64 vault key",
			login: login,
			keys:  []string{encoded},
			want:  []string{login + "/contacts/a.json"},
		},
		{
			name:    "all keys wrong mailbox",
			login:   login,
			keys:    []string{"other@x.com/contacts/a.json"},
			wantErr: "no selected keys belong to this mailbox",
		},
		{
			name:    "too many keys",
			login:   login,
			keys:    tooManySelectedKeys(login),
			wantErr: "at most 200 selected keys per restore job",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSelectedRestoreKeys(tc.login, tc.keys)
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
