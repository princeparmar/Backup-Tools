package database

import (
	"time"
)

// Config represents database configuration
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"ssl_mode"`

	MigrationFilePath string `json:"migration_file_path"`

	// Connection pool settings
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
}

// Page represents pagination parameters
type Page struct {
	Offset int `json:"offset"`
	Limit  int `json:"page_size"`
}
