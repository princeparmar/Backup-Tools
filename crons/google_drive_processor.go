package crons

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/quota"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// errDriveAbusiveSkipped means Google refused the download as malware/spam even after AcknowledgeAbuse.
var errDriveAbusiveSkipped = errors.New("drive file skipped: cannot download abusive file")

// errDriveNonExportable means a Google Apps type with no Drive files.export mapping (e.g. project).
var errDriveNonExportable = errors.New("drive file has no exportable content")

type googleDriveProcessor struct{}

func NewGoogleDriveProcessor() *googleDriveProcessor {
	return &googleDriveProcessor{}
}

// driveQuotaSession caches CyberLS remaining storage for per-file skip decisions.
type driveQuotaSession struct {
	remaining    int64
	ok           bool // false = usage-limits unavailable → do not skip (Layer-2 still enforces)
	synced       int
	skippedQuota int
	skippedBytes int64
}

func newDriveQuotaSession(ctx context.Context, input ProcessorInput) *driveQuotaSession {
	s := &driveQuotaSession{}
	usage, err := quota.FetchUsageLimits(ctx, input.Database, input.Job)
	if err != nil {
		logger.Warn(ctx, "drive per-file quota: usage-limits unavailable; not skipping files", logger.ErrorField(err))
		return s
	}
	s.remaining = usage.RemainingStorage()
	s.ok = true
	logger.Info(ctx, "drive per-file quota session started",
		logger.Int64("remaining_bytes", s.remaining),
		logger.Int64("storage_used", usage.StorageUsed),
		logger.Int64("storage_limit", usage.StorageLimit),
	)
	return s
}

// shouldSkipFile returns true when fileBytes exceeds remaining CyberLS storage.
func (s *driveQuotaSession) shouldSkipFile(fileBytes int64) bool {
	if s == nil || !s.ok || fileBytes <= 0 {
		return false
	}
	return !quota.FileFitsStorage(fileBytes, s.remaining)
}

func (s *driveQuotaSession) recordQuotaSkip(fileBytes int64) {
	if s == nil {
		return
	}
	s.skippedQuota++
	if fileBytes > 0 {
		s.skippedBytes += fileBytes
	}
}

func (s *driveQuotaSession) accountUpload(fileBytes int64) {
	if s == nil || !s.ok || fileBytes <= 0 {
		return
	}
	need := quota.RequiredBytes(fileBytes)
	if need > s.remaining {
		s.remaining = 0
		return
	}
	s.remaining -= need
	s.synced++
}

func (p *googleDriveProcessor) Run(input ProcessorInput) error {
	return runGoogleDriveAutosync(input)
}

func refreshTokenFromCronJob(job *repo.CronJobListingDB) string {
	if job == nil || job.InputData == nil || job.InputData.Json() == nil {
		return ""
	}
	if rt, ok := (*job.InputData.Json())["refresh_token"].(string); ok {
		return strings.TrimSpace(rt)
	}
	return ""
}

func scheduledTaskShellFromCronJob(job *repo.CronJobListingDB, accessToken, storx string) *repo.ScheduledTasks {
	return &repo.ScheduledTasks{
		UserID:     job.UserID,
		LoginId:    job.Name,
		Method:     job.Method,
		StorxToken: strings.TrimSpace(storx),
		Status:     "running",
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"access_token": accessToken,
		}),
		Errors: *database.NewDbJsonFromValue([]string{}),
	}
}

// googleMediaAuth is the result of media autosync auth for Drive/Contacts/Calendar/Photos.
// Corporate employee jobs share the admin refresh token; UseDWD must be true so API reads
// impersonate the employee mailbox instead of fetching admin data into employee vaults.
type googleMediaAuth struct {
	AccessToken string
	Storx       string
	Mailbox     string
	UseDWD      bool
}

func googleMediaJobMailbox(job *repo.CronJobListingDB) string {
	if job == nil {
		return ""
	}
	if job.InputData != nil && job.InputData.Json() != nil {
		if e, ok := (*job.InputData.Json())["email"].(string); ok && strings.TrimSpace(e) != "" {
			return strings.TrimSpace(e)
		}
	}
	return strings.TrimSpace(job.Name)
}

