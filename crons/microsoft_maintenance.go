package crons

import (
	"context"
	"time"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

// microsoftDailyChecks runs once at startup and then daily.
func (a *AutosyncManager) microsoftDailyChecks(ctx context.Context) {
	if warning := outlook.ClientSecretExpiryWarning(time.Now()); warning != "" {
		logger.Warn(ctx, warning)
	}
}
