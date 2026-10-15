package crons

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/repo"
)

const (
	evilDeltaLink = "https://evil.example.com/v1.0/drives/b!u/root/delta?token=2"
	pagedNext     = "https://graph.microsoft.com/v1.0/drives/b!u/root/delta?token=p2"
	pagedFinal    = "https://graph.microsoft.com/v1.0/drives/b!u/root/delta?token=end"
)

// stubPagedDelta serves a baseline in two pages: the initial URL lists the drive's first folder
// and points to next; next lists the rest and ends with final. Every requested URL is recorded.
func stubPagedDelta(t *testing.T, next, final string) *[]string {
	t.Helper()
	prevDelta, prevRoot := oneDriveDeltaPageFn, oneDriveRootIDFn
	initial := outlook.OneDriveInitialDeltaURL(driveTestRoot)
	var urls []string
	oneDriveDeltaPageFn = func(_ context.Context, _, u string) (*outlook.OneDriveDeltaPage, error) {
		urls = append(urls, u)
		switch u {
		case initial:
			return &outlook.OneDriveDeltaPage{Items: []outlook.OneDriveItem{driveFolder("A", "ROOT", "Docs")}, NextLink: next}, nil
		case next:
			return &outlook.OneDriveDeltaPage{Items: []outlook.OneDriveItem{
				driveFolder("B", "A", "Reports"),
				driveFile("F1", "B", "q3.txt", "c1"),
				driveFile("F2", "ROOT", "notes.txt", "c1"),
			}, DeltaLink: final}, nil
		}
		return nil, fmt.Errorf("unexpected delta url %s", u)
	}
	oneDriveRootIDFn = func(context.Context, string, string) (string, error) { return "ROOT", nil }
	t.Cleanup(func() { oneDriveDeltaPageFn, oneDriveRootIDFn = prevDelta, prevRoot })
	return &urls
}

func TestOneDriveDeltaBadLinksStopWithoutChanges(t *testing.T) {
	initial := outlook.OneDriveInitialDeltaURL(driveTestRoot)
	cases := []struct {
		name, next, final string
		requested         []string
	}{
		{"foreign nextLink", evilDeltaLink, pagedFinal, []string{initial}},
		{"nextLink loops to the first URL", initial, pagedFinal, []string{initial}},
		{"nextLink to a folder delta", "https://graph.microsoft.com/v1.0/drives/b!u/items/A/delta?token=2", pagedFinal, []string{initial}},
		{"foreign deltaLink", pagedNext, evilDeltaLink, []string{initial, pagedNext}},
		{"deltaLink to another drive", pagedNext, "https://graph.microsoft.com/v1.0/drives/b!other/root/delta?token=end", []string{initial, pagedNext}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := seedDrive(t)
			before := maps.Clone(store.objects)
			urls := stubPagedDelta(t, c.next, c.final)
			tm := &repo.TaskMemory{OneDriveLayout: repo.OneDriveLayoutTree}

			run := newOneDriveRun(ProcessorInput{}, store, "tok", driveTestRoot, driveTestPrefix, store.keySet())
			err := run.syncAll(context.Background(), tm)
			if !errors.Is(err, outlook.ErrOneDriveDeltaPaging) {
				t.Fatalf("err %v", err)
			}
			if !slices.Equal(*urls, c.requested) {
				t.Fatalf("requested %v", *urls)
			}
			if tm.OneDriveDeltaLink != nil {
				t.Fatalf("cursor saved: %q", *tm.OneDriveDeltaLink)
			}
			if bin := store.keysUnder(outlook.OneDriveSectionBin); len(bin) != 0 || len(store.objects) != len(before) {
				t.Fatalf("backup changed: bin=%v keys=%v", bin, store.keySet())
			}
		})
	}
}

func TestOneDriveSavedForeignCursorRebaselines(t *testing.T) {
	store := seedDrive(t)
	urls := stubPagedDelta(t, pagedNext, pagedFinal)
	foreign := evilDeltaLink
	tm := &repo.TaskMemory{OneDriveDeltaLink: &foreign, OneDriveLayout: repo.OneDriveLayoutTree}

	run := newOneDriveRun(ProcessorInput{}, store, "tok", driveTestRoot, driveTestPrefix, store.keySet())
	if err := run.syncAll(context.Background(), tm); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(*urls, foreign) || len(*urls) == 0 || (*urls)[0] != outlook.OneDriveInitialDeltaURL(driveTestRoot) {
		t.Fatalf("requested %v", *urls)
	}
	if !run.baseline || tm.OneDriveDeltaLink == nil || *tm.OneDriveDeltaLink != pagedFinal {
		t.Fatalf("baseline=%v cursor=%v", run.baseline, tm.OneDriveDeltaLink)
	}
	if bin := store.keysUnder(outlook.OneDriveSectionBin); len(bin) != 0 {
		t.Fatalf("complete baseline moved files to bin: %v", bin)
	}
}

func TestSharePointDeltaPagingErrorIsNotFatal(t *testing.T) {
	if isSharePointRunFatal(fmt.Errorf("library: %w", outlook.ErrOneDriveDeltaPaging)) {
		t.Fatal("a paging error in one library must not stop the other libraries")
	}
}