func googleMediaAutosyncPreflight(input ProcessorInput) (googleMediaAuth, error) {
	var out googleMediaAuth
	out.Storx = strings.TrimSpace(input.Database.CronJobRepo.ResolvedStorxToken(input.Job))
	if out.Storx == "" {
		return out, fmt.Errorf("storx_token is required on job (set via PUT /auto-sync/job/:id)")
	}
	out.Mailbox = googleMediaJobMailbox(input.Job)
	oauthHolder := input.Database.CronJobRepo.ResolvedOAuthHolderEmail(input.Job)
	out.UseDWD = google.MediaBackupNeedsDelegation(out.Mailbox, oauthHolder)

	if out.UseDWD {
		if !google.WorkspaceServiceAccountConfigured() {
			return out, fmt.Errorf("corporate backup for mailbox %q needs domain-wide delegation (workspace service account not configured)", out.Mailbox)
		}
		if _, err := google.MediaBackupDelegationSubject(out.Mailbox, oauthHolder); err != nil {
			return out, err
		}
		if err := input.HeartBeatFunc(); err != nil {
			return out, err
		}
		return out, nil
	}

	rt := strings.TrimSpace(input.Database.CronJobRepo.ResolvedRefreshToken(input.Job))
	if rt == "" {
		return out, fmt.Errorf("refresh token not found in job input_data")
	}
	accessToken, err := google.AuthTokenUsingRefreshToken(rt)
	if err != nil {
		return out, fmt.Errorf("error while generating auth token: %w", err)
	}
	if strings.TrimSpace(accessToken) == "" {
		return out, fmt.Errorf("error while generating auth token: empty access token")
	}
	out.AccessToken = accessToken
	if err := input.HeartBeatFunc(); err != nil {
		return out, err
	}
	return out, nil
}

func runGoogleDriveAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	auth, err := googleMediaAutosyncPreflight(input)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, auth.Storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync", logger.ErrorField(processErr))
		}
	}()

	// Also move Drive placeholder after precheck
	task := scheduledTaskShellFromCronJob(input.Job, auth.AccessToken, auth.Storx)

	var service *drive.Service
	if auth.UseDWD {
		service, err = google.GetDriveServiceForBackupDWD(ctx, auth.Mailbox)
	} else {
		service, err = createDriveServiceWithAccessToken(ctx, auth.AccessToken)
	}
	if err != nil {
		return err
	}
	// Drive autosync: per-file size vs CyberLS remaining (skip oversized files).
	// Full Drive about.storageQuota estimate is used by Backup Now / handler quota-check APIs only.
	quotaSess := newDriveQuotaSession(ctx, input)

	if err := handler.UploadObjectAndSync(ctx, input.Database, auth.Storx, satellite.ReserveBucket_Drive, task.LoginId+"/.file_placeholder", nil, task.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("google_drive", fmt.Errorf("setup storage placeholder: %w", err))
	}

	if input.Job.TaskMemory.DriveSharedDrives == nil {
		input.Job.TaskMemory.DriveSharedDrives = make(map[string]repo.DriveSharedDriveState)
	}
	// Migrate legacy single DrivePageToken into DriveUserPageToken once.
	if (input.Job.TaskMemory.DriveUserPageToken == nil || strings.TrimSpace(*input.Job.TaskMemory.DriveUserPageToken) == "") &&
		input.Job.TaskMemory.DrivePageToken != nil && strings.TrimSpace(*input.Job.TaskMemory.DrivePageToken) != "" {
		tok := strings.TrimSpace(*input.Job.TaskMemory.DrivePageToken)
		input.Job.TaskMemory.DriveUserPageToken = &tok
	}

	synced, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, auth.Storx, satellite.ReserveBucket_Drive, "", task.UserID, "google", "drive", input.StorxRecovery)
	if err != nil {
		logger.Warn(ctx, "drive synced map load failed; continuing with empty map", logger.ErrorField(err))
		synced = map[string]bool{}
	}
	parentCache := make(map[string][]string)
	shortcutTargetCache := make(map[string]*drive.File)

	needUserBaseline := input.Job.TaskMemory.DriveUserPageToken == nil || strings.TrimSpace(*input.Job.TaskMemory.DriveUserPageToken) == ""
	if needUserBaseline || !input.Job.TaskMemory.DriveBaselineDone {
		if err := runDriveTreeBaseline(ctx, input, task, service, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
			return err
		}
		userTok, err := service.Changes.GetStartPageToken().SupportsAllDrives(true).Do()
		if err != nil {
			return fmt.Errorf("drive user start page token: %w", err)
		}
		tok := strings.TrimSpace(userTok.StartPageToken)
		input.Job.TaskMemory.DriveUserPageToken = &tok
		input.Job.TaskMemory.DrivePageToken = &tok
		input.Job.TaskMemory.DriveBaselineDone = true
	} else {
		newTok, err := runDriveUserIncremental(ctx, input, task, service, strings.TrimSpace(*input.Job.TaskMemory.DriveUserPageToken), synced, parentCache, shortcutTargetCache, quotaSess)
		if err != nil {
			return err
		}
		input.Job.TaskMemory.DriveUserPageToken = &newTok
		input.Job.TaskMemory.DrivePageToken = &newTok
	}

	// Trash is excluded from trashed=false lists — sync BIN every run so vault Trash stays populated
	// (including jobs that finished baseline before trash support existed).
	if err := runDriveTrashSync(ctx, input, task, service, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
		if shouldAbortOnItemError(err) {
			return wrapStorageAbort("google_drive", err)
		}
		logger.Warn(ctx, "drive trash sync failed", logger.ErrorField(err))
	}

	// Per Shared Drive baseline + incremental.
	drivePage := ""
	for {
		drives, next, err := google.ListSharedDrivesPage(service, drivePage)
		if err != nil {
			logger.Warn(ctx, "list shared drives failed", logger.ErrorField(err))
			break
		}
		for _, d := range drives {
			if d == nil || strings.TrimSpace(d.Id) == "" {
				continue
			}
			driveID := strings.TrimSpace(d.Id)
			if markerKey := google.BuildDriveSharedDriveMarkerKey(task.LoginId, driveID, d.Name); markerKey != "" && !synced[markerKey] {
				meta := map[string]string{
					google.DriveMetaOriginalName: strings.TrimSpace(d.Name),
					google.DriveMetaGoogleFileID: driveID,
				}
				if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, markerKey, nil, meta, task.UserID, input.StorxRecovery); err != nil {
					logger.Warn(ctx, "shared drive name marker upload failed", logger.String("drive_id", driveID), logger.ErrorField(err))
				} else {
					synced[markerKey] = true
				}
			}
			state := input.Job.TaskMemory.DriveSharedDrives[driveID]
			if !state.BaselineDone || strings.TrimSpace(state.PageToken) == "" {
				if err := runDriveSharedDriveBaseline(ctx, input, task, service, driveID, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
					if shouldAbortOnItemError(err) {
						return wrapStorageAbort("google_drive", err)
					}
					logger.Warn(ctx, "shared drive baseline failed", logger.String("drive_id", driveID), logger.ErrorField(err))
					continue
				}
				st, err := service.Changes.GetStartPageToken().SupportsAllDrives(true).DriveId(driveID).Do()
				if err != nil {
					logger.Warn(ctx, "shared drive start token failed", logger.String("drive_id", driveID), logger.ErrorField(err))
					continue
				}
				state.BaselineDone = true
				state.PageToken = strings.TrimSpace(st.StartPageToken)
				input.Job.TaskMemory.DriveSharedDrives[driveID] = state
			} else {
				newTok, err := runDriveSharedDriveIncremental(ctx, input, task, service, driveID, state.PageToken, synced, parentCache, shortcutTargetCache, quotaSess)
				if err != nil {
					if shouldAbortOnItemError(err) {
						return wrapStorageAbort("google_drive", err)
					}
					logger.Warn(ctx, "shared drive incremental failed", logger.String("drive_id", driveID), logger.ErrorField(err))
					continue
				}
				state.PageToken = newTok
				input.Job.TaskMemory.DriveSharedDrives[driveID] = state
			}
		}
		if strings.TrimSpace(next) == "" {
			break
		}
		drivePage = next
	}

	if quotaSess != nil && quotaSess.skippedQuota > 0 {
		ApplyDriveStorageSkipMessage(input, quotaSess.synced, quotaSess.skippedQuota, quotaSess.skippedBytes, quotaSess.remaining)
	}
	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func createDriveServiceWithAccessToken(ctx context.Context, accessToken string) (*drive.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := oauth2google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	token := &oauth2.Token{AccessToken: accessToken}
	client := config.Client(ctx, token)
	svc, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create drive service: %w", err)
	}
	return svc, nil
}

