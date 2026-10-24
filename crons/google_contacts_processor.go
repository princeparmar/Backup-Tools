package crons

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	people "google.golang.org/api/people/v1"
)

type googleContactsProcessor struct{}

func NewGoogleContactsProcessor() *googleContactsProcessor {
	return &googleContactsProcessor{}
}

func (p *googleContactsProcessor) Run(input ProcessorInput) error {
	return runGoogleContactsAutosync(input)
}

func runGoogleContactsAutosync(input ProcessorInput) error {
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

	task := scheduledTaskShellFromCronJob(input.Job, auth.AccessToken, auth.Storx)

	var service *people.Service
	if auth.UseDWD {
		service, err = google.NewPeopleServiceForBackupDWD(ctx, auth.Mailbox)
	} else {
		service, err = google.NewPeopleServiceWithAccessToken(ctx, auth.AccessToken)
	}
	if err != nil {
		return err
	}
	if err := estimateAndEnforceContacts(ctx, input, service); err != nil {
		return err
	}

	if err := handler.UploadObjectAndSync(ctx, input.Database, auth.Storx, satellite.ReserveBucket_Contacts, task.LoginId+"/.file_placeholder", nil, task.UserID, input.StorxRecovery); err != nil {
		return mapUploadErr("google_contacts", fmt.Errorf("setup storage placeholder: %w", err))
	}

	syncedKeys, err := loadContactsSyncedKeys(ctx, input, task, auth.Storx)
	if err != nil {
		return err
	}
	syncedSet := google.BuildContactsSyncedIDSet(syncedKeys, task.LoginId)
	groups, groupErr := google.ListUserContactGroups(ctx, service)
	if groupErr != nil {
		logger.Warn(ctx, "contact labels not listed", logger.ErrorField(groupErr))
		groups = nil
	}
	// One pass rewrites contacts saved before the photo token and display labels existed.
	const contactsDetailVersion = 2
	refresh := input.Job.TaskMemory.ContactsDetailVersion < contactsDetailVersion
	photoClient, photoErr := google.ContactsPhotoClient(ctx, auth.AccessToken, auth.Mailbox, auth.UseDWD)
	if photoErr != nil {
		logger.Warn(ctx, "contact photo client unavailable", logger.ErrorField(photoErr))
	}

	if refresh || !input.Job.TaskMemory.ContactsBaselineDone {
		if err := runContactsBaselineSync(ctx, input, task, service, syncedKeys, syncedSet, groups, refresh, photoClient); err != nil {
			return err
		}
		input.Job.TaskMemory.ContactsBaselineDone = true
		input.Job.TaskMemory.ContactsDetailVersion = contactsDetailVersion
	} else if err := runContactsIncrementalSync(ctx, input, task, service, syncedKeys, syncedSet, groups, photoClient); err != nil {
		return err
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func loadContactsSyncedKeys(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, storx string) (map[string]bool, error) {
	objectKeys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_Contacts, task.LoginId+"/", task.UserID, "google", "contacts", input.StorxRecovery)
	if err != nil {
		return nil, fmt.Errorf("load synced objects: %w", err)
	}
	return objectKeys, nil
}

func runContactsBaselineSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *people.Service, syncedKeys map[string]bool, syncedSet map[string]struct{}, groups map[string]string, refresh bool, photoClient *http.Client) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListAllContactsFlatWithService(service, pageToken)
		if err != nil {
			return err
		}
		if _, err := processContactsPage(ctx, input, task, page.Contacts, syncedKeys, syncedSet, groups, refresh, photoClient); err != nil {
			return err
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return nil
}

func runContactsIncrementalSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *people.Service, syncedKeys map[string]bool, syncedSet map[string]struct{}, groups map[string]string, photoClient *http.Client) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListAllContactsFlatWithService(service, pageToken)
		if err != nil {
			return err
		}
		newFoundInPage, err := processContactsPage(ctx, input, task, page.Contacts, syncedKeys, syncedSet, groups, false, photoClient)
		if err != nil {
			return err
		}
		nextToken := strings.TrimSpace(page.NextPageToken)
		if !newFoundInPage {
			if nextToken == "" {
				break
			}
			lookahead, err := google.ListAllContactsFlatWithService(service, nextToken)
			if err != nil {
				return err
			}
			if !google.PageHasAnyNewContactsItems(lookahead.Contacts, syncedSet) {
				break
			}
		}
		if nextToken == "" {
			break
		}
		pageToken = nextToken
	}
	return nil
}

func processContactsPage(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, items []google.FlatContact, syncedKeys map[string]bool, syncedSet map[string]struct{}, groups map[string]string, refresh bool, photoClient *http.Client) (bool, error) {
	newFound := false
	for i := range items {
		google.ApplyContactGroupNames(&items[i], groups)
		id := google.ContactsIDFromResourceName(items[i].ID)
		if id == "" {
			continue
		}
		_, synced := syncedSet[id]
		stale := synced && google.ContactBackupStale(google.ExistingContactObjectKey(syncedKeys, task.LoginId, items[i].ID), items[i].SourceUpdatedAt)
		if synced && !refresh && !stale {
			continue
		}
		newFound = true
		if err := retrySyncContactByID(ctx, input, task, items[i], syncedKeys, photoClient); err != nil {
			if shouldAbortOnItemError(err) {
				return false, wrapStorageAbort("google_contacts", err)
			}
			logger.Warn(ctx, "Contacts sync failed", logger.String("contact_id", id), logger.ErrorField(err))
			continue
		}
		syncedSet[id] = struct{}{}
	}
	return newFound, nil
}

func syncContactByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item google.FlatContact, syncedKeys map[string]bool, photoClient *http.Client) error {
	freshKey := google.ContactsObjectKey(task.LoginId, item.ID, item.SourceUpdatedAt)
	existing := google.ExistingContactObjectKey(syncedKeys, task.LoginId, item.ID)
	objectKey := freshKey
	if existing != "" && !google.ContactBackupStale(existing, item.SourceUpdatedAt) {
		objectKey = existing
	}
	payload := google.BackupFromFlat(item)
	payload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if item.PhotoURL != "" {
		photo, photoErr := google.DownloadContactPhoto(ctx, item.PhotoURL, photoClient)
		if photoErr != nil {
			logger.Warn(ctx, "contact photo not backed up", logger.String("contact_id", item.ID), logger.ErrorField(photoErr))
		} else {
			payload.Photo = photo
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal contact: %w", err)
	}
	if err := handler.UploadBufferedObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Contacts, objectKey, b, task.UserID, input.StorxRecovery); err != nil {
		return err
	}
	syncedKeys[objectKey] = true
	if existing != "" && existing != objectKey {
		if err := satellite.DeleteObject(ctx, task.StorxToken, satellite.ReserveBucket_Contacts, existing); err != nil {
			logger.Warn(ctx, "old contact file not removed", logger.String("key", existing), logger.ErrorField(err))
		} else if err := input.Database.SyncedObjectRepo.DeleteSyncedObjectsByBucketAndKeys(satellite.ReserveBucket_Contacts, []string{existing}); err != nil {
			logger.Warn(ctx, "old contact record not removed", logger.String("key", existing), logger.ErrorField(err))
		}
		delete(syncedKeys, existing)
	}
	return nil
}

func retrySyncContactByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item google.FlatContact, syncedKeys map[string]bool, photoClient *http.Client) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := syncContactByID(ctx, input, task, item, syncedKeys, photoClient); err != nil {
			lastErr = err
			logger.Warn(ctx, "contacts sync attempt failed", logger.String("contact_id", item.ID), logger.Int("attempt", attempt), logger.ErrorField(err))
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}
