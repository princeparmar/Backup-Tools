package storage

import (
	"context"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/glebarez/sqlite"
	quickbooksLibrary "github.com/rwestlund/quickbooks-go"
	"gorm.io/gorm"
)

type SQLiteQuickbooksDatabase struct {
	*gorm.DB
}

func ConnectToQuickbooksDB() (*SQLiteQuickbooksDatabase, error) {
	start := time.Now()

	dbPath := "./cache/quickbooks.db"

	// TODO check if db exists locally
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Database file does not exist, create new file
		logger.Info(context.Background(), "Creating new database file...")
		file, err := utils.CreateFile(dbPath)
		if err != nil {
			prometheus.RecordError("sqlite_quickbooks_db_creation_failed", "storage")
			return nil, err
		}
		file.Close()
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		prometheus.RecordError("sqlite_quickbooks_db_connection_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&quickbooksLibrary.Customer{})
	if err != nil {
		prometheus.RecordError("sqlite_quickbooks_db_migration_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&quickbooksLibrary.Item{})
	if err != nil {
		prometheus.RecordError("sqlite_quickbooks_db_migration_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&quickbooksLibrary.Invoice{})
	if err != nil {
		prometheus.RecordError("sqlite_quickbooks_db_migration_failed", "storage")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_quickbooks_db_connection_duration", duration, "database", "quickbooks")
	prometheus.RecordCounter("sqlite_quickbooks_db_connection_total", 1, "database", "quickbooks", "status", "success")

	return &SQLiteQuickbooksDatabase{db}, nil
}

func (db *SQLiteQuickbooksDatabase) WriteCustomersToDB(customer *quickbooksLibrary.Customer) error {
	start := time.Now()

	resp := db.Create(&customer)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_quickbooks_customer_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_quickbooks_customer_write_duration", duration, "database", "quickbooks")
	prometheus.RecordCounter("sqlite_quickbooks_customer_write_total", 1, "database", "quickbooks", "status", "success")

	return nil
}

func (db *SQLiteQuickbooksDatabase) WriteItemsToDB(item *quickbooksLibrary.Item) error {
	start := time.Now()

	resp := db.Create(&item)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_quickbooks_item_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_quickbooks_item_write_duration", duration, "database", "quickbooks")
	prometheus.RecordCounter("sqlite_quickbooks_item_write_total", 1, "database", "quickbooks", "status", "success")

	return nil
}

func (db *SQLiteQuickbooksDatabase) WriteInvoicesToDB(invoice *quickbooksLibrary.Invoice) error {
	start := time.Now()

	resp := db.Create(&invoice)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_quickbooks_invoice_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_quickbooks_invoice_write_duration", duration, "database", "quickbooks")
	prometheus.RecordCounter("sqlite_quickbooks_invoice_write_total", 1, "database", "quickbooks", "status", "success")

	return nil
}
