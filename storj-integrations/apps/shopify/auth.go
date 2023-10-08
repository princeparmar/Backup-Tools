package shopify

import (
	"os"

	goshopify "github.com/bold-commerce/go-shopify"
)

type ShopifyApp struct {
	App goshopify.App
}

var ShopifyInitApp ShopifyApp

// Create an app somewhere.
func Init() {
	apiKey := os.Getenv("SHOPIFY_API_KEY")
	apiSecret := os.Getenv("SHOPIFY_API_SECRET")
	ShopifyInitApp = ShopifyApp{App: goshopify.App{
		ApiKey:      apiKey,
		ApiSecret:   apiSecret,
		RedirectUrl: "http://localhost:8000/shopify/callback",
		Scope:       "read_products,read_orders,read_customers",
	}}
}

type ShopifyClient struct {
	*goshopify.Client
}

func CreateClient(token string, shopname string) *ShopifyClient {
	// Create a new API client
	client := goshopify.NewClient(ShopifyInitApp.App, shopname, token)

	return &ShopifyClient{Client: client}
}

func (client *ShopifyClient) GetProducts() ([]goshopify.Product, error) {
	products, err := client.Product.List(goshopify.ProductOption{})
	if err != nil {
		return nil, err
	}
	return products, nil
}

func (client *ShopifyClient) GetCustomers() ([]goshopify.Customer, error) {
	customers, err := client.Customer.List(goshopify.CustomerSearchOptions{})
	if err != nil {
		return nil, err
	}
	return customers, nil
}

func (client *ShopifyClient) GetOrders() ([]goshopify.Order, error) {
	orders, err := client.Order.List(goshopify.OrderListOptions{})
	if err != nil {
		return nil, err
	}
	return orders, nil

}

// - write postman requests
// - publish on GitHub
