package storage

import (
	"context"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type GmailMessageSQL struct {
	ID          string `gorm:"primaryKey"`
	Date        int64
	From        string
	To          string
	Subject     string
	Body        string
	Attachments string
}

type SQLiteEmailDatabase struct {
	*gorm.DB
}

func ConnectToEmailDB(dbPath string) (*SQLiteEmailDatabase, error) {
	start := time.Now()

	// TODO check if db exists locally
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Database file does not exist, create new file
		logger.Info(context.Background(), "Creating new database file...")
		file, err := utils.CreateFile(dbPath)
		if err != nil {
			prometheus.RecordError("sqlite_email_db_creation_failed", "storage")
			return nil, err
		}
		file.Close()
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		prometheus.RecordError("sqlite_email_db_connection_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&GmailMessageSQL{})
	if err != nil {
		prometheus.RecordError("sqlite_email_db_migration_failed", "storage")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_email_db_connection_duration", duration, "database", "email")
	prometheus.RecordCounter("sqlite_email_db_connection_total", 1, "database", "email", "status", "success")

	return &SQLiteEmailDatabase{db}, nil
}

func (db *SQLiteEmailDatabase) WriteEmailToDB(msg *GmailMessageSQL) error {
	start := time.Now()

	resp := db.Create(&msg)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_email_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_email_write_duration", duration, "database", "email")
	prometheus.RecordCounter("sqlite_email_write_total", 1, "database", "email", "status", "success")

	return nil
}

func (db *SQLiteEmailDatabase) GetAllEmailsFromDB() ([]*GmailMessageSQL, error) {
	start := time.Now()

	var messages []*GmailMessageSQL
	if err := db.Order("id asc").Find(&messages).Error; err != nil {
		prometheus.RecordError("sqlite_email_read_failed", "storage")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_email_read_duration", duration, "database", "email")
	prometheus.RecordCounter("sqlite_email_read_total", 1, "database", "email", "status", "success")
	prometheus.RecordCounter("sqlite_emails_read_total", int64(len(messages)), "database", "email")

	return messages, nil
}
