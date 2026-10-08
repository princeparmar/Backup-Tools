package crons

import (
	"context"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"
)

// Graph seams (overridden in tests).
var (
	pimCalendarsFn      = outlook.ListPIMCalendars
	pimContactFoldersFn = outlook.ListPIMContactFolders
)

type outlookCalendarProcessor struct{}

func NewOutlookCalendarProcessor() *outlookCalendarProcessor {
	return &outlookCalendarProcessor{}
}

func (p *outlookCalendarProcessor) Run(input ProcessorInput) error {
	return runOutlookPIMJob(input, "outlook_calendar", satellite.ReserveBucket_OutlookCalendar, google.CalendarMinEstimateBytes, syncOutlookCalendars)
}

// syncOutlookCalendars backs up every calendar of the user. A calendar that cannot be listed is
// skipped; the job fails only when none can be backed up.
func syncOutlookCalendars(ctx context.Context, run *pimRun, userBase, keyPrefix string) error {
	calendars, err := pimCalendarsFn(ctx, run.accessToken, userBase)
	if err != nil {
		return err
	}
	collections := make([]pimCollection, 0, len(calendars))
	for _, cal := range calendars {
		dir := outlook.PIMCalendarDir(keyPrefix, cal.ID)
		collections = append(collections, pimCollection{
			name: cal.Name, dir: dir, listURL: outlook.PIMEventsURL(userBase, cal.ID),
			metaKey: dir + outlook.PIMCalendarMetaName, meta: cal,
		})
	}
	return run.syncCollections(ctx, collections)
}

// pimCollection is one calendar or contact folder to back up.
type pimCollection struct {
	name, dir, listURL string
	metaKey            string
	meta               any
}

func (r *pimRun) syncCollections(ctx context.Context, collections []pimCollection) error {
	var firstErr error
	failed := 0
	for _, c := range collections {
		if err := r.heartbeat(); err != nil {
			return err
		}
		if c.metaKey != "" {
			if err := r.putJSON(ctx, c.metaKey, c.meta); err != nil {
				return err
			}
		}
		err := r.syncCollection(ctx, c.listURL, c.dir)
		if err == nil {
			continue
		}
		if asErrStorageQuota(err) != nil {
			return err
		}
		if hbErr := r.heartbeat(); hbErr != nil {
			return hbErr
		}
		logger.Warn(ctx, "outlook collection backup failed", logger.String("method", r.method), logger.String("collection", c.name), logger.ErrorField(err))
		failed++
		if firstErr == nil {
			firstErr = err
		}
	}
	if failed > 0 && failed == len(collections) {
		return firstErr
	}
	return nil
}

// runOutlookPIMJob resolves auth, the backed-up user and storage for a calendar or contacts job,
// then runs sync and saves task memory.
func runOutlookPIMJob(input ProcessorInput, method, bucket string, minEstimate int64, sync func(ctx context.Context, run *pimRun, userBase, keyPrefix string) error) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	auth, err := microsoftJobAccessToken(input)
	if err != nil {
		return err
	}
	storx := auth.StorxToken

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from "+method+" auto-sync", logger.ErrorField(processErr))
		}
	}()

	client, err := microsoftJobClient(auth, jobOutlookMailbox(input.Job))
	if err != nil {
		return err
	}
	mailbox, err := microsoftJobMailbox(auth, input.Job, client)
	if err != nil {
		return err
	}
	userBase, err := client.MailUserBaseURL(mailbox)
	if err != nil {
		return err
	}
	keyPrefix, err := microsoftJobKeyPrefix(input.Job)
	if err != nil {
		return err
	}
	if err := enforceStoragePrecheck(ctx, input, minEstimate); err != nil {
		return err
	}
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, bucket, keyPrefix+"/.file_placeholder", nil, input.Job.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr(method, fmt.Errorf("setup storage placeholder: %w", err))
	}
	synced, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, bucket, keyPrefix+"/", input.Job.UserID, "outlook", method, input.StorxRecovery)
	if err != nil {
		return fmt.Errorf("load backed-up %s objects: %w", method, err)
	}

	run := &pimRun{
		store:       &satellitePIMStore{input: input, storx: storx, bucket: bucket},
		accessToken: auth.AccessToken,
		method:      method,
		synced:      synced,
		heartbeat:   input.HeartBeatFunc,
		now:         time.Now,
	}
	if err := sync(ctx, run, userBase, keyPrefix); err != nil {
		return err
	}
	logger.Info(ctx, "outlook backup finished", logger.String("method", method),
		logger.Int("uploaded", run.uploaded), logger.Int("unchanged", run.unchanged), logger.Int("removed", run.removed))

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}
