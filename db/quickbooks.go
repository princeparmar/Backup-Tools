package db

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/glebarez/sqlite"
	quickbooksLibrary "github.com/rwestlund/quickbooks-go"
	"gorm.io/gorm"
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

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&quickbooksLibrary.Customer{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&quickbooksLibrary.Item{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&quickbooksLibrary.Invoice{})
	if err != nil {
		return nil, err
	}

	return &SQLiteQuickbooksDatabase{db}, nil
}

func (db *SQLiteQuickbooksDatabase) WriteCustomersToDB(customer *quickbooksLibrary.Customer) error {

	resp := db.Create(&customer)
	if resp.Error != nil {
		return resp.Error
	}

	return nil
}

func (db *SQLiteQuickbooksDatabase) WriteItemsToDB(item *quickbooksLibrary.Item) error {

	resp := db.Create(&item)
	if resp.Error != nil {
		return resp.Error
	}

	return nil
}

func (db *SQLiteQuickbooksDatabase) WriteInvoicesToDB(invoice *quickbooksLibrary.Invoice) error {

	resp := db.Create(&invoice)
	if resp.Error != nil {
		return resp.Error
	}

	return nil
}