func runDriveTreeBaseline(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) error {
	// My Drive (user corpus, include folders, exclude trash).
	if err := driveListAndSync(ctx, input, task, service, "trashed=false", "user", "", google.DriveSectionMyDrive, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
		return err
	}
	// Shared with me discovery.
	if err := driveListAndSync(ctx, input, task, service, "sharedWithMe=true and trashed=false", "user", "", google.DriveSectionSharedWithMe, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
		return err
	}
	return nil
}

func runDriveTrashSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) error {
	return driveListAndSync(ctx, input, task, service, "trashed=true", "user", "", google.DriveSectionBin, synced, parentCache, shortcutTargetCache, quotaSess)
}

func runDriveSharedDriveBaseline(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, driveID string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) error {
	section := google.DriveSharedDriveSection(driveID)
	if err := driveListAndSync(ctx, input, task, service, "trashed=false", "drive", driveID, section, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
		return err
	}
	return driveListAndSync(ctx, input, task, service, "trashed=true", "drive", driveID, google.DriveSectionBin, synced, parentCache, shortcutTargetCache, quotaSess)
}

func driveListAndSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, query, corpora, driveID, section string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		files, next, err := google.ListDriveFilesPage(service, query, pageToken, corpora, driveID)
		if err != nil {
			return err
		}
		for _, f := range files {
			if f == nil || strings.TrimSpace(f.Id) == "" {
				continue
			}
			if err := retrySyncDriveTreeItem(ctx, input, task, service, f, section, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
				if shouldAbortOnItemError(err) {
					return wrapStorageAbort("google_drive", err)
				}
				logger.Warn(ctx, "drive sync item failed", logger.String("file_id", f.Id), logger.String("section", section), logger.ErrorField(err))
			}
		}
		if strings.TrimSpace(next) == "" {
			return nil
		}
		pageToken = next
	}
}

