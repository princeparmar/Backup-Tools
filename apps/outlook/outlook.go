package outlook

import (
	"context"
	"errors"

	abs "github.com/microsoft/kiota-abstractions-go"
	msgraph "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

type OutlookClient struct {
	*msgraph.GraphServiceClient
}

// BearerTokenAuthenticationProvider implements the AuthenticationProvider interface
type BearerTokenAuthenticationProvider struct {
	accessToken string
}

// AuthenticateRequest adds the Bearer token to the request
func (b *BearerTokenAuthenticationProvider) AuthenticateRequest(ctx context.Context, req *abs.RequestInformation, additionalAuthenticationContext map[string]interface{}) error {
	req.Headers.Add("Authorization", "Bearer "+b.accessToken)
	return nil
}

func NewOutlookClientUsingToken(accessToken string) (*OutlookClient, error) {
	authProvider := &BearerTokenAuthenticationProvider{accessToken: accessToken}
	adapter, err := msgraph.NewGraphRequestAdapter(authProvider)
	if err != nil {
		return nil, err
	}
	client := msgraph.NewGraphServiceClient(adapter)
	return &OutlookClient{client}, nil
}

func (client *OutlookClient) GetCurrentUser() (*OutlookUser, error) {
	user, err := client.Me().Get(context.Background(), nil)
	if err != nil {
		return nil, err
	}

	u := NewOutlookUser(user)
	if u == nil || u.ID == "" || u.Mail == "" {
		return nil, errors.New("user is nil")
	}

	return u, nil
}

// GetUserMessages retrieves messages from Outlook with pagination support
func (client *OutlookClient) GetUserMessages(skip, limit int32) ([]*OutlookMinimalMessage, error) {
	requestBuilder := client.Me().Messages()

	if limit > 100 || limit < 1 {
		limit = 100
	}

	if skip < 0 {
		skip = 0
	}

	// Set up request parameters with expanded fields
	query := users.ItemMessagesRequestBuilderGetQueryParameters{
		Top:    int32Ptr(limit),
		Skip:   int32Ptr(skip),
		Select: []string{"id", "subject", "from", "receivedDateTime"},
	}

	configuration := users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &query,
	}

	result, err := requestBuilder.Get(context.Background(), &configuration)
	if err != nil {
		return nil, err
	}

	outlookMessages := make([]*OutlookMinimalMessage, 0)
	for _, message := range result.GetValue() {
		outlookMessages = append(outlookMessages, NewOutlookMinimalMessage(message))
	}

	return outlookMessages, nil
}

func (client *OutlookClient) GetMessageWithDetail(skip, limit int32) ([]*OutlookMessage, error) {
	requestBuilder := client.Me().Messages()

	if limit > 100 || limit < 1 {
		limit = 100
	}

	query := users.ItemMessagesRequestBuilderGetQueryParameters{
		Top:    int32Ptr(limit),
		Skip:   int32Ptr(skip),
		Select: []string{"subject", "body", "from", "toRecipients", "receivedDateTime", "ccRecipients", "bccRecipients", "attachments"},
		Expand: []string{"attachments"},
	}

	configuration := users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &query,
	}

	result, err := requestBuilder.Get(context.Background(), &configuration)
	if err != nil {
		return nil, err
	}

	outlookMessages := make([]*OutlookMessage, 0)
	for _, message := range result.GetValue() {
		outlookMessages = append(outlookMessages, NewOutlookMessage(message))
	}

	return outlookMessages, nil
}

func (client *OutlookClient) GetMessage(msgID string) (*OutlookMessage, error) {
	msg, err := client.Me().Messages().ByMessageId(msgID).Get(context.Background(), &users.ItemMessagesMessageItemRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemMessagesMessageItemRequestBuilderGetQueryParameters{
			Select: []string{"subject", "body", "from", "toRecipients", "receivedDateTime", "ccRecipients", "bccRecipients", "attachments"},
			Expand: []string{"attachments"},
		},
	})
	if err != nil {
		return nil, err
	}

	return NewOutlookMessage(msg), nil
}

func (client *OutlookClient) GetAttachment(msgID string, attID string) (*OutlookAttachment, error) {
	att, err := client.Me().Messages().ByMessageId(msgID).Attachments().ByAttachmentId(attID).Get(context.Background(), nil)
	if err != nil {
		return nil, err
	}

	return NewOutlookAttachment(att), nil
}

// Helper function to create int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}
