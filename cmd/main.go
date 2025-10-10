package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/StorX2-0/Backup-Tools/crons"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/logger/newrelic"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/router"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/joho/godotenv"
)

func main() {
	defer logger.Sync()
	ctx := context.Background()

	// Initialize environment and dependencies
	if err := initApp(ctx); err != nil {
		logger.Error(ctx, "failed to initialize application", logger.ErrorField(err))
		os.Exit(1)
	}

	// Setup storage
	storage, err := setupStorage(ctx)
	if err != nil {
		os.Exit(1)
	}

	// Start background jobs
	crons.NewAutosyncManager(storage).Start()

	// Start server
	router.StartServer(storage, getAddress())
}

func initApp(ctx context.Context) error {
	// Get the directory where the binary is located
	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	execDir := filepath.Dir(execPath)
	envPath := filepath.Join(execDir, ".env")

	// Try to load .env from the binary's directory first
	if err := godotenv.Load(envPath); err != nil {
		// Fallback to current directory
		if err := godotenv.Load(); err != nil {
			return err
		}
	}

	satellite.StorxSatelliteService = utils.GetEnvWithKey("STORX_SATELLITE_SERVICE")

	// Initialize logger with New Relic integration
	if apiKey := utils.GetEnvWithKey("NEWRELIC_API_KEY"); apiKey != "" || utils.GetEnvWithKey("NEWRELIC_ENABLED") == "true" {
		logger.InitWithNewRelic(newrelic.NewLogInterceptor(apiKey, true))
	}

	// Initialize Prometheus metrics
	if err := monitor.InitializeGlobalManager(); err != nil {
		logger.Error(ctx, "Failed to initialize Prometheus metrics, retrying...", logger.ErrorField(err))
		// Simple retry
		time.Sleep(2 * time.Second)
		if err := monitor.InitializeGlobalManager(); err != nil {
			logger.Error(ctx, "Metrics initialization failed after retry, continuing without metrics", logger.ErrorField(err))
		}
	}

	// Start system metrics updater (updates every 30 seconds)
	monitor.StartSystemMetricsUpdater(30 * time.Second)
	logger.Info(ctx, "System metrics updater started")
	return nil
}

func setupStorage(ctx context.Context) (*storage.PosgresStore, error) {
	store, err := storage.NewPostgresStore(
		utils.GetEnvWithKey("POSTGRES_DSN"),
		utils.GetEnvWithKey("QUERY_LOGGING") == "true",
	)
	if err != nil {
		logger.Error(ctx, "failed to create postgres store", logger.ErrorField(err))
		return nil, err
	}

	if err := store.Migrate(); err != nil {
		logger.Error(ctx, "failed to migrate database", logger.ErrorField(err))
		return nil, err
	}

	return store, nil
}

func getAddress() string {
	if port := utils.GetEnvWithKey("PORT"); port != "" {
		return ":" + port
	}
	return ":8005"
}
