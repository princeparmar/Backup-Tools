package gorm

import (
	"log"
	"os"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewDatabase(config DatabaseConfig) (*DB, error) {
	if config.DSN != "" {
		return newPostgres(config)
	}
	return newSQLite(config)
}

func newPostgres(config DatabaseConfig) (*DB, error) {
	logLevel := logger.Silent
	if config.QueryLogging {
		logLevel = logger.Info
	}

	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold: time.Second,
			LogLevel:      logLevel,
			Colorful:      true,
		},
	)

	db, err := gorm.Open(postgres.Open(config.DSN), &gorm.Config{Logger: newLogger})
	if err != nil {
		return nil, err
	}

	return setupConnectionPool(db, config)
}

func newSQLite(config DatabaseConfig) (*DB, error) {
	os.MkdirAll("./cache", 0755)
	db, err := gorm.Open(sqlite.Open(config.DBPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return NewDB(db), nil
}

func setupConnectionPool(db *gorm.DB, config DatabaseConfig) (*DB, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	sqlDB.SetMaxIdleConns(config.MaxIdleConns)
	sqlDB.SetMaxOpenConns(config.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(config.ConnMaxLifetime)

	return NewDB(db), nil
}