func runDriveUserIncremental(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, pageToken string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) (string, error) {
	return runDriveChangesLoop(ctx, input, task, service, pageToken, "", synced, parentCache, shortcutTargetCache, quotaSess)
}

func runDriveSharedDriveIncremental(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, driveID, pageToken string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) (string, error) {
	return runDriveChangesLoop(ctx, input, task, service, pageToken, driveID, synced, parentCache, shortcutTargetCache, quotaSess)
}

func runDriveChangesLoop(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, pageToken, driveID string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) (string, error) {
	current := pageToken
	newStart := pageToken
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return "", err
		}
		call := service.Changes.List(current).
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true).
			IncludeRemoved(true).
			PageSize(1000).
			Fields("nextPageToken,newStartPageToken,changes(fileId,removed,file(id,name,mimeType,size,parents,createdTime,modifiedTime,version,md5Checksum,permissions,driveId,starred,trashed,shortcutDetails(targetId,targetMimeType),owners(emailAddress)))")
		if strings.TrimSpace(driveID) != "" {
			call = call.DriveId(strings.TrimSpace(driveID))
		}
		ch, err := call.Do()
		if err != nil {
			return "", fmt.Errorf("drive changes list: %w", err)
		}
		for _, change := range ch.Changes {
			if change == nil || strings.TrimSpace(change.FileId) == "" {
				continue
			}
			if change.Removed {
				if err := writeDriveRemovedTreeAlias(ctx, input, task, change.FileId, synced); err != nil {
					if shouldAbortOnItemError(err) {
						return "", wrapStorageAbort("google_drive", err)
					}
					logger.Warn(ctx, "drive removed rekey failed", logger.String("file_id", change.FileId), logger.ErrorField(err))
				}
				continue
			}
			section := classifyDriveSection(change.File, driveID, task.LoginId)
			item := change.File
			if item == nil || strings.TrimSpace(item.Id) == "" {
				got, gerr := service.Files.Get(change.FileId).Fields("id,name,mimeType,size,parents,createdTime,modifiedTime,version,md5Checksum,permissions,driveId,starred,trashed,shortcutDetails(targetId),owners(emailAddress)").SupportsAllDrives(true).Do()
				if gerr != nil {
					logger.Warn(ctx, "drive change get failed", logger.String("file_id", change.FileId), logger.ErrorField(gerr))
					continue
				}
				item = got
				section = classifyDriveSection(item, driveID, task.LoginId)
			}
			if err := retrySyncDriveTreeItem(ctx, input, task, service, item, section, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
				if shouldAbortOnItemError(err) {
					return "", wrapStorageAbort("google_drive", err)
				}
				logger.Warn(ctx, "drive incremental sync failed", logger.String("file_id", change.FileId), logger.ErrorField(err))
			}
		}
		if strings.TrimSpace(ch.NextPageToken) == "" {
			if strings.TrimSpace(ch.NewStartPageToken) != "" {
				newStart = strings.TrimSpace(ch.NewStartPageToken)
			}
			return newStart, nil
		}
		current = strings.TrimSpace(ch.NextPageToken)
	}
}

func classifyDriveSection(file *drive.File, changesDriveID, mailbox string) string {
	if file != nil && file.Trashed {
		return google.DriveSectionBin
	}
	if file != nil && strings.TrimSpace(file.DriveId) != "" {
		return google.DriveSharedDriveSection(file.DriveId)
	}
	if strings.TrimSpace(changesDriveID) != "" {
		return google.DriveSharedDriveSection(changesDriveID)
	}
	if file != nil && !driveFileOwnedByMailbox(file, mailbox) {
		return google.DriveSectionSharedWithMe
	}
	return google.DriveSectionMyDrive
}

func driveFileOwnedByMailbox(file *drive.File, mailbox string) bool {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if file == nil || mailbox == "" {
		return true
	}
	if len(file.Owners) == 0 {
		// Ownership unknown — assume owned (My Drive list). SWM tree walks keep
		// SHARED_WITH_ME via resolveDriveSyncSection regardless.
		return true
	}
	for _, o := range file.Owners {
		if o != nil && strings.ToLower(strings.TrimSpace(o.EmailAddress)) == mailbox {
			return true
		}
	}
	return false
}

