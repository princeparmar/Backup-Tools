package main

import (
	"log"
	"log/slog"
	"os"
	"storj-integrations/server"
	"storj-integrations/storage"

	"github.com/joho/godotenv"
)

func main() {
	storage, err := storage.NewPostgresStore()
	if err != nil {
		log.Fatal(err)
	}
	err = storage.Migrate()
	if err != nil {
		log.Fatal(err)
	}

	server.StartServer(storage)
}

// Loads all data from .env file into Environmental variables.
func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{AddSource: true, Level: slog.LevelInfo})))
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

}
