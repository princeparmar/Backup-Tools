package crons

import (
	"context"
	"encoding/json"
	"fmt"
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

type contactsStoredObject struct {
	ResourceName  string   `json:"resource_name"`
	Name          string   `json:"name"`
	Phones        []string `json:"phones"`
	Emails        []string `json:"emails"`
	Organizations []string `json:"organizations,omitempty"`
	ETag          string   `json:"etag"`
	UpdatedAt     string   `json:"updated_at"`
}

func runGoogleContactsAutosync(input ProcessorInput) error {
	ctx := context.Background()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accessToken, storx, err := googleMediaAutosyncPreflight(input)
	if err != nil {
		return err
	}

	go func() {
		processCtx := context.Background()
		if processErr := handler.ProcessWebhookEvents(processCtx, input.Database, storx, 100); processErr != nil {
			logger.Warn(processCtx, "Failed to process webhook events from auto-sync", logger.ErrorField(processErr))
		}
	}()

	task := scheduledTaskShellFromCronJob(input.Job, accessToken, storx)
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Contacts, task.LoginId+"/.file_placeholder", nil, task.UserID, input.StorxRecovery); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	service, err := google.NewPeopleServiceWithAccessToken(ctx, accessToken)
	if err != nil {
		return err
	}

	syncedSet, err := loadContactsSyncedIDSet(ctx, input, task, storx)
	if err != nil {
		return err
	}

	if !input.Job.TaskMemory.ContactsBaselineDone {
		if err := runContactsBaselineSync(ctx, input, task, service, syncedSet); err != nil {
			return err
		}
		input.Job.TaskMemory.ContactsBaselineDone = true
	} else if err := runContactsIncrementalSync(ctx, input, task, service, syncedSet); err != nil {
		return err
	}

	return input.Database.CronJobRepo.UpdateCronJobFieldsForCron(input.Job.ID, map[string]interface{}{
		"task_memory": input.Job.TaskMemory,
	})
}

func loadContactsSyncedIDSet(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, storx string) (map[string]struct{}, error) {
	objectKeys, err := handler.GetSyncedObjectsWithPrefix(ctx, input.Database, storx, satellite.ReserveBucket_Contacts, task.LoginId+"/", task.UserID, "google", "contacts", input.StorxRecovery)
	if err != nil {
		return nil, fmt.Errorf("load synced objects: %w", err)
	}
	return google.BuildContactsSyncedIDSet(objectKeys, task.LoginId), nil
}

func runContactsBaselineSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *people.Service, syncedSet map[string]struct{}) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListAllContactsFlatWithService(service, pageToken)
		if err != nil {
			return err
		}
		if _, err := processContactsPage(ctx, input, task, page.Contacts, syncedSet); err != nil {
			return err
		}
		if strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return nil
}

func runContactsIncrementalSync(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, service *people.Service, syncedSet map[string]struct{}) error {
	pageToken := ""
	for {
		if err := input.HeartBeatFunc(); err != nil {
			return err
		}
		page, err := google.ListAllContactsFlatWithService(service, pageToken)
		if err != nil {
			return err
		}
		newFoundInPage, err := processContactsPage(ctx, input, task, page.Contacts, syncedSet)
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

func processContactsPage(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, items []google.FlatContact, syncedSet map[string]struct{}) (bool, error) {
	newFound := false
	for i := range items {
		id := google.ContactsIDFromResourceName(items[i].ID)
		if id == "" {
			continue
		}
		if _, synced := syncedSet[id]; synced {
			continue
		}
		newFound = true
		if err := retrySyncContactByID(ctx, input, task, items[i]); err != nil {
			logger.Warn(ctx, "Contacts sync failed", logger.String("contact_id", id), logger.ErrorField(err))
			continue
		}
		syncedSet[id] = struct{}{}
	}
	return newFound, nil
}

func syncContactByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item google.FlatContact) error {
	objectKey := google.ContactsObjectKey(task.LoginId, item.ID)
	payload := contactsStoredObject{
		ResourceName:  item.ID,
		Name:          item.Name,
		Phones:        item.Phones,
		Emails:        item.Emails,
		Organizations: item.Organizations,
		ETag:          item.ETag,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal contact: %w", err)
	}
	return handler.UploadObjectAndSync(ctx, input.Database, task.StorxToken, satellite.ReserveBucket_Contacts, objectKey, b, task.UserID, input.StorxRecovery)
}

func retrySyncContactByID(ctx context.Context, input ProcessorInput, task *repo.ScheduledTasks, item google.FlatContact) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := syncContactByID(ctx, input, task, item); err != nil {
			lastErr = err
			logger.Warn(ctx, "contacts sync attempt failed", logger.String("contact_id", item.ID), logger.Int("attempt", attempt), logger.ErrorField(err))
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
			continue
		}
		return nil
	}
	return lastErr
}