func resolveDriveSyncSection(file *drive.File, forcedSection, mailbox string) string {
	hintDriveID := ""
	if strings.HasPrefix(forcedSection, google.DriveSectionSharedDrivePrefix) {
		hintDriveID = strings.TrimPrefix(forcedSection, google.DriveSectionSharedDrivePrefix)
	}
	if file != nil && file.Trashed {
		return google.DriveSectionBin
	}
	if forcedSection == google.DriveSectionBin {
		return google.DriveSectionBin
	}
	classified := classifyDriveSection(file, hintDriveID, mailbox)
	// Walking a Shared-with-me tree: keep SWM unless this is clearly a Shared Drive item.
	if forcedSection == google.DriveSectionSharedWithMe {
		if strings.HasPrefix(classified, google.DriveSectionSharedDrivePrefix) {
			return classified
		}
		return google.DriveSectionSharedWithMe
	}
	// Shared Drive baseline/incremental: keep the drive section when classify lacks driveId.
	if strings.HasPrefix(forcedSection, google.DriveSectionSharedDrivePrefix) {
		if strings.HasPrefix(classified, google.DriveSectionSharedDrivePrefix) {
			return classified
		}
		return forcedSection
	}
	// My Drive / empty: always reclassify so shared roots land in SHARED_WITH_ME.
	return classified
}

func isAbusiveDownloadError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errDriveAbusiveSkipped) {
		return true
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		for _, e := range gerr.Errors {
			if e.Reason == "cannotDownloadAbusiveFile" {
				return true
			}
		}
	}
	return strings.Contains(err.Error(), "cannotDownloadAbusiveFile")
}

func isNonExportableDriveError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errDriveNonExportable) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "unsupported export mime") ||
		strings.Contains(msg, "shortcut has no downloadable body")
}

// driveExportMimeForGoogleApps returns the Drive export MIME for Workspace types that support files.export.
func driveExportMimeForGoogleApps(mimeType string) (exportMime string, ok bool) {
	switch mimeType {
	case "application/vnd.google-apps.document":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", true
	case "application/vnd.google-apps.spreadsheet":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", true
	case "application/vnd.google-apps.presentation":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", true
	case "application/vnd.google-apps.drawing":
		return "image/png", true
	case "application/vnd.google-apps.script":
		return "application/vnd.google-apps.script+json", true
	case "application/vnd.google-apps.site":
		return "text/plain", true
	default:
		// project, form, map, jam, fusiontable, shortcut, folder, …
		return "", false
	}
}

