package db

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
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

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&GmailMessageSQL{})
	if err != nil {
		return nil, err
	}

	return &SQLiteEmailDatabase{db}, nil
}

func (db *SQLiteEmailDatabase) WriteEmailToDB(msg *GmailMessageSQL) error {
	resp := db.Create(&msg)
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

func (db *SQLiteEmailDatabase) GetAllEmailsFromDB() ([]*GmailMessageSQL, error) {
	var messages []*GmailMessageSQL
	if err := db.Order("id asc").Find(&messages).Error; err != nil {
		return nil, err
	}

	return messages, nil
}
