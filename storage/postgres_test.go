package storage

import (
	"context"
	"os"
	"testing"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
)

func TestConnect(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")

	postgres, _ := NewPostgresStore(dsn, false)
	var res GoogleAuthStorage
	postgres.DB.Where("cookie = ?", "cookie1234").First(&res)
	logger.Info(context.Background(), "res", logger.Any("res", res))
}
