package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/StorX2-0/Backup-Tools/crons"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/handler"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/logger/newrelic"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/router"
	"github.com/StorX2-0/Backup-Tools/satellite"
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
	store, err := setupStorage(ctx)
	if err != nil {
		os.Exit(1)
	}

	// Start background jobs
	crons.NewAutosyncManager(store).Start()
	crons.NewRestoreManager(store).Start()
	handler.StartWebhookEventCleanupLoop(ctx, store)

	// Start server
	router.StartServer(store, getAddress())
}

func initApp(ctx context.Context) error {
	// Get the directory where the binary is located
	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	execDir := filepath.Dir(execPath)
	if err := loadEnvFile(execDir); err != nil {
		return err
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

// envFilePath is the file this process loads. ENV_FILE is set per Windows
// service, so the Google and Microsoft services can share one exe in D:\bin
// and still read different files. An empty ENV_FILE keeps the existing
// default: .env next to the exe.
func envFilePath(execDir, envFile string) string {
	if path := strings.TrimSpace(envFile); path != "" {
		return path
	}
	return filepath.Join(execDir, ".env")
}

func loadEnvFile(execDir string) error {
	path := envFilePath(execDir, os.Getenv("ENV_FILE"))
	if strings.TrimSpace(os.Getenv("ENV_FILE")) != "" {
		// Do not fall back to another file. A Microsoft service must not
		// silently load the Google environment.
		return godotenv.Load(path)
	}
	if err := godotenv.Load(path); err != nil {
		return godotenv.Load()
	}
	return nil
}

func setupStorage(ctx context.Context) (*db.PostgresDb, error) {
	store, err := db.NewPostgresStore(
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
	if address := utils.GetEnvWithKey("ADDRESS"); address != "" {
		return address
	}
	return ":8005"
}
