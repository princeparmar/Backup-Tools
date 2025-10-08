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
	"gorm.io/gorm"
)

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
