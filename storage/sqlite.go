package storage

import (
	"context"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	goshopify "github.com/bold-commerce/go-shopify/v4"
	"github.com/glebarez/sqlite"
	quickbooksLibrary "github.com/rwestlund/quickbooks-go"
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

// SHOPIFY

type SQLiteShopifyDatabase struct {
	*gorm.DB
}

func ConnectToShopifyDB() (*SQLiteShopifyDatabase, error) {
	start := time.Now()

	dbPath := "./cache/shopify.db"

	// TODO check if db exists locally
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Database file does not exist, create new file
		logger.Info(context.Background(), "Creating new database file...")
		file, err := utils.CreateFile(dbPath)
		if err != nil {
			prometheus.RecordError("sqlite_shopify_db_creation_failed", "storage")
			return nil, err
		}
		file.Close()
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		prometheus.RecordError("sqlite_shopify_db_connection_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&goshopify.Product{})
	if err != nil {
		prometheus.RecordError("sqlite_shopify_db_migration_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&goshopify.Order{})
	if err != nil {
		prometheus.RecordError("sqlite_shopify_db_migration_failed", "storage")
		return nil, err
	}

	err = db.AutoMigrate(&goshopify.Customer{})
	if err != nil {
		prometheus.RecordError("sqlite_shopify_db_migration_failed", "storage")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_shopify_db_connection_duration", duration, "database", "shopify")
	prometheus.RecordCounter("sqlite_shopify_db_connection_total", 1, "database", "shopify", "status", "success")

	return &SQLiteShopifyDatabase{db}, nil
}

func (db *SQLiteShopifyDatabase) WriteProductsToDB(product *goshopify.Product) error {
	start := time.Now()

	resp := db.Create(&product)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_shopify_product_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_shopify_product_write_duration", duration, "database", "shopify")
	prometheus.RecordCounter("sqlite_shopify_product_write_total", 1, "database", "shopify", "status", "success")

	return nil
}

func (db *SQLiteShopifyDatabase) WriteOrdersToDB(order *goshopify.Order) error {
	start := time.Now()

	resp := db.Create(&order)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_shopify_order_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_shopify_order_write_duration", duration, "database", "shopify")
	prometheus.RecordCounter("sqlite_shopify_order_write_total", 1, "database", "shopify", "status", "success")

	return nil
}

func (db *SQLiteShopifyDatabase) WriteCustomersToDB(customer *goshopify.Customer) error {
	start := time.Now()

	resp := db.Create(&customer)
	if resp.Error != nil {
		prometheus.RecordError("sqlite_shopify_customer_write_failed", "storage")
		return resp.Error
	}

	duration := time.Since(start)
	prometheus.RecordTimer("sqlite_shopify_customer_write_duration", duration, "database", "shopify")
	prometheus.RecordCounter("sqlite_shopify_customer_write_total", 1, "database", "shopify", "status", "success")

	return nil
}

// QUICKBOOKS

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
