package crons

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

type outlookSharePointProcessor struct{}

func NewOutlookSharePointProcessor() *outlookSharePointProcessor {
	return &outlookSharePointProcessor{}
}

func (p *outlookSharePointProcessor) Run(input ProcessorInput) error {
	return runOutlookSharePointAutosync(input)
}

// Seams (overridden in tests).
var (
	sharePointLibrariesFn = outlook.ListSharePointSiteLibraries
	libraryKeysFn         = func(ctx context.Context, input ProcessorInput, d libraryDrive) (map[string]bool, error) {
		return handler.GetSyncedObjectsWithPrefix(ctx, input.Database, d.storx, d.bucket, d.prefix+"/", input.Job.UserID, "outlook", d.service, input.StorxRecovery)
	}
	libraryStoreFn = func(input ProcessorInput, d libraryDrive) oneDriveStore {
		return &satelliteOneDriveStore{
			input: input, storx: d.storx, bucket: d.bucket,
			extraMeta: map[string]string{outlook.OneDriveMetaDriveID: d.driveID},
		}
	}
)

// sharePointWarningPrefix starts the job message of a run that left a library behind.
const sharePointWarningPrefix = "SharePoint backup finished with warnings"

// runOutlookSharePointAutosync backs up every document library of the site in the OneDrive
// folder layout (MY_DRIVE / BIN): the default library at the site's resource prefix, the others
// under SharePointLibraryPrefix.
func runOutlookSharePointAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return err
	}
	accessToken, storx := auth.AccessToken, auth.StorxToken

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from sharepoint auto-sync", logger.ErrorField(processErr))
		}
	}()

	if err := input.HeartBeatFunc(); err != nil {
		return err
	}

	siteID := repo.JobSharePointSiteID(input.Job)
	driveID := repo.JobSharePointDriveID(input.Job)
	if siteID == "" || driveID == "" {
		return fmt.Errorf("site_id and drive_id are required on job for sharepoint backup")
	}
	siteKey, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_OutlookSharePoint, siteKey+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("outlook_sharepoint", fmt.Errorf("setup storage placeholder: %w", err))
	}

	q := newDriveQuotaSession(ctx, input)
	site := sharePointSite{siteID: siteID, siteKey: siteKey, defaultDriveID: driveID, storx: storx, token: accessToken, quota: q}
	res, err := syncSharePointSite(ctx, input, site)
	if err != nil {
		return err
	}
	if q != nil && q.skippedQuota > 0 {
		ApplyDriveStorageSkipMessage(input, q.synced, q.skippedQuota, q.skippedBytes, q.remaining)
	} else if msg := res.warning(); msg != "" {
		input.Job.Message, input.Job.MessageStatus = msg, repo.JobMessageStatusWarning
	}
	return saveSharePointMemory(input)
}

