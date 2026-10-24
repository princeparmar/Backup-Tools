package microsoft

import (
	"context"
	"errors"
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

func withEnsureCalendar(t *testing.T, id string, err error) *[]string {
	t.Helper()
	var names []string
	prev := ensureCalendarFn
	ensureCalendarFn = func(_ context.Context, _, _, name string) (string, error) {
		names = append(names, name)
		return id, err
	}
	t.Cleanup(func() { ensureCalendarFn = prev })
	return &names
}

func TestRestoreCalendarIDUsesOriginalCalendar(t *testing.T) {
	withCalendarMeta(t, `{"id":"CAL1","name":"Trip"}`)
	names := withEnsureCalendar(t, "NEW", nil)
	if got := RestoreCalendarID(t.Context(), "", "tok", "https://g/me", calendarTestDir+"e1.json", nil); got != "CAL1" {
		t.Fatalf("calendar = %q", got)
	}
	if len(*names) != 0 {
		t.Fatal("listed calendar must not be recreated")
	}
}

func TestRestoreCalendarIDRecreatesDeletedCalendarOnce(t *testing.T) {
	withCalendarMeta(t, `{"id":"CAL1","name":"Trip","removed_at":"2026-10-07T00:00:00Z"}`)
	names := withEnsureCalendar(t, "NEW", nil)
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
	if len(*names) != 1 || (*names)[0] != "Trip" {
		t.Fatalf("calendar created %v", *names)
	}
}

func TestRestoreCalendarIDFallsBackToDefault(t *testing.T) {
	withCalendarMeta(t, `{"id":"CAL1","name":"Trip","removed_at":"2026-10-07T00:00:00Z"}`)
	withEnsureCalendar(t, "", errors.New("http 403"))
	if got := RestoreCalendarID(t.Context(), "", "tok", "https://g/me", calendarTestDir+"e1.json", nil); got != "" {
		t.Fatalf("calendar = %q, want default", got)
	}
	withCalendarMeta(t, "")
	if got := RestoreCalendarID(t.Context(), "", "tok", "https://g/me", calendarTestDir+"e1.json", nil); got != "" {
		t.Fatalf("calendar without meta = %q, want default", got)
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
	if len(*names) != 1 || (*names)[0] != "Trip" {
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
	ensureContactFolderFn = func(_ context.Context, _, _, name string) (string, error) {
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

func TestSeriesRestoreErrorIsNotRetried(t *testing.T) {
	err := error(&outlook.SeriesRestoreError{Err: errors.New("patch failed")})
	if got := restore.ErrorCodeFromErr(err); got != restore.ErrorCodeOutcomeUnknown {
		t.Fatalf("error code = %q", got)
	}
}