func retrySyncDriveTreeItem(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, preloaded *drive.File, section string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := syncDriveTreeItem(ctx, input, task, service, preloaded, section, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
			if isAbusiveDownloadError(err) {
				logger.Warn(ctx, "drive file skipped: marked abusive by Google",
					logger.String("file_id", preloadedID(preloaded)),
					logger.ErrorField(err),
				)
				return nil
			}
			if isNonExportableDriveError(err) {
				logger.Warn(ctx, "drive file skipped: no exportable content",
					logger.String("file_id", preloadedID(preloaded)),
					logger.ErrorField(err),
				)
				return nil
			}
			lastErr = err
			logger.Warn(ctx, "drive tree sync attempt failed",
				logger.String("file_id", preloadedID(preloaded)),
				logger.Int("attempt", attempt),
				logger.ErrorField(err),
			)
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}

func preloadedID(f *drive.File) string {
	if f == nil {
		return ""
	}
	return f.Id
}

func syncDriveTreeItem(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, preloaded *drive.File, section string, synced map[string]bool, parentCache map[string][]string, shortcutTargetCache map[string]*drive.File, quotaSess *driveQuotaSession) error {
	file := preloaded
	var err error
	if file == nil || strings.TrimSpace(file.Id) == "" || strings.TrimSpace(file.MimeType) == "" || strings.TrimSpace(file.Name) == "" {
		return fmt.Errorf("missing drive file payload")
	}
	if strings.TrimSpace(file.ModifiedTime) == "" {
		file, err = service.Files.Get(file.Id).Fields("id,name,mimeType,size,parents,createdTime,modifiedTime,version,md5Checksum,permissions,driveId,starred,trashed,shortcutDetails(targetId),owners(emailAddress)").SupportsAllDrives(true).Do()
		if err != nil {
			return fmt.Errorf("get file metadata: %w", err)
		}
	}

	isFolder := file.MimeType == "application/vnd.google-apps.folder"
	shortcutTarget := ""
	if file.MimeType == "application/vnd.google-apps.shortcut" && file.ShortcutDetails != nil {
		shortcutTarget = strings.TrimSpace(file.ShortcutDetails.TargetId)
	}

	section = resolveDriveSyncSection(file, section, task.LoginId)

	parentID := google.DriveParentID(file.Parents)
	var parentIDs []string
	// Trash is a flat view (matches Google Drive Trash); skip parent nesting.
	if section != google.DriveSectionBin {
		excludeRoot := strings.TrimSpace(file.DriveId)
		parentIDs, err = google.BuildDriveParentIDChain(ctx, service, parentID, parentCache, excludeRoot)
		if err != nil {
			logger.Warn(ctx, "drive parent chain failed; using immediate parent only", logger.String("file_id", file.Id), logger.ErrorField(err))
			if parentID != "" && parentID != "root" {
				rootID := google.ResolveMyDriveRootID(service, parentCache)
				if parentID != rootID && parentID != excludeRoot {
					parentIDs = []string{parentID}
				}
			}
		}
	}

	sections := []string{section}
	if file.Starred && section != google.DriveSectionBin {
		sections = append(sections, google.DriveSectionStarred)
	}

	// Ensure every parent folder has a named .folder__ placeholder (avoids ID-only rows in Vault).
	if err := ensureDriveParentFolderPlaceholders(ctx, input, task, service, sections, parentIDs, synced); err != nil {
		logger.Warn(ctx, "drive parent folder placeholders failed", logger.String("file_id", file.Id), logger.ErrorField(err))
	}

	logicalKey := google.BuildDriveObjectKey(task.LoginId, sections, parentIDs, file.Id, file.Name, file.MimeType, isFolder, shortcutTarget)

	// 1A: Shared with me and content already exists under another mailbox key for this CyberLS user.
	if section == google.DriveSectionSharedWithMe && !isFolder {
		if existing := google.FindSyncedDriveKeyByFileID(synced, file.Id); existing != "" {
			ep, ok := google.ParseDriveObjectKey(existing)
			if ok && !ep.IsFolder {
				if existing == logicalKey {
					return nil
				}
				// Alias to existing physical (cross-mailbox or same-mailbox other section).
				meta := driveCustomMeta(file, "", shortcutTarget, existing, true)
				if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, nil, meta, task.UserID, input.StorxRecovery); err != nil {
					return err
				}
				synced[logicalKey] = true
				return nil
			}
		}
	}

	if isFolder {
		meta := driveCustomMeta(file, "", "", "", false)
		meta[google.DriveMetaIsFolder] = "true"
		if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, nil, meta, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
		synced[logicalKey] = true
		// sharedWithMe=true only returns top-level shares — recurse into shared folders.
		if section == google.DriveSectionSharedWithMe {
			childQ := fmt.Sprintf("'%s' in parents and trashed=false", file.Id)
			if err := driveListAndSync(ctx, input, task, service, childQ, "allDrives", "", google.DriveSectionSharedWithMe, synced, parentCache, shortcutTargetCache, quotaSess); err != nil {
				if shouldAbortOnItemError(err) {
					return err
				}
				logger.Warn(ctx, "drive shared-folder children sync failed",
					logger.String("folder_id", file.Id),
					logger.String("name", file.Name),
					logger.ErrorField(err),
				)
			}
		}
		return nil
	}

	// Rekey: same mailbox fileId at different path → delete old after successful write.
	oldKey := google.FindSyncedDriveKeyByFileID(filterSyncedByEmail(synced, task.LoginId), file.Id)

	// Skip content if version/md5 unchanged and key unchanged.
	if oldKey == logicalKey {
		if unchanged, _ := driveContentUnchanged(ctx, task.StorxToken, logicalKey, file); unchanged {
			return nil
		}
	}

	if quotaSess != nil && quotaSess.shouldSkipFile(file.Size) {
		quotaSess.recordQuotaSkip(file.Size)
		logger.Warn(ctx, "drive file skipped: size exceeds remaining CyberLS storage",
			logger.String("file_id", file.Id),
			logger.String("name", file.Name),
			logger.Int64("file_bytes", file.Size),
			logger.Int64("remaining_bytes", quotaSess.remaining),
		)
		return nil
	}

	exportMime := ""
	meta := driveCustomMeta(file, "", shortcutTarget, "", false)
	if handler.ShouldUseStreamingUpload(file.Size, file.MimeType) {
		resp, mime, err := openDriveFileDownload(service, file)
		if err != nil {
			if shortcutTarget != "" || isNonExportableDriveError(err) {
				if shortcutTarget != "" {
					meta[google.DriveMetaShortcutTargetID] = shortcutTarget
				}
				logger.Warn(ctx, "drive file stored as metadata placeholder (no exportable bytes)",
					logger.String("file_id", file.Id),
					logger.String("name", file.Name),
					logger.String("mime", file.MimeType),
					logger.ErrorField(err),
				)
				if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, nil, meta, task.UserID, input.StorxRecovery); err != nil {
					return err
				}
				synced[logicalKey] = true
				return nil
			}
			return err
		}
		defer resp.Body.Close()
		exportMime = mime
		if exportMime != "" {
			meta[google.DriveMetaBackupMimeType] = exportMime
		}
		if err := handler.UploadObjectStreamWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, resp.Body, meta, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
	} else {
		content, mime, err := downloadDriveFileContent(service, file)
		if err != nil {
			if shortcutTarget != "" || isNonExportableDriveError(err) {
				if shortcutTarget != "" {
					meta[google.DriveMetaShortcutTargetID] = shortcutTarget
				}
				logger.Warn(ctx, "drive file stored as metadata placeholder (no exportable bytes)",
					logger.String("file_id", file.Id),
					logger.String("name", file.Name),
					logger.String("mime", file.MimeType),
					logger.ErrorField(err),
				)
				if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, nil, meta, task.UserID, input.StorxRecovery); err != nil {
					return err
				}
				synced[logicalKey] = true
				return nil
			}
			return err
		}
		exportMime = mime
		if exportMime != "" {
			meta[google.DriveMetaBackupMimeType] = exportMime
		}
		if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, content, meta, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
	}
	if quotaSess != nil {
		quotaSess.accountUpload(file.Size)
	}
	synced[logicalKey] = true

	if oldKey != "" && oldKey != logicalKey {
		_ = satellite.DeleteObject(ctx, task.StorxToken, satellite.ReserveBucket_Drive, oldKey)
		_ = input.Database.SyncedObjectRepo.DeleteSyncedObject(satellite.ReserveBucket_Drive, oldKey)
		delete(synced, oldKey)
	}

	// Optionally try to backup shortcut target if reachable and not present.
	if shortcutTarget != "" {
		if google.FindSyncedDriveKeyByFileID(synced, shortcutTarget) == "" {
			tf, err := service.Files.Get(shortcutTarget).Fields("id,name,mimeType,size,parents,createdTime,modifiedTime,version,md5Checksum,driveId,starred,trashed,owners(emailAddress)").SupportsAllDrives(true).Do()
			if err == nil && tf != nil {
				shortcutTargetCache[shortcutTarget] = tf
				tsec := classifyDriveSection(tf, "", task.LoginId)
				_ = syncDriveTreeItem(ctx, input, task, service, tf, tsec, synced, parentCache, shortcutTargetCache, quotaSess)
			}
		}
	}
	return nil
}

