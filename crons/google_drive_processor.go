package crons

import (
	"context"
	"fmt"
	"strings"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/database"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/repo"
	"github.com/StorX2-0/Backup-Tools/satellite"
	crontasks "github.com/StorX2-0/Backup-Tools/tasks"
)

type googleDriveProcessor struct{}

func NewGoogleDriveProcessor() *googleDriveProcessor {
	return &googleDriveProcessor{}
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

func scheduledTaskShellFromCronJob(job *repo.CronJobListingDB, accessToken string) *repo.ScheduledTasks {
	return &repo.ScheduledTasks{
		UserID:     job.UserID,
		LoginId:    job.Name,
		Method:     job.Method,
		StorxToken: strings.TrimSpace(job.StorxToken),
		Status:     "running",
		InputData: database.NewDbJsonFromValue(map[string]interface{}{
			"access_token": accessToken,
		}),
		Errors: *database.NewDbJsonFromValue([]string{}),
	}
}

func googleMediaAutosyncPreflight(input ProcessorInput) (accessToken, storx string, err error) {
	storx = strings.TrimSpace(input.Database.CronJobRepo.ResolvedStorxToken(input.Job))
	if storx == "" {
		return "", "", fmt.Errorf("storx_token is required on job (set via PUT /auto-sync/job/:id)")
	}
	rt := strings.TrimSpace(input.Database.CronJobRepo.ResolvedRefreshToken(input.Job))
	if rt == "" {
		return "", "", fmt.Errorf("refresh token not found in job input_data")
	}
	accessToken, err = google.AuthTokenUsingRefreshToken(rt)
	if err != nil {
		return "", "", fmt.Errorf("error while generating auth token: %w", err)
	}
	if strings.TrimSpace(accessToken) == "" {
		return "", "", fmt.Errorf("error while generating auth token: empty access token")
	}
	if err := input.HeartBeatFunc(); err != nil {
		return "", "", err
	}
	return accessToken, storx, nil
}

func runGoogleDriveAutosync(input ProcessorInput) error {
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

	deps := &crontasks.TaskProcessorDeps{Store: input.Database}
	task := scheduledTaskShellFromCronJob(input.Job, accessToken)
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Drive, task.LoginId+"/.file_placeholder", nil, task.UserID); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	proc := crontasks.NewScheduledGoogleDriveProcessor(deps)
	return proc.Run(crontasks.ScheduledTaskProcessorInput{
		InputData:     map[string]interface{}{"access_token": accessToken},
		Memory:        map[string][]string{"pending": {}},
		Task:          task,
		HeartBeatFunc: input.HeartBeatFunc,
		Deps:          deps,
	})
}
