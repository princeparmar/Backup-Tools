package microsoft

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/restore"
)

const calendarTestDir = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/CAL1/"

func withCalendarMeta(t *testing.T, meta string) {
	t.Helper()
	prev := msDownloadObject
	msDownloadObject = func(_ context.Context, _, _, key string) ([]byte, error) {
		if key != calendarTestDir+outlook.PIMCalendarMetaName || meta == "" {
			return nil, errors.New("not found")
		}
		return []byte(meta), nil
	}
	t.Cleanup(func() { msDownloadObject = prev })
}

func withEnsureCalendar(t *testing.T, id string, err error) *[][2]string {
	t.Helper()
	var calls [][2]string
	prev := ensureCalendarFn
	ensureCalendarFn = func(_ context.Context, _, _, name, color string) (string, error) {
		calls = append(calls, [2]string{name, color})
		return id, err
	}
	t.Cleanup(func() { ensureCalendarFn = prev })
	return &calls
}

func TestRestoreCalendarID(t *testing.T) {
	cases := []struct {
		name      string
		meta      string
		ensureID  string
		ensureErr error
		want      string
		wantCall  [2]string
	}{
		{
			name:     "live calendar rematched by name",
			meta:     `{"id":"CAL1","name":"Trip","color":"lightBlue"}`,
			ensureID: "CAL1",
			want:     "CAL1",
			wantCall: [2]string{"Trip", "lightBlue"},
		},
		{
			name:     "deleted after backup still recreated by name",
			meta:     `{"id":"CAL1","name":"Trip","color":"lightGreen"}`,
			ensureID: "NEW",
			want:     "NEW",
			wantCall: [2]string{"Trip", "lightGreen"},
		},
		{
			name:     "removed_at recreates with color",
			meta:     `{"id":"CAL1","name":"Trip","color":"lightPink","removed_at":"2026-10-07T00:00:00Z"}`,
			ensureID: "NEW",
			want:     "NEW",
			wantCall: [2]string{"Trip", "lightPink"},
		},
		{
			name: "default calendar is not recreated",
			meta: `{"id":"CAL1","name":"Calendar","is_default":true}`,
			want: "CAL1",
		},
		{
			name:      "create failure uses default calendar",
			meta:      `{"id":"CAL1","name":"Trip","removed_at":"2026-10-07T00:00:00Z"}`,
			ensureErr: errors.New("http 403"),
			wantCall:  [2]string{"Trip", ""},
		},
		{name: "missing meta uses default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCalendarMeta(t, tc.meta)
			calls := withEnsureCalendar(t, tc.ensureID, tc.ensureErr)
			got := RestoreCalendarID(t.Context(), "", "tok", "https://g/me", calendarTestDir+"e1.json", nil)
			if got != tc.want {
				t.Fatalf("calendar = %q want %q", got, tc.want)
			}
			if tc.wantCall == [2]string{} {
				if len(*calls) != 0 {
					t.Fatalf("ensure called %v", *calls)
				}
				return
			}
			if len(*calls) != 1 || (*calls)[0] != tc.wantCall {
				t.Fatalf("ensure = %v want %v", *calls, tc.wantCall)
			}
		})
	}
}

func TestRestoreCalendarIDRecreatesDeletedCalendarOnce(t *testing.T) {
	withCalendarMeta(t, `{"id":"CAL1","name":"Trip","removed_at":"2026-10-07T00:00:00Z"}`)
	calls := withEnsureCalendar(t, "NEW", nil)
	var cache sync.Map
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := RestoreCalendarID(t.Context(), "", "tok", "https://g/me", calendarTestDir+"e1.json", &cache); got != "NEW" {
				t.Errorf("calendar = %q", got)
			}
		}()
	}
	wg.Wait()
	if len(*calls) != 1 || (*calls)[0][0] != "Trip" {
		t.Fatalf("calendar created %v", *calls)
	}
}

func TestMigratedCalendarIDMatchesByName(t *testing.T) {
	withCalendarMeta(t, `{"id":"CAL1","name":"Trip","is_default":false}`)
	names := withEnsureCalendar(t, "DEST", nil)
	if got := migratedCalendarID(t.Context(), "", "tok", "https://g/users/bob", calendarTestDir+"e1.json", nil); got != "DEST" {
		t.Fatalf("calendar = %q, the source's id must not be reused in another mailbox", got)
	}
	withCalendarMeta(t, `{"id":"CAL1","name":"Calendar","is_default":true}`)
	if got := migratedCalendarID(t.Context(), "", "tok", "https://g/users/bob", calendarTestDir+"e1.json", nil); got != "" {
		t.Fatalf("default calendar = %q, want the destination's default", got)
	}
	if len(*names) != 1 || (*names)[0][0] != "Trip" {
		t.Fatalf("calendars ensured %v", *names)
	}
}

