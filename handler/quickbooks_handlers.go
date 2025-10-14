package handler

import (
	"context"
	"net/http"
	"os"

	"github.com/StorX2-0/Backup-Tools/apps/quickbooks"
	"github.com/StorX2-0/Backup-Tools/db"
	"github.com/StorX2-0/Backup-Tools/pkg/monitor"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"

	"github.com/labstack/echo/v4"
)

// HandleQuickbooksCustomersToSatellite uploads QuickBooks customers to Satellite
func HandleQuickbooksCustomersToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, _ := quickbooks.CreateClient()
	customers, err := client.Client.FetchCustomers()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.RestoreBucket_Quickbooks, "quickbooks.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		dbFile.Close()
	}

	db, err := db.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range customers {
		err = db.WriteCustomersToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "customers are successfully uploaded from quickbooks to satellite"})
}

// HandleQuickbooksItemsToSatellite uploads QuickBooks items to Satellite
func HandleQuickbooksItemsToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, _ := quickbooks.CreateClient()
	items, err := client.Client.FetchItems()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.RestoreBucket_Quickbooks, "quickbooks.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		dbFile.Close()
	}

	db, err := db.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range items {
		err = db.WriteItemsToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "items are successfully uploaded from quickbooks to satellite"})
}

// HandleQuickbooksInvoicesToSatellite uploads QuickBooks invoices to Satellite
func HandleQuickbooksInvoicesToSatellite(c echo.Context) error {
	ctx := c.Request().Context()
	var err error
	defer monitor.Mon.Task()(&ctx)(&err)

	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}

	client, _ := quickbooks.CreateClient()
	invoices, err := client.Client.FetchInvoices()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/quickbooks.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.RestoreBucket_Quickbooks, "quickbooks.db")
	// Copy file from satellite to local cache if everything's fine.
	// Skip error check, if there's error - we will check that and create new file
	if err == nil {
		dbFile, err := utils.CreateFile(userCacheDBPath)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		_, err = dbFile.Write(byteDB)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
		dbFile.Close()
	}

	db, err := db.ConnectToQuickbooksDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, n := range invoices {
		err = db.WriteInvoicesToDB(&n)
		if err != nil {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// DELETE OLD DB COPY FROM SATELLITE UPLOAD UP TO DATE DB FILE BACK TO SATELLITE AND DELETE IT FROM LOCAL CACHE

	// get db file data
	dbByte, err := os.ReadFile(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete old db copy from satellite
	err = satellite.DeleteObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "quickbooks", "quickbooks.db", dbByte)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// delete from local cache copy of database
	err = os.Remove(userCacheDBPath)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "invoices are successfully uploaded from quickbooks to satellite"})
}
