package crons

import (
	"context"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/restore"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type RestoreManager struct {
	store *db.PostgresDb
}

func NewRestoreManager(store *db.PostgresDb) *RestoreManager {
	return &RestoreManager{store: store}
}

func (m *RestoreManager) Start() {
	c := cron.New()
	c.AddFunc("@every 30s", func() {
		ctx := createRestoreCronContext()
		if err := restore.ProcessRestoreJobs(ctx, m.store); err != nil {
			logger.Error(ctx, "Restore worker tick failed", logger.ErrorField(err))
		}
	})
	c.Start()
	logger.Info(context.Background(), "Restore manager cron started (every 30s)")
}

func createRestoreCronContext() context.Context {
	traceID := uuid.New().String()
	ctx := logger.WithTraceID(context.Background(), traceID)
	logger.Info(ctx, "Restore worker tick started")
	return ctx
}
