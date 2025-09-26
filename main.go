package main

import (
	"log/slog"
	"os"

	"github.com/StorX2-0/Backup-Tools/crons"
	"github.com/StorX2-0/Backup-Tools/logger"
	"github.com/StorX2-0/Backup-Tools/logger/newrelic"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/server"
	"github.com/StorX2-0/Backup-Tools/storage"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	defer logger.Sync()

	dsn := os.Getenv("POSTGRES_DSN")

	storage, err := storage.NewPostgresStore(dsn)
	if err != nil {
		logger.Error("error starting the postgress store", zap.Error(err))
		logger.Warn("exiting...")
		os.Exit(1)
	}

	err = storage.Migrate()
	if err != nil {
		logger.Error("error migrating to the postgress store", zap.Error(err))
		logger.Warn("exiting...")
		os.Exit(1)
	}

	// setup cron jobs
	cronManager := crons.NewAutosyncManager(storage)
	go cronManager.Start()

	address := ":8005"
	if envPortVal := os.Getenv("PORT"); envPortVal != "" {
		address = envPortVal
	}

	server.StartServer(storage, address)
}

func init() {
	err := godotenv.Load()
	if err != nil {
		// Fallback to basic logger for init errors
		slog.Error("error loading the environment", "error", err)
		slog.Warn("exiting...")
		os.Exit(1)
	}

	satellite.StorxSatelliteService = os.Getenv("STORX_SATELLITE_SERVICE")

	// Initialize logger with New Relic integration
	newRelicAPIKey := os.Getenv("NEWRELIC_API_KEY")
	newRelicEnabled := os.Getenv("NEWRELIC_ENABLED") == "true"

	if newRelicAPIKey != "" || newRelicEnabled {
		logger.InitWithNewRelic(newrelic.NewLogInterceptor(newRelicAPIKey, newRelicEnabled))
	}
}
