package outlook

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	abs "github.com/microsoft/kiota-abstractions-go"
	msgraph "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

// OutlookFilter represents filter parameters for Outlook message queries
type OutlookFilter struct {
	From          string `json:"from,omitempty"`          // Filter by sender email
	To            string `json:"to,omitempty"`            // Filter by recipient email
	Subject       string `json:"subject,omitempty"`       // Filter by subject
	HasAttachment bool   `json:"hasAttachment,omitempty"` // Filter messages with attachments
	After         string `json:"after,omitempty"`         // Filter messages after date (YYYY-MM-DD or RFC3339)
	Before        string `json:"before,omitempty"`        // Filter messages before date (YYYY-MM-DD or RFC3339)
	NewerThan     string `json:"newerThan,omitempty"`     // Filter messages newer than (e.g., "1d", "1w", "1m")
	OlderThan     string `json:"olderThan,omitempty"`     // Filter messages older than (e.g., "1d", "1w", "1m")
	Query         string `json:"query,omitempty"`         // Raw OData filter query
	Search        string `json:"search,omitempty"`        // Full-text search across message properties (subject, body, sender, etc.)
}

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
		return nil, fmt.Errorf("failed to create Graph request adapter: %w", err)
	}
	client := msgraph.NewGraphServiceClient(adapter)

	return &OutlookClient{client}, nil
}