func filterSyncedByEmail(synced map[string]bool, email string) map[string]bool {
	email = strings.TrimSpace(email)
	out := make(map[string]bool)
	prefix := email + "/"
	for k, v := range synced {
		if v && strings.HasPrefix(k, prefix) {
			out[k] = true
		}
	}
	return out
}

func driveCustomMeta(file *drive.File, backupMime, shortcutTarget, physicalKey string, isAlias bool) map[string]string {
	meta := map[string]string{
		google.DriveMetaGoogleFileID:   strings.TrimSpace(file.Id),
		google.DriveMetaGoogleMimeType: strings.TrimSpace(file.MimeType),
		google.DriveMetaOriginalName:   strings.TrimSpace(file.Name),
	}
	if file.Version != 0 {
		meta[google.DriveMetaVersion] = fmt.Sprintf("%d", file.Version)
	}
	if strings.TrimSpace(file.Md5Checksum) != "" {
		meta[google.DriveMetaMD5Checksum] = strings.TrimSpace(file.Md5Checksum)
	}
	if strings.TrimSpace(backupMime) != "" {
		meta[google.DriveMetaBackupMimeType] = strings.TrimSpace(backupMime)
	}
	if strings.TrimSpace(shortcutTarget) != "" {
		meta[google.DriveMetaShortcutTargetID] = strings.TrimSpace(shortcutTarget)
	}
	if strings.TrimSpace(physicalKey) != "" {
		meta[google.DriveMetaPhysicalObjectKey] = strings.TrimSpace(physicalKey)
	}
	if isAlias {
		meta[google.DriveMetaIsAlias] = "true"
	}
	if file.Starred {
		meta[google.DriveMetaStarred] = "true"
	}
	return meta
}

func driveContentUnchanged(ctx context.Context, accessGrant, objectKey string, file *drive.File) (bool, error) {
	obj, err := satellite.StatObject(ctx, accessGrant, satellite.ReserveBucket_Drive, objectKey)
	if err != nil || obj == nil {
		return false, err
	}
	if obj.Custom == nil {
		return false, nil
	}
	if v := strings.TrimSpace(obj.Custom[google.DriveMetaVersion]); v != "" && file.Version != 0 {
		if v == fmt.Sprintf("%d", file.Version) {
			if md5 := strings.TrimSpace(file.Md5Checksum); md5 == "" || strings.TrimSpace(obj.Custom[google.DriveMetaMD5Checksum]) == md5 {
				return true, nil
			}
		}
	}
	return false, nil
}

func downloadDriveFileContent(service *drive.Service, file *drive.File) ([]byte, string, error) {
	resp, exportMime, err := openDriveFileDownload(service, file)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return body, exportMime, nil
}

