package storage

import (
	"context"
	"os"

	"github.com/StorX2-0/Backup-Tools/pkg/logger"
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

// SHOPIFY

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

// QUICKBOOKS

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