func (client *OutlookClient) GetCurrentUser() (*OutlookUser, error) {

	user, err := client.Me().Get(context.Background(), &users.UserItemRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.UserItemRequestBuilderGetQueryParameters{
			Select: []string{"id", "displayName", "mail", "userPrincipalName"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get user from Microsoft Graph API: %w", err)
	}

	u := NewOutlookUser(user)
	if u == nil {
		return nil, errors.New("NewOutlookUser returned nil")
	}
	if u.ID == "" {
		return nil, errors.New("user ID is empty")
	}
	if u.Mail == "" {
		return nil, errors.New("user email is empty")
	}

	return u, nil
}

// validateSubject checks if subject contains any special characters
// Returns error if subject contains special characters that could cause OData filter issues
func validateSubject(subject string) error {
	if subject == "" {
		return nil
	}
	// Allow alphanumeric, spaces, and basic punctuation: period, comma, hyphen, underscore, colon, semicolon
	// Reject parentheses, brackets, quotes, backslashes, and other special characters
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9\s.,\-_:;]+$`)
	if !validPattern.MatchString(subject) {
		return fmt.Errorf("subject contains special characters that are not allowed. Only alphanumeric characters, spaces, and basic punctuation (.,-_:;) are allowed")
	}
	return nil
}

// buildOutlookFilter constructs an OData filter query string from filter parameters
func (filter *OutlookFilter) buildOutlookFilter() (string, error) {
	var filterParts []string

	// If a raw query is provided, use it directly
	if filter.Query != "" {
		return filter.Query, nil
	}

	// Build OData filter from individual filter parameters
	// Include subject filter if provided
	if filter.Subject != "" {
		// Wrap subject in contains() - only escape single quotes for OData
		escapedSubject := strings.ReplaceAll(filter.Subject, "'", "''")
		filterParts = append(filterParts, fmt.Sprintf("contains(subject,'%s')", escapedSubject))
	}
	if filter.From != "" {
		// Escape single quotes in email address
		escapedFrom := strings.ReplaceAll(filter.From, "'", "''")
		filterParts = append(filterParts, fmt.Sprintf("from/emailAddress/address eq '%s'", escapedFrom))
	}

	if filter.To != "" {
		// Escape single quotes in email address
		escapedTo := strings.ReplaceAll(filter.To, "'", "''")
		filterParts = append(filterParts, fmt.Sprintf("toRecipients/any(r:r/emailAddress/address eq '%s')", escapedTo))
	}

	if filter.HasAttachment {
		filterParts = append(filterParts, "hasAttachments eq true")
	}

	// Handle date filters
	now := time.Now()
	if filter.After != "" {
		dateStr := parseDateFilter(filter.After)
		if dateStr != "" {
			filterParts = append(filterParts, fmt.Sprintf("receivedDateTime ge %s", dateStr))
		}
	}

	if filter.Before != "" {
		dateStr := parseDateFilter(filter.Before)
		if dateStr != "" {
			filterParts = append(filterParts, fmt.Sprintf("receivedDateTime le %s", dateStr))
		}
	}

	if filter.NewerThan != "" {
		dateStr := parseRelativeDateFilter(filter.NewerThan, now, true)
		if dateStr != "" {
			filterParts = append(filterParts, fmt.Sprintf("receivedDateTime ge %s", dateStr))
		}
	}

	if filter.OlderThan != "" {
		dateStr := parseRelativeDateFilter(filter.OlderThan, now, false)
		if dateStr != "" {
			filterParts = append(filterParts, fmt.Sprintf("receivedDateTime le %s", dateStr))
		}
	}

	// Join all filter parts with 'and'
	return strings.Join(filterParts, " and "), nil
}

// parseDateFilter parses date string and returns RFC3339 format for OData
func parseDateFilter(dateStr string) string {
	// Try RFC3339 format first
	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return t.Format(time.RFC3339)
	}
	// Try YYYY-MM-DD format
	if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		return t.Format(time.RFC3339)
	}
	// Try YYYY/MM/DD format (Gmail style)
	if t, err := time.Parse("2006/01/02", dateStr); err == nil {
		return t.Format(time.RFC3339)
	}
	return ""
}

// parseRelativeDateFilter parses relative date strings like "1d", "1w", "1m"
func parseRelativeDateFilter(relativeStr string, now time.Time, newer bool) string {
	var duration time.Duration
	var err error

	// Remove common suffixes and parse
	relativeStr = strings.ToLower(strings.TrimSpace(relativeStr))
	if strings.HasSuffix(relativeStr, "d") {
		days := strings.TrimSuffix(relativeStr, "d")
		var d int
		if _, err = fmt.Sscanf(days, "%d", &d); err == nil {
			duration = time.Duration(d) * 24 * time.Hour
		}
	} else if strings.HasSuffix(relativeStr, "w") {
		weeks := strings.TrimSuffix(relativeStr, "w")
		var w int
		if _, err = fmt.Sscanf(weeks, "%d", &w); err == nil {
			duration = time.Duration(w) * 7 * 24 * time.Hour
		}
	} else if strings.HasSuffix(relativeStr, "m") {
		months := strings.TrimSuffix(relativeStr, "m")
		var m int
		if _, err = fmt.Sscanf(months, "%d", &m); err == nil {
			// Approximate month as 30 days
			duration = time.Duration(m) * 30 * 24 * time.Hour
		}
	}

	if err != nil || duration == 0 {
		return ""
	}

	var targetTime time.Time
	if newer {
		// Messages newer than X (received after now - duration)
		targetTime = now.Add(-duration)
	} else {
		// Messages older than X (received before now - duration)
		targetTime = now.Add(-duration)
	}

	return targetTime.Format(time.RFC3339)
}

// GetUserMessagesControlled retrieves messages from Outlook with pagination and filter support
func (client *OutlookClient) GetUserMessagesControlled(skip, limit int32, filter *OutlookFilter) (*OutlookResponse, error) {
	if limit > 100 || limit < 1 {
		limit = 100
	}
	if skip < 0 {
		skip = 0
	}

	query := users.ItemMessagesRequestBuilderGetQueryParameters{
		Select: []string{"id", "subject", "from", "receivedDateTime", "isRead", "hasAttachments"},
		Top:    int32Ptr(limit),
	}

	// Apply skip if provided (except when using $search)
	if skip > 0 {
		query.Skip = int32Ptr(skip)
	}

	// Apply filter if provided
	if filter != nil {
		// If search is provided, use it
		searchStr := strings.TrimSpace(filter.Search)
		if searchStr != "" {
			query.Search = stringPtr(searchStr)
			if limit > 250 {
				query.Top = int32Ptr(250)
			}
			// $search doesn't support $skip or $orderby
		} else {
			// For OData filter
			if filter.Subject != "" {
				if err := validateSubject(filter.Subject); err != nil {
					return nil, fmt.Errorf("invalid subject: %w", err)
				}
			}

			// Build the OData filter
			if filterStr, err := filter.buildOutlookFilter(); err != nil {
				return nil, err
			} else if filterStr != "" {
				query.Filter = stringPtr(filterStr)
			}

			// FIX: Only use orderBy when we don't have subject filter
			// Subject filter with contains() makes the query too complex when combined with orderBy
			if filter.Subject == "" {
				query.Orderby = []string{"receivedDateTime DESC"}
			}
			// If subject filter is present, don't use orderBy - let API use default ordering
		}
	} else {
		// No filter - use normal ordering
		query.Orderby = []string{"receivedDateTime DESC"}
	}

	configuration := users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &query,
	}

	result, err := client.Me().Messages().Get(context.Background(), &configuration)
	if err != nil {
		return nil, fmt.Errorf("failed to get user messages: %w", err)
	}

	outlookMessages := make([]*OutlookMinimalMessage, 0, len(result.GetValue()))
	for _, message := range result.GetValue() {
		outlookMessages = append(outlookMessages, NewOutlookMinimalMessage(message))
	}

	response := &OutlookResponse{
		Messages: outlookMessages,
		Skip:     int(skip),
		Limit:    int(limit),
	}

	return response, nil
}

// GetMessageWithDetails retrieves detailed messages with attachments
func (client *OutlookClient) GetMessageWithDetails(skip, limit int32) ([]*OutlookMessage, error) {

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
		return nil, fmt.Errorf("failed to get detailed messages: %w", err)
	}

	outlookMessages := make([]*OutlookMessage, 0, len(result.GetValue()))
	for _, message := range result.GetValue() {
		outlookMessages = append(outlookMessages, NewOutlookMessage(message))
	}

	return outlookMessages, nil
}

// GetMessage retrieves a specific message by ID
func (client *OutlookClient) GetMessage(msgID string) (*OutlookMessage, error) {

	if msgID == "" {
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
		return nil, fmt.Errorf("failed to get message %s: %w", msgID, err)
	}

	return NewOutlookMessage(msg), nil
}

// GetAttachment retrieves a specific attachment
func (client *OutlookClient) GetAttachment(msgID, attID string) (*OutlookAttachment, error) {

	if msgID == "" || attID == "" {
		return nil, errors.New("message ID and attachment ID cannot be empty")
	}

	att, err := client.Me().Messages().ByMessageId(msgID).Attachments().ByAttachmentId(attID).Get(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get attachment %s for message %s: %w", attID, msgID, err)
	}

	return NewOutlookAttachment(att), nil
}

// InsertMessage inserts a message into Outlook
func (client *OutlookClient) InsertMessage(message *OutlookMessage) (models.Messageable, error) {

	if message == nil {
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
	}

	// Add attachments
	if len(message.Attachments) > 0 {
		attachments := make([]models.Attachmentable, 0, len(message.Attachments))
		var totalAttachmentSize int64
		var validAttachments int64

		for _, attachment := range message.Attachments {
			if attachment.Name == "" || len(attachment.Data) == 0 {
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
	}

	// Set received date time if available
	if message.ReceivedDateTime != "" {
		receivedDateTime, err := time.Parse(time.RFC3339, message.ReceivedDateTime)
		if err != nil {
			return nil, fmt.Errorf("failed to parse received date time: %w", err)
		}
		messageRequest.SetReceivedDateTime(&receivedDateTime)
	}

	// Create the message in drafts
	createdMessage, err := client.Me().Messages().Post(context.Background(), messageRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	// Move the message to inbox to make it appear as received
	req := users.NewItemMailFoldersItemMovePostRequestBody()
	req.SetDestinationId(stringPointer("inbox"))

	_, err = client.Me().Messages().ByMessageId(*createdMessage.GetId()).
		Move().Post(context.Background(), req, nil)
	if err != nil {
		// Log the error but don't fail the entire operation
		// The message was created successfully, just not moved
		return createdMessage, fmt.Errorf("message created but failed to move to inbox: %w", err)
	}

	return createdMessage, nil
}

// SendMessage sends a message immediately (without saving to drafts)
func (client *OutlookClient) SendMessage(message *OutlookMessage) error {

	if message == nil {
		return errors.New("message cannot be nil")
	}

	// First create the message in drafts
	createdMessage, err := client.InsertMessage(message)
	if err != nil {
		return fmt.Errorf("failed to create message for sending: %w", err)
	}

	// Then send it immediately
	err = client.Me().Messages().ByMessageId(*createdMessage.GetId()).
		Send().Post(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// Helper function to create int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}
