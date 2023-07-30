package main

import (
	"log"
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
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

}
