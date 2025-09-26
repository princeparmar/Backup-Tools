package storage

import (
	"os"
	"testing"

	"github.com/StorX2-0/Backup-Tools/logger"
)

func TestConnect(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")

	postgres, _ := NewPostgresStore(dsn)
	var res GoogleAuthStorage
	postgres.DB.Where("cookie = ?", "cookie1234").First(&res)
	logger.Info("res", logger.Any("res", res))
}