func openDriveFileDownload(service *drive.Service, file *drive.File) (*http.Response, string, error) {
	var resp *http.Response
	var err error
	exportMime := ""
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps") {
		if file.MimeType == "application/vnd.google-apps.shortcut" {
			return nil, "", errDriveNonExportable
		}
		var ok bool
		exportMime, ok = driveExportMimeForGoogleApps(file.MimeType)
		if !ok || exportMime == "" {
			return nil, "", fmt.Errorf("%w: %s", errDriveNonExportable, file.MimeType)
		}
		resp, err = service.Files.Export(file.Id, exportMime).Download()
	} else {
		resp, err = service.Files.Get(file.Id).SupportsAllDrives(true).Download()
		if err != nil && isAbusiveDownloadError(err) {
			// User-authorized backup: acknowledge Google's malware/spam flag and retry once.
			resp, err = service.Files.Get(file.Id).SupportsAllDrives(true).AcknowledgeAbuse(true).Download()
			if err != nil && isAbusiveDownloadError(err) {
				return nil, "", errDriveAbusiveSkipped
			}
		}
	}
	if err != nil {
		return nil, "", err
	}
	return resp, exportMime, nil
}

func ensureDriveParentFolderPlaceholders(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *drive.Service, sections, parentIDs []string, synced map[string]bool) error {
	if len(parentIDs) == 0 {
		return nil
	}
	for i, folderID := range parentIDs {
		folderID = strings.TrimSpace(folderID)
		if folderID == "" || folderID == "root" {
			continue
		}
		if existing := google.FindSyncedDriveFolderKeyByID(filterSyncedByEmail(synced, task.LoginId), folderID); existing != "" {
			continue
		}
		name := folderID
		f, err := service.Files.Get(folderID).Fields("id,name,mimeType,starred,trashed").SupportsAllDrives(true).Do()
		if err != nil {
			logger.Warn(ctx, "drive parent folder get failed", logger.String("folder_id", folderID), logger.ErrorField(err))
		} else if f != nil && strings.TrimSpace(f.Name) != "" {
			name = strings.TrimSpace(f.Name)
		}
		parentsOfFolder := append([]string{}, parentIDs[:i]...)
		folderSections := append([]string{}, sections...)
		// Folders inherit STARRED only when the folder itself is starred.
		if f != nil && f.Starred {
			folderSections = append(folderSections, google.DriveSectionStarred)
		} else {
			// Strip STARRED from parent placeholders of a starred *file*.
			cleaned := folderSections[:0]
			for _, s := range folderSections {
				if s != google.DriveSectionStarred {
					cleaned = append(cleaned, s)
				}
			}
			folderSections = cleaned
		}
		key := google.BuildDriveObjectKey(task.LoginId, folderSections, parentsOfFolder, folderID, name, "application/vnd.google-apps.folder", true, "")
		if synced[key] {
			continue
		}
		meta := map[string]string{
			google.DriveMetaGoogleFileID: folderID,
			google.DriveMetaOriginalName: name,
			google.DriveMetaIsFolder:     "true",
			google.DriveMetaGoogleMimeType: "application/vnd.google-apps.folder",
		}
		if f != nil && f.Starred {
			meta[google.DriveMetaStarred] = "true"
		}
		if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, key, nil, meta, task.UserID, input.StorxRecovery); err != nil {
			return err
		}
		synced[key] = true
	}
	return nil
}

func writeDriveRemovedTreeAlias(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, fileID string, synced map[string]bool) error {
	oldKey := google.FindSyncedDriveKeyByFileID(filterSyncedByEmail(synced, task.LoginId), fileID)
	name := fileID
	if oldKey != "" {
		if p, ok := google.ParseDriveObjectKey(oldKey); ok {
			if p.Name != "" {
				name = p.Name
			}
		}
	}
	// Flat BIN key (no parent nesting) so Trash tab lists items at root.
	logicalKey := google.BuildDriveObjectKey(task.LoginId, []string{google.DriveSectionBin}, nil, fileID, name, "application/octet-stream", false, "")
	meta := map[string]string{
		google.DriveMetaGoogleFileID: fileID,
		google.DriveMetaOriginalName: name,
		google.DriveMetaIsAlias:      "true",
	}
	if oldKey != "" && oldKey != logicalKey {
		meta[google.DriveMetaPhysicalObjectKey] = oldKey
	}
	if err := handler.UploadObjectWithMetadataAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Drive, logicalKey, nil, meta, task.UserID, input.StorxRecovery); err != nil {
		return err
	}
	synced[logicalKey] = true
	if oldKey != "" && oldKey != logicalKey {
		if p, ok := google.ParseDriveObjectKey(oldKey); ok && strings.EqualFold(p.Email, task.LoginId) {
			_ = p
		}
	}
	return nil
}