func TestMigratedContactFolderIDMatchesByName(t *testing.T) {
	const dir = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/folders/F1/"
	prevDownload, prevEnsure := msDownloadObject, ensureContactFolderFn
	t.Cleanup(func() { msDownloadObject, ensureContactFolderFn = prevDownload, prevEnsure })
	msDownloadObject = func(_ context.Context, _, _, key string) ([]byte, error) {
		if key != dir+outlook.PIMFolderMetaName {
			return nil, errors.New("not found")
		}
		return []byte(`{"id":"F1","displayName":"Clients"}`), nil
	}
	var names []string
	ensureContactFolderFn = func(_ context.Context, _, _, name, _ string) (string, error) {
		names = append(names, name)
		return "DESTF", nil
	}
	if got := migratedContactFolderID(t.Context(), "", "tok", "https://g/users/bob", dir+"c1.json", nil); got != "DESTF" {
		t.Fatalf("folder = %q", got)
	}
	if len(names) != 1 || names[0] != "Clients" {
		t.Fatalf("folders ensured %v", names)
	}
}

func TestRestoreContactFolderIDUsesOriginalFolder(t *testing.T) {
	const dir = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/folders/F1/"
	prevDownload := msDownloadObject
	t.Cleanup(func() { msDownloadObject = prevDownload })
	msDownloadObject = func(_ context.Context, _, _, key string) ([]byte, error) {
		if key != dir+outlook.PIMFolderMetaName {
			return nil, errors.New("not found")
		}
		return []byte(`{"id":"F1","displayName":"Clients"}`), nil
	}
	if got := RestoreContactFolderID(t.Context(), "", "tok", "https://g/me", dir+"c1.json", nil); got != "F1" {
		t.Fatalf("folder = %q", got)
	}
}

func TestRestoreContactFolderIDRecreatesDeletedFolder(t *testing.T) {
	const (
		dir       = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/folders/F1/"
		parentDir = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/folders/PARENT/"
	)
	prevDownload, prevEnsure := msDownloadObject, ensureContactFolderFn
	t.Cleanup(func() { msDownloadObject, ensureContactFolderFn = prevDownload, prevEnsure })
	msDownloadObject = func(_ context.Context, _, _, key string) ([]byte, error) {
		switch key {
		case dir + outlook.PIMFolderMetaName:
			return []byte(`{"id":"F1","displayName":"Clients","parentFolderId":"PARENT","removed_at":"2026-01-01T00:00:00Z"}`), nil
		case parentDir + outlook.PIMFolderMetaName:
			return []byte(`{"id":"PARENT","displayName":"Root","removed_at":"2026-01-01T00:00:00Z"}`), nil
		default:
			return nil, errors.New("not found")
		}
	}
	var ensured []string
	ensureContactFolderFn = func(_ context.Context, _, _, name, parent string) (string, error) {
		ensured = append(ensured, name+"@"+parent)
		if name == "Root" {
			return "NEW-PARENT", nil
		}
		if name == "Clients" && parent == "NEW-PARENT" {
			return "NEW", nil
		}
		t.Fatalf("unexpected ensure name=%q parent=%q", name, parent)
		return "", errors.New("unexpected")
	}
	var cache sync.Map
	if got := RestoreContactFolderID(t.Context(), "", "tok", "https://g/me", dir+"c1.json", &cache); got != "NEW" {
		t.Fatalf("folder = %q ensured=%v", got, ensured)
	}
	if got := RestoreContactFolderID(t.Context(), "", "tok", "https://g/me", dir+"c1.json", &cache); got != "NEW" {
		t.Fatalf("cached folder = %q", got)
	}
}

func TestMigratedContactFolderIDRecreatesNestedTree(t *testing.T) {
	const (
		dir       = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/folders/F1/"
		parentDir = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/user/oid/folders/PARENT/"
	)
	prevDownload, prevEnsure := msDownloadObject, ensureContactFolderFn
	t.Cleanup(func() { msDownloadObject, ensureContactFolderFn = prevDownload, prevEnsure })
	msDownloadObject = func(_ context.Context, _, _, key string) ([]byte, error) {
		switch key {
		case dir + outlook.PIMFolderMetaName:
			return []byte(`{"id":"F1","displayName":"Clients","parentFolderId":"PARENT"}`), nil
		case parentDir + outlook.PIMFolderMetaName:
			return []byte(`{"id":"PARENT","displayName":"Root"}`), nil
		default:
			return nil, errors.New("not found")
		}
	}
	ensureContactFolderFn = func(_ context.Context, _, _, name, parent string) (string, error) {
		if name == "Root" && parent == "" {
			return "DEST-ROOT", nil
		}
		if name == "Clients" && parent == "DEST-ROOT" {
			return "DEST-CHILD", nil
		}
		return "", fmt.Errorf("unexpected ensure name=%q parent=%q", name, parent)
	}
	if got := migratedContactFolderID(t.Context(), "", "tok", "https://g/users/bob", dir+"c1.json", nil); got != "DEST-CHILD" {
		t.Fatalf("folder = %q", got)
	}
}

func TestSeriesRestoreErrorIsNotRetried(t *testing.T) {
	err := error(&outlook.SeriesRestoreError{Err: errors.New("patch failed")})
	if got := restore.ErrorCodeFromErr(err); got != restore.ErrorCodeOutcomeUnknown {
		t.Fatalf("error code = %q", got)
	}
}
