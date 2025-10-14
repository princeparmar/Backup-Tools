package db

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/gorm"
	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	goshopify "github.com/bold-commerce/go-shopify/v4"
)

type SQLiteShopifyDatabase struct {
	*gorm.DB
}

func ConnectToShopifyDB() (*SQLiteShopifyDatabase, error) {
	dbPath := "./cache/shopify.db"

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

	err = db.DB.AutoMigrate(&goshopify.Product{}, &goshopify.Order{}, &goshopify.Customer{})
	if err != nil {
		return nil, err
	}

	return &SQLiteShopifyDatabase{DB: db}, nil
}

func (db *SQLiteShopifyDatabase) WriteProductsToDB(product *goshopify.Product) error {
	return db.DB.Create(&product).Error
}

func (db *SQLiteShopifyDatabase) WriteOrdersToDB(order *goshopify.Order) error {
	return db.DB.Create(&order).Error
}

func (db *SQLiteShopifyDatabase) WriteCustomersToDB(customer *goshopify.Customer) error {
	return db.DB.Create(&customer).Error
}
