package storage

import (
	"fmt"
	"os"
	"testing"
)

func TestConnect(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")

	postgres, _ := NewPostgresStore(dsn)
	var res GoogleAuthStorage
	postgres.DB.Where("cookie = ?", "cookie1234").First(&res)
	fmt.Println(res)
}
