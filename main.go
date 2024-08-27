package main

import (
	"log/slog"
	"os"
	"storj-integrations/server"
	"storj-integrations/storage"

	"github.com/joho/godotenv"
)

func main() {
	storage, err := storage.NewPostgresStore()
	if err != nil {
		slog.Error("error starting the postgress store", "error", err)
		slog.Warn("exiting...")
		os.Exit(1)
	}
	err = storage.Migrate()
	if err != nil {
		slog.Error("error migrating to the postgress store", "error", err)
		slog.Warn("exiting...")
		os.Exit(1)
	}

	address := ":8005"
	if envPortVal := os.Getenv("PORT"); envPortVal != "" {
		address = envPortVal
	}

	server.StartServer(storage, address)
}

// Loads all data from .env file into Environmental variables.
func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{AddSource: true, Level: slog.LevelInfo})))
	err := godotenv.Load()
	if err != nil {
		slog.Error("error loading the environment", "error", err)
		slog.Warn("exiting...")
		os.Exit(1)
	}

}
