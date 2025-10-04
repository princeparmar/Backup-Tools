package outlook

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	abs "github.com/microsoft/kiota-abstractions-go"
	msgraph "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
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
	start := time.Now()

	authProvider := &BearerTokenAuthenticationProvider{accessToken: accessToken}
	adapter, err := msgraph.NewGraphRequestAdapter(authProvider)
	if err != nil {
		prometheus.RecordError("outlook_client_adapter_creation_failed", "outlook")
		return nil, fmt.Errorf("failed to create Graph request adapter: %w", err)
	}
	client := msgraph.NewGraphServiceClient(adapter)

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_client_creation_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_client_created_total", 1, "component", "outlook", "status", "success")

	return &OutlookClient{client}, nil
}

func (client *OutlookClient) GetCurrentUser() (*OutlookUser, error) {
	start := time.Now()

	user, err := client.Me().Get(context.Background(), &users.UserItemRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.UserItemRequestBuilderGetQueryParameters{
			Select: []string{"id", "displayName", "mail", "userPrincipalName"},
		},
	})
	if err != nil {
		prometheus.RecordError("outlook_get_current_user_failed", "outlook")
		return nil, fmt.Errorf("failed to get user from Microsoft Graph API: %w", err)
	}

	u := NewOutlookUser(user)
	if u == nil {
		prometheus.RecordError("outlook_user_creation_failed", "outlook")
		return nil, errors.New("NewOutlookUser returned nil")
	}
	if u.ID == "" {
		prometheus.RecordError("outlook_user_id_empty", "outlook")
		return nil, errors.New("user ID is empty")
	}
	if u.Mail == "" {
		prometheus.RecordError("outlook_user_email_empty", "outlook")
		return nil, errors.New("user email is empty")
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_get_current_user_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_get_current_user_total", 1, "component", "outlook", "status", "success")

	return u, nil
}

// GetUserMessages retrieves messages from Outlook with pagination support
func (client *OutlookClient) GetUserMessages(skip, limit int32) ([]*OutlookMinimalMessage, error) {
	start := time.Now()

	if limit > 100 || limit < 1 {
		limit = 100
	}
	if skip < 0 {
		skip = 0
	}

	query := users.ItemMessagesRequestBuilderGetQueryParameters{
		Top:     int32Ptr(limit),
		Skip:    int32Ptr(skip),
		Select:  []string{"id", "subject", "from", "receivedDateTime", "isRead", "hasAttachments"},
		Orderby: []string{"receivedDateTime DESC"},
	}

	configuration := users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &query,
	}

	result, err := client.Me().Messages().Get(context.Background(), &configuration)
	if err != nil {
		prometheus.RecordError("outlook_get_user_messages_failed", "outlook")
		return nil, fmt.Errorf("failed to get user messages: %w", err)
	}

	outlookMessages := make([]*OutlookMinimalMessage, 0, len(result.GetValue()))
	for _, message := range result.GetValue() {
		outlookMessages = append(outlookMessages, NewOutlookMinimalMessage(message))
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_get_user_messages_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_get_user_messages_total", 1, "component", "outlook", "status", "success")
	prometheus.RecordCounter("outlook_messages_retrieved_total", int64(len(outlookMessages)), "component", "outlook", "type", "minimal")

	return outlookMessages, nil
}

// GetMessageWithDetails retrieves detailed messages with attachments
func (client *OutlookClient) GetMessageWithDetails(skip, limit int32) ([]*OutlookMessage, error) {
	start := time.Now()

	if limit > 50 || limit < 1 { // Reduced limit for detailed requests
		limit = 50
	}
	if skip < 0 {
		skip = 0
	}

	query := users.ItemMessagesRequestBuilderGetQueryParameters{
		Top:     int32Ptr(limit),
		Skip:    int32Ptr(skip),
		Select:  []string{"subject", "body", "from", "toRecipients", "receivedDateTime", "ccRecipients", "bccRecipients", "attachments", "isRead", "importance"},
		Expand:  []string{"attachments"},
		Orderby: []string{"receivedDateTime DESC"},
	}

	configuration := users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &query,
	}

	result, err := client.Me().Messages().Get(context.Background(), &configuration)
	if err != nil {
		prometheus.RecordError("outlook_get_detailed_messages_failed", "outlook")
		return nil, fmt.Errorf("failed to get detailed messages: %w", err)
	}

	outlookMessages := make([]*OutlookMessage, 0, len(result.GetValue()))
	for _, message := range result.GetValue() {
		outlookMessages = append(outlookMessages, NewOutlookMessage(message))
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_get_detailed_messages_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_get_detailed_messages_total", 1, "component", "outlook", "status", "success")
	prometheus.RecordCounter("outlook_messages_retrieved_total", int64(len(outlookMessages)), "component", "outlook", "type", "detailed")

	return outlookMessages, nil
}

