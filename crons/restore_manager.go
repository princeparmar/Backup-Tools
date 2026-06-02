package crons

import (
	"context"

	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/restore"
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
		ctx := context.Background()
		if err := restore.ProcessRestoreJobs(ctx, m.store); err != nil {
			logger.Error(ctx, "restore worker tick failed", logger.ErrorField(err))
		}
	})
	c.Start()
	logger.Info(context.Background(), "Restore manager cron started")
}
