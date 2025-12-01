package gorm

import "time"

type DatabaseConfig struct {
	DSN             string
	DBPath          string
	QueryLogging    bool
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
}

func DefaultConfig() DatabaseConfig {
	return DatabaseConfig{
		MaxIdleConns:    25,
		MaxOpenConns:    100,
		ConnMaxLifetime: time.Hour,
	}
}

// OptimizedConfig returns configuration optimized for high concurrency
func OptimizedConfig() DatabaseConfig {
	return DatabaseConfig{
		MaxIdleConns:    30,
		MaxOpenConns:    100,
		ConnMaxLifetime: time.Hour,
	}
}

func PostgresConfig(dsn string, queryLogging bool) DatabaseConfig {
	config := DefaultConfig()
	config.DSN, config.QueryLogging = dsn, queryLogging
	return config
}

func SQLiteConfig(dbPath string) DatabaseConfig {
	config := DefaultConfig()
	config.DBPath = dbPath
	return config
}
