package storage

import (
	"fmt"
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type GmailMessageSQL struct {
	ID          uint64 `gorm:"primaryKey"`
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

func ConnectToEmailDB() (*SQLiteEmailDatabase, error) {
	dbPath := "./cache/gmails.db"

	// TODO check if db exists locally
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Database file does not exist, create new file
		fmt.Println("Creating new database file...")
		if _, err := os.Create(dbPath); err != nil {
			return nil, err
		}
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
