package db

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	goshopify "github.com/bold-commerce/go-shopify/v4"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
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

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&goshopify.Product{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&goshopify.Order{})
	if err != nil {
		return nil, err
	}

	err = db.AutoMigrate(&goshopify.Customer{})
	if err != nil {
		return nil, err
	}

	return &SQLiteShopifyDatabase{db}, nil
}

func (db *SQLiteShopifyDatabase) WriteProductsToDB(product *goshopify.Product) error {
	resp := db.Create(&product)
	if resp.Error != nil {
		return resp.Error
	}

	return nil
}

func (db *SQLiteShopifyDatabase) WriteOrdersToDB(order *goshopify.Order) error {
	resp := db.Create(&order)
	if resp.Error != nil {
		return resp.Error
	}

	return nil
}

func (db *SQLiteShopifyDatabase) WriteCustomersToDB(customer *goshopify.Customer) error {
	resp := db.Create(&customer)
	if resp.Error != nil {
		return resp.Error
	}

	return nil
}
