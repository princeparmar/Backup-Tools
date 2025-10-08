package storage

import (
	"context"
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
)

func TestConnect(t *testing.T) {
	dsn := utils.GetEnvWithKey("POSTGRES_DSN")

	postgres, _ := NewPostgresStore(dsn, false)
	var res GoogleAuthStorage
	postgres.DB.Where("cookie = ?", "cookie1234").First(&res)
	logger.Info(context.Background(), "res", logger.Any("res", res))
}
