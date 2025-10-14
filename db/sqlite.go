package db

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
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
	// TODO check if db exists locally
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Database file does not exist, create new file
		logger.Info(context.Background(), "Creating new database file...")
		file, err := utils.CreateFile(dbPath)
		if err != nil {
			return nil, err
		}
		file.Close()
	}

	config := gorm.SQLiteConfig(dbPath)
	db, err := gorm.NewDatabase(config)
	if err != nil {
		return nil, err
	}

	err = db.DB.AutoMigrate(&GmailMessageSQL{})
	if err != nil {
		return nil, err
	}

	return &SQLiteEmailDatabase{DB: db}, nil
}

func (db *SQLiteEmailDatabase) WriteEmailToDB(msg *GmailMessageSQL) error {
	return db.DB.Create(&msg).Error
}

func (db *SQLiteEmailDatabase) GetAllEmailsFromDB() ([]*GmailMessageSQL, error) {
	var messages []*GmailMessageSQL
	if err := db.DB.Order("id asc").Find(&messages).Error; err != nil {
		return nil, err
	}

	return messages, nil
}
