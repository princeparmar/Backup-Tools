package db

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	quickbooksLibrary "github.com/rwestlund/quickbooks-go"
)

type SQLiteQuickbooksDatabase struct {
	*gorm.DB
}

func ConnectToQuickbooksDB() (*SQLiteQuickbooksDatabase, error) {
	dbPath := "./cache/quickbooks.db"

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

	err = db.DB.AutoMigrate(&quickbooksLibrary.Customer{}, &quickbooksLibrary.Item{}, &quickbooksLibrary.Invoice{})
	if err != nil {
		return nil, err
	}

	return &SQLiteQuickbooksDatabase{DB: db}, nil
}

func (db *SQLiteQuickbooksDatabase) WriteCustomersToDB(customer *quickbooksLibrary.Customer) error {
	return db.DB.Create(&customer).Error
}

func (db *SQLiteQuickbooksDatabase) WriteItemsToDB(item *quickbooksLibrary.Item) error {
	return db.DB.Create(&item).Error
}

func (db *SQLiteQuickbooksDatabase) WriteInvoicesToDB(invoice *quickbooksLibrary.Invoice) error {
	return db.DB.Create(&invoice).Error
}
