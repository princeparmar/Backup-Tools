package crons

import (
	"errors"
	"fmt"
	"testing"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

func TestResolveDriveSyncSection_ReclassifiesSharedUnderMyDrive(t *testing.T) {
	file := &drive.File{
		Id:   "shared-folder",
		Name: "Technical Publications Book WD",
		Owners: []*drive.User{{EmailAddress: "other@example.com"}},
	}
	got := resolveDriveSyncSection(file, google.DriveSectionMyDrive, "me@example.com")
	if got != google.DriveSectionSharedWithMe {
		t.Fatalf("got %q, want SHARED_WITH_ME", got)
	}
}

func TestResolveDriveSyncSection_KeepsSWMForChildrenWithoutOwners(t *testing.T) {
	file := &drive.File{Id: "child-pdf", Name: "WD_CH-1.pdf"}
	got := resolveDriveSyncSection(file, google.DriveSectionSharedWithMe, "me@example.com")
	if got != google.DriveSectionSharedWithMe {
		t.Fatalf("got %q, want SHARED_WITH_ME", got)
	}
}

func TestResolveDriveSyncSection_OwnedStaysMyDrive(t *testing.T) {
	file := &drive.File{
		Id:     "mine",
		Name:   "ada_prac.pdf",
		Owners: []*drive.User{{EmailAddress: "me@example.com"}},
	}
	got := resolveDriveSyncSection(file, google.DriveSectionMyDrive, "me@example.com")
	if got != google.DriveSectionMyDrive {
		t.Fatalf("got %q, want MY_DRIVE", got)
	}
}

func TestResolveDriveSyncSection_TrashedIsBin(t *testing.T) {
	file := &drive.File{
		Id:      "trashed-1",
		Name:    "old.pdf",
		Trashed: true,
		Owners:  []*drive.User{{EmailAddress: "me@example.com"}},
	}
	got := resolveDriveSyncSection(file, google.DriveSectionMyDrive, "me@example.com")
	if got != google.DriveSectionBin {
		t.Fatalf("got %q, want BIN", got)
	}
	got = resolveDriveSyncSection(file, google.DriveSectionBin, "me@example.com")
	if got != google.DriveSectionBin {
		t.Fatalf("forced BIN got %q, want BIN", got)
	}
}

func TestIsAbusiveDownloadError(t *testing.T) {
	gerr := &googleapi.Error{
		Code: 403,
		Errors: []googleapi.ErrorItem{{Reason: "cannotDownloadAbusiveFile"}},
	}
	if !isAbusiveDownloadError(gerr) {
		t.Fatal("expected abusive googleapi error")
	}
	if !isAbusiveDownloadError(errDriveAbusiveSkipped) {
		t.Fatal("expected sentinel")
	}
	if isAbusiveDownloadError(errors.New("rateLimitExceeded")) {
		t.Fatal("should not match unrelated error")
	}
}

func TestDriveExportMimeForGoogleApps(t *testing.T) {
	if _, ok := driveExportMimeForGoogleApps("application/vnd.google-apps.project"); ok {
		t.Fatal("project must not be exportable")
	}
	if mime, ok := driveExportMimeForGoogleApps("application/vnd.google-apps.document"); !ok || mime == "" {
		t.Fatal("document should export")
	}
	if !isNonExportableDriveError(fmt.Errorf("%w: application/vnd.google-apps.project", errDriveNonExportable)) {
		t.Fatal("expected non-exportable")
	}
}

