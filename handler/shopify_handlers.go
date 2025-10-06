package handler

import (
	"context"
	"net/http"
	"os"

	"github.com/StorX2-0/Backup-Tools/apps/shopify"
	"github.com/StorX2-0/Backup-Tools/middleware"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"github.com/StorX2-0/Backup-Tools/storage"

	"github.com/labstack/echo/v4"
)

// createShopifyCleint creates a Shopify client using the cookie token
func createShopifyCleint(c echo.Context, shopname string) *shopify.ShopifyClient {
	cookieToken, err := c.Cookie("shopify-auth")
	if err != nil {
		c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error": "UNAUTHENTICATED!",
		})
		return nil
	}
	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)
	token, err := database.ReadShopifyAuthToken(cookieToken.Value)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Error reading token from database",
			"error":   err.Error(),
		})
		return nil
	}
	client, err := shopify.CreateClient(token, shopname)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]interface{}{
			"message": "Error creating shopify client",
			"error":   err.Error(),
		})
		return nil
	}
	return client
}

// HandleShopifyProductsToSatellite uploads Shopify products to Satellite
func HandleShopifyProductsToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	products, err := client.GetProducts()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting products", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Shopify, "shopify.db")
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

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, product := range products {
		err = db.WriteProductsToDB(&product)
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
	err = satellite.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
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

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with products data was successfully uploaded"})
}

// HandleShopifyCustomersToSatellite uploads Shopify customers to Satellite
func HandleShopifyCustomersToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	customers, err := client.GetCustomers()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting customers", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Shopify, "shopify.db")
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

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, customer := range customers {
		err = db.WriteCustomersToDB(&customer)
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
	err = satellite.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
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

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with customers data was successfully uploaded"})

}

// HandleShopifyOrdersToSatellite uploads Shopify orders to Satellite
func HandleShopifyOrdersToSatellite(c echo.Context) error {
	accesGrant := c.Request().Header.Get("ACCESS_TOKEN")
	if accesGrant == "" {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": "access token not found",
		})
	}
	shopname := c.Param("shopname")

	client := createShopifyCleint(c, shopname)

	if client == nil {
		return http.ErrNoCookie
	}
	orders, err := client.GetOrders()
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"message": "Error getting orders", "error": err.Error()})
	}

	userCacheDBPath := "./cache/" + utils.CreateUserTempCacheFolder() + "/shopify.db"

	byteDB, err := satellite.DownloadObject(context.Background(), accesGrant, satellite.ReserveBucket_Shopify, "shopify.db")
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

	db, err := storage.ConnectToShopifyDB()
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}
	for _, order := range orders {
		err = db.WriteOrdersToDB(&order)
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
	err = satellite.DeleteObject(context.Background(), accesGrant, "shopify", "shopify.db")
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]interface{}{
			"error": err.Error(),
		})
	}

	// upload file to satellite
	err = satellite.UploadObject(context.Background(), accesGrant, "shopify", "shopify.db", dbByte)
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

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "DB with orders data was successfully uploaded"})
}

// HandleShopifyAuth initiates Shopify OAuth authentication
func HandleShopifyAuth(c echo.Context) error {
	shopName := c.QueryParam("shop")
	state := c.QueryParam("state")

	authUrl, _ := shopify.ShopifyInitApp.App.AuthorizeUrl(shopName, state)

	return c.Redirect(http.StatusFound, authUrl)
}

// HandleShopifyAuthRedirect handles Shopify OAuth callback
func HandleShopifyAuthRedirect(c echo.Context) error {
	// Check that the callback signature is valid
	if ok, err := shopify.ShopifyInitApp.App.VerifyAuthorizationURL(c.Request().URL); !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Signature",
			"error":   err.Error(),
		})
	}
	query := c.Request().URL.Query()
	shopName := query.Get("shop")
	code := query.Get("code")
	token, err := shopify.ShopifyInitApp.App.GetAccessToken(c.Request().Context(), shopName, code)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"message": "Invalid Signature",
			"error":   err.Error(),
		})
	}

	database := c.Get(middleware.DbContextKey).(*storage.PosgresStore)

	cookieNew := new(http.Cookie)
	cookieNew.Name = "shopify-auth"
	cookieNew.Value = utils.RandStringRunes(50)
	database.WriteShopifyAuthToken(cookieNew.Value, token)

	c.SetCookie(cookieNew)

	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Authorized!"})
}
