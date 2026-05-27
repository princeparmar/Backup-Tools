package crons

import (
	"context"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/satellite"
	crontasks "github.com/StorX2-0/Backup-Tools/tasks"
)

type googlePhotosProcessor struct{}

func NewGooglePhotosProcessor() *googlePhotosProcessor {
	return &googlePhotosProcessor{}
}

func (p *googlePhotosProcessor) Run(input ProcessorInput) error {
	return runGooglePhotosAutosync(input)
}

func runGooglePhotosAutosync(input ProcessorInput) error {
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
	if err := handler.UploadObjectAndSync(ctx, input.Database, storx, satellite.ReserveBucket_Photos, task.LoginId+"/.file_placeholder", nil, task.UserID); err != nil {
		return fmt.Errorf("setup storage placeholder: %w", err)
	}

	proc := crontasks.NewScheduledGooglePhotosProcessor(deps)
	return proc.Run(crontasks.ScheduledTaskProcessorInput{
		InputData:     map[string]interface{}{"access_token": accessToken},
		Memory:        map[string][]string{"pending": {}},
		Task:          task,
		HeartBeatFunc: input.HeartBeatFunc,
		Deps:          deps,
	})
}
