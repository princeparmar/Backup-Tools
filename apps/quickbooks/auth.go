package quickbooks

import (
	"os"

	quickbooks "github.com/rwestlund/quickbooks-go"
)

type QBClient struct {
	Client *quickbooks.Client
}

// Create an app.
func CreateClient() (*QBClient, error) {

	clientId := os.Getenv("QUICKBOOKS_API_KEY")
	clientSecret := os.Getenv("QUICKBOOKS_API_SECRET")
	realmId := os.Getenv("QUICKBOOKS_REALM_ID")

	client, err := quickbooks.NewQuickbooksClient(clientId, clientSecret, realmId, false, nil)
	if err != nil {
		return nil, err

	}
	return &QBClient{Client: client}, nil

}

// func Init() {
// 	QuickbooksClient = &QBClient{ Client: &quickbooks.Client{
// 		ClientID: os.Getenv("QUICKBOOKS_API_KEY"),
// 		ClientSecret: os.Getenv("QUICKBOOKS_API_SECRET"),
// 		RedirectURL: "http://localhost:8000/shopify/callback",
// 		Scopes: []string{"com.intuit.quickbooks.accounting, com.intuit.quickbooks.payment, openid, profile, email, phone, address"},
// 	},
// }

// }
