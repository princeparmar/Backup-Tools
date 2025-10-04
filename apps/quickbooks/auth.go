package quickbooks

import (
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	quickbooks "github.com/rwestlund/quickbooks-go"
)

type QBClient struct {
	Client *quickbooks.Client
}

// Create an app.
func CreateClient() (*QBClient, error) {
	start := time.Now()

	clientId := os.Getenv("QUICKBOOKS_API_KEY")
	clientSecret := os.Getenv("QUICKBOOKS_API_SECRET")
	realmId := os.Getenv("QUICKBOOKS_REALM_ID")

	client, err := quickbooks.NewQuickbooksClient(clientId, clientSecret, realmId, false, nil)
	if err != nil {
		prometheus.RecordError("quickbooks_client_creation_failed", "quickbooks")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("quickbooks_client_creation_duration", duration, "service", "quickbooks")
	prometheus.RecordCounter("quickbooks_client_creation_total", 1, "service", "quickbooks", "status", "success")

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
