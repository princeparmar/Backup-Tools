package storage

import (
	"fmt"
	"os"

	quickbooksLibrary "github.com/rwestlund/quickbooks-go"

	goshopify "github.com/bold-commerce/go-shopify"
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

// SHOPIFY

type SQLiteShopifyDatabase struct {
	*gorm.DB
}

func ConnectToShopifyDB() (*SQLiteShopifyDatabase, error) {
	dbPath := "./cache/shopify.db"

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
		fmt.Println("Creating new database file...")
		if _, err := os.Create(dbPath); err != nil {
			return nil, err
		}
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
