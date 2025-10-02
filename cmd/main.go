package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/StorX2-0/Backup-Tools/crons"
	"github.com/StorX2-0/Backup-Tools/logger"
	"github.com/StorX2-0/Backup-Tools/logger/newrelic"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/server"
	"github.com/StorX2-0/Backup-Tools/storage"
	"github.com/joho/godotenv"
)

func main() {
	defer logger.Sync()
	ctx := context.Background()

	// Initialize environment and dependencies
	if err := initApp(); err != nil {
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
	server.StartServer(storage, getAddress())
}

func initApp() error {
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

	satellite.StorxSatelliteService = os.Getenv("STORX_SATELLITE_SERVICE")

	// Initialize logger with New Relic integration
	if apiKey := os.Getenv("NEWRELIC_API_KEY"); apiKey != "" || os.Getenv("NEWRELIC_ENABLED") == "true" {
		logger.InitWithNewRelic(newrelic.NewLogInterceptor(apiKey, true))
	}

	return nil
}

func setupStorage(ctx context.Context) (*storage.PosgresStore, error) {
	store, err := storage.NewPostgresStore(
		os.Getenv("POSTGRES_DSN"),
		os.Getenv("QUERY_LOGGING") == "true",
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
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8005"
}
