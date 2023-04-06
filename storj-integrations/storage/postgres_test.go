package storage

import (
	"fmt"
	"testing"
)

func TestConnect(t *testing.T) {
	postgres, _ := NewPostgresStore()
	var res GoogleAuthStorage
	postgres.DB.Where("cookie = ?", "cookie1234").First(&res)
	fmt.Println(res)
}