func saveSharePointMemory(input ProcessorInput) error {
	if input.Database == nil {
		return nil
	}
	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

// sharePointSite is one site job's run.
type sharePointSite struct {
	siteID, siteKey, defaultDriveID string
	storx, token                    string
	// quota is shared by every library of the run.
	quota *driveQuotaSession
}

// sharePointSiteResult counts the libraries of one run.
type sharePointSiteResult struct {
	synced int
	failed []string
	// listErr is set when the library list could not be read; only known libraries were synced.
	listErr error
}

func (r sharePointSiteResult) warning() string {
	var parts []string
	if n := len(r.failed); n > 0 {
		names := r.failed
		if len(names) > 3 {
			names = append(names[:3:3], "…")
		}
		parts = append(parts, fmt.Sprintf("%d of %d libraries not backed up (%s)", n, n+r.synced, strings.Join(names, ", ")))
	}
	if r.listErr != nil {
		parts = append(parts, "the library list could not be read, so new libraries were not checked")
	}
	if len(parts) == 0 {
		return ""
	}
	return sharePointWarningPrefix + ": " + strings.Join(parts, "; ")
}

// syncSharePointSite syncs every document library of the site. A library that fails is recorded
// and the others continue; the run fails only when no library could be backed up, or on
// cancellation and storage errors. Libraries are removed (moved to BIN) only after a complete
// listing that still has the default library no longer lists them.
func syncSharePointSite(ctx context.Context, input ProcessorInput, s sharePointSite) (sharePointSiteResult, error) {
	var res sharePointSiteResult
	job := input.Job
	if job.MessageStatus == repo.JobMessageStatusWarning && strings.HasPrefix(job.Message, sharePointWarningPrefix) {
		job.Message, job.MessageStatus = "", ""
	}
	tm := &job.TaskMemory
	if tm.SharePointLibraries == nil {
		tm.SharePointLibraries = map[string]repo.SharePointLibraryState{}
	}

	// A stopped heartbeat (task cancelled, job turned off) ends the whole run.
	var stopErr error
	beat := input.HeartBeatFunc
	input.HeartBeatFunc = func() error {
		if beat == nil {
			return nil
		}
		if err := beat(); err != nil {
			stopErr = err
			return err
		}
		return nil
	}

	listed, listErr := sharePointLibrariesFn(ctx, s.token, s.siteID)
	complete := listErr == nil
	if !complete {
		if isSharePointRunFatal(listErr) {
			return res, listErr
		}
		res.listErr = listErr
		logger.Warn(ctx, "sharepoint library list failed; syncing known libraries", logger.String("site_id", s.siteID), logger.ErrorField(listErr))
	}
	libs := sharePointRunLibraries(s.defaultDriveID, listed, tm.SharePointLibraries, complete)
	inList := map[string]bool{}
	for _, l := range listed {
		inList[l.ID] = true
	}
	trusted := complete && inList[s.defaultDriveID]

	var firstErr error
	prefixes := map[string]string{}
	for _, lib := range libs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		isDefault := lib.ID == s.defaultDriveID
		state := tm.SharePointLibraries[lib.ID]
		state.Default = isDefault
		if lib.Name != "" {
			state.Name = lib.Name
		}
		if lib.WebURL != "" {
			state.WebURL = lib.WebURL
		}
		label := cmp.Or(state.Name, lib.ID)

		prefix := s.siteKey
		if !isDefault {
			prefix = outlook.SharePointLibraryPrefix(s.siteKey, lib.ID)
		}
		if other, taken := prefixes[prefix]; taken && other != lib.ID {
			state.LastError = "library key collides with another library of the site"
			tm.SharePointLibraries[lib.ID] = state
			res.failed = append(res.failed, label)
			continue
		}
		prefixes[prefix] = lib.ID
		if state.RemovedAt != "" {
			// Listed again: back up from a new baseline; files in BIN return to MY_DRIVE unchanged.
			state.RemovedAt, state.DeltaLink = "", nil
		}

		d := libraryDrive{
			storx: s.storx, token: s.token,
			bucket: satellite.ReserveBucket_OutlookSharePoint, service: "outlook_sharepoint",
			driveRoot: outlook.DriveRootURLFromDriveID(lib.ID), driveID: lib.ID, prefix: prefix, quota: s.quota,
		}
		var err error
		if isDefault {
			err = runLibraryDriveSync(ctx, input, d, &tm.SharePointDeltaLink, &tm.SharePointLayout)
		} else {
			err = runLibraryDriveSync(ctx, input, d, &state.DeltaLink, &state.Layout)
		}
		if err != nil {
			if stopErr != nil || isSharePointRunFatal(err) {
				return res, err
			}
			logger.Warn(ctx, "sharepoint library sync failed", logger.String("site_id", s.siteID),
				logger.String("drive_id", lib.ID), logger.ErrorField(err))
			state.LastError = truncateLibraryError(err)
			tm.SharePointLibraries[lib.ID] = state
			res.failed = append(res.failed, label)
			if firstErr == nil {
				firstErr = fmt.Errorf("sharepoint library %s: %w", label, err)
			}
			continue
		}
		state.LastError, state.BaselineDone = "", true
		state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
		if isDefault {
			tm.SharePointBaselineDone = true
		}
		tm.SharePointLibraries[lib.ID] = state
		res.synced++
		if err := saveSharePointMemory(input); err != nil {
			logger.Warn(ctx, "sharepoint checkpoint failed", logger.ErrorField(err))
		}
	}

	if trusted {
		if err := removeSharePointLibraries(ctx, input, s, inList); err != nil {
			return res, err
		}
	}
	if res.synced == 0 && firstErr != nil {
		return res, firstErr
	}
	return res, nil
}

// sharePointRunLibraries is the libraries to sync: the default library first, then every listed
// library, or every known library that is not removed when the list could not be read.
func sharePointRunLibraries(defaultID string, listed []outlook.SharePointLibrary, known map[string]repo.SharePointLibraryState, complete bool) []outlook.SharePointLibrary {
	var others []outlook.SharePointLibrary
	var def *outlook.SharePointLibrary
	if complete {
		for i := range listed {
			if listed[i].ID == defaultID {
				def = &listed[i]
				continue
			}
			others = append(others, listed[i])
		}
	} else {
		for id, st := range known {
			if id != defaultID && st.RemovedAt == "" {
				others = append(others, outlook.SharePointLibrary{ID: id, Name: st.Name, WebURL: st.WebURL})
			}
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].ID < others[j].ID })
	if def == nil {
		def = &outlook.SharePointLibrary{ID: defaultID}
	}
	return append([]outlook.SharePointLibrary{*def}, others...)
}

