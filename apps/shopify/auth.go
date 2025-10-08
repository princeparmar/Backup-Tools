package shopify

import (
	"context"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	goshopify "github.com/bold-commerce/go-shopify/v4"
)

type ShopifyApp struct {
	App goshopify.App
}

var ShopifyInitApp *ShopifyApp

// Create an app.
func Init() {
	start := time.Now()

	apiKey := utils.GetEnvWithKey("SHOPIFY_API_KEY")
	apiSecret := utils.GetEnvWithKey("SHOPIFY_API_SECRET")
	ShopifyInitApp = &ShopifyApp{App: goshopify.App{
		ApiKey:      apiKey,
		ApiSecret:   apiSecret,
		RedirectUrl: "http://localhost:8000/shopify/callback",
		Scope:       "read_products,read_orders,read_customers",
	}}

	duration := time.Since(start)
	prometheus.RecordTimer("shopify_app_init_duration", duration, "service", "shopify")
	prometheus.RecordCounter("shopify_app_init_total", 1, "service", "shopify", "status", "success")
}

type ShopifyClient struct {
	*goshopify.Client
}

func CreateClient(token string, shopname string) (*ShopifyClient, error) {
	start := time.Now()

	// Create a new API client
	client, err := goshopify.NewClient(ShopifyInitApp.App, shopname, token)
	if err != nil {
		prometheus.RecordError("shopify_client_creation_failed", "shopify")
		return &ShopifyClient{Client: client}, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("shopify_client_creation_duration", duration, "shop", shopname)
	prometheus.RecordCounter("shopify_client_creation_total", 1, "shop", shopname, "status", "success")

	return &ShopifyClient{Client: client}, err
}

func (client *ShopifyClient) GetProducts() ([]goshopify.Product, error) {
	start := time.Now()

	products, err := client.Product.List(context.Background(), goshopify.ProductOption{})
	if err != nil {
		prometheus.RecordError("shopify_get_products_failed", "shopify")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("shopify_get_products_duration", duration, "service", "shopify")
	prometheus.RecordCounter("shopify_get_products_total", 1, "service", "shopify", "status", "success")
	prometheus.RecordCounter("shopify_products_listed_total", int64(len(products)), "service", "shopify")

	return products, nil
}

func (client *ShopifyClient) GetCustomers() ([]goshopify.Customer, error) {
	start := time.Now()

	customers, err := client.Customer.List(context.Background(), goshopify.CustomerSearchOptions{})
	if err != nil {
		prometheus.RecordError("shopify_get_customers_failed", "shopify")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("shopify_get_customers_duration", duration, "service", "shopify")
	prometheus.RecordCounter("shopify_get_customers_total", 1, "service", "shopify", "status", "success")
	prometheus.RecordCounter("shopify_customers_listed_total", int64(len(customers)), "service", "shopify")

	return customers, nil
}

func (client *ShopifyClient) GetOrders() ([]goshopify.Order, error) {
	start := time.Now()

	orders, err := client.Order.List(context.Background(), goshopify.OrderListOptions{})
	if err != nil {
		prometheus.RecordError("shopify_get_orders_failed", "shopify")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("shopify_get_orders_duration", duration, "service", "shopify")
	prometheus.RecordCounter("shopify_get_orders_total", 1, "service", "shopify", "status", "success")
	prometheus.RecordCounter("shopify_orders_listed_total", int64(len(orders)), "service", "shopify")

	return orders, nil
}