// GetMessage retrieves a specific message by ID
func (client *OutlookClient) GetMessage(msgID string) (*OutlookMessage, error) {
	start := time.Now()

	if msgID == "" {
		prometheus.RecordError("outlook_get_message_empty_id", "outlook")
		return nil, errors.New("message ID cannot be empty")
	}

	msg, err := client.Me().Messages().ByMessageId(msgID).Get(context.Background(), &users.ItemMessagesMessageItemRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemMessagesMessageItemRequestBuilderGetQueryParameters{
			Select: []string{
				"subject", "body", "from", "toRecipients", "receivedDateTime",
				"ccRecipients", "bccRecipients", "attachments", "internetMessageHeaders",
				"internetMessageId", "isRead", "importance", "conversationId",
			},
			Expand: []string{"attachments"},
		},
	})
	if err != nil {
		prometheus.RecordError("outlook_get_message_failed", "outlook")
		return nil, fmt.Errorf("failed to get message %s: %w", msgID, err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_get_message_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_get_message_total", 1, "component", "outlook", "status", "success")

	return NewOutlookMessage(msg), nil
}

// GetAttachment retrieves a specific attachment
func (client *OutlookClient) GetAttachment(msgID, attID string) (*OutlookAttachment, error) {
	start := time.Now()

	if msgID == "" || attID == "" {
		prometheus.RecordError("outlook_get_attachment_empty_ids", "outlook")
		return nil, errors.New("message ID and attachment ID cannot be empty")
	}

	att, err := client.Me().Messages().ByMessageId(msgID).Attachments().ByAttachmentId(attID).Get(context.Background(), nil)
	if err != nil {
		prometheus.RecordError("outlook_get_attachment_failed", "outlook")
		return nil, fmt.Errorf("failed to get attachment %s for message %s: %w", attID, msgID, err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_get_attachment_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_get_attachment_total", 1, "component", "outlook", "status", "success")

	return NewOutlookAttachment(att), nil
}

// InsertMessage inserts a message into Outlook
func (client *OutlookClient) InsertMessage(message *OutlookMessage) (models.Messageable, error) {
	start := time.Now()

	if message == nil {
		prometheus.RecordError("outlook_insert_message_nil", "outlook")
		return nil, errors.New("message cannot be nil")
	}

	messageRequest := models.NewMessage()
	messageRequest.SetSubject(stringPointer(message.Subject))

	// Set body content
	body := models.NewItemBody()
	body.SetContent(stringPointer(message.Body))
	if message.ContentType != nil {
		body.SetContentType(message.ContentType)
	} else {
		textType := models.TEXT_BODYTYPE
		body.SetContentType(&textType)
	}
	body.SetOdataType(stringPointer("#microsoft.graph.itemBody"))
	messageRequest.SetBody(body)

	// Set internet message ID if available
	if message.InternetMessageID != "" {
		messageRequest.SetInternetMessageId(stringPointer(message.InternetMessageID))
	}

	// Set sender
	if message.From != "" {
		from := models.NewRecipient()
		emailAddress := models.NewEmailAddress()
		emailAddress.SetAddress(stringPointer(message.From))
		from.SetEmailAddress(emailAddress)
		messageRequest.SetFrom(from)
	}

	// Set recipients
	if len(message.ToRecipients) > 0 {
		toRecipients := make([]models.Recipientable, 0, len(message.ToRecipients))
		for _, addr := range message.ToRecipients {
			if addr != "" {
				recipient := models.NewRecipient()
				emailAddress := models.NewEmailAddress()
				emailAddress.SetAddress(stringPointer(addr))
				recipient.SetEmailAddress(emailAddress)
				toRecipients = append(toRecipients, recipient)
			}
		}
		messageRequest.SetToRecipients(toRecipients)
		prometheus.RecordCounter("outlook_insert_message_to_recipients_total", int64(len(toRecipients)), "component", "outlook")
	}

	// Set CC recipients
	if len(message.CcRecipients) > 0 {
		ccRecipients := make([]models.Recipientable, 0, len(message.CcRecipients))
		for _, addr := range message.CcRecipients {
			if addr != "" {
				recipient := models.NewRecipient()
				emailAddress := models.NewEmailAddress()
				emailAddress.SetAddress(stringPointer(addr))
				recipient.SetEmailAddress(emailAddress)
				ccRecipients = append(ccRecipients, recipient)
			}
		}
		messageRequest.SetCcRecipients(ccRecipients)
		prometheus.RecordCounter("outlook_insert_message_cc_recipients_total", int64(len(ccRecipients)), "component", "outlook")
	}

	// Set BCC recipients
	if len(message.BccRecipients) > 0 {
		bccRecipients := make([]models.Recipientable, 0, len(message.BccRecipients))
		for _, addr := range message.BccRecipients {
			if addr != "" {
				recipient := models.NewRecipient()
				emailAddress := models.NewEmailAddress()
				emailAddress.SetAddress(stringPointer(addr))
				recipient.SetEmailAddress(emailAddress)
				bccRecipients = append(bccRecipients, recipient)
			}
		}
		messageRequest.SetBccRecipients(bccRecipients)
		prometheus.RecordCounter("outlook_insert_message_bcc_recipients_total", int64(len(bccRecipients)), "component", "outlook")
	}

	// Add attachments
	if len(message.Attachments) > 0 {
		attachments := make([]models.Attachmentable, 0, len(message.Attachments))
		var totalAttachmentSize int64
		var validAttachments int64

		for _, attachment := range message.Attachments {
			if attachment.Name == "" || len(attachment.Data) == 0 {
				prometheus.RecordCounter("outlook_insert_message_invalid_attachments_total", 1, "component", "outlook", "reason", "empty_name_or_data")
				continue // Skip invalid attachments
			}

			fileAttachment := models.NewFileAttachment()
			fileAttachment.SetName(&attachment.Name)

			if attachment.ContentType != nil {
				fileAttachment.SetContentType(attachment.ContentType)
			} else {
				contentType := stringPointer("application/octet-stream")
				fileAttachment.SetContentType(contentType)
			}

			fileAttachment.SetOdataType(stringPointer("#microsoft.graph.fileAttachment"))
			fileAttachment.SetContentBytes(attachment.Data)
			fileAttachment.SetSize(int32Ptr(int32(len(attachment.Data))))

			attachments = append(attachments, fileAttachment)
			totalAttachmentSize += int64(len(attachment.Data))
			validAttachments++
		}
		messageRequest.SetAttachments(attachments)

		prometheus.RecordCounter("outlook_insert_message_attachments_total", validAttachments, "component", "outlook")
		prometheus.RecordSize("outlook_insert_message_attachment_size_bytes", totalAttachmentSize, "component", "outlook")
	}

	// Set received date time if available
	if message.ReceivedDateTime != "" {
		receivedDateTime, err := time.Parse(time.RFC3339, message.ReceivedDateTime)
		if err != nil {
			prometheus.RecordError("outlook_insert_message_datetime_parse_failed", "outlook")
			return nil, fmt.Errorf("failed to parse received date time: %w", err)
		}
		messageRequest.SetReceivedDateTime(&receivedDateTime)
		prometheus.RecordCounter("outlook_insert_message_with_datetime_total", 1, "component", "outlook")
	}

	// Create the message in drafts
	createdMessage, err := client.Me().Messages().Post(context.Background(), messageRequest, nil)
	if err != nil {
		prometheus.RecordError("outlook_insert_message_creation_failed", "outlook")
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	prometheus.RecordCounter("outlook_insert_message_draft_created_total", 1, "component", "outlook", "status", "success")

	// Move the message to inbox to make it appear as received
	req := users.NewItemMailFoldersItemMovePostRequestBody()
	req.SetDestinationId(stringPointer("inbox"))

	_, err = client.Me().Messages().ByMessageId(*createdMessage.GetId()).
		Move().Post(context.Background(), req, nil)
	if err != nil {
		// Log the error but don't fail the entire operation
		// The message was created successfully, just not moved
		prometheus.RecordError("outlook_insert_message_move_failed", "outlook")
		prometheus.RecordCounter("outlook_insert_message_total", 1, "component", "outlook", "status", "partial_success")
		return createdMessage, fmt.Errorf("message created but failed to move to inbox: %w", err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_insert_message_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_insert_message_total", 1, "component", "outlook", "status", "success")
	prometheus.RecordCounter("outlook_insert_message_moved_to_inbox_total", 1, "component", "outlook")

	return createdMessage, nil
}

// SendMessage sends a message immediately (without saving to drafts)
func (client *OutlookClient) SendMessage(message *OutlookMessage) error {
	start := time.Now()

	if message == nil {
		prometheus.RecordError("outlook_send_message_nil", "outlook")
		return errors.New("message cannot be nil")
	}

	// First create the message in drafts
	createdMessage, err := client.InsertMessage(message)
	if err != nil {
		prometheus.RecordError("outlook_send_message_creation_failed", "outlook")
		return fmt.Errorf("failed to create message for sending: %w", err)
	}

	// Then send it immediately
	err = client.Me().Messages().ByMessageId(*createdMessage.GetId()).
		Send().Post(context.Background(), nil)
	if err != nil {
		prometheus.RecordError("outlook_send_message_failed", "outlook")
		return fmt.Errorf("failed to send message: %w", err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("outlook_send_message_duration_seconds", duration, "component", "outlook")
	prometheus.RecordCounter("outlook_send_message_total", 1, "component", "outlook", "status", "success")

	return nil
}

// Helper function to create int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}