// removeSharePointLibraries moves the backup of every known library missing from the complete
// listing to that library's BIN. A library is marked removed once nothing of it is left outside
// BIN, so a partly moved library is finished on the next run.
func removeSharePointLibraries(ctx context.Context, input ProcessorInput, s sharePointSite, listed map[string]bool) error {
	tm := &input.Job.TaskMemory
	ids := make([]string, 0, len(tm.SharePointLibraries))
	for id := range tm.SharePointLibraries {
		if !listed[id] && id != s.defaultDriveID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		state := tm.SharePointLibraries[id]
		if state.RemovedAt != "" {
			continue
		}
		d := libraryDrive{
			storx: s.storx, token: s.token,
			bucket: satellite.ReserveBucket_OutlookSharePoint, service: "outlook_sharepoint",
			driveRoot: outlook.DriveRootURLFromDriveID(id), driveID: id,
			prefix: outlook.SharePointLibraryPrefix(s.siteKey, id),
		}
		left, err := moveLibraryToBin(ctx, input, d)
		if err != nil {
			if isSharePointRunFatal(err) {
				return err
			}
			logger.Warn(ctx, "sharepoint removed library not moved to bin", logger.String("drive_id", id), logger.ErrorField(err))
			continue
		}
		if left > 0 {
			logger.Warn(ctx, "sharepoint removed library partly moved to bin", logger.String("drive_id", id), logger.Int("left", left))
			continue
		}
		state.RemovedAt = time.Now().UTC().Format(time.RFC3339)
		state.DeltaLink = nil
		tm.SharePointLibraries[id] = state
		logger.Info(ctx, "sharepoint library removed; backup moved to bin", logger.String("site_id", s.siteID), logger.String("drive_id", id))
	}
	return nil
}

// moveLibraryToBin moves every backed-up file of a library to its BIN and drops its folders. It
// reads only the backup, never Graph. left counts objects still outside BIN.
func moveLibraryToBin(ctx context.Context, input ProcessorInput, d libraryDrive) (left int, err error) {
	keys, err := libraryKeysFn(ctx, input, d)
	if err != nil {
		return 0, fmt.Errorf("list backed-up %s files: %w", d.service, err)
	}
	run := newOneDriveRun(input, libraryStoreFn(input, d), d.token, d.driveRoot, d.prefix, keys)
	run.service = d.service
	run.sweepUnseen(ctx)
	for _, key := range run.index {
		if !oneDriveInBin(key) {
			left++
		}
	}
	return left, nil
}

// isSharePointRunFatal reports errors that stop the site run instead of one library: cancellation,
// storage limits and StorX access errors (recovered by the job runner).
func isSharePointRunFatal(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errJobDeactivated) || shouldAbortOnItemError(err) ||
		handler.IsStorxUplinkError(err) || handler.IsStorxSatelliteRefreshError(err) || handler.IsStorxStorageLimitError(err)
}

func truncateLibraryError(err error) string {
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// libraryDrive is a SharePoint or group document library backed up in the OneDrive layout.
type libraryDrive struct {
	storx, token    string
	bucket, service string
	driveRoot       string
	driveID         string
	prefix          string
	// quota is shared across the libraries of one run; nil starts a session for this library
	// and applies its skip message.
	quota *driveQuotaSession
}

// runLibraryDriveSync syncs one library: each file is one object in its folder tree, deleted
// files move to BIN, and the first run rewrites the old meta/data backup in place. Every object
// records the drive id so a restore returns it to the same library.
func runLibraryDriveSync(ctx context.Context, input ProcessorInput, d libraryDrive, deltaLink **string, layout *int) error {
	keys, err := libraryKeysFn(ctx, input, d)
	if err != nil {
		return fmt.Errorf("list backed-up %s files: %w", d.service, err)
	}
	run := newOneDriveRun(input, libraryStoreFn(input, d), d.token, d.driveRoot, d.prefix, keys)
	run.service = d.service
	shared := d.quota != nil
	run.quota = d.quota
	if !shared {
		run.quota = newDriveQuotaSession(ctx, input)
	}
	if err := run.syncDrive(ctx, deltaLink, layout); err != nil {
		return err
	}
	if q := run.quota; !shared && q != nil && q.skippedQuota > 0 {
		ApplyDriveStorageSkipMessage(input, q.synced, q.skippedQuota, q.skippedBytes, q.remaining)
	}
	return nil
}
